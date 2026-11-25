package pipeline

import (
	"context"
	"errors"

	"necromanciarr/internal/sources"
	"necromanciarr/internal/store"
	"necromanciarr/internal/torrentcheck"
	"necromanciarr/internal/torznab"
)

// errCandidateDeferred : la recherche du .torrent chez les indexeurs n'a pas pu aller au bout (budget, indexeur
// indisponible ou refusant) ; le candidat garde son état et sera repris, ce n'est pas un échec.
var errCandidateDeferred = errors.New("pipeline: candidate deferred to the next run")

// recordFallback compte ce que le secours par les indexeurs a rencontré et note les sources interrogées ; deferred
// est vrai quand la recherche est incomplète (candidat sauté, repris à la prochaine exécution).
func (run candidateRun) recordFallback(candidate store.Candidate, err error) (deferred bool) {
	var fallback *sources.FallbackError
	if !errors.As(err, &fallback) {
		return false
	}
	if run.result.SearchedSources == nil {
		run.result.SearchedSources = map[int64][]sources.ID{}
	}
	run.result.SearchedSources[candidate.ID] = fallback.Searched
	run.result.count(ReasonMagnetOnly, fallback.MagnetOnly)
	for _, refusal := range append(fallback.Skipped, fallback.Interrupted...) {
		run.result.count(indexerReason(refusal), 1)
	}
	if fallback.Incomplete() {
		run.result.Skipped++
		return true
	}
	return false
}

// indexerReason nomme un refus d'indexeur ou d'élément ; le v2 seul passe avant tout : torznab.ErrBadResponse l'englobe.
func indexerReason(err error) string {
	switch {
	case errors.Is(err, sources.ErrBudgetExhausted):
		return ReasonIndexerBudgetExhausted
	case errors.Is(err, sources.ErrNoExactSize):
		return ReasonTorznabNoExactSize
	case errors.Is(err, torrentcheck.ErrV2Only):
		return ReasonV2Only
	case errors.Is(err, sources.ErrInvalidTorrent) || errors.Is(err, torrentcheck.ErrInvalidTorrent):
		return ReasonInvalidTorrent
	case errors.Is(err, torznab.ErrInfohashMismatch):
		return ReasonInfohashMismatch
	case errors.Is(err, torznab.ErrForeignDownloadHost):
		return ReasonForeignDownloadHost
	case errors.Is(err, sources.ErrTorrentTooLarge):
		return ReasonTorrentTooLarge
	case errors.Is(err, torznab.ErrHostileResponse):
		return ReasonIndexerHostileResponse
	case errors.Is(err, torznab.ErrAccessDenied):
		return ReasonIndexerAccessDenied
	case errors.Is(err, torznab.ErrUnavailable):
		return ReasonIndexerUnavailable
	case errors.Is(err, torznab.ErrBadResponse):
		// Réponse illisible (page HTML, mauvais type, corps trop gros) : pas de raison dédiée dans la spec.
		return ReasonTorrentFetchFailed
	default:
		return ReasonTorrentFetchFailed
	}
}

// saveFetched conserve la copie assainie ; un .torrent d'indexeur est enregistré avec sa source et son scellement
// (vide quand une entrée trackers donne l'annonce) en une seule écriture, la liaison du scellement reposant sur
// cette source.
func (run candidateRun) saveFetched(ctx context.Context, candidate store.Candidate, fetched sources.Fetched, sanitized []byte) error {
	if fetched.Source == sources.ProviderID || fetched.Source == "" {
		return run.deps.Store.SaveCandidateTorrent(ctx, candidate.ID, sanitized, run.deps.now())
	}
	return run.deps.Store.SaveCandidateFetched(ctx, candidate.ID, string(fetched.Source), sanitized, fetched.Sealed, run.deps.now())
}
