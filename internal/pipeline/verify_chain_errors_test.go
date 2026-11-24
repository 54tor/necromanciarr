package pipeline

import (
	"fmt"
	"testing"

	"necromanciarr/internal/sources"
	"necromanciarr/internal/store"
	"necromanciarr/internal/torrentcheck"
	"necromanciarr/internal/torznab"
)

// Les erreurs de contenu de la chaîne deviennent la raison du candidat ; le v2 seul passe avant ErrBadResponse qui
// l'englobe chez Torznab.
func TestVerifyStage_mapsSourceChainContentErrorsToReasons(t *testing.T) {
	for _, testCase := range []struct {
		name           string
		failure        error
		expectedReason string
	}{
		{"v2 only wrapped in a bad response", fmt.Errorf("%w: %w", torznab.ErrBadResponse, torrentcheck.ErrV2Only), ReasonV2Only},
		{"invalid torrent from sanitizing", sources.ErrInvalidTorrent, ReasonInvalidTorrent},
		{"invalid torrent from the indexer", fmt.Errorf("%w: %w", torznab.ErrBadResponse, torrentcheck.ErrInvalidTorrent), ReasonInvalidTorrent},
		{"infohash mismatch", torznab.ErrInfohashMismatch, ReasonInfohashMismatch},
		{"other bad response", torznab.ErrBadResponse, ReasonTorrentFetchFailed},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newVerifyFixture(t)
			candidate := fixture.addCandidate("Movie.2020", "", movieOnly())
			fixture.fetcher.torrentErrs[candidate.Infohash] = testCase.failure

			result := fixture.mustRun()

			if result.Reasons[testCase.expectedReason] != 1 || fixture.candidate(candidate.Infohash).State != store.CandidateFailed {
				t.Fatalf("valeurObtenue %+v, valeurAttendue failed with %s", result, testCase.expectedReason)
			}
		})
	}
}
