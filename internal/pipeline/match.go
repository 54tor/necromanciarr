package pipeline

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"necromanciarr/internal/libraryprovider"
	"necromanciarr/internal/matcher"
	"necromanciarr/internal/store"
)

// Raisons comptées par l'étape Match.
const (
	ReasonNoExactSize   = "no_exact_size"
	ReasonSearchFailed  = "search_failed"
	ReasonStaleDetected = "stale_candidate"
	// ReasonUpdateDeferred compte les candidats connus non réassociés parce que l'exécution n'a pas tout cherché.
	ReasonUpdateDeferred = "update_deferred_incomplete_run"
)

// candidateKey identifie un candidat : le même infohash sur deux trackers donne deux entrées distinctes.
type candidateKey struct {
	infohash, tracker string
}

// pendingCandidate cumule, pendant une exécution, les fichiers locaux des groupes où la release a été trouvée.
type pendingCandidate struct {
	release libraryprovider.Release
	local   map[int64]store.MediaFile
}

// runMatch périme chaque candidat dont un fichier a changé, cherche une fois par identifiant externe en cumulant pour
// chaque release les fichiers locaux de tous les groupes où elle apparaît, puis enregistre chaque release une seule fois
// si un de ses fichiers a exactement la taille d'un fichier local ; tous les torrents sont gardés.
func runMatch(ctx context.Context, deps Deps) (Result, error) {
	var result Result
	now := deps.now()
	stale, err := deps.Store.MarkChangedCandidatesStale(ctx, now)
	if err != nil {
		return result, err
	}
	result.CandidatesStale = stale
	result.count(ReasonStaleDetected, stale)
	files, err := deps.Store.ListMediaFiles(ctx)
	if err != nil {
		return result, err
	}
	known, err := knownCandidateKeys(ctx, deps)
	if err != nil {
		return result, err
	}
	pending := map[candidateKey]*pendingCandidate{}
	searchErr := searchGroups(ctx, deps, &result, groupByExternalID(files), pending)
	if lockLost(ctx) {
		// Un autre processus tient le verrou : plus aucune écriture.
		return result, searchErr
	}
	complete := searchErr == nil && result.Failed == 0
	// Les écritures restantes sont chacune une transaction, faites même après annulation pour garder ce qui est trouvé.
	persistContext := context.WithoutCancel(ctx)
	saveErr := savePending(persistContext, deps, &result, pending, known, complete, now)
	regroupErr := deps.Store.RegroupCandidates(persistContext)
	return result, errors.Join(searchErr, saveErr, regroupErr)
}

func knownCandidateKeys(ctx context.Context, deps Deps) (map[candidateKey]bool, error) {
	candidates, err := deps.Store.ListCandidatesByState(ctx)
	if err != nil {
		return nil, err
	}
	known := map[candidateKey]bool{}
	for _, candidate := range candidates {
		known[candidateKey{infohash: candidate.Infohash, tracker: candidate.Tracker}] = true
	}
	return known, nil
}

// searchGroups cherche groupe par groupe et cumule les releases dans pending, sans rien écrire.
func searchGroups(ctx context.Context, deps Deps, result *Result, groups map[externalIDs][]store.MediaFile, pending map[candidateKey]*pendingCandidate) error {
	for _, ids := range slices.SortedFunc(maps.Keys(groups), compareExternalIDs) {
		if err := ctx.Err(); err != nil {
			return err
		}
		releases, err := deps.Provider.Search(ctx, groupQuery(ids, groups[ids]))
		if err != nil {
			if stopErr := stopError(err); stopErr != nil {
				return stopErr
			}
			result.Failed++
			result.count(ReasonSearchFailed, 1)
			continue
		}
		result.Processed++
		for _, release := range releases {
			accumulate(pending, release, groups[ids])
		}
	}
	return nil
}

// stopError renvoie l'erreur qui doit arrêter l'étape, ou nil si seule cette recherche a échoué : un fournisseur
// limité ou indisponible n'est pas relancé en boucle, un refus d'accès vaudrait pour toutes les recherches.
func stopError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, libraryprovider.ErrAccessDenied) {
		return err
	}
	if !errors.Is(err, libraryprovider.ErrUnavailable) {
		return nil
	}
	resumable := &ResumableError{Stage: StageMatch, Cause: err}
	var rateLimited *libraryprovider.RateLimitedError
	if errors.As(err, &rateLimited) {
		resumable.RetryAfter = rateLimited.RetryAfter
	}
	return resumable
}

// groupByExternalID regroupe par TMDB, à défaut par TVDB ; un fichier sans identifiant n'est pas cherchable en v1.
func groupByExternalID(files []store.MediaFile) map[externalIDs][]store.MediaFile {
	groups := map[externalIDs][]store.MediaFile{}
	for _, file := range files {
		switch {
		case file.TMDBID != 0:
			groups[externalIDs{tmdbID: file.TMDBID}] = append(groups[externalIDs{tmdbID: file.TMDBID}], file)
		case file.TVDBID != 0:
			groups[externalIDs{tvdbID: file.TVDBID}] = append(groups[externalIDs{tvdbID: file.TVDBID}], file)
		}
	}
	return groups
}

func compareExternalIDs(left, right externalIDs) int {
	return cmp.Or(cmp.Compare(left.tmdbID, right.tmdbID), cmp.Compare(left.tvdbID, right.tvdbID))
}

// groupQuery demande les releases de l'identifiant ayant une des tailles du groupe ; le client découpe les tailles.
func groupQuery(ids externalIDs, files []store.MediaFile) libraryprovider.Query {
	sizes := make([]int64, 0, len(files))
	for _, file := range files {
		if file.Size > 0 {
			sizes = append(sizes, file.Size)
		}
	}
	slices.Sort(sizes)
	return libraryprovider.Query{TMDBID: ids.tmdbID, TVDBID: ids.tvdbID, Sizes: slices.Compact(sizes)}
}

// savePending enregistre chaque release cumulée. Après une exécution incomplète (arrêt ou recherche en échec), les
// associations cumulées peuvent être partielles : seuls les nouveaux candidats sont créés, un candidat connu n'est
// jamais réassocié (ce qui le ramènerait à matched).
func savePending(ctx context.Context, deps Deps, result *Result, pending map[candidateKey]*pendingCandidate,
	known map[candidateKey]bool, complete bool, now time.Time,
) error {
	for _, key := range slices.SortedFunc(maps.Keys(pending), compareCandidateKeys) {
		accumulated := pending[key]
		// Filtre local de défense : le fournisseur peut ignorer le paramètre size.
		matches := matcher.Candidates(accumulated.release, slices.Collect(maps.Values(accumulated.local)))
		if len(matches) == 0 {
			result.Skipped++
			result.count(ReasonNoExactSize, 1)
			continue
		}
		if known[key] && !complete {
			result.count(ReasonUpdateDeferred, 1)
			continue
		}
		candidate, err := candidateOf(accumulated.release)
		if err != nil {
			return err
		}
		_, outcome, err := deps.Store.SaveCandidate(ctx, candidate, candidateFiles(accumulated.release, matches), now)
		if err != nil {
			return err
		}
		countOutcome(result, outcome)
	}
	return nil
}

func compareCandidateKeys(left, right candidateKey) int {
	return cmp.Or(cmp.Compare(left.infohash, right.infohash), cmp.Compare(left.tracker, right.tracker))
}

func accumulate(pending map[candidateKey]*pendingCandidate, release libraryprovider.Release, groupFiles []store.MediaFile) {
	key := candidateKey{infohash: release.Infohash, tracker: release.Tracker}
	accumulated, known := pending[key]
	if !known {
		accumulated = &pendingCandidate{release: release, local: map[int64]store.MediaFile{}}
		pending[key] = accumulated
	}
	for _, file := range groupFiles {
		accumulated.local[file.ID] = file
	}
}

func countOutcome(result *Result, outcome store.SaveOutcome) {
	switch outcome {
	case store.SaveCreated:
		result.CandidatesCreated++
	case store.SaveUpdated:
		result.CandidatesUpdated++
	case store.SaveUnchanged:
		result.CandidatesUnchanged++
	}
}

// providerSummary est le minimum gardé de la réponse du fournisseur : les identifiants qui ont servi à la trouver.
type providerSummary struct {
	TMDBID int    `json:"tmdb,omitempty"`
	TVDBID int    `json:"tvdb,omitempty"`
	IMDBID string `json:"imdb,omitempty"`
}

func candidateOf(release libraryprovider.Release) (store.Candidate, error) {
	summary, err := json.Marshal(providerSummary{TMDBID: release.TMDBID, TVDBID: release.TVDBID, IMDBID: release.IMDBID})
	if err != nil {
		return store.Candidate{}, fmt.Errorf("encode provider summary: %w", err)
	}
	return store.Candidate{
		Infohash: release.Infohash, Tracker: release.Tracker, Name: release.Name, TotalSize: release.TotalSize,
		ProviderJSON: string(summary),
	}, nil
}

// candidateFiles donne une ligne par fichier du torrent sans fichier local, et une ligne par fichier local possible
// sinon ; plusieurs fichiers locaux de même taille sont marqués ambigus, le hash les départagera.
func candidateFiles(release libraryprovider.Release, matches []matcher.FileMatch) []store.CandidateFile {
	localByIndex := map[int][]store.MediaFile{}
	for _, match := range matches {
		localByIndex[match.ReleaseFileIndex] = match.LocalFiles
	}
	var files []store.CandidateFile
	for fileIndex, releaseFile := range release.Files {
		base := store.CandidateFile{FileIndex: fileIndex, Path: releaseFile.Path, Size: releaseFile.Size}
		locals := localByIndex[fileIndex]
		if len(locals) == 0 {
			files = append(files, base)
			continue
		}
		for _, local := range locals {
			row := base
			row.MediaFileID, row.MediaSize, row.MediaModTime, row.Ambiguous = local.ID, local.Size, local.ModTime, len(locals) > 1
			files = append(files, row)
		}
	}
	return files
}
