package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

var cacheNow = time.Unix(1700000000, 0).UTC()

func putCache(t *testing.T, openedStore *Store, indexer, key string, items string, empty bool, expiresAt time.Time) {
	t.Helper()
	if err := openedStore.PutIndexerCache(context.Background(), indexer, key, "search", []byte(items), empty, expiresAt); err != nil {
		t.Fatal(err)
	}
}

func TestIndexerCache_returnsRowsUntilTheyExpire(t *testing.T) {
	openedStore := openTestStore(t)
	putCache(t, openedStore, "torznab:a", "k1", `[{"s":1}]`, false, cacheNow.Add(6*time.Hour))
	putCache(t, openedStore, "torznab:a", "k2", `[]`, true, cacheNow.Add(24*time.Hour))

	items, found, empty, err := openedStore.GetIndexerCache(context.Background(), "torznab:a", "k1", cacheNow.Add(5*time.Hour))
	if err != nil || !found || empty || string(items) != `[{"s":1}]` {
		t.Fatalf("valeurObtenue %s %t %t %v, valeurAttendue the live success row", items, found, empty, err)
	}
	_, found, _, err = openedStore.GetIndexerCache(context.Background(), "torznab:a", "k1", cacheNow.Add(6*time.Hour))
	if err != nil || found {
		t.Fatalf("valeurObtenue found %t %v, valeurAttendue an expired row to be a miss", found, err)
	}
	_, found, empty, err = openedStore.GetIndexerCache(context.Background(), "torznab:a", "k2", cacheNow.Add(23*time.Hour))
	if err != nil || !found || !empty {
		t.Fatalf("valeurObtenue found %t empty %t %v, valeurAttendue the empty row", found, empty, err)
	}
}

func TestIndexerCache_keysAreScopedByIndexer(t *testing.T) {
	openedStore := openTestStore(t)
	putCache(t, openedStore, "torznab:a", "same", `[{"s":1}]`, false, cacheNow.Add(time.Hour))

	_, found, _, err := openedStore.GetIndexerCache(context.Background(), "torznab:b", "same", cacheNow)
	if err != nil || found {
		t.Fatalf("valeurObtenue found %t %v, valeurAttendue no poisoning across indexers", found, err)
	}
}

func TestIndexerCache_putReplacesAndPurgesLongExpiredRows(t *testing.T) {
	openedStore := openTestStore(t)
	putCache(t, openedStore, "torznab:a", "old", `[]`, true, cacheNow.Add(-48*time.Hour))
	putCache(t, openedStore, "torznab:a", "k", `[{"s":1}]`, false, cacheNow.Add(time.Hour))
	putCache(t, openedStore, "torznab:a", "k", `[{"s":2}]`, false, cacheNow.Add(6*time.Hour))

	var rows int
	if err := openedStore.database.QueryRow(`SELECT count(*) FROM indexer_cache`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("valeurObtenue %d rows (%v), valeurAttendue 1: the expired row purged and the key replaced", rows, err)
	}
	items, _, _, _ := openedStore.GetIndexerCache(context.Background(), "torznab:a", "k", cacheNow)
	if string(items) != `[{"s":2}]` {
		t.Fatalf("valeurObtenue %s, valeurAttendue the fresh answer", items)
	}
}

func TestIndexerCache_capsRowsPerIndexerKeepingTheFreshest(t *testing.T) {
	openedStore := openTestStore(t)
	for index := range IndexerCacheMaxRows + 5 {
		putCache(t, openedStore, "torznab:a", fmt.Sprintf("k%05d", index), `[]`, true, cacheNow.Add(time.Hour+time.Duration(index)*time.Second))
	}
	putCache(t, openedStore, "torznab:b", "other", `[]`, true, cacheNow.Add(time.Hour))

	var rows int
	if err := openedStore.database.QueryRow(`SELECT count(*) FROM indexer_cache WHERE indexer = 'torznab:a'`).Scan(&rows); err != nil || rows != IndexerCacheMaxRows {
		t.Fatalf("valeurObtenue %d rows (%v), valeurAttendue %d", rows, err, IndexerCacheMaxRows)
	}
	_, found, _, _ := openedStore.GetIndexerCache(context.Background(), "torznab:a", "k00000", cacheNow)
	_, newest, _, _ := openedStore.GetIndexerCache(context.Background(), "torznab:a", fmt.Sprintf("k%05d", IndexerCacheMaxRows+4), cacheNow)
	_, other, _, _ := openedStore.GetIndexerCache(context.Background(), "torznab:b", "other", cacheNow)
	if found || !newest || !other {
		t.Fatalf("valeurObtenue oldest %t newest %t other indexer %t, valeurAttendue false true true", found, newest, other)
	}
}

func TestIndexerCache_refusesAnOversizedAnswer(t *testing.T) {
	openedStore := openTestStore(t)
	err := openedStore.PutIndexerCache(context.Background(), "torznab:a", "k", "search", []byte(strings.Repeat("x", IndexerCacheMaxItemsBytes+1)), false, cacheNow.Add(time.Hour))
	if err == nil {
		t.Fatal("valeurObtenue no error, valeurAttendue the oversized answer refused")
	}
}

func TestGroupSearched_markIsIdempotentAndExpires(t *testing.T) {
	openedStore := openTestStore(t)
	ctx := context.Background()
	for range 2 {
		if err := openedStore.MarkGroupSearched(ctx, "g1", "torznab:a", cacheNow); err != nil {
			t.Fatal(err)
		}
	}
	var rows int
	if err := openedStore.database.QueryRow(`SELECT count(*) FROM indexer_searched`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("valeurObtenue %d rows (%v), valeurAttendue 1", rows, err)
	}
	searched, err := openedStore.GroupSearched(ctx, "g1", "torznab:a")
	other, otherErr := openedStore.GroupSearched(ctx, "g1", "torznab:b")
	if err != nil || otherErr != nil || !searched || other {
		t.Fatalf("valeurObtenue %t %t, valeurAttendue true false", searched, other)
	}
	recent, _ := openedStore.GroupSearchedSince(ctx, "g1", "torznab:a", cacheNow.Add(-time.Hour))
	expired, _ := openedStore.GroupSearchedSince(ctx, "g1", "torznab:a", cacheNow.Add(time.Hour))
	if !recent || expired {
		t.Fatalf("valeurObtenue recent %t expired %t, valeurAttendue true false", recent, expired)
	}
	if err := openedStore.MarkGroupSearched(ctx, "g2", "torznab:a", cacheNow.Add(IndexerSearchedTTL+time.Hour)); err != nil {
		t.Fatal(err)
	}
	if searched, _ := openedStore.GroupSearched(ctx, "g1", "torznab:a"); searched {
		t.Fatal("valeurObtenue the 7 day old mark kept, valeurAttendue purged by the next write")
	}
}

func TestStore_upgradeCreatesCacheTablesOnAnOldDatabaseTwice(t *testing.T) {
	databasePath := createV1Database(t)
	for openingIndex := range 2 {
		openedStore, err := Open(databasePath)
		if err != nil {
			t.Fatalf("opening %d: %v", openingIndex, err)
		}
		putCache(t, openedStore, "torznab:a", "k", `[]`, true, cacheNow.Add(time.Hour))
		if err := openedStore.MarkGroupSearched(context.Background(), "g", "torznab:a", cacheNow); err != nil {
			t.Fatal(err)
		}
		if err := openedStore.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestKnownCandidate_ignoresStaleRowsOnly(t *testing.T) {
	openedStore := openTestStore(t)
	known, err := openedStore.KnownCandidate(context.Background(), "aa", "tracker-a")
	if err != nil || known {
		t.Fatalf("valeurObtenue %t %v, valeurAttendue an unknown pair", known, err)
	}
	if _, _, err := openedStore.SaveCandidate(context.Background(), Candidate{Infohash: "aa", Tracker: "tracker-a", Name: "n", TotalSize: 1}, nil, cacheNow); err != nil {
		t.Fatal(err)
	}
	known, _ = openedStore.KnownCandidate(context.Background(), "aa", "tracker-a")
	otherTracker, _ := openedStore.KnownCandidate(context.Background(), "aa", "tracker-b")
	if !known || otherTracker {
		t.Fatalf("valeurObtenue %t %t, valeurAttendue true false", known, otherTracker)
	}
	if _, err := openedStore.database.Exec(`UPDATE torrent_candidates SET state = 'stale'`); err != nil {
		t.Fatal(err)
	}
	if known, _ = openedStore.KnownCandidate(context.Background(), "aa", "tracker-a"); known {
		t.Fatal("valeurObtenue a stale row known, valeurAttendue revivable (not known)")
	}
}

func TestGroupSearched_aCandidateGoingStaleClearsTheMarks(t *testing.T) {
	openedStore := openTestStore(t)
	ctx := context.Background()
	candidateID, _, err := openedStore.SaveCandidate(ctx, Candidate{Infohash: "aa", Tracker: "tracker-a", Name: "n", TotalSize: 1}, nil, cacheNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := openedStore.MarkGroupSearched(ctx, "g1", "torznab:a", cacheNow); err != nil {
		t.Fatal(err)
	}

	if err := openedStore.SetCandidateState(ctx, candidateID, CandidateStale, cacheNow); err != nil {
		t.Fatal(err)
	}

	if searched, _ := openedStore.GroupSearched(ctx, "g1", "torznab:a"); searched {
		t.Fatal("valeurObtenue the mark kept, valeurAttendue cleared so the stale candidate can be found again")
	}
}
