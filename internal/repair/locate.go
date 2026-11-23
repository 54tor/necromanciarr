package repair

import (
	"errors"
	"os"
	"sort"

	"necromanciarr/internal/dedupe"
	"necromanciarr/internal/store"
)

// located est le résultat de la recherche d'un lien : unique dit qu'un seul fichier convient, autoSafe que la méthode
// permet une application sans validation.
type located struct {
	method   RepairMethod
	matches  []string
	unique   bool
	autoSafe bool
}

func paths(files []store.MediaFile) []string {
	listed := make([]string, 0, len(files))
	for _, file := range files {
		listed = append(listed, file.Path)
	}
	sort.Strings(listed)
	return listed
}

// locate essaie les méthodes dans l'ordre : inode (si fiable), taille et date, puis empreinte.
func (run *scanRun) locate(link store.Link) (located, error) {
	sized := run.bySize[link.Size]
	if link.InodeReliable {
		byInode := run.sameInode(link, sized)
		if len(byInode) > 0 {
			return located{method: ByInode, matches: paths(byInode), unique: len(byInode) == 1, autoSafe: true}, nil
		}
	}
	sameModTime := run.sameModTime(link, sized)
	if len(sameModTime) == 1 {
		outcome, refuted, err := run.confirmSingle(link, sameModTime[0])
		if err != nil || !refuted {
			return outcome, err
		}
		sized = without(sized, sameModTime[0].Path)
		sameModTime = nil
	}
	if !run.canConfirm(link) {
		return located{method: BySizeAndModTime, matches: paths(sameModTime)}, nil
	}
	return run.byFingerprint(link, sized)
}

func without(files []store.MediaFile, excludedPath string) []store.MediaFile {
	kept := make([]store.MediaFile, 0, len(files))
	for _, file := range files {
		if file.Path != excludedPath {
			kept = append(kept, file)
		}
	}
	return kept
}

func (run *scanRun) sameInode(link store.Link, sized []store.MediaFile) []store.MediaFile {
	var matching []store.MediaFile
	for _, candidate := range sized {
		info, err := os.Stat(candidate.Path)
		if err != nil {
			continue
		}
		device, inode, known := dedupe.FileIdentity(info)
		if known && device == link.TargetDevice && inode == link.TargetInode {
			matching = append(matching, candidate)
		}
	}
	return matching
}

func (run *scanRun) sameModTime(link store.Link, sized []store.MediaFile) []store.MediaFile {
	recorded := run.options.RecordedModTimes[link.TargetPath]
	if recorded.IsZero() {
		return nil
	}
	var matching []store.MediaFile
	for _, candidate := range sized {
		if candidate.ModTime.Equal(recorded) {
			matching = append(matching, candidate)
		}
	}
	return matching
}

func (run *scanRun) canConfirm(link store.Link) bool {
	return link.Fingerprint != "" || run.options.HashConfirmer != nil
}

// confirmSingle confirme le candidat unique de taille et date ; sans moyen de confirmer, il est proposé tel quel, sans
// application automatique. refuted dit que le contenu diffère : le candidat est écarté.
func (run *scanRun) confirmSingle(link store.Link, candidate store.MediaFile) (outcome located, refuted bool, err error) {
	outcome = located{method: BySizeAndModTime, matches: []string{candidate.Path}, unique: true}
	if !run.canConfirm(link) {
		return outcome, false, nil
	}
	budget := &readBudget{remaining: run.options.MaxCandidatesToRead}
	matches, skipped, err := run.confirm(link, candidate, budget)
	if err != nil || skipped {
		return located{}, false, err
	}
	outcome.autoSafe = matches
	return outcome, !matches, nil
}

// byFingerprint confirme les fichiers de même taille, au plus MaxCandidatesToRead lectures ; une recherche tronquée
// ne prouve pas l'unicité et ne propose donc jamais de réparation.
func (run *scanRun) byFingerprint(link store.Link, sized []store.MediaFile) (located, error) {
	confirmed, truncated, err := run.confirmAll(link, sized)
	if err != nil {
		return located{}, err
	}
	return located{method: ByFingerprint, matches: paths(confirmed), unique: len(confirmed) == 1 && !truncated, autoSafe: true}, nil
}

type readBudget struct{ remaining int }

func (run *scanRun) confirmAll(link store.Link, candidates []store.MediaFile) (confirmed []store.MediaFile, truncated bool, err error) {
	budget := &readBudget{remaining: run.options.MaxCandidatesToRead}
	for _, candidate := range candidates {
		matches, skipped, err := run.confirm(link, candidate, budget)
		if err != nil {
			return nil, false, err
		}
		truncated = truncated || skipped
		if matches {
			confirmed = append(confirmed, candidate)
		}
	}
	return confirmed, truncated, nil
}

// confirm compare un candidat au lien : confirmation distante, puis cache, puis lecture début et fin tant que le
// budget le permet ; skipped dit que le budget était épuisé et que rien n'a été lu.
func (run *scanRun) confirm(link store.Link, candidate store.MediaFile, budget *readBudget) (matches, skipped bool, err error) {
	if run.options.HashConfirmer != nil {
		matches, known, err := run.options.HashConfirmer.Confirm(run.ctx, link, candidate.Path)
		if err != nil || known {
			return matches, false, err
		}
	}
	if link.Fingerprint == "" {
		return false, false, nil
	}
	fingerprint, fromCache, err := dedupe.HeadTailFingerprint(run.ctx, run.options.FingerprintCache, run.options.Limiter,
		candidate.Path, candidate.Size, candidate.ModTime, budget.remaining > 0)
	if errors.Is(err, dedupe.ErrNoCachedFingerprint) {
		return false, true, nil
	}
	if err != nil {
		return false, false, err
	}
	if !fromCache {
		budget.remaining--
	}
	return fingerprint == link.Fingerprint, false, nil
}
