package sources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"necromanciarr/internal/store"
	"necromanciarr/internal/torznab"
)

const cachedInfohash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// cachedCapsIndexer annonce ses capacités comme déjà en cache : la recherche n'a alors aucune requête t=caps à payer.
type cachedCapsIndexer struct{ *searchIndexer }

func (indexer cachedCapsIndexer) CachedCaps() (torznab.Caps, bool) { return indexer.caps, true }

func cachedSetup(t *testing.T) (*searchSetup, *searchIndexer) {
	t.Helper()
	setup := newSearchSetup(t)
	setup.withCache = true
	setup.clock = &fakeClock{now: budgetNow}
	return setup, setup.add(indexerA, movieCaps())
}

func cacheRow(t *testing.T, usage *store.Store, indexer ID, query torznab.Query, at time.Time) ([]byte, bool, bool) {
	t.Helper()
	items, found, empty, err := usage.GetIndexerCache(context.Background(), string(indexer), queryKey(indexer, query), at)
	if err != nil {
		t.Fatal(err)
	}
	return items, found, empty
}

func TestCache_successFor6HoursEmptyFor24Hours(t *testing.T) {
	setup, indexer := cachedSetup(t)
	idQuery := torznab.Query{Mode: "movie", TMDBID: 603, Categories: movieCategories}
	textQuery := torznab.Query{Mode: "search", Text: searchTitle + " 1999", Categories: movieCategories}
	indexer.respond = func(query torznab.Query) torznab.Results {
		if query.Mode == "movie" {
			return torznab.Results{Items: []torznab.Item{{GUID: "sha256:x", Size: 1, InfoHash: cachedInfohash}}}
		}
		return torznab.Results{}
	}
	if _, _, err := runSearch(t, setup.chain(t), movieQuery(4*gibibyte)); err != nil {
		t.Fatal(err)
	}

	_, liveSuccess, _ := cacheRow(t, setup.usage, indexerA, idQuery, budgetNow.Add(6*time.Hour-time.Second))
	_, expiredSuccess, _ := cacheRow(t, setup.usage, indexerA, idQuery, budgetNow.Add(6*time.Hour+time.Minute))
	_, liveEmpty, empty := cacheRow(t, setup.usage, indexerA, textQuery, budgetNow.Add(24*time.Hour-time.Second))
	_, expiredEmpty, _ := cacheRow(t, setup.usage, indexerA, textQuery, budgetNow.Add(24*time.Hour+time.Minute))
	if !liveSuccess || expiredSuccess || !liveEmpty || !empty || expiredEmpty {
		t.Fatalf("valeurObtenue success %t/%t empty %t/%t flag %t, valeurAttendue true/false true/false true", liveSuccess, expiredSuccess, liveEmpty, expiredEmpty, empty)
	}
}

func TestCache_neverStoresDownloadURL(t *testing.T) {
	const passkey = "fake-passkey-0000"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
		if request.URL.Query().Get("t") == "caps" {
			_, _ = writer.Write([]byte(`<caps><searching><movie-search available="yes" supportedParams="q,tmdbid"/></searching></caps>`))
			return
		}
		link := "http://" + request.Host + "/dl/" + passkey + "/release.torrent?apikey=fake-api-key"
		_, _ = fmt.Fprintf(writer, `<?xml version="1.0"?><rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>
<item><title>Release</title><guid>%s</guid><link>%s</link><enclosure url="%s" length="9" type="application/x-bittorrent"/>
<torznab:attr name="magneturl" value="magnet:?xt=urn:btih:%s&amp;tr=https://t.example.com/%s"/>
<torznab:attr name="infohash" value="%s"/><torznab:attr name="size" value="%d"/></item></channel></rss>`, link, link, link, cachedInfohash, passkey, cachedInfohash, 5*gibibyte)
	}))
	t.Cleanup(server.Close)
	client, err := torznab.New(torznab.Endpoint{Name: "a", URL: server.URL + "/api?apikey=fake-api-key", MinInterval: time.Millisecond, Timeout: 5 * time.Second}, torznab.Deps{})
	if err != nil {
		t.Fatal(err)
	}
	usage := realUsage(t)
	limits := testLimits()
	limits.MinInterval = 0
	chain, err := NewChain(&fakeProvider{log: &callLog{}}, map[ID]IndexerSource{indexerA: client}, []ID{ProviderID, indexerA}, map[ID]Limits{indexerA: limits}, usage, testSealer(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	chain.SetSearchStore(usage)

	_, _, _ = chain.SearchCandidates(context.Background(), movieQuery(4*gibibyte), chain.NewBudget())

	var rows []string
	for _, query := range []torznab.Query{{Mode: "movie", TMDBID: 603}, {Mode: "search", Text: searchTitle + " 1999"}} {
		if items, found, _ := cacheRow(t, usage, indexerA, query, time.Now()); found {
			rows = append(rows, string(items))
		}
	}
	if len(rows) == 0 {
		t.Fatal("valeurObtenue no cached row, valeurAttendue the answer cached")
	}
	for _, row := range rows {
		for _, secret := range []string{"apikey", "fake-api-key", passkey, "/dl/", "release.torrent", "magnet:", "127.0.0.1", "http"} {
			if strings.Contains(row, secret) {
				t.Fatalf("valeurObtenue row %s containing %q, valeurAttendue no download URL, key, host or magnet", row, secret)
			}
		}
	}
}

func TestCache_errorsNotCached(t *testing.T) {
	setup, indexer := cachedSetup(t)
	indexer.searchErr = errors.New("indexer broke")
	chain := setup.chain(t)
	if _, _, err := runSearch(t, chain, movieQuery(4*gibibyte)); err == nil {
		t.Fatal("valeurObtenue no refusal, valeurAttendue the search error reported")
	}

	_, found, _ := cacheRow(t, setup.usage, indexerA, torznab.Query{Mode: "movie", TMDBID: 603, Categories: movieCategories}, budgetNow)
	indexer.searchErr = nil
	_, _, _ = runSearch(t, chain, movieQuery(4*gibibyte))

	if found || len(indexer.queries) < 2 {
		t.Fatalf("valeurObtenue cached %t queries %d, valeurAttendue an error never cached and the query sent again", found, len(indexer.queries))
	}
}

func TestCache_twoSpellingsOfTheSameQueryShareOneKey(t *testing.T) {
	left := torznab.Query{Mode: "search", Text: "  The   MATRIX 1999 ", Categories: []int{2040, 2000, 2000}, IMDBID: "TT0133093"}
	right := torznab.Query{Mode: "SEARCH", Text: "the matrix 1999", Categories: []int{2000, 2040}, IMDBID: "tt0133093"}

	if queryKey(indexerA, left) != queryKey(indexerA, right) {
		t.Fatal("valeurObtenue two keys, valeurAttendue one key for two spellings of the same query")
	}
	if queryKey(indexerA, left) == queryKey(indexerB, left) {
		t.Fatal("valeurObtenue the same key for two indexers, valeurAttendue the indexer id in the key")
	}
	if queryKey(indexerA, left) == queryKey(indexerA, torznab.Query{Mode: "search", Text: "the matrix 2003"}) {
		t.Fatal("valeurObtenue one key for two queries, valeurAttendue distinct keys")
	}
	if key := queryKey(indexerA, left); len(key) != 64 || strings.Trim(key, "0123456789abcdef") != "" {
		t.Fatalf("valeurObtenue key %q, valeurAttendue a hex SHA-256", key)
	}
}

func TestCache_hitCostsNoQuotaAndNoWait(t *testing.T) {
	setup, indexer := cachedSetup(t)
	setup.limits.MinInterval = 3 * time.Second
	wrapped := cachedCapsIndexer{indexer}
	chain := cachedChain(t, setup, wrapped)
	if _, _, err := runSearch(t, chain, movieQuery(4*gibibyte)); err != nil {
		t.Fatal(err)
	}
	usedBefore, _ := setup.usage.CountIndexerUsage(context.Background(), string(indexerA), KindQuery, budgetNow.Add(-48*time.Hour))
	sleepsBefore := len(setup.clock.sleeps)

	_, budget, err := runSearch(t, chain, movieQuery(4*gibibyte))

	usedAfter, _ := setup.usage.CountIndexerUsage(context.Background(), string(indexerA), KindQuery, budgetNow.Add(-48*time.Hour))
	if err != nil || usedAfter != usedBefore || len(setup.clock.sleeps) != sleepsBefore || len(indexer.queries) != 2 {
		t.Fatalf("valeurObtenue rows %d->%d sleeps %d->%d queries %d err %v, valeurAttendue the second run served from the cache",
			usedBefore, usedAfter, sleepsBefore, len(setup.clock.sleeps), len(indexer.queries), err)
	}
	if report := budget.Report()[indexerA]; report.CacheHits != 2 || report.Queries != 0 {
		t.Fatalf("valeurObtenue %+v, valeurAttendue two cache hits and no query", report)
	}
}

// cachedChain bâtit une chaîne autour d'un indexeur déjà enveloppé.
func cachedChain(t *testing.T, setup *searchSetup, indexer IndexerSource) *Chain {
	t.Helper()
	if setup.usage == nil {
		setup.usage = realUsage(t)
	}
	chain, err := NewChain(&fakeProvider{log: &callLog{}}, map[ID]IndexerSource{indexerA: indexer}, []ID{ProviderID, indexerA},
		map[ID]Limits{indexerA: setup.limits}, setup.usage, setup.sealer, setup.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	chain.sleep = setup.clock.Sleep
	chain.SetSearchStore(setup.usage)
	return chain
}

func TestCache_hitWithAnUnknownCandidateSearchesAgainAndReplacesTheRow(t *testing.T) {
	setup, indexer := cachedSetup(t)
	size := 4 * gibibyte
	indexer.respond = func(query torznab.Query) torznab.Results {
		if query.Mode == "movie" {
			return torznab.Results{Items: []torznab.Item{{GUID: "sha256:decoy", Size: size, InfoHash: cachedInfohash}}}
		}
		return torznab.Results{}
	}
	indexer.torrents["sha256:decoy"] = sizedTorrent(t, "decoy", size+1)
	chain := cachedChain(t, setup, cachedCapsIndexer{indexer})
	_, _, _ = runSearch(t, chain, movieQuery(size))
	setup.clock.Advance(5 * time.Hour)
	queriesBefore := len(indexer.queries)

	_, _, err := runSearch(t, chain, movieQuery(size))

	idQuery := torznab.Query{Mode: "movie", TMDBID: 603, Categories: movieCategories}
	_, stillLive, _ := cacheRow(t, setup.usage, indexerA, idQuery, setup.clock.Now().Add(5*time.Hour+30*time.Minute))
	_ = err // l'écart de taille du leurre est un refus attendu
	if len(indexer.queries) != queriesBefore+1 || !stillLive {
		t.Fatalf("valeurObtenue queries %d->%d row live at +10h30 %t, valeurAttendue one new search whose answer replaced the row", queriesBefore, len(indexer.queries), stillLive)
	}
}

func TestCache_hitWithOnlyKnownInfohashesSpendsNothing(t *testing.T) {
	setup, indexer := cachedSetup(t)
	size := 4 * gibibyte
	indexer.respond = func(query torznab.Query) torznab.Results {
		if query.Mode == "movie" {
			return torznab.Results{Items: []torznab.Item{{GUID: "sha256:known", Size: size, InfoHash: cachedInfohash}}}
		}
		return torznab.Results{}
	}
	chain := cachedChain(t, setup, cachedCapsIndexer{indexer})
	_, _, _ = runSearch(t, chain, movieQuery(size))
	if _, _, err := setup.usage.SaveCandidate(context.Background(), store.Candidate{Infohash: cachedInfohash, Tracker: string(indexerA), Name: "n", TotalSize: size}, nil, budgetNow); err != nil {
		t.Fatal(err)
	}
	queriesBefore := len(indexer.queries)
	downloadsBefore := len(indexer.downloads)

	_, _, err := runSearch(t, chain, movieQuery(size))

	if err != nil || len(indexer.queries) != queriesBefore || len(indexer.downloads) != downloadsBefore {
		t.Fatalf("valeurObtenue queries %d->%d downloads %d->%d err %v, valeurAttendue nothing spent", queriesBefore, len(indexer.queries), downloadsBefore, len(indexer.downloads), err)
	}
}

func TestSearch_neverDownloadsAnInfohashThatIsAlreadyACandidate(t *testing.T) {
	setup, indexer := cachedSetup(t)
	size := 4 * gibibyte
	indexer.results = torznab.Results{Items: []torznab.Item{
		indexer.serve("sha256:known", size, sizedTorrent(t, "known", size)),
		indexer.serve("sha256:fresh", size+1, sizedTorrent(t, "fresh", size)),
	}}
	indexer.results.Items[0].InfoHash = cachedInfohash
	chain := setup.chain(t)
	if _, _, err := setup.usage.SaveCandidate(context.Background(), store.Candidate{Infohash: cachedInfohash, Tracker: string(indexerA), Name: "n", TotalSize: size}, nil, budgetNow); err != nil {
		t.Fatal(err)
	}

	found, _, err := runSearch(t, chain, movieQuery(size))

	if err != nil || len(found) != 1 || len(indexer.downloads) != 1 || indexer.downloads[0] != "sha256:fresh" {
		t.Fatalf("valeurObtenue found %d downloads %v err %v, valeurAttendue only the unknown item downloaded", len(found), indexer.downloads, err)
	}
}

func TestCache_truncatesAHostileAnswerSafely(t *testing.T) {
	setup, indexer := cachedSetup(t)
	var hostile torznab.Results
	for index := range 500 {
		hostile.Items = append(hostile.Items, torznab.Item{GUID: "sha256:" + strings.Repeat("a", 400) + fmt.Sprint(index), InfoHash: cachedInfohash, Size: int64(index)})
	}
	indexer.results = hostile

	_, _, err := runSearch(t, setup.chain(t), movieQuery(4*gibibyte))

	items, found, _ := cacheRow(t, setup.usage, indexerA, torznab.Query{Mode: "movie", TMDBID: 603, Categories: movieCategories}, budgetNow)
	var decoded []json.RawMessage
	if err != nil || !found || len(items) > store.IndexerCacheMaxItemsBytes || json.Unmarshal(items, &decoded) != nil || len(decoded) == 0 || len(decoded) > maxCachedItems {
		t.Fatalf("valeurObtenue found %t %d bytes %d items err %v, valeurAttendue a bounded valid row", found, len(items), len(decoded), err)
	}
}

func markedSearch(t *testing.T, setup *searchSetup, group string) (*Chain, MediaQuery) {
	t.Helper()
	media := movieQuery(4 * gibibyte)
	media.GroupKey = group
	return setup.chain(t), media
}

func TestSearch_marksAnAnsweredGroupEvenWhenEmptyAndSkipsItNextTime(t *testing.T) {
	setup, indexer := cachedSetup(t)
	chain, media := markedSearch(t, setup, "group-1")
	if _, _, err := chain.SearchCandidates(context.Background(), media, chain.NewBudget()); err != nil {
		t.Fatal(err)
	}
	queriesAfterFirst := len(indexer.queries)

	_, _, err := chain.SearchCandidates(context.Background(), media, chain.NewBudget())

	marked, _ := setup.usage.GroupSearchedSince(context.Background(), "group-1", string(indexerA), time.Time{})
	if err != nil || !marked || len(indexer.queries) != queriesAfterFirst {
		t.Fatalf("valeurObtenue marked %t queries %d->%d err %v, valeurAttendue a mark and no request on the second run", marked, queriesAfterFirst, len(indexer.queries), err)
	}
}

func TestSearch_doesNotMarkAGroupInterruptedByBudgetOrError(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*searchSetup, *searchIndexer)
	}{
		{"budget exhausted", func(setup *searchSetup, _ *searchIndexer) { setup.limits.MaxQueriesPerRun = 1 }},
		{"search error", func(_ *searchSetup, indexer *searchIndexer) { indexer.searchErr = errors.New("indexer broke") }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			setup, indexer := cachedSetup(t)
			testCase.setup(setup, indexer)
			chain, media := markedSearch(t, setup, "group-1")

			_, _, _ = chain.SearchCandidates(context.Background(), media, chain.NewBudget())

			if marked, _ := setup.usage.GroupSearchedSince(context.Background(), "group-1", string(indexerA), time.Time{}); marked {
				t.Fatal("valeurObtenue a mark after an interruption, valeurAttendue none")
			}
		})
	}
}

func TestSearch_marksExpireAfterAWeekSoANewReleaseIsFound(t *testing.T) {
	setup, indexer := cachedSetup(t)
	chain, media := markedSearch(t, setup, "group-1")
	_, _, _ = chain.SearchCandidates(context.Background(), media, chain.NewBudget())
	queriesBefore := len(indexer.queries)
	setup.clock.Advance(store.IndexerSearchedTTL + time.Hour)

	_, _, _ = chain.SearchCandidates(context.Background(), media, chain.NewBudget())

	if len(indexer.queries) == queriesBefore {
		t.Fatal("valeurObtenue the expired mark still skips the group, valeurAttendue a new search")
	}
}
