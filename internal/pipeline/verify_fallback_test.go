package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"necromanciarr/internal/sources"
	"necromanciarr/internal/store"
	"necromanciarr/internal/torrentcheck"
	"necromanciarr/internal/torznab"
)

const (
	fallbackTrackerName = "tracker-a"
	fallbackPasskey     = "fake-passkey-fall"
	indexerSameTracker  = sources.ID("torznab:a")
)

// fallbackIndexer sert des .torrent par GUID et compte chaque appel : un indexeur jamais sollicité reste à zéro.
type fallbackIndexer struct {
	mutex        sync.Mutex
	results      torznab.Results
	searchErr    error
	torrents     map[string][]byte
	downloadErrs map[string]error
	searches     int
	downloads    int
}

func newFallbackIndexer() *fallbackIndexer {
	return &fallbackIndexer{torrents: map[string][]byte{}, downloadErrs: map[string]error{}}
}

func (indexer *fallbackIndexer) Caps(context.Context) (torznab.Caps, error) {
	return torznab.Caps{}, nil
}

func (indexer *fallbackIndexer) Search(context.Context, torznab.Query) (torznab.Results, error) {
	indexer.mutex.Lock()
	defer indexer.mutex.Unlock()
	indexer.searches++
	return indexer.results, indexer.searchErr
}

func (indexer *fallbackIndexer) Download(_ context.Context, item torznab.Item) ([]byte, error) {
	indexer.mutex.Lock()
	defer indexer.mutex.Unlock()
	indexer.downloads++
	if err := indexer.downloadErrs[item.GUID]; err != nil {
		return nil, err
	}
	return bytes.Clone(indexer.torrents[item.GUID]), nil
}

func (indexer *fallbackIndexer) calls() int {
	indexer.mutex.Lock()
	defer indexer.mutex.Unlock()
	return indexer.searches + indexer.downloads
}

// withPasskeyAnnounce rend le .torrent tel qu'un indexeur le sert : même info, annonce avec passkey.
func withPasskeyAnnounce(t *testing.T, raw []byte) []byte {
	t.Helper()
	metaInfo, err := metainfo.Load(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	metaInfo.Announce = "https://tracker-a.example.com/" + fallbackPasskey + "/announce"
	served, err := bencode.Marshal(metaInfo)
	if err != nil {
		t.Fatal(err)
	}
	return served
}

type fallbackCase struct {
	fixture  *verifyFixture
	indexers map[sources.ID]*fallbackIndexer
	order    []sources.ID
	trackers map[sources.ID]string
	limits   sources.Limits
}

func newFallbackCase(t *testing.T) *fallbackCase {
	t.Helper()
	fixture := newVerifyFixture(t)
	fixture.sealer = testSealer(t)
	limits := sources.Limits{MaxQueriesPerRun: 30, MaxDownloadsPerRun: 10, MaxQueriesPerDay: 200, MaxDownloadsPerDay: 50}
	return &fallbackCase{
		fixture: fixture, indexers: map[sources.ID]*fallbackIndexer{}, order: []sources.ID{sources.ProviderID},
		trackers: map[sources.ID]string{}, limits: limits,
	}
}

func (testCase *fallbackCase) addIndexer(identifier sources.ID, tracker string) *fallbackIndexer {
	indexer := newFallbackIndexer()
	testCase.indexers[identifier] = indexer
	testCase.order = append(testCase.order, identifier)
	if tracker != "" {
		testCase.trackers[identifier] = tracker
	}
	return indexer
}

// missingAtProvider ajoute un candidat que le fournisseur ne connaît pas ; rend aussi le .torrent servi par un indexeur.
func (testCase *fallbackCase) missingAtProvider(name string) (store.Candidate, []byte) {
	fixture := testCase.fixture
	candidate := fixture.addCandidate(name, fallbackTrackerName, movieOnly())
	served := withPasskeyAnnounce(fixture.t, fixture.fetcher.torrents[candidate.Infohash])
	delete(fixture.fetcher.torrents, candidate.Infohash)
	return candidate, served
}

func (testCase *fallbackCase) run() Result {
	t := testCase.fixture.t
	t.Helper()
	indexers, limits := map[sources.ID]sources.IndexerSource{}, map[sources.ID]sources.Limits{}
	for identifier, indexer := range testCase.indexers {
		indexers[identifier], limits[identifier] = indexer, testCase.limits
	}
	chain, err := sources.NewChain(providerOnly{testCase.fixture.fetcher}, indexers, testCase.order, limits, testCase.fixture.store,
		testCase.fixture.sealer, func() time.Time { return fixedNow })
	if err != nil {
		t.Fatal(err)
	}
	chain.SetFallbackTrackers(testCase.trackers)
	testCase.fixture.chain = chain
	return testCase.fixture.mustRun()
}

func storedCandidate(t *testing.T, fixture *verifyFixture, candidateID int64) store.Candidate {
	t.Helper()
	candidates, err := fixture.store.ListCandidatesByState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range candidates {
		if candidate.ID == candidateID {
			return candidate
		}
	}
	t.Fatalf("candidate %d not found", candidateID)
	return store.Candidate{}
}

func TestVerify_fallsBackToIndexerOfSameTracker(t *testing.T) {
	testCase := newFallbackCase(t)
	candidate, served := testCase.missingAtProvider("Movie.2020")
	indexer := testCase.addIndexer(indexerSameTracker, "Tracker-A")
	indexer.results = torznab.Results{Items: []torznab.Item{{GUID: "win"}}}
	indexer.torrents["win"] = served

	result := testCase.run()

	stored := storedCandidate(t, testCase.fixture, candidate.ID)
	if result.Verified != 1 || stored.State != store.CandidateVerified || stored.Source != string(indexerSameTracker) {
		t.Fatalf("valeurObtenue %+v state %s source %q, valeurAttendue verified from %s", result, stored.State, stored.Source, indexerSameTracker)
	}
}

func TestVerify_skipsIndexersOfOtherTrackersAndWithoutTracker(t *testing.T) {
	testCase := newFallbackCase(t)
	candidate, served := testCase.missingAtProvider("Movie.2020")
	other := testCase.addIndexer("torznab:b", "tracker-b")
	untracked := testCase.addIndexer("torznab:c", "")
	for _, indexer := range []*fallbackIndexer{other, untracked} {
		indexer.results = torznab.Results{Items: []torznab.Item{{GUID: "win"}}}
		indexer.torrents["win"] = served
	}

	result := testCase.run()

	if result.Reasons[ReasonTorrentNotFound] != 1 || testCase.fixture.candidate(candidate.Infohash).State != store.CandidateFailed {
		t.Fatalf("valeurObtenue %+v, valeurAttendue torrent_not_found as in v1", result)
	}
	if other.calls() != 0 || untracked.calls() != 0 {
		t.Fatalf("valeurObtenue %d and %d calls, valeurAttendue none", other.calls(), untracked.calls())
	}
}

func TestVerify_reportsSearchedSourcesWhenNotFound(t *testing.T) {
	testCase := newFallbackCase(t)
	candidate, _ := testCase.missingAtProvider("Movie.2020")
	testCase.addIndexer(indexerSameTracker, fallbackTrackerName)

	result := testCase.run()

	valeurAttendue := []sources.ID{sources.ProviderID, indexerSameTracker}
	if valeurObtenue := result.SearchedSources[candidate.ID]; !slices.Equal(valeurObtenue, valeurAttendue) || result.Reasons[ReasonTorrentNotFound] != 1 {
		t.Fatalf("valeurObtenue %v (%+v), valeurAttendue %v with torrent_not_found", valeurObtenue, result, valeurAttendue)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if text := string(encoded); !strings.Contains(text, `"searched_sources"`) || strings.Contains(text, "://") || strings.Contains(text, "Movie.2020") {
		t.Fatalf("valeurObtenue %s, valeurAttendue source ids only", text)
	}
}

func TestVerify_fallbackStoresSanitizedAndSealed(t *testing.T) {
	testCase := newFallbackCase(t)
	candidate, served := testCase.missingAtProvider("Movie.2020")
	indexer := testCase.addIndexer(indexerSameTracker, fallbackTrackerName)
	indexer.results = torznab.Results{Items: []torznab.Item{{GUID: "win"}}}
	indexer.torrents["win"] = served

	testCase.run()

	ctx := context.Background()
	torrent, err := testCase.fixture.store.CandidateTorrent(ctx, candidate.ID)
	if err != nil || bytes.Contains(torrent, []byte("announce")) || bytes.Contains(torrent, []byte(fallbackPasskey)) || infohashOf(t, torrent) != candidate.Infohash {
		t.Fatalf("valeurObtenue torrent %q (err %v), valeurAttendue the sanitized copy of the same infohash", torrent, err)
	}
	sealed, found, err := testCase.fixture.store.CandidateSealed(ctx, candidate.ID)
	if err != nil || !found {
		t.Fatalf("valeurObtenue found %v err %v, valeurAttendue a sealed copy", found, err)
	}
	stored := storedCandidate(t, testCase.fixture, candidate.ID)
	unsealed, err := testCase.fixture.sealer.Unseal(sealed, sources.Binding{Infohash: stored.Infohash, Tracker: stored.Tracker, Source: sources.ID(stored.Source)})
	if err != nil || !bytes.Equal(unsealed, served) {
		t.Fatalf("valeurObtenue unsealed %q (err %v), valeurAttendue the torrent the indexer served", unsealed, err)
	}
}

func TestVerify_fallbackStopsWhenBudgetExhausted(t *testing.T) {
	testCase := newFallbackCase(t)
	testCase.limits.MaxQueriesPerRun = 1
	first, _ := testCase.missingAtProvider("Movie.2020")
	second, _ := testCase.missingAtProvider("Other.2021")
	indexer := testCase.addIndexer(indexerSameTracker, fallbackTrackerName)

	result := testCase.run()

	if result.Reasons[ReasonIndexerBudgetExhausted] != 1 || result.Reasons[ReasonTorrentNotFound] != 1 || indexer.calls() != 1 {
		t.Fatalf("valeurObtenue %+v calls %d, valeurAttendue one search, then the budget reason", result, indexer.calls())
	}
	states := []store.CandidateState{testCase.fixture.candidate(first.Infohash).State, testCase.fixture.candidate(second.Infohash).State}
	if !slices.Contains(states, store.CandidateMatched) || !slices.Contains(states, store.CandidateFailed) {
		t.Fatalf("valeurObtenue states %v, valeurAttendue the deferred candidate still matched", states)
	}
}

func TestVerify_fallbackWithoutSecretKeyStoresNothing(t *testing.T) {
	testCase := newFallbackCase(t)
	testCase.fixture.sealer = nil
	candidate, served := testCase.missingAtProvider("Movie.2020")
	indexer := testCase.addIndexer(indexerSameTracker, fallbackTrackerName)
	indexer.results = torznab.Results{Items: []torznab.Item{{GUID: "win"}}}
	indexer.torrents["win"] = served

	result := testCase.run()

	if result.Reasons[ReasonSecretKeyMissing] != 1 || indexer.calls() != 0 || testCase.fixture.candidate(candidate.Infohash).State != store.CandidateFailed {
		t.Fatalf("valeurObtenue %+v calls %d, valeurAttendue secret_key_missing without any indexer call", result, indexer.calls())
	}
	torrent, _ := testCase.fixture.store.CandidateTorrent(context.Background(), candidate.ID)
	_, found, _ := testCase.fixture.store.CandidateSealed(context.Background(), candidate.ID)
	if torrent != nil || found {
		t.Fatal("valeurAttendue nothing stored")
	}
}

func TestVerify_fallbackOnlyOnTorrentNotFound(t *testing.T) {
	testCase := newFallbackCase(t)
	candidate, served := testCase.missingAtProvider("Movie.2020")
	testCase.fixture.fetcher.torrentErrs[candidate.Infohash] = errors.New("provider failed")
	indexer := testCase.addIndexer(indexerSameTracker, fallbackTrackerName)
	indexer.results = torznab.Results{Items: []torznab.Item{{GUID: "win"}}}
	indexer.torrents["win"] = served

	result := testCase.run()

	if result.Reasons[ReasonTorrentFetchFailed] != 1 || indexer.calls() != 0 {
		t.Fatalf("valeurObtenue %+v calls %d, valeurAttendue the provider failure and no indexer call", result, indexer.calls())
	}
}

// Les refus ne sont comptés que pour un candidat resté introuvable : un candidat trouvé ne rapporte que son succès.
func TestVerify_fallbackCountsRefusedItemsAndStoresNone(t *testing.T) {
	testCase := newFallbackCase(t)
	candidate, _ := testCase.missingAtProvider("Movie.2020")
	otherCandidate, otherServed := testCase.missingAtProvider("Other.2021")
	indexer := testCase.addIndexer(indexerSameTracker, fallbackTrackerName)
	indexer.results = torznab.Results{MagnetOnly: 2, Items: []torznab.Item{{GUID: "v2"}, {GUID: "foreign"}, {GUID: "other"}}}
	indexer.downloadErrs["v2"] = v2OnlyFallbackError()
	indexer.downloadErrs["foreign"] = torznab.ErrForeignDownloadHost
	indexer.torrents["other"] = otherServed

	result := testCase.run()

	for reason, valeurAttendue := range map[string]int{
		ReasonMagnetOnly: 2, ReasonV2Only: 1, ReasonForeignDownloadHost: 1, ReasonInfohashMismatch: 1, ReasonTorrentNotFound: 1,
	} {
		if valeurObtenue := result.Reasons[reason]; valeurObtenue != valeurAttendue {
			t.Errorf("reason %s: valeurObtenue %d, valeurAttendue %d (%+v)", reason, valeurObtenue, valeurAttendue, result.Reasons)
		}
	}
	torrent, _ := testCase.fixture.store.CandidateTorrent(context.Background(), candidate.ID)
	_, found, _ := testCase.fixture.store.CandidateSealed(context.Background(), candidate.ID)
	if torrent != nil || found {
		t.Fatal("valeurAttendue no torrent stored for a candidate whose items were all refused")
	}
	if testCase.fixture.candidate(otherCandidate.Infohash).State != store.CandidateVerified {
		t.Fatal("valeurAttendue the item of the other infohash kept for its own candidate only")
	}
}

// v2OnlyFallbackError est ce que torznab.Download rend pour un .torrent v2 seul.
func v2OnlyFallbackError() error {
	return fmt.Errorf("%w: %w", torznab.ErrBadResponse, torrentcheck.ErrV2Only)
}

func TestVerify_fallbackIndexerFailureKeepsTheCandidateForTheNextRun(t *testing.T) {
	for reason, failure := range map[string]error{
		ReasonIndexerUnavailable:     &torznab.UnavailableError{},
		ReasonIndexerAccessDenied:    torznab.ErrAccessDenied,
		ReasonIndexerHostileResponse: torznab.ErrHostileResponse,
	} {
		t.Run(reason, func(t *testing.T) {
			testCase := newFallbackCase(t)
			candidate, _ := testCase.missingAtProvider("Movie.2020")
			testCase.addIndexer(indexerSameTracker, fallbackTrackerName).searchErr = failure

			result := testCase.run()

			if result.Reasons[reason] != 1 || result.Failed != 0 || testCase.fixture.candidate(candidate.Infohash).State != store.CandidateMatched {
				t.Fatalf("valeurObtenue %+v, valeurAttendue %s and the candidate still matched", result, reason)
			}
		})
	}
}

func TestVerify_fallbackHonoursRetryAfterOnTheNextCandidate(t *testing.T) {
	testCase := newFallbackCase(t)
	testCase.missingAtProvider("Movie.2020")
	testCase.missingAtProvider("Other.2021")
	indexer := testCase.addIndexer(indexerSameTracker, fallbackTrackerName)
	indexer.searchErr = &torznab.UnavailableError{RetryAfter: 10 * time.Minute}

	result := testCase.run()

	if indexer.calls() != 1 || result.Reasons[ReasonIndexerUnavailable] != 2 {
		t.Fatalf("valeurObtenue %+v calls %d, valeurAttendue one request, the second candidate skipped during Retry-After", result, indexer.calls())
	}
}
