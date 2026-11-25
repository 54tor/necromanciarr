package repair

import (
	"errors"
	"slices"
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

// locate essaie les méthodes dans l'ordre : inode (enregistré fiable, et volume qui annonce l'identité), taille et
// date, puis empreinte.
func (run *scanRun) locate(link store.Link) (located, error) {
	budget := &readBudget{remaining: run.options.MaxCandidatesToRead}
	sized := run.bySize[link.Size]
	if link.InodeReliable {
		byInode := run.sameInode(link, sized)
		if len(byInode) > 0 {
			// Un inode distant (stat par ssh exec) propose la réparation sans jamais l'appliquer d'office : il faut
			// l'acceptation explicite de l'opérateur.
			autoSafe := !slices.ContainsFunc(byInode, func(candidate store.MediaFile) bool { return dedupe.IsRemote(candidate.Path) })
			return located{method: ByInode, matches: paths(byInode), unique: len(byInode) == 1, autoSafe: autoSafe}, nil
		}
	}
	sameModTime := run.sameModTime(link, sized)
	if len(sameModTime) == 1 {
		outcome, refuted, err := run.confirmSingle(link, sameModTime[0], budget)
		if err != nil || !refuted {
			return outcome, err
		}
		sized = without(sized, sameModTime[0].Path)
		sameModTime = nil
	}
	if !run.canConfirm(link) {
		return located{method: BySizeAndModTime, matches: paths(sameModTime)}, nil
	}
	return run.byFingerprint(link, sized, budget)
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
	files := run.options.files
	for _, candidate := range sized {
		if !run.announcesFileIdentity(candidate.Path) {
			continue
		}
		info, err := files.Stat(candidate.Path)
		if err != nil {
			continue
		}
		identity, known := files.FileID(candidate.Path, info)
		if known && identity.Device == link.TargetDevice && identity.Inode == link.TargetInode {
			matching = append(matching, candidate)
		}
	}
	return matching
}

// announcesFileIdentity : la méthode inode n'a de sens que si le volume du candidat annonce FileIdentity ou
// RemoteStat ; lu une fois par volume, une erreur vaut « non annoncé ».
func (run *scanRun) announcesFileIdentity(candidatePath string) bool {
	volume := dedupe.VolumeOf(candidatePath)
	if capable, found := run.identityCapable[volume]; found {
		return capable
	}
	capabilities, err := run.options.files.Capabilities(run.ctx, candidatePath)
	capable := err == nil && (capabilities.FileIdentity || capabilities.RemoteStat)
	run.identityCapable[volume] = capable
	return capable
}

func (run *scanRun) sameModTime(link store.Link, sized []store.MediaFile) []store.MediaFile {
	recorded := link.ModTime
	if override, found := run.options.RecordedModTimes[link.TargetPath]; found {
		recorded = override
	}
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
func (run *scanRun) confirmSingle(link store.Link, candidate store.MediaFile, budget *readBudget) (outcome located, refuted bool, err error) {
	outcome = located{method: BySizeAndModTime, matches: []string{candidate.Path}, unique: true}
	if !run.canConfirm(link) {
		return outcome, false, nil
	}
	matches, skipped, err := run.confirm(link, candidate, budget)
	if err != nil || skipped {
		return located{}, false, err
	}
	outcome.autoSafe = matches
	return outcome, !matches, nil
}

// byFingerprint confirme les fichiers de même taille, au plus MaxCandidatesToRead lectures ; une recherche tronquée
// ne prouve pas l'unicité et ne propose donc jamais de réparation.
func (run *scanRun) byFingerprint(link store.Link, sized []store.MediaFile, budget *readBudget) (located, error) {
	confirmed, truncated, err := run.confirmAll(link, sized, budget)
	if err != nil {
		return located{}, err
	}
	return located{method: ByFingerprint, matches: paths(confirmed), unique: len(confirmed) == 1 && !truncated, autoSafe: true}, nil
}

// readBudget est partagé par toutes les méthodes d'un même lien : MaxCandidatesToRead lectures au total.
type readBudget struct{ remaining int }

func (run *scanRun) confirmAll(link store.Link, candidates []store.MediaFile, budget *readBudget) (confirmed []store.MediaFile, truncated bool, err error) {
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
	fingerprint, err := run.candidateFingerprint(candidate, budget)
	if errors.Is(err, dedupe.ErrNoCachedFingerprint) {
		return false, true, nil
	}
	if err != nil {
		return false, false, err
	}
	return fingerprint == link.Fingerprint, false, nil
}

// candidateFingerprint lit le cache sans rien lire, puis, si le budget le permet, l'empreinte par le registre sous le
// limiteur ; une lecture consomme le budget.
func (run *scanRun) candidateFingerprint(candidate store.MediaFile, budget *readBudget) (string, error) {
	cache := run.options.FingerprintCache
	fingerprint, _, err := dedupe.HeadTailFingerprint(run.ctx, cache, nil, candidate.Path, candidate.Size, candidate.ModTime, false)
	if !errors.Is(err, dedupe.ErrNoCachedFingerprint) || budget.remaining <= 0 {
		return fingerprint, err
	}
	budget.remaining--
	open := func() (dedupe.ReadAtCloser, error) { return run.options.files.Open(candidate.Path) }
	return dedupe.HeadTailFingerprintOf(run.ctx, cache, run.options.Limiter, open, candidate.Path, candidate.Size, candidate.ModTime)
}
