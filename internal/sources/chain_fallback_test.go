package sources

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"

	"necromanciarr/internal/libraryprovider"
	"necromanciarr/internal/torrentcheck"
	"necromanciarr/internal/torznab"
)

const (
	fallbackTracker = "Tracker A"
	fallbackName    = "Movie.2020.1080p"
)

// recordingIndexer répond par GUID et note chaque appel avec son nom : un indexeur jamais sollicité n'a aucune ligne.
type recordingIndexer struct {
	name         string
	log          *callLog
	results      torznab.Results
	searchErr    error
	torrents     map[string][]byte
	downloadErrs map[string]error
	queries      []torznab.Query
}

func (indexer *recordingIndexer) Caps(context.Context) (torznab.Caps, error) {
	indexer.log.add(indexer.name + ".caps")
	return torznab.Caps{}, nil
}

func (indexer *recordingIndexer) Search(_ context.Context, query torznab.Query) (torznab.Results, error) {
	indexer.log.add(indexer.name + ".search")
	indexer.queries = append(indexer.queries, query)
	return indexer.results, indexer.searchErr
}

func (indexer *recordingIndexer) Download(_ context.Context, item torznab.Item) ([]byte, error) {
	indexer.log.add(indexer.name + ".download." + item.GUID)
	if err := indexer.downloadErrs[item.GUID]; err != nil {
		return nil, err
	}
	return bytes.Clone(indexer.torrents[item.GUID]), nil
}

// trackerTorrent est un .torrent v1 valide dont l'annonce porte une passkey factice ; name change l'infohash.
func trackerTorrent(name string) []byte {
	info := "d6:lengthi5e4:name" + bencodedString(name) + "12:piece lengthi16384e6:pieces20:AAAAAAAAAAAAAAAAAAAA7:privatei1ee"
	return []byte("d" + bencodedString("announce") + bencodedString("https://tracker-a.example.com/"+fakePasskey+"/announce") +
		bencodedString("info") + info + "e")
}

// v2OnlyTorrent n'a que le format v2 (meta version 2, sans pieces) : torrentcheck le refuse.
func v2OnlyTorrent() []byte {
	return []byte("d4:infod6:lengthi5e12:meta versioni2e4:name5:b.mkv12:piece lengthi16384eee")
}

func torrentInfohash(t *testing.T, raw []byte) string {
	t.Helper()
	parsed, err := torrentcheck.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.InfoHash
}

func testSealer(t *testing.T) *Sealer {
	t.Helper()
	sealer, err := NewSealer(bytes.Repeat([]byte{7}, KeySize))
	if err != nil {
		t.Fatal(err)
	}
	return sealer
}

type fallbackSetup struct {
	log      *callLog
	provider *fakeProvider
	indexers map[ID]*recordingIndexer
	order    []ID
	trackers map[ID]string
	limits   Limits
	sealer   *Sealer
}

func newFallbackSetup(t *testing.T) *fallbackSetup {
	t.Helper()
	log := &callLog{}
	limits := testLimits()
	limits.MinInterval = 0
	return &fallbackSetup{
		log: log, provider: &fakeProvider{log: log, failure: libraryprovider.ErrNotFound},
		indexers: map[ID]*recordingIndexer{}, order: []ID{ProviderID}, trackers: map[ID]string{}, limits: limits, sealer: testSealer(t),
	}
}

// addIndexer ajoute un indexeur à la fin de l'ordre ; tracker vide = indexeur sans tracker déclaré.
func (setup *fallbackSetup) addIndexer(identifier ID, tracker string) *recordingIndexer {
	indexer := &recordingIndexer{name: string(identifier), log: setup.log, torrents: map[string][]byte{}, downloadErrs: map[string]error{}}
	setup.indexers[identifier] = indexer
	setup.order = append(setup.order, identifier)
	if tracker != "" {
		setup.trackers[identifier] = tracker
	}
	return indexer
}

func (setup *fallbackSetup) chain(t *testing.T) *Chain {
	t.Helper()
	indexers, limits := map[ID]IndexerSource{}, map[ID]Limits{}
	for identifier, indexer := range setup.indexers {
		indexers[identifier], limits[identifier] = indexer, setup.limits
	}
	chain, err := NewChain(setup.provider, indexers, setup.order, limits, realUsage(t), setup.sealer, nil)
	if err != nil {
		t.Fatal(err)
	}
	chain.SetFallbackTrackers(setup.trackers)
	return chain
}

func (setup *fallbackSetup) indexerCalls() []string {
	var calls []string
	for _, call := range setup.log.calls {
		if !strings.HasPrefix(call, "provider.") {
			calls = append(calls, call)
		}
	}
	return calls
}

func releaseOf(t *testing.T, raw []byte) Release {
	t.Helper()
	return Release{Infohash: torrentInfohash(t, raw), Tracker: "tracker a", Name: fallbackName}
}

func TestChain_fallbackReturnsTheSanitizedAndSealedTorrentOfTheSameInfohash(t *testing.T) {
	setup := newFallbackSetup(t)
	raw := trackerTorrent("a.mkv")
	indexer := setup.addIndexer(indexerA, fallbackTracker)
	indexer.results = torznab.Results{Items: []torznab.Item{{GUID: "win", Title: fallbackName}}}
	indexer.torrents["win"] = raw
	chain := setup.chain(t)
	budget := chain.NewBudget()
	release := releaseOf(t, raw)

	fetched, err := chain.FetchTorrent(context.Background(), release, budget)
	if err != nil {
		t.Fatal(err)
	}

	if fetched.Source != indexerA {
		t.Fatalf("valeurObtenue source %q, valeurAttendue %q", fetched.Source, indexerA)
	}
	expectedSanitized, err := Sanitize(raw)
	if err != nil || !bytes.Equal(fetched.Sanitized, expectedSanitized) || bytes.Contains(fetched.Sanitized, []byte(fakePasskey)) {
		t.Fatalf("valeurObtenue sanitized %q (err %v), valeurAttendue the sanitized download without passkey", fetched.Sanitized, err)
	}
	if valeurObtenue := torrentInfohash(t, fetched.Sanitized); valeurObtenue != release.Infohash {
		t.Fatalf("valeurObtenue infohash %s, valeurAttendue %s", valeurObtenue, release.Infohash)
	}
	unsealed, err := setup.sealer.Unseal(fetched.Sealed, Binding{Infohash: release.Infohash, Tracker: release.Tracker, Source: indexerA})
	if err != nil || !bytes.Equal(unsealed, raw) {
		t.Fatalf("valeurObtenue unsealed %q (err %v), valeurAttendue the original download", unsealed, err)
	}
	valeurAttendue := torznab.Query{Mode: "search", Text: fallbackName}
	if len(indexer.queries) != 1 || !queryEqual(indexer.queries[0], valeurAttendue) {
		t.Fatalf("valeurObtenue queries %+v, valeurAttendue one text query on the release name", indexer.queries)
	}
	if report := budget.Report()[indexerA]; report.Queries != 1 || report.Downloads != 1 {
		t.Fatalf("valeurObtenue report %+v, valeurAttendue one query and one download spent through the budget", report)
	}
}

func queryEqual(left, right torznab.Query) bool {
	return left.Mode == right.Mode && left.Text == right.Text && left.TMDBID == right.TMDBID && left.IMDBID == right.IMDBID &&
		left.TVDBID == right.TVDBID && left.Season == right.Season && left.Episode == right.Episode && len(left.Categories) == len(right.Categories)
}

func TestChain_fallbackSkipsItemsOfAnotherInfohash(t *testing.T) {
	setup := newFallbackSetup(t)
	raw, other := trackerTorrent("a.mkv"), trackerTorrent("other.mkv")
	release := releaseOf(t, raw)
	indexer := setup.addIndexer(indexerA, fallbackTracker)
	indexer.results = torznab.Results{Items: []torznab.Item{
		{GUID: "announced-other", InfoHash: torrentInfohash(t, other)},
		{GUID: "computed-other"},
		{GUID: "win"},
	}}
	indexer.torrents["announced-other"], indexer.torrents["computed-other"], indexer.torrents["win"] = other, other, raw
	chain := setup.chain(t)

	fetched, err := chain.FetchTorrent(context.Background(), release, chain.NewBudget())

	if err != nil || torrentInfohash(t, fetched.Sanitized) != release.Infohash {
		t.Fatalf("valeurObtenue %v, valeurAttendue the item of the candidate's infohash", err)
	}
	valeurAttendue := []string{"torznab:a.search", "torznab:a.download.computed-other", "torznab:a.download.win"}
	if valeurObtenue := setup.indexerCalls(); !slices.Equal(valeurObtenue, valeurAttendue) {
		t.Fatalf("valeurObtenue %v, valeurAttendue %v: an announced other infohash is never downloaded", valeurObtenue, valeurAttendue)
	}
}

func TestChain_fallbackNeverReturnsAnItemOfAnotherInfohash(t *testing.T) {
	setup := newFallbackSetup(t)
	release := releaseOf(t, trackerTorrent("a.mkv"))
	other := trackerTorrent("other.mkv")
	indexer := setup.addIndexer(indexerA, fallbackTracker)
	indexer.results = torznab.Results{Items: []torznab.Item{{GUID: "liar", InfoHash: release.Infohash}, {GUID: "plain"}}}
	indexer.torrents["liar"], indexer.torrents["plain"] = other, other
	chain := setup.chain(t)

	fetched, err := chain.FetchTorrent(context.Background(), release, chain.NewBudget())

	var fallback *FallbackError
	if !errors.As(err, &fallback) || !errors.Is(err, libraryprovider.ErrNotFound) || fallback.Incomplete() {
		t.Fatalf("valeurObtenue %v, valeurAttendue a complete not-found search", err)
	}
	if fetched.Sanitized != nil || fetched.Sealed != nil {
		t.Fatal("valeurAttendue nothing returned for a torrent of another infohash")
	}
	if len(fallback.Skipped) != 2 || !errors.Is(fallback.Skipped[0], torznab.ErrInfohashMismatch) || !errors.Is(fallback.Skipped[1], torznab.ErrInfohashMismatch) {
		t.Fatalf("valeurObtenue skipped %v, valeurAttendue two infohash mismatches (the attribute lied for the first)", fallback.Skipped)
	}
	if valeurObtenue := fallback.Searched; !slices.Equal(valeurObtenue, []ID{ProviderID, indexerA}) {
		t.Fatalf("valeurObtenue searched %v, valeurAttendue the provider then the indexer", valeurObtenue)
	}
	text := fmt.Sprintf("%v %+v %#v", err, err, err)
	if strings.Contains(text, fallbackName) || strings.Contains(text, fakePasskey) || strings.Contains(text, "a.mkv") {
		t.Fatalf("valeurObtenue %q, valeurAttendue an error without release name, file name or passkey", text)
	}
}

func TestChain_fallbackUsesOnlyIndexersOfTheSameTracker(t *testing.T) {
	setup := newFallbackSetup(t)
	raw := trackerTorrent("a.mkv")
	setup.addIndexer(indexerA, "Other Tracker")
	setup.addIndexer("torznab:c", "")
	same := setup.addIndexer(indexerB, " TRACKER a")
	same.results = torznab.Results{Items: []torznab.Item{{GUID: "win"}}}
	same.torrents["win"] = raw
	chain := setup.chain(t)

	fetched, err := chain.FetchTorrent(context.Background(), releaseOf(t, raw), chain.NewBudget())

	if err != nil || fetched.Source != indexerB {
		t.Fatalf("valeurObtenue %v %q, valeurAttendue the indexer whose tracker matches after normalisation", err, fetched.Source)
	}
	valeurAttendue := []string{"torznab:b.search", "torznab:b.download.win"}
	if valeurObtenue := setup.indexerCalls(); !slices.Equal(valeurObtenue, valeurAttendue) {
		t.Fatalf("valeurObtenue %v, valeurAttendue %v: other tracker and no tracker never called", valeurObtenue, valeurAttendue)
	}
}

func TestChain_fallbackNeedsTheCandidateTracker(t *testing.T) {
	setup := newFallbackSetup(t)
	setup.addIndexer(indexerA, fallbackTracker)
	chain := setup.chain(t)
	release := releaseOf(t, trackerTorrent("a.mkv"))
	release.Tracker = " "

	_, err := chain.FetchTorrent(context.Background(), release, chain.NewBudget())

	var fallback *FallbackError
	if !errors.Is(err, libraryprovider.ErrNotFound) || errors.As(err, &fallback) || len(setup.indexerCalls()) != 0 {
		t.Fatalf("valeurObtenue %v calls %v, valeurAttendue the provider error unchanged and no indexer call", err, setup.indexerCalls())
	}
}

func TestChain_fallbackFirstMatchingIndexerWins(t *testing.T) {
	setup := newFallbackSetup(t)
	raw := trackerTorrent("a.mkv")
	for _, identifier := range []ID{indexerB, indexerA} {
		indexer := setup.addIndexer(identifier, fallbackTracker)
		indexer.results = torznab.Results{Items: []torznab.Item{{GUID: "win"}}}
		indexer.torrents["win"] = raw
	}
	chain := setup.chain(t)

	fetched, err := chain.FetchTorrent(context.Background(), releaseOf(t, raw), chain.NewBudget())

	if err != nil || fetched.Source != indexerB {
		t.Fatalf("valeurObtenue %v %q, valeurAttendue the first indexer of the order", err, fetched.Source)
	}
	if valeurObtenue := setup.indexerCalls(); slices.ContainsFunc(valeurObtenue, func(call string) bool { return strings.HasPrefix(call, "torznab:a") }) {
		t.Fatalf("valeurObtenue %v, valeurAttendue no call to the second indexer", valeurObtenue)
	}
}

func TestChain_fallbackOnlyAfterTorrentNotFound(t *testing.T) {
	for name, failure := range map[string]error{
		"unavailable":   libraryprovider.ErrUnavailable,
		"access denied": libraryprovider.ErrAccessDenied,
		"other":         errors.New("provider failed"),
	} {
		t.Run(name, func(t *testing.T) {
			setup := newFallbackSetup(t)
			setup.provider.failure = failure
			setup.addIndexer(indexerA, fallbackTracker)
			chain := setup.chain(t)

			_, err := chain.FetchTorrent(context.Background(), releaseOf(t, trackerTorrent("a.mkv")), chain.NewBudget())

			if !errors.Is(err, failure) || len(setup.indexerCalls()) != 0 {
				t.Fatalf("valeurObtenue %v calls %v, valeurAttendue the provider error and no indexer call", err, setup.indexerCalls())
			}
		})
	}
}

func TestChain_fallbackWithoutSealerCallsNoIndexer(t *testing.T) {
	setup := newFallbackSetup(t)
	setup.sealer = nil
	setup.addIndexer(indexerA, fallbackTracker)
	chain := setup.chain(t)

	fetched, err := chain.FetchTorrent(context.Background(), releaseOf(t, trackerTorrent("a.mkv")), chain.NewBudget())

	if !errors.Is(err, ErrSecretKeyMissing) || fetched.Sanitized != nil || fetched.Sealed != nil || len(setup.indexerCalls()) != 0 {
		t.Fatalf("valeurObtenue %v %+v calls %v, valeurAttendue ErrSecretKeyMissing before any indexer call", err, fetched, setup.indexerCalls())
	}
}

func TestChain_fallbackWithoutBudgetCallsNoIndexer(t *testing.T) {
	setup := newFallbackSetup(t)
	setup.addIndexer(indexerA, fallbackTracker)
	chain := setup.chain(t)

	_, err := chain.FetchTorrent(context.Background(), releaseOf(t, trackerTorrent("a.mkv")), nil)

	if !errors.Is(err, libraryprovider.ErrNotFound) || len(setup.indexerCalls()) != 0 {
		t.Fatalf("valeurObtenue %v calls %v, valeurAttendue the provider error and no indexer call", err, setup.indexerCalls())
	}
}

func TestChain_fallbackSkipsV2OnlyItemsAndCountsMagnetOnly(t *testing.T) {
	setup := newFallbackSetup(t)
	indexer := setup.addIndexer(indexerA, fallbackTracker)
	indexer.results = torznab.Results{MagnetOnly: 2, Items: []torznab.Item{{GUID: "v2"}, {GUID: "foreign"}}}
	indexer.torrents["v2"] = v2OnlyTorrent()
	indexer.downloadErrs["foreign"] = torznab.ErrForeignDownloadHost
	chain := setup.chain(t)
	budget := chain.NewBudget()

	_, err := chain.FetchTorrent(context.Background(), releaseOf(t, trackerTorrent("a.mkv")), budget)

	var fallback *FallbackError
	if !errors.As(err, &fallback) || fallback.MagnetOnly != 2 || budget.Report()[indexerA].MagnetOnly != 2 {
		t.Fatalf("valeurObtenue %v, valeurAttendue two magnet-only results counted", err)
	}
	if len(fallback.Skipped) != 2 || !errors.Is(fallback.Skipped[0], torrentcheck.ErrV2Only) || !errors.Is(fallback.Skipped[1], torznab.ErrForeignDownloadHost) {
		t.Fatalf("valeurObtenue skipped %v, valeurAttendue the v2-only item then the foreign host", fallback.Skipped)
	}
}

func TestChain_fallbackStopsWhenBudgetExhausted(t *testing.T) {
	setup := newFallbackSetup(t)
	setup.limits.MaxQueriesPerRun = 1
	setup.addIndexer(indexerA, fallbackTracker)
	chain := setup.chain(t)
	budget := chain.NewBudget()
	release := releaseOf(t, trackerTorrent("a.mkv"))
	if _, err := chain.FetchTorrent(context.Background(), release, budget); !errors.Is(err, libraryprovider.ErrNotFound) {
		t.Fatal(err)
	}

	_, err := chain.FetchTorrent(context.Background(), release, budget)

	var fallback *FallbackError
	if !errors.As(err, &fallback) || !fallback.Incomplete() || !errors.Is(fallback.Interrupted[0], ErrBudgetExhausted) {
		t.Fatalf("valeurObtenue %v, valeurAttendue an incomplete search interrupted by the budget", err)
	}
	if valeurObtenue := setup.indexerCalls(); !slices.Equal(valeurObtenue, []string{"torznab:a.search"}) {
		t.Fatalf("valeurObtenue %v, valeurAttendue no request once the budget is exhausted", valeurObtenue)
	}
}

func TestChain_fallbackHonoursRetryAfterOnTheNextCandidate(t *testing.T) {
	setup := newFallbackSetup(t)
	indexer := setup.addIndexer(indexerA, fallbackTracker)
	indexer.searchErr = &torznab.UnavailableError{RetryAfter: 10 * time.Minute}
	chain := setup.chain(t)
	budget := chain.NewBudget()
	release := releaseOf(t, trackerTorrent("a.mkv"))
	if _, err := chain.FetchTorrent(context.Background(), release, budget); err == nil {
		t.Fatal("valeurAttendue an error")
	}

	_, err := chain.FetchTorrent(context.Background(), release, chain.NewBudget())

	var fallback *FallbackError
	if !errors.As(err, &fallback) || !fallback.Incomplete() || !errors.Is(fallback.Interrupted[0], torznab.ErrUnavailable) {
		t.Fatalf("valeurObtenue %v, valeurAttendue the indexer skipped while Retry-After runs", err)
	}
	if valeurObtenue := setup.indexerCalls(); !slices.Equal(valeurObtenue, []string{"torznab:a.search"}) {
		t.Fatalf("valeurObtenue %v, valeurAttendue no request during Retry-After", valeurObtenue)
	}
}

func TestChain_fallbackCapsDownloadsPerCandidate(t *testing.T) {
	setup := newFallbackSetup(t)
	indexer := setup.addIndexer(indexerA, fallbackTracker)
	other := trackerTorrent("other.mkv")
	for itemNumber := range 6 {
		guid := fmt.Sprintf("item-%d", itemNumber)
		indexer.results.Items = append(indexer.results.Items, torznab.Item{GUID: guid})
		indexer.torrents[guid] = other
	}
	chain := setup.chain(t)
	budget := chain.NewBudget()

	_, err := chain.FetchTorrent(context.Background(), releaseOf(t, trackerTorrent("a.mkv")), budget)

	if !errors.Is(err, libraryprovider.ErrNotFound) || budget.Report()[indexerA].Downloads != maxFallbackDownloads {
		t.Fatalf("valeurObtenue %v %+v, valeurAttendue at most %d downloads per candidate", err, budget.Report(), maxFallbackDownloads)
	}
}

func TestChain_fallbackQueryTextIsBoundedAndClean(t *testing.T) {
	setup := newFallbackSetup(t)
	indexer := setup.addIndexer(indexerA, fallbackTracker)
	chain := setup.chain(t)
	release := releaseOf(t, trackerTorrent("a.mkv"))
	release.Name = "Movie\x00.\x1b[31m2020\u009b" + strings.Repeat("x", 2*maxFallbackQueryRunes)

	_, _ = chain.FetchTorrent(context.Background(), release, chain.NewBudget())

	if len(indexer.queries) != 1 {
		t.Fatalf("valeurObtenue %d queries, valeurAttendue 1", len(indexer.queries))
	}
	text := indexer.queries[0].Text
	if strings.ContainsFunc(text, unicode.IsControl) || len([]rune(text)) > maxFallbackQueryRunes || !strings.HasPrefix(text, "Movie.[31m2020") {
		t.Fatalf("valeurObtenue %q, valeurAttendue the name without control characters and bounded", text)
	}
}
