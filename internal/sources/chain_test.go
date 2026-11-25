package sources

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"necromanciarr/internal/libraryprovider"
	"necromanciarr/internal/torznab"
)

// callLog note, dans l'ordre global, chaque appel reçu par une source.
type callLog struct {
	mutex sync.Mutex
	calls []string
}

func (log *callLog) add(call string) {
	log.mutex.Lock()
	defer log.mutex.Unlock()
	log.calls = append(log.calls, call)
}

type fakeProvider struct {
	log     *callLog
	torrent []byte
	failure error
}

func (provider *fakeProvider) Search(context.Context, libraryprovider.Query) ([]libraryprovider.Release, error) {
	provider.log.add("provider.search")
	return []libraryprovider.Release{{Infohash: "a", Name: "n"}}, provider.failure
}

func (provider *fakeProvider) Torrent(context.Context, string, string) ([]byte, error) {
	provider.log.add("provider.torrent")
	return provider.torrent, provider.failure
}

func (provider *fakeProvider) Nfos(context.Context, string, string) ([]libraryprovider.Nfo, error) {
	provider.log.add("provider.nfos")
	return []libraryprovider.Nfo{{Label: "nfo"}}, provider.failure
}

type fakeIndexer struct{ log *callLog }

func (indexer *fakeIndexer) Caps(context.Context) (torznab.Caps, error) {
	indexer.log.add("indexer.caps")
	return torznab.Caps{}, nil
}

func (indexer *fakeIndexer) Search(context.Context, torznab.Query) (torznab.Results, error) {
	indexer.log.add("indexer.search")
	return torznab.Results{}, nil
}

func (indexer *fakeIndexer) Download(context.Context, torznab.Item) ([]byte, error) {
	indexer.log.add("indexer.download")
	return nil, nil
}

func newTestChain(t *testing.T, provider ProviderSource, order []ID) *Chain {
	t.Helper()
	log := &callLog{}
	chain, err := NewChain(provider, map[ID]IndexerSource{indexerA: &fakeIndexer{log: log}, indexerB: &fakeIndexer{log: log}}, order,
		map[ID]Limits{indexerA: testLimits(), indexerB: testLimits()}, openUsageStore(t, filepath.Join(t.TempDir(), "necromanciarr.db")), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return chain
}

func TestChain_queriesProviderBeforeIndexers(t *testing.T) {
	log := &callLog{}
	provider := &fakeProvider{log: log, torrent: indexerTorrent()}
	indexers := map[ID]IndexerSource{indexerA: &fakeIndexer{log: log}, indexerB: &fakeIndexer{log: log}}
	chain, err := NewChain(provider, indexers, []ID{ProviderID, indexerB, indexerA},
		map[ID]Limits{indexerA: testLimits(), indexerB: testLimits()}, openUsageStore(t, filepath.Join(t.TempDir(), "necromanciarr.db")), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if _, err := chain.Search(ctx, libraryprovider.Query{TMDBID: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := chain.Nfos(ctx, "a", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := chain.FetchTorrent(ctx, Release{Infohash: "a"}, chain.NewBudget()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := chain.SearchCandidates(ctx, MediaQuery{TMDBID: 1}, chain.NewBudget()); err != nil {
		t.Fatal(err)
	}

	valeurAttendue := []string{"provider.search", "provider.nfos", "provider.torrent"}
	if valeurObtenue := log.calls; !slices.Equal(valeurObtenue, valeurAttendue) {
		t.Fatalf("valeurObtenue %v, valeurAttendue %v: the provider alone answers, no indexer is called before B10/B12", valeurObtenue, valeurAttendue)
	}
	if valeurObtenue := chain.Order(); !slices.Equal(valeurObtenue, []ID{ProviderID, indexerB, indexerA}) {
		t.Fatalf("valeurObtenue order %v, valeurAttendue the configured order, provider first", valeurObtenue)
	}
}

func TestChain_refusesOrderWithoutProviderFirst(t *testing.T) {
	provider := &fakeProvider{log: &callLog{}}
	limits := map[ID]Limits{indexerA: testLimits()}
	indexers := map[ID]IndexerSource{indexerA: &fakeIndexer{log: &callLog{}}}
	usage := openUsageStore(t, filepath.Join(t.TempDir(), "necromanciarr.db"))
	for _, testCase := range []struct {
		name     string
		provider ProviderSource
		order    []ID
		indexers map[ID]IndexerSource
	}{
		{"indexer first", provider, []ID{indexerA, ProviderID}, indexers},
		{"indexer alone", provider, []ID{indexerA}, indexers},
		{"empty order", provider, nil, indexers},
		{"missing indexer", provider, []ID{ProviderID, indexerB}, indexers},
		{"provider listed twice", provider, []ID{ProviderID, ProviderID, indexerA}, indexers},
		{"no provider", nil, []ID{ProviderID, indexerA}, indexers},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			chain, err := NewChain(testCase.provider, testCase.indexers, testCase.order, limits, usage, nil, nil)

			if err == nil || chain != nil {
				t.Fatalf("valeurObtenue chain %v err %v, valeurAttendue a refusal", chain, err)
			}
		})
	}
}

func TestNewChain_refusesIndexerWithoutLimitsOrUsageStore(t *testing.T) {
	provider := &fakeProvider{log: &callLog{}}
	indexers := map[ID]IndexerSource{indexerA: &fakeIndexer{log: &callLog{}}}
	if _, err := NewChain(provider, indexers, []ID{ProviderID, indexerA}, nil, realUsage(t), nil, nil); err == nil {
		t.Fatal("valeurAttendue a refusal: no limits for the indexer")
	}
	if _, err := NewChain(provider, indexers, []ID{ProviderID, indexerA}, map[ID]Limits{indexerA: testLimits()}, nil, nil, nil); err == nil {
		t.Fatal("valeurAttendue a refusal: indexers need a usage store")
	}
}

func TestNewChain_acceptsTheProviderAlone(t *testing.T) {
	chain, err := NewChain(&fakeProvider{log: &callLog{}}, nil, []ID{ProviderID}, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("valeurObtenue %v, valeurAttendue a provider-only chain without usage store", err)
	}
	if report := chain.NewBudget().Report(); len(report) != 0 {
		t.Fatalf("valeurObtenue %v, valeurAttendue an empty report", report)
	}
}

func TestChain_fetchTorrentFromProviderIsSanitizedAndUnsealed(t *testing.T) {
	chain := newTestChain(t, &fakeProvider{log: &callLog{}, torrent: indexerTorrent()}, []ID{ProviderID, indexerA})

	fetched, err := chain.FetchTorrent(context.Background(), Release{Infohash: "a", Tracker: "t"}, chain.NewBudget())
	if err != nil {
		t.Fatal(err)
	}

	if fetched.Source != ProviderID || fetched.Sealed != nil {
		t.Fatalf("valeurObtenue source %q sealed %v, valeurAttendue the provider and no sealed copy", fetched.Source, fetched.Sealed)
	}
	expectedSanitized, err := Sanitize(indexerTorrent())
	if err != nil || !bytes.Equal(fetched.Sanitized, expectedSanitized) || bytes.Contains(fetched.Sanitized, []byte(fakePasskey)) {
		t.Fatalf("valeurObtenue sanitized %q (err %v), valeurAttendue the sanitized provider torrent", fetched.Sanitized, err)
	}
}

func TestChain_fetchTorrentKeepsProviderErrorsAndRefusesUnsanitizable(t *testing.T) {
	failure := libraryprovider.ErrNotFound
	chain := newTestChain(t, &fakeProvider{log: &callLog{}, failure: failure}, []ID{ProviderID, indexerA})
	if _, err := chain.FetchTorrent(context.Background(), Release{Infohash: "a"}, chain.NewBudget()); !errors.Is(err, failure) {
		t.Fatalf("valeurObtenue %v, valeurAttendue the provider error unchanged", err)
	}

	chain = newTestChain(t, &fakeProvider{log: &callLog{}, torrent: []byte("not bencode")}, []ID{ProviderID, indexerA})
	if _, err := chain.FetchTorrent(context.Background(), Release{Infohash: "a"}, nil); !errors.Is(err, ErrInvalidTorrent) {
		t.Fatalf("valeurObtenue %v, valeurAttendue ErrInvalidTorrent (and a nil budget is accepted for the provider)", err)
	}
}

func TestChain_searchCandidatesIsAStubUntilB12(t *testing.T) {
	chain := newTestChain(t, &fakeProvider{log: &callLog{}}, []ID{ProviderID, indexerA})

	found, report, err := chain.SearchCandidates(context.Background(), MediaQuery{TMDBID: 1}, chain.NewBudget())

	if err != nil || len(found) != 0 || len(report) != 0 {
		t.Fatalf("valeurObtenue %v %v %v, valeurAttendue nothing", found, report, err)
	}
}

func TestChain_newBudgetUsesTheConfiguredLimits(t *testing.T) {
	limits := testLimits()
	limits.MinInterval, limits.MaxQueriesPerRun = 0, 1
	chain, err := NewChain(&fakeProvider{log: &callLog{}}, map[ID]IndexerSource{indexerA: &fakeIndexer{log: &callLog{}}}, []ID{ProviderID, indexerA},
		map[ID]Limits{indexerA: limits}, realUsage(t), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	budget := chain.NewBudget()

	_ = budget.TakeQuery(context.Background(), indexerA)

	if err := budget.TakeQuery(context.Background(), indexerA); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("valeurObtenue %v, valeurAttendue ErrBudgetExhausted after the per-run cap of 1", err)
	}
	if again := chain.NewBudget().TakeQuery(context.Background(), indexerA); again != nil {
		t.Fatalf("valeurObtenue %v, valeurAttendue per-run counters to restart with a new budget", again)
	}
}

func newClockChain(t *testing.T, clock *fakeClock) *Chain {
	t.Helper()
	limits := testLimits()
	limits.MinInterval = 0
	chain, err := NewChain(&fakeProvider{log: &callLog{}}, map[ID]IndexerSource{indexerA: &fakeIndexer{log: &callLog{}}},
		[]ID{ProviderID, indexerA}, map[ID]Limits{indexerA: limits}, realUsage(t), nil, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	return chain
}

// Un Retry-After reçu pendant Match vaut aussi pour le Verify qui suit : il vit sur la chaîne, pas sur le budget.
func TestChain_retryAfterCarriesAcrossBudgetsOfTheSameProcess(t *testing.T) {
	clock := &fakeClock{now: budgetNow}
	chain := newClockChain(t, clock)
	rateLimited := &torznab.UnavailableError{RetryAfter: 30 * time.Minute}
	if err := chain.Call(context.Background(), chain.NewBudget(), indexerA, KindQuery, func(context.Context) error { return rateLimited }); !errors.Is(err, torznab.ErrUnavailable) {
		t.Fatalf("valeurObtenue %v, valeurAttendue the request error returned", err)
	}
	clock.Advance(time.Minute)

	requested := false
	err := chain.Call(context.Background(), chain.NewBudget(), indexerA, KindDownload, func(context.Context) error { requested = true; return nil })

	if !errors.Is(err, torznab.ErrUnavailable) || requested {
		t.Fatalf("valeurObtenue err %v requested %v, valeurAttendue a new budget refused during Retry-After", err, requested)
	}
	clock.Advance(30 * time.Minute)
	if err := chain.Call(context.Background(), chain.NewBudget(), indexerA, KindDownload, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("valeurObtenue %v, valeurAttendue the indexer back once Retry-After has passed", err)
	}
}

func TestChain_callSpendsBeforeTheRequestAndObservesItsError(t *testing.T) {
	clock := &fakeClock{now: budgetNow}
	chain := newClockChain(t, clock)
	budget := chain.NewBudget()
	failure := errors.New("request failed")

	err := chain.Call(context.Background(), budget, indexerA, KindQuery, func(context.Context) error {
		if budget.Report()[indexerA].Queries != 1 {
			t.Error("valeurAttendue the quota spent before the request runs")
		}
		return failure
	})

	if !errors.Is(err, failure) {
		t.Fatalf("valeurObtenue %v, valeurAttendue the request error unchanged", err)
	}
	if err := chain.Call(context.Background(), budget, indexerA, KindQuery, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("valeurObtenue %v, valeurAttendue an ordinary error not to block the indexer", err)
	}
}

func TestChain_callDoesNotRunTheRequestWhenTheBudgetRefuses(t *testing.T) {
	clock := &fakeClock{now: budgetNow}
	chain := newClockChain(t, clock)
	budget := chain.NewBudget()
	chain.Call(context.Background(), budget, indexerA, KindQuery, func(context.Context) error { return torznab.ErrAccessDenied }) //nolint:errcheck // l'erreur est observée plus bas

	requested := false
	err := chain.Call(context.Background(), budget, indexerA, KindQuery, func(context.Context) error { requested = true; return nil })

	if !errors.Is(err, torznab.ErrAccessDenied) || requested {
		t.Fatalf("valeurObtenue err %v requested %v, valeurAttendue refusal without a request", err, requested)
	}
	if err := chain.Call(context.Background(), nil, indexerA, KindQuery, func(context.Context) error { return nil }); err == nil {
		t.Fatal("valeurAttendue an error for a nil budget")
	}
}

func TestNewChain_keepsLimitsOfCitedIndexersOnly(t *testing.T) {
	limits := map[ID]Limits{indexerA: testLimits(), indexerB: testLimits()}
	chain, err := NewChain(&fakeProvider{log: &callLog{}}, map[ID]IndexerSource{indexerA: &fakeIndexer{log: &callLog{}}},
		[]ID{ProviderID, indexerA}, limits, realUsage(t), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	limits[indexerA] = Limits{}

	budget := chain.NewBudget()

	if err := budget.TakeQuery(context.Background(), indexerB); !errors.Is(err, ErrUnknownIndexer) {
		t.Fatalf("valeurObtenue %v, valeurAttendue ErrUnknownIndexer for a non-cited indexer", err)
	}
	if err := budget.TakeQuery(context.Background(), indexerA); err != nil {
		t.Fatalf("valeurObtenue %v, valeurAttendue the limits copied at construction", err)
	}
}
