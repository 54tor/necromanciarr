package pipeline

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"
	"time"

	"necromanciarr/internal/libraryprovider"
	"necromanciarr/internal/matcher"
	"necromanciarr/internal/sources"
	"necromanciarr/internal/store"
	"necromanciarr/internal/torrentcheck"
)

// indexerGroupKey est un groupe cherché chez les indexeurs : un film (TMDB), ou une saison d'une série (TVDB + saison).
type indexerGroupKey struct {
	tmdbID, tvdbID, season int
}

func compareIndexerGroupKeys(left, right indexerGroupKey) int {
	return cmp.Or(cmp.Compare(left.tmdbID, right.tmdbID), cmp.Compare(left.tvdbID, right.tvdbID), cmp.Compare(left.season, right.season))
}

// coveredFiles rend les fichiers locaux qu'au moins une release du fournisseur couvre par un fichier de taille exacte.
func coveredFiles(pending map[candidateKey]*pendingCandidate) map[int64]bool {
	covered := map[int64]bool{}
	for _, accumulated := range pending {
		for _, match := range matcher.Candidates(accumulated.release, slices.Collect(maps.Values(accumulated.local))) {
			for _, local := range match.LocalFiles {
				covered[local.ID] = true
			}
		}
	}
	return covered
}

// searchIndexers cherche chez les indexeurs chaque groupe dont un fichier n'a aucun candidat de taille exacte après le
// fournisseur, avec un seul budget pour l'exécution. Un indexeur ou un groupe en échec n'arrête pas l'étape : seuls
// l'annulation et la perte du verrou l'arrêtent ; sans clé de scellement, secret_key_missing est compté une fois et
// aucun indexeur n'est sollicité.
func searchIndexers(ctx context.Context, deps Deps, result *Result, files []store.MediaFile, covered map[int64]bool, now time.Time) error {
	budget := deps.Sources.NewBudget()
	defer recordIndexerReport(result, budget)
	groups := indexerGroups(files)
	for _, key := range slices.SortedFunc(maps.Keys(groups), compareIndexerGroupKeys) {
		group := groups[key]
		uncovered := uncoveredSizes(group, covered)
		if len(uncovered) == 0 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		found, _, err := deps.Sources.SearchCandidates(ctx, mediaQueryOf(deps, key, group, uncovered), budget)
		stop, stopErr := recordSearchRefusals(result, err)
		if stopErr != nil {
			return stopErr
		}
		if saveErr := saveFoundCandidates(ctx, deps, result, found, group, covered, now); saveErr != nil {
			return saveErr
		}
		if stop {
			return nil
		}
	}
	return nil
}

// recordIndexerReport reporte les comptes du budget (identifiants de source seulement) et les résultats magnet seul.
func recordIndexerReport(result *Result, budget *sources.Budget) {
	report := budget.Report()
	if len(report) == 0 {
		return
	}
	result.Indexers = map[string]sources.IndexerReport{}
	for indexer, indexerReport := range report {
		result.Indexers[string(indexer)] = indexerReport
		result.count(ReasonMagnetOnly, indexerReport.MagnetOnly)
	}
}

// recordSearchRefusals compte les refus d'une recherche ; stop : plus aucun groupe à chercher (clé de scellement
// absente) ; stopErr : l'étape s'arrête (annulation).
func recordSearchRefusals(result *Result, err error) (stop bool, stopErr error) {
	var refusals *sources.SearchError
	switch {
	case err == nil:
		return false, nil
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return true, err
	case errors.Is(err, sources.ErrSecretKeyMissing):
		result.count(ReasonSecretKeyMissing, 1)
		return true, nil
	case errors.As(err, &refusals):
		for _, refusal := range append(slices.Clone(refusals.Skipped), refusals.Interrupted...) {
			result.count(indexerReason(refusal), 1)
		}
		return false, nil
	default:
		result.count(indexerReason(err), 1)
		return false, nil
	}
}

// indexerGroups regroupe comme le fournisseur (TMDB, à défaut TVDB), une série étant découpée par saison ; les fichiers
// d'un groupe sont triés par chemin pour que titre et année ne dépendent pas de l'ordre de lecture.
func indexerGroups(files []store.MediaFile) map[indexerGroupKey][]store.MediaFile {
	groups := map[indexerGroupKey][]store.MediaFile{}
	for _, file := range files {
		switch {
		case file.TMDBID != 0:
			key := indexerGroupKey{tmdbID: file.TMDBID}
			groups[key] = append(groups[key], file)
		case file.TVDBID != 0:
			key := indexerGroupKey{tvdbID: file.TVDBID, season: file.Season}
			groups[key] = append(groups[key], file)
		}
	}
	for key := range groups {
		slices.SortFunc(groups[key], func(left, right store.MediaFile) int { return cmp.Compare(left.Path, right.Path) })
	}
	return groups
}

func uncoveredSizes(group []store.MediaFile, covered map[int64]bool) []int64 {
	var sizes []int64
	for _, file := range group {
		if file.Size > 0 && !covered[file.ID] {
			sizes = append(sizes, file.Size)
		}
	}
	return sizes
}

// mediaQueryOf décrit le groupe aux indexeurs : l'épisode n'est donné que pour un fichier seul (sinon pack de saison),
// le titre, l'année et l'IMDb viennent du premier fichier qui les porte.
func mediaQueryOf(deps Deps, key indexerGroupKey, group []store.MediaFile, uncovered []int64) sources.MediaQuery {
	query := sources.MediaQuery{
		TMDBID: key.tmdbID, TVDBID: key.tvdbID, Season: key.season, Series: key.tmdbID == 0,
		Sizes: groupQuery(externalIDs{}, group).Sizes, Uncovered: uncovered,
		AnnounceConfigured: func(tracker string) bool {
			return deps.Announces.Resolve(tracker, false).Source == sources.AnnounceConfig
		},
	}
	if len(group) == 1 {
		query.Episode = group[0].Episode
	}
	for _, file := range group {
		query.Title = cmp.Or(query.Title, file.Title)
		query.IMDBID = cmp.Or(query.IMDBID, file.IMDBID)
		query.Year = cmp.Or(query.Year, file.Year)
	}
	return query
}

// saveFoundCandidates enregistre chaque .torrent retenu, directement à fetched ; la preuve de taille exacte est refaite
// sur les fichiers du groupe encore sans candidat (les associations couvrent, elles, tout le groupe), et un candidat
// déjà connu garde sa source.
func saveFoundCandidates(ctx context.Context, deps Deps, result *Result, found []sources.Found, group []store.MediaFile,
	covered map[int64]bool, now time.Time,
) error {
	persistContext := context.WithoutCancel(ctx)
	for _, kept := range found {
		if lockLost(ctx) {
			return nil
		}
		release, failure := foundRelease(kept)
		release.TMDBID, release.TVDBID, release.IMDBID = group[0].TMDBID, group[0].TVDBID, group[0].IMDBID
		matches := matcher.Candidates(release, group)
		if failure != "" || !provesAnUncoveredFile(matches, covered) {
			result.Skipped++
			result.count(cmp.Or(failure, ReasonTorznabNoExactSize), 1)
			continue
		}
		candidate, err := candidateOf(release)
		if err != nil {
			return err
		}
		candidate.Source = string(kept.Source)
		_, outcome, err := deps.Store.SaveFetchedCandidate(persistContext, candidate, candidateFiles(release, matches), kept.Fetched.Sanitized, kept.Fetched.Sealed, now)
		if err != nil {
			return err
		}
		countOutcome(result, outcome)
	}
	return nil
}

// foundRelease relit les fichiers de la copie assainie : le .torrent enregistré est celui dont les fichiers sont associés.
func foundRelease(kept sources.Found) (libraryprovider.Release, string) {
	parsed, err := torrentcheck.Parse(kept.Fetched.Sanitized)
	if err != nil || parsed.InfoHash != kept.Release.Infohash {
		return libraryprovider.Release{}, ReasonInvalidTorrent
	}
	files := make([]libraryprovider.ReleaseFile, 0, len(parsed.Files))
	for _, file := range parsed.Files {
		files = append(files, libraryprovider.ReleaseFile{Path: file.Path, Size: file.Size})
	}
	return libraryprovider.Release{
		Infohash: kept.Release.Infohash, Tracker: kept.Release.Tracker, Name: kept.Release.Name, TotalSize: kept.TotalSize, Files: files,
	}, ""
}

// provesAnUncoveredFile dit si un fichier du .torrent a la taille exacte d'un fichier local encore sans candidat.
func provesAnUncoveredFile(matches []matcher.FileMatch, covered map[int64]bool) bool {
	for _, match := range matches {
		for _, local := range match.LocalFiles {
			if !covered[local.ID] {
				return true
			}
		}
	}
	return false
}
