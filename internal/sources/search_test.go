package sources

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"necromanciarr/internal/torrentcheck"
	"necromanciarr/internal/torznab"
)

const (
	gibibyte          = int64(1) << 30
	searchPieceLength = int64(64) << 20
	searchTitle       = "Movie Title"
)

// searchIndexer répond à toute recherche avec results (ou respond), sert les .torrent par GUID et note l'heure du faux
// horloge de chaque requête : la cadence se lit sans aucun vrai sommeil.
type searchIndexer struct {
	mutex        sync.Mutex
	caps         torznab.Caps
	capsErr      error
	results      torznab.Results
	respond      func(torznab.Query) torznab.Results
	torrents     map[string][]byte
	queries      []torznab.Query
	downloads    []string
	clock        *fakeClock
	requestTimes []time.Time
}

func newSearchIndexer(caps torznab.Caps) *searchIndexer {
	return &searchIndexer{caps: caps, torrents: map[string][]byte{}}
}

func (indexer *searchIndexer) note() {
	if indexer.clock != nil {
		indexer.requestTimes = append(indexer.requestTimes, indexer.clock.Now())
	}
}

func (indexer *searchIndexer) Caps(context.Context) (torznab.Caps, error) {
	indexer.mutex.Lock()
	defer indexer.mutex.Unlock()
	indexer.note()
	return indexer.caps, indexer.capsErr
}

func (indexer *searchIndexer) Search(_ context.Context, query torznab.Query) (torznab.Results, error) {
	indexer.mutex.Lock()
	defer indexer.mutex.Unlock()
	indexer.note()
	indexer.queries = append(indexer.queries, query)
	if indexer.respond != nil {
		return indexer.respond(query), nil
	}
	return indexer.results, nil
}

func (indexer *searchIndexer) Download(_ context.Context, item torznab.Item) ([]byte, error) {
	indexer.mutex.Lock()
	defer indexer.mutex.Unlock()
	indexer.note()
	indexer.downloads = append(indexer.downloads, item.GUID)
	return bytes.Clone(indexer.torrents[item.GUID]), nil
}

func (indexer *searchIndexer) serve(guid string, size int64, raw []byte) torznab.Item {
	indexer.torrents[guid] = raw
	return torznab.Item{GUID: guid, Title: "Release " + guid, Size: size}
}

func movieCaps() torznab.Caps {
	return torznab.Caps{
		Modes:      map[string][]string{"search": {"q"}, "movie": {"q", "imdbid", "tmdbid"}},
		Categories: []int{2000, 2040, 5000, 5040},
	}
}

// movieCategories et tvCategories sont les catégories films et séries de movieCaps et tvCaps.
var (
	movieCategories = []int{2000, 2040}
	tvCategories    = []int{5000, 5040}
)

func tvCaps() torznab.Caps {
	return torznab.Caps{
		Modes:      map[string][]string{"search": {"q"}, "tvsearch": {"q", "tvdbid", "season", "ep"}},
		Categories: []int{2000, 5000, 5040},
	}
}

// sizedTorrent est un .torrent v1 valide aux fichiers de ces tailles (un seul : fichier unique), annonce avec passkey.
func sizedTorrent(t *testing.T, name string, sizes ...int64) []byte {
	t.Helper()
	info := metainfo.Info{Name: name, PieceLength: searchPieceLength}
	var total int64
	for fileIndex, size := range sizes {
		total += size
		if len(sizes) > 1 {
			info.Files = append(info.Files, metainfo.FileInfo{Path: []string{fmt.Sprintf("file%d.mkv", fileIndex)}, Length: size})
		}
	}
	if len(sizes) == 1 {
		info.Length = sizes[0]
	}
	pieceCount := (total + searchPieceLength - 1) / searchPieceLength
	info.Pieces = bytes.Repeat([]byte("A"), int(pieceCount)*20)
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := bencode.Marshal(metainfo.MetaInfo{InfoBytes: infoBytes, Announce: "https://tracker-a.example.com/" + fakePasskey + "/announce"})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type searchSetup struct {
	indexers   map[ID]*searchIndexer
	order      []ID
	trackers   map[ID]string
	categories map[ID][]int
	limits     Limits
	sealer     *Sealer
	clock      *fakeClock
}

func newSearchSetup(t *testing.T) *searchSetup {
	t.Helper()
	limits := testLimits()
	limits.MinInterval = 0
	return &searchSetup{
		indexers: map[ID]*searchIndexer{}, order: []ID{ProviderID}, trackers: map[ID]string{}, categories: map[ID][]int{},
		limits: limits, sealer: testSealer(t),
	}
}

func (setup *searchSetup) add(identifier ID, caps torznab.Caps) *searchIndexer {
	indexer := newSearchIndexer(caps)
	setup.indexers[identifier] = indexer
	setup.order = append(setup.order, identifier)
	return indexer
}

func (setup *searchSetup) chain(t *testing.T) *Chain {
	t.Helper()
	indexers, limits := map[ID]IndexerSource{}, map[ID]Limits{}
	for identifier, indexer := range setup.indexers {
		indexers[identifier], limits[identifier] = indexer, setup.limits
	}
	var now func() time.Time
	if setup.clock != nil {
		now = setup.clock.Now
		for _, indexer := range setup.indexers {
			indexer.clock = setup.clock
		}
	}
	chain, err := NewChain(&fakeProvider{log: &callLog{}}, indexers, setup.order, limits, realUsage(t), setup.sealer, now)
	if err != nil {
		t.Fatal(err)
	}
	if setup.clock != nil {
		chain.sleep = setup.clock.Sleep
	}
	chain.SetFallbackTrackers(setup.trackers)
	chain.SetSearchCategories(setup.categories)
	return chain
}

func movieQuery(sizes ...int64) MediaQuery {
	return MediaQuery{TMDBID: 603, IMDBID: "tt0133093", Title: searchTitle, Year: 1999, Sizes: sizes, Uncovered: sizes}
}

func seriesQuery(season, episode int, sizes ...int64) MediaQuery {
	return MediaQuery{TVDBID: 81189, Title: "Show", Series: true, Season: season, Episode: episode, Sizes: sizes, Uncovered: sizes}
}

func runSearch(t *testing.T, chain *Chain, media MediaQuery) ([]Found, *Budget, error) {
	t.Helper()
	budget := chain.NewBudget()
	found, _, err := chain.SearchCandidates(context.Background(), media, budget)
	return found, budget, err
}

func searchRefusals(t *testing.T, err error) *SearchError {
	t.Helper()
	var refusals *SearchError
	if err != nil && !errors.As(err, &refusals) {
		t.Fatalf("valeurObtenue %v, valeurAttendue nil or a *SearchError", err)
	}
	if refusals == nil {
		return &SearchError{}
	}
	return refusals
}

func containsError(errs []error, target error) bool {
	return slices.ContainsFunc(errs, func(candidateErr error) bool { return errors.Is(candidateErr, target) })
}

func TestSearch_prefersIDQueryWhenCapsAllow(t *testing.T) {
	cases := []struct {
		name          string
		params        []string
		expectedQuery torznab.Query
	}{
		{"tmdb", []string{"q", "imdbid", "tmdbid"}, torznab.Query{Mode: "movie", TMDBID: 603, Categories: movieCategories}},
		{"imdb only", []string{"q", "imdbid"}, torznab.Query{Mode: "movie", IMDBID: "tt0133093", Categories: movieCategories}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			setup := newSearchSetup(t)
			caps := movieCaps()
			caps.Modes["movie"] = testCase.params
			indexer := setup.add(indexerA, caps)
			size := 4 * gibibyte
			indexer.results = torznab.Results{Items: []torznab.Item{indexer.serve("win", size, sizedTorrent(t, "m", size))}}

			found, _, err := runSearch(t, setup.chain(t), movieQuery(size))
			if err != nil {
				t.Fatal(err)
			}

			if len(found) != 1 || len(indexer.queries) != 1 || !queryEqual(indexer.queries[0], testCase.expectedQuery) {
				t.Fatalf("valeurObtenue found %d queries %+v, valeurAttendue one id query %+v and no text query", len(found), indexer.queries, testCase.expectedQuery)
			}
		})
	}
}

func TestSearch_fallsBackToTextQueryWithoutIDParam(t *testing.T) {
	setup := newSearchSetup(t)
	caps := movieCaps()
	caps.Modes["movie"] = []string{"q"}
	indexer := setup.add(indexerA, caps)

	_, _, err := runSearch(t, setup.chain(t), movieQuery(4*gibibyte))
	if err != nil {
		t.Fatal(err)
	}

	valeurAttendue := torznab.Query{Mode: "search", Text: searchTitle + " 1999", Categories: movieCategories}
	if len(indexer.queries) != 1 || !queryEqual(indexer.queries[0], valeurAttendue) {
		t.Fatalf("valeurObtenue %+v, valeurAttendue the text query %+v", indexer.queries, valeurAttendue)
	}
}

func TestSearch_triesTheTextQueryWhenTheIDQueryIsNotUseful(t *testing.T) {
	setup := newSearchSetup(t)
	indexer := setup.add(indexerA, movieCaps())

	_, _, err := runSearch(t, setup.chain(t), movieQuery(4*gibibyte))
	if err != nil {
		t.Fatal(err)
	}

	if len(indexer.queries) != 2 || indexer.queries[0].Mode != "movie" || indexer.queries[1].Text != searchTitle+" 1999" {
		t.Fatalf("valeurObtenue %+v, valeurAttendue the id query then the text query", indexer.queries)
	}
}

func TestSearch_textQueryForSeriesUsesSeasonEpisode(t *testing.T) {
	cases := []struct {
		name            string
		season, episode int
		expectedText    string
		expectedID      torznab.Query
	}{
		{"episode", 1, 2, "Show S01E02", torznab.Query{Mode: "tvsearch", TVDBID: 81189, Season: 1, Episode: 2, Categories: tvCategories}},
		{"season pack", 1, 0, "Show S01", torznab.Query{Mode: "tvsearch", TVDBID: 81189, Season: 1, Categories: tvCategories}},
		{"unknown season and episode", 0, 0, "Show", torznab.Query{Mode: "tvsearch", TVDBID: 81189, Categories: tvCategories}},
		{"special", 0, 5, "Show", torznab.Query{Mode: "tvsearch", TVDBID: 81189, Categories: tvCategories}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			setup := newSearchSetup(t)
			indexer := setup.add(indexerA, tvCaps())

			_, _, err := runSearch(t, setup.chain(t), seriesQuery(testCase.season, testCase.episode, gibibyte))
			if err != nil {
				t.Fatal(err)
			}

			if len(indexer.queries) != 2 || !queryEqual(indexer.queries[0], testCase.expectedID) {
				t.Fatalf("valeurObtenue %+v, valeurAttendue id query %+v first", indexer.queries, testCase.expectedID)
			}
			if gotText := indexer.queries[1].Text; gotText != testCase.expectedText || strings.Contains(gotText, "S00") {
				t.Fatalf("valeurObtenue text %q, valeurAttendue %q (never S00)", gotText, testCase.expectedText)
			}
		})
	}
}

func TestSearch_sendsOnlyCategoriesAdvertisedInCaps(t *testing.T) {
	cases := []struct {
		name               string
		configured         []int
		media              MediaQuery
		expectedCategories []int
		expectedQueries    int
	}{
		{"configured intersect caps", []int{2040, 9999}, movieQuery(gibibyte), []int{2040}, 2},
		{"movie categories of caps", nil, movieQuery(gibibyte), []int{2000, 2040}, 2},
		{"tv categories of caps", nil, seriesQuery(1, 2, gibibyte), []int{5000, 5040}, 2},
		{"nothing in caps", []int{9999}, movieQuery(gibibyte), nil, 0},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			setup := newSearchSetup(t)
			caps := movieCaps()
			caps.Modes["tvsearch"] = []string{"q", "tvdbid", "season", "ep"}
			indexer := setup.add(indexerA, caps)
			setup.categories[indexerA] = testCase.configured

			_, _, err := runSearch(t, setup.chain(t), testCase.media)
			if err != nil {
				t.Fatal(err)
			}

			if len(indexer.queries) != testCase.expectedQueries {
				t.Fatalf("valeurObtenue %d queries, valeurAttendue %d", len(indexer.queries), testCase.expectedQueries)
			}
			for _, query := range indexer.queries {
				if !slices.Equal(query.Categories, testCase.expectedCategories) {
					t.Fatalf("valeurObtenue categories %v, valeurAttendue %v", query.Categories, testCase.expectedCategories)
				}
			}
		})
	}
}

// Un téléchargement attend MinInterval comme une recherche : aucune rafale vers le tracker.
func TestSearch_downloadsWaitTheMinIntervalBetweenRequests(t *testing.T) {
	setup := newSearchSetup(t)
	setup.limits.MinInterval = 3 * time.Second
	setup.clock = &fakeClock{now: budgetNow}
	indexer := setup.add(indexerA, movieCaps())
	size := 4 * gibibyte
	indexer.results = torznab.Results{Items: []torznab.Item{
		indexer.serve("decoy-1", size, sizedTorrent(t, "d1", size+1)),
		indexer.serve("decoy-2", size, sizedTorrent(t, "d2", size+2)),
		indexer.serve("win", size, sizedTorrent(t, "w", size)),
	}}

	if _, _, err := runSearch(t, setup.chain(t), movieQuery(size)); err == nil {
		t.Fatal("valeurAttendue the decoys refused")
	}

	if len(indexer.downloads) != 3 {
		t.Fatalf("valeurObtenue downloads %v, valeurAttendue three downloads", indexer.downloads)
	}
	for requestIndex := 1; requestIndex < len(indexer.requestTimes); requestIndex++ {
		if gap := indexer.requestTimes[requestIndex].Sub(indexer.requestTimes[requestIndex-1]); gap < 3*time.Second {
			t.Fatalf("valeurObtenue gap %s before request %d, valeurAttendue at least MinInterval", gap, requestIndex)
		}
	}
}

// Un pack dont la taille totale convient mais dont aucun fichier n'a la taille exacte d'un fichier local est écarté.
func TestSearch_dropsPackWithRightTotalButNoExactSizeFile(t *testing.T) {
	setup := newSearchSetup(t)
	indexer := setup.add(indexerA, tvCaps())
	first, second := gibibyte, 2*gibibyte
	indexer.results = torznab.Results{Items: []torznab.Item{
		indexer.serve("same-total", first+second, sizedTorrent(t, "fake", first+second-10, 10)),
		indexer.serve("win", 5*gibibyte, sizedTorrent(t, "pack", first, second, 2*gibibyte)),
	}}

	found, _, err := runSearch(t, setup.chain(t), seriesQuery(1, 0, first, second))

	if !containsError(searchRefusals(t, err).Skipped, ErrNoExactSize) {
		t.Fatalf("valeurObtenue %v, valeurAttendue ErrNoExactSize for the pack without an exact file", err)
	}
	if len(found) != 1 || !bytes.Contains(found[0].Fetched.Sanitized, []byte("pack")) {
		t.Fatalf("valeurObtenue %d found, valeurAttendue only the pack with exact files", len(found))
	}
}

func TestSearch_movieUpperSizeBound(t *testing.T) {
	setup := newSearchSetup(t)
	indexer := setup.add(indexerA, movieCaps())
	size := 4 * gibibyte
	upper := size + size/100 + 64<<20
	indexer.results = torznab.Results{Items: []torznab.Item{
		indexer.serve("too-big", upper+1, sizedTorrent(t, "big", size, upper+1-size)),
		indexer.serve("too-small", size-1, sizedTorrent(t, "small", size-1)),
		indexer.serve("at-bound", upper, sizedTorrent(t, "bound", size, upper-size)),
	}}

	found, _, err := runSearch(t, setup.chain(t), movieQuery(size))
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(indexer.downloads, []string{"at-bound"}) || len(found) != 1 {
		t.Fatalf("valeurObtenue downloads %v, valeurAttendue only the item at the bound", indexer.downloads)
	}
}

func TestSearch_seriesHasNoUpperBoundAndSortsBySizeGap(t *testing.T) {
	setup := newSearchSetup(t)
	indexer := setup.add(indexerA, tvCaps())
	size := gibibyte
	indexer.results = torznab.Results{Items: []torznab.Item{
		indexer.serve("far", 40*gibibyte, sizedTorrent(t, "far", 40*gibibyte)),
		indexer.serve("near", size+5, sizedTorrent(t, "near", size+5)),
	}}

	_, _, err := runSearch(t, setup.chain(t), seriesQuery(1, 0, size))

	if !slices.Equal(indexer.downloads, []string{"near", "far"}) || !containsError(searchRefusals(t, err).Skipped, ErrNoExactSize) {
		t.Fatalf("valeurObtenue downloads %v err %v, valeurAttendue nearest first and no upper bound", indexer.downloads, err)
	}
}

func TestSearch_skipsV2OnlyItem(t *testing.T) {
	setup := newSearchSetup(t)
	indexer := setup.add(indexerA, movieCaps())
	size := int64(5)
	indexer.results = torznab.Results{Items: []torznab.Item{
		indexer.serve("v2", size, v2OnlyTorrent()),
		indexer.serve("win", size, trackerTorrent("a.mkv")),
	}}

	found, _, err := runSearch(t, setup.chain(t), movieQuery(size))

	if !containsError(searchRefusals(t, err).Skipped, torrentcheck.ErrV2Only) || len(found) != 1 {
		t.Fatalf("valeurObtenue found %d err %v, valeurAttendue v2-only skipped and the next item kept", len(found), err)
	}
}

func TestSearch_withoutSecretKeyCallsNoIndexer(t *testing.T) {
	setup := newSearchSetup(t)
	setup.sealer = nil
	indexer := setup.add(indexerA, movieCaps())

	_, budget, err := runSearch(t, setup.chain(t), movieQuery(gibibyte))

	if !errors.Is(err, ErrSecretKeyMissing) || len(indexer.queries) != 0 || budget.Report()[indexerA].Queries != 0 {
		t.Fatalf("valeurObtenue %v queries %d, valeurAttendue ErrSecretKeyMissing and no request", err, len(indexer.queries))
	}
}

func TestSearch_sealsWithTheTrackerAndSourceStoredOnTheCandidate(t *testing.T) {
	cases := []struct {
		name, declared, expectedTracker string
	}{
		{"declared tracker", "Tracker A", "Tracker A"},
		{"default tracker", "", string(indexerA)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			setup := newSearchSetup(t)
			indexer := setup.add(indexerA, movieCaps())
			setup.trackers[indexerA] = testCase.declared
			raw := trackerTorrent("a.mkv")
			indexer.results = torznab.Results{Items: []torznab.Item{indexer.serve("win", 5, raw)}}

			found, _, err := runSearch(t, setup.chain(t), movieQuery(5))
			if err != nil || len(found) != 1 {
				t.Fatalf("valeurObtenue %d found err %v, valeurAttendue one", len(found), err)
			}

			kept := found[0]
			valeurAttendue := Release{Infohash: torrentInfohash(t, raw), Tracker: testCase.expectedTracker, Name: "Release win"}
			if kept.Source != indexerA || kept.Release != valeurAttendue || bytes.Contains(kept.Fetched.Sanitized, []byte(fakePasskey)) {
				t.Fatalf("valeurObtenue %+v, valeurAttendue release %+v from %s without passkey in clear", kept.Release, valeurAttendue, indexerA)
			}
			unsealed, err := setup.sealer.Unseal(kept.Fetched.Sealed, Binding{Infohash: valeurAttendue.Infohash, Tracker: valeurAttendue.Tracker, Source: indexerA})
			if err != nil || !bytes.Equal(unsealed, raw) {
				t.Fatalf("valeurObtenue unseal err %v, valeurAttendue the download bound to infohash, tracker and source", err)
			}
		})
	}
}

func TestSearch_noSealedCopyWhenATrackersEntryCoversTheTracker(t *testing.T) {
	setup := newSearchSetup(t)
	indexer := setup.add(indexerA, movieCaps())
	setup.trackers[indexerA] = "Tracker A"
	indexer.results = torznab.Results{Items: []torznab.Item{indexer.serve("win", 5, trackerTorrent("a.mkv"))}}
	media := movieQuery(5)
	media.AnnounceConfigured = func(tracker string) bool { return tracker == "Tracker A" }

	found, _, err := runSearch(t, setup.chain(t), media)

	if err != nil || len(found) != 1 || found[0].Fetched.Sealed != nil || len(found[0].Fetched.Sanitized) == 0 {
		t.Fatalf("valeurObtenue %d found err %v, valeurAttendue the sanitized copy only", len(found), err)
	}
}

func TestSearch_budgetExhaustedInterruptsTheIndexer(t *testing.T) {
	setup := newSearchSetup(t)
	setup.limits.MaxDownloadsPerRun = 0
	indexer := setup.add(indexerA, movieCaps())
	indexer.results = torznab.Results{Items: []torznab.Item{indexer.serve("win", 5, trackerTorrent("a.mkv"))}}

	found, budget, err := runSearch(t, setup.chain(t), movieQuery(5))

	if !containsError(searchRefusals(t, err).Interrupted, ErrBudgetExhausted) || len(found) != 0 || len(indexer.downloads) != 0 {
		t.Fatalf("valeurObtenue %v downloads %v, valeurAttendue ErrBudgetExhausted without download", err, indexer.downloads)
	}
	if !budget.Report()[indexerA].BudgetExhausted || len(indexer.queries) != 1 {
		t.Fatalf("valeurObtenue report %+v queries %d, valeurAttendue exhausted and no second query", budget.Report()[indexerA], len(indexer.queries))
	}
}

// Chaque requête (capacités, recherches, téléchargements) passe par le budget : le résumé les compte toutes.
func TestSearch_everyRequestIsSpentThroughTheBudget(t *testing.T) {
	setup := newSearchSetup(t)
	indexer := setup.add(indexerA, movieCaps())
	indexer.results = torznab.Results{Items: []torznab.Item{
		indexer.serve("win", 5, trackerTorrent("a.mkv")),
	}}
	media := movieQuery(5)
	chain := setup.chain(t)
	budget := chain.NewBudget()

	if _, _, err := chain.SearchCandidates(context.Background(), media, budget); err != nil {
		t.Fatal(err)
	}
	if _, _, err := chain.SearchCandidates(context.Background(), media, budget); err != nil {
		t.Fatal(err)
	}

	report := budget.Report()[indexerA]
	if report.Queries != 3 || report.Downloads != 2 {
		t.Fatalf("valeurObtenue %+v, valeurAttendue caps once per run plus one query and one download per search", report)
	}
}

// Premier résultat utile : l'indexeur suivant n'est interrogé que si le groupe n'est pas encore couvert.
func TestSearch_stopsAtTheFirstUsefulResultAndOnlyAsksTheNextIndexerWhileUncovered(t *testing.T) {
	setup := newSearchSetup(t)
	first := setup.add(indexerA, tvCaps())
	second := setup.add(indexerB, tvCaps())
	sizeOne, sizeTwo := gibibyte, 2*gibibyte
	partial := sizedTorrent(t, "partial", sizeOne, 3*gibibyte)
	first.results = torznab.Results{Items: []torznab.Item{first.serve("partial", 4*gibibyte, partial), first.serve("unused", 4*gibibyte, partial)}}
	second.results = torznab.Results{Items: []torznab.Item{second.serve("rest", 4*gibibyte, sizedTorrent(t, "rest", sizeTwo, 2*gibibyte))}}

	found, _, err := runSearch(t, setup.chain(t), seriesQuery(1, 0, sizeOne, sizeTwo))
	if err != nil {
		t.Fatal(err)
	}

	if len(found) != 2 || !slices.Equal(first.downloads, []string{"partial"}) || len(first.queries) != 1 || len(second.queries) != 1 {
		t.Fatalf("valeurObtenue found %d first %v/%d second %d, valeurAttendue one kept per indexer", len(found), first.downloads, len(first.queries), len(second.queries))
	}

	covered := newSearchSetup(t)
	alone := covered.add(indexerA, tvCaps())
	never := covered.add(indexerB, tvCaps())
	alone.results = torznab.Results{Items: []torznab.Item{alone.serve("all", 3*gibibyte, sizedTorrent(t, "all", sizeOne, sizeTwo))}}
	if _, _, err := runSearch(t, covered.chain(t), seriesQuery(1, 0, sizeOne, sizeTwo)); err != nil {
		t.Fatal(err)
	}
	if len(never.queries) != 0 {
		t.Fatalf("valeurObtenue %d queries, valeurAttendue none once the group is covered", len(never.queries))
	}
}

func TestSearch_withoutIndexerReturnsNothing(t *testing.T) {
	chain, err := NewChain(&fakeProvider{log: &callLog{}}, nil, []ID{ProviderID}, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	found, _, err := chain.SearchCandidates(context.Background(), movieQuery(5), chain.NewBudget())

	if err != nil || len(found) != 0 {
		t.Fatalf("valeurObtenue %d found err %v, valeurAttendue nothing and no error", len(found), err)
	}
}

func TestIndexerReport_jsonFieldNames(t *testing.T) {
	encoded, err := json.Marshal(IndexerReport{Queries: 1, Downloads: 2, MagnetOnly: 3, BudgetExhausted: true})
	if err != nil {
		t.Fatal(err)
	}

	valeurAttendue := `{"torznab_queries":1,"torznab_downloads":2,"magnet_only":3,"budget_exhausted":true}`
	if string(encoded) != valeurAttendue {
		t.Fatalf("valeurObtenue %s, valeurAttendue %s", encoded, valeurAttendue)
	}
}

// Un .torrent qui ne prouve qu'un fichier déjà couvert n'est pas utile : écarté, et l'indexeur continue.
func TestSearch_provesExactSizeAgainstUncoveredFilesOnly(t *testing.T) {
	setup := newSearchSetup(t)
	indexer := setup.add(indexerA, tvCaps())
	coveredSize, uncoveredSize := gibibyte, 2*gibibyte
	indexer.results = torznab.Results{Items: []torznab.Item{indexer.serve("covered-only", 4*gibibyte, sizedTorrent(t, "pack", coveredSize, 3*gibibyte))}}
	media := seriesQuery(1, 0, coveredSize, uncoveredSize)
	media.Uncovered = []int64{uncoveredSize}

	found, _, err := runSearch(t, setup.chain(t), media)

	if len(found) != 0 || !containsError(searchRefusals(t, err).Skipped, ErrNoExactSize) || len(indexer.queries) != 2 {
		t.Fatalf("valeurObtenue found %d err %v queries %d, valeurAttendue the pack refused and the text query still sent", len(found), err, len(indexer.queries))
	}
}

// Au plus maxGroupDownloads téléchargements par groupe, tous indexeurs confondus : le groupe s'arrête là pour l'exécution,
// le groupe suivant garde son budget.
func TestSearch_capsDownloadsPerGroup(t *testing.T) {
	setup := newSearchSetup(t)
	first := setup.add(indexerA, movieCaps())
	second := setup.add(indexerB, movieCaps())
	size := 4 * gibibyte
	for decoyIndex := range 5 {
		guid := fmt.Sprintf("decoy-%d", decoyIndex)
		first.results.Items = append(first.results.Items, first.serve(guid, size, sizedTorrent(t, guid, size+int64(decoyIndex)+1)))
	}
	second.results = first.results
	second.torrents = first.torrents
	chain := setup.chain(t)
	budget := chain.NewBudget()

	_, _, err := chain.SearchCandidates(context.Background(), movieQuery(size), budget)

	if len(first.downloads) != maxGroupDownloads || len(second.queries) != 0 || len(searchRefusals(t, err).Skipped) != maxGroupDownloads {
		t.Fatalf("valeurObtenue downloads %v second queries %d, valeurAttendue %d downloads and the group stopped", first.downloads, len(second.queries), maxGroupDownloads)
	}
	if _, _, err := chain.SearchCandidates(context.Background(), movieQuery(size-100), budget); err == nil || len(first.downloads) != 2*maxGroupDownloads {
		t.Fatalf("valeurObtenue downloads %v, valeurAttendue the next group downloading again", first.downloads)
	}
}

// Un indexeur dont le budget est épuisé est ignoré sans bruit pour les groupes suivants : la raison n'est comptée qu'une fois.
func TestSearch_exhaustedIndexerIsSkippedSilentlyForLaterGroups(t *testing.T) {
	setup := newSearchSetup(t)
	setup.limits.MaxDownloadsPerRun = 0
	indexer := setup.add(indexerA, movieCaps())
	indexer.results = torznab.Results{Items: []torznab.Item{indexer.serve("win", 5, trackerTorrent("a.mkv"))}}
	chain := setup.chain(t)
	budget := chain.NewBudget()
	if _, _, err := chain.SearchCandidates(context.Background(), movieQuery(5), budget); !containsError(searchRefusals(t, err).Interrupted, ErrBudgetExhausted) {
		t.Fatalf("valeurObtenue %v, valeurAttendue ErrBudgetExhausted for the first group", err)
	}
	queriesBefore := len(indexer.queries)

	_, _, err := chain.SearchCandidates(context.Background(), movieQuery(6), budget)

	if err != nil || len(indexer.queries) != queriesBefore {
		t.Fatalf("valeurObtenue %v queries %d, valeurAttendue no refusal and no request for a later group", err, len(indexer.queries)-queriesBefore)
	}
}

// Des capacités servies par le cache du client ne coûtent aucune requête au quota : seule la vraie requête t=caps compte.
func TestSearch_cachedCapsCostNoQuota(t *testing.T) {
	var capsRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
		if request.URL.Query().Get("t") == "caps" {
			capsRequests.Add(1)
			_, _ = writer.Write([]byte(`<caps><searching><search available="yes"/><movie-search available="yes" supportedParams="q,tmdbid"/></searching></caps>`))
			return
		}
		_, _ = writer.Write([]byte(`<?xml version="1.0"?><rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel></channel></rss>`))
	}))
	t.Cleanup(server.Close)
	client, err := torznab.New(torznab.Endpoint{Name: "a", URL: server.URL + "/api?apikey=k", MinInterval: time.Millisecond, Timeout: 5 * time.Second}, torznab.Deps{})
	if err != nil {
		t.Fatal(err)
	}
	limits := testLimits()
	limits.MinInterval = 0
	chain, err := NewChain(&fakeProvider{log: &callLog{}}, map[ID]IndexerSource{indexerA: client}, []ID{ProviderID, indexerA},
		map[ID]Limits{indexerA: limits}, realUsage(t), testSealer(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	firstRun, secondRun := chain.NewBudget(), chain.NewBudget()
	if _, _, err := chain.SearchCandidates(context.Background(), movieQuery(5), firstRun); err != nil {
		t.Fatal(err)
	}

	if _, _, err := chain.SearchCandidates(context.Background(), movieQuery(5), secondRun); err != nil {
		t.Fatal(err)
	}

	if capsRequests.Load() != 1 || firstRun.Report()[indexerA].Queries != 3 || secondRun.Report()[indexerA].Queries != 2 {
		t.Fatalf("valeurObtenue caps requests %d, queries %d then %d, valeurAttendue one caps request counted once (3 then 2)",
			capsRequests.Load(), firstRun.Report()[indexerA].Queries, secondRun.Report()[indexerA].Queries)
	}
}
