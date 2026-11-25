package pipeline

import (
	"slices"
	"strings"
	"testing"
	"time"

	"necromanciarr/internal/torznab"
)

// Run 1 : le groupe 604 est cherché jusqu'au bout, le budget s'arrête au texte du groupe 605. Run 2 : seul 605 est repris.
func TestMatch_resumesAfterBudgetExhausted(t *testing.T) {
	testCase := newIndexerMatchCase(t)
	testCase.limits.MaxQueriesPerRun = 3
	size := 4 * gibibyte
	testCase.addMovie("/srv/media/missing.mkv", size, 604, "Missing")
	testCase.addMovie("/srv/media/other.mkv", 3*gibibyte, 605, "Other")
	indexer := testCase.addIndexer(indexerSameTracker, sealedTracker)
	indexer.offer("win", size, indexerTorrent(t, "win", size))
	first := testCase.run()
	queriesAfterFirst := len(indexer.queries)

	second := testCase.run()

	resumed := indexer.queries[queriesAfterFirst:]
	for _, query := range resumed {
		if query.TMDBID == 604 || strings.Contains(query.Text, "Missing") {
			t.Fatalf("valeurObtenue query %+v on run 2, valeurAttendue the searched group skipped", query)
		}
	}
	if first.Reasons[ReasonIndexerBudgetExhausted] != 1 || second.Reasons[ReasonIndexerBudgetExhausted] != 0 ||
		!slices.ContainsFunc(resumed, func(query torznab.Query) bool { return strings.Contains(query.Text, "Other") }) {
		t.Fatalf("valeurObtenue first %+v second %+v resumed %+v, valeurAttendue the interrupted group searched to the end on run 2", first.Reasons, second.Reasons, resumed)
	}
	if hits := second.Indexers[string(indexerSameTracker)].CacheHits; hits != 1 {
		t.Fatalf("valeurObtenue %d cache hits, valeurAttendue the id query of the resumed group served by the cache", hits)
	}
}

func TestMatch_neverRedownloadsKnownInfohash(t *testing.T) {
	testCase := newIndexerMatchCase(t)
	size := 4 * gibibyte
	testCase.addMovie("/srv/media/missing.mkv", size, 604, "Missing")
	indexer := testCase.addIndexer(indexerSameTracker, sealedTracker)
	raw := indexerTorrent(t, "win", size)
	indexer.offer("win", size, raw)
	indexer.results.Items[0].InfoHash = infohashOfTorrent(t, raw)
	testCase.run()
	requestsAfterFirst := indexer.requests()

	testCase.run()

	if indexer.requests() != requestsAfterFirst {
		t.Fatalf("valeurObtenue %d requests, valeurAttendue none: the searched group is skipped", indexer.requests()-requestsAfterFirst)
	}
	testCase.now = testCase.now.Add(8 * 24 * time.Hour)
	queriesBefore := len(indexer.queries)

	result := testCase.run()

	if len(indexer.queries) == queriesBefore || len(indexer.downloads) != 1 || len(testCase.candidates()) != 1 || result.Failed != 0 {
		t.Fatalf("valeurObtenue queries %d->%d downloads %v candidates %d, valeurAttendue a new search but the known infohash not downloaded again",
			queriesBefore, len(indexer.queries), indexer.downloads, len(testCase.candidates()))
	}
}

func TestMatch_staleIndexerCandidateIsDownloadedAgain(t *testing.T) {
	testCase := newIndexerMatchCase(t)
	size := 4 * gibibyte
	file := testCase.addMovie("/srv/media/missing.mkv", size, 604, "Missing")
	indexer := testCase.addIndexer(indexerSameTracker, sealedTracker)
	raw := indexerTorrent(t, "win", size)
	indexer.offer("win", size, raw)
	indexer.results.Items[0].InfoHash = infohashOfTorrent(t, raw)
	testCase.run()
	if _, err := testCase.store.InvalidateCandidatesForFile(t.Context(), file.ID, testCase.now); err != nil {
		t.Fatal(err)
	}
	testCase.now = testCase.now.Add(8 * 24 * time.Hour)

	testCase.run()

	if len(indexer.downloads) != 2 {
		t.Fatalf("valeurObtenue downloads %v, valeurAttendue the stale row revived by a second download", indexer.downloads)
	}
}

func TestGroupKey_changesWithTheGroupAndItsUncoveredSizes(t *testing.T) {
	movie := indexerGroupKey{tmdbID: 604}
	base := groupKeyOf(movie, []int64{5, 9})

	if groupKeyOf(movie, []int64{9, 5}) != base {
		t.Fatal("valeurObtenue a key depending on the file order, valeurAttendue the same key")
	}
	for name, other := range map[string]string{
		"another size":   groupKeyOf(movie, []int64{5, 10}),
		"fewer files":    groupKeyOf(movie, []int64{5}),
		"another movie":  groupKeyOf(indexerGroupKey{tmdbID: 605}, []int64{5, 9}),
		"series season":  groupKeyOf(indexerGroupKey{tvdbID: 604, season: 1}, []int64{5, 9}),
		"another season": groupKeyOf(indexerGroupKey{tvdbID: 604, season: 2}, []int64{5, 9}),
	} {
		if other == base {
			t.Fatalf("valeurObtenue the same key for %s, valeurAttendue a new key so the library is searched again", name)
		}
	}
}
