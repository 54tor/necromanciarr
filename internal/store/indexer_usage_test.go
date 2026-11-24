package store

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var usageNow = time.Unix(1700000000, 0).UTC()

func TestStore_purgesIndexerUsageOlderThanDay(t *testing.T) {
	openedStore := openTestStore(t)
	ctx := context.Background()
	if err := openedStore.RecordIndexerUsage(ctx, "torznab:a", "query", usageNow.Add(-25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := openedStore.RecordIndexerUsage(ctx, "torznab:a", "query", usageNow.Add(-23*time.Hour)); err != nil {
		t.Fatal(err)
	}

	if err := openedStore.RecordIndexerUsage(ctx, "torznab:a", "query", usageNow); err != nil {
		t.Fatal(err)
	}

	valeurObtenue, err := openedStore.CountIndexerUsage(ctx, "torznab:a", "query", usageNow.Add(-48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if valeurAttendue := 2; valeurObtenue != valeurAttendue {
		t.Fatalf("valeurObtenue %d rows, valeurAttendue %d: the 25 h old row must be purged on write", valeurObtenue, valeurAttendue)
	}
}

func TestStore_countsIndexerUsageByIndexerKindAndWindow(t *testing.T) {
	openedStore := openTestStore(t)
	ctx := context.Background()
	for _, usage := range []struct {
		indexer, kind string
		age           time.Duration
	}{
		{"torznab:a", "query", time.Hour},
		{"torznab:a", "query", 30 * time.Minute},
		{"torznab:a", "download", time.Hour},
		{"torznab:b", "query", time.Hour},
		{"torznab:a", "query", 5 * time.Hour},
	} {
		if err := openedStore.RecordIndexerUsage(ctx, usage.indexer, usage.kind, usageNow.Add(-usage.age)); err != nil {
			t.Fatal(err)
		}
	}

	valeurObtenue, err := openedStore.CountIndexerUsage(ctx, "torznab:a", "query", usageNow.Add(-2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if valeurAttendue := 2; valeurObtenue != valeurAttendue {
		t.Fatalf("valeurObtenue %d, valeurAttendue %d", valeurObtenue, valeurAttendue)
	}
}

// Deux handles sur le même fichier imitent deux processus : jamais plus de limit prises au total.
func TestStore_takeIndexerUsageNeverExceedsLimitAcrossHandles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "necromanciarr.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	const limit, attemptsPerHandle = 7, 20
	var granted atomic.Int32
	var group sync.WaitGroup
	for _, handle := range []*Store{first, second} {
		for range attemptsPerHandle {
			group.Add(1)
			go func() {
				defer group.Done()
				taken, takeErr := handle.TakeIndexerUsage(context.Background(), "torznab:a", "download", usageNow, usageNow.Add(-24*time.Hour), limit)
				if takeErr != nil {
					t.Error(takeErr)
				}
				if taken {
					granted.Add(1)
				}
			}()
		}
	}
	group.Wait()

	if valeurObtenue := int(granted.Load()); valeurObtenue != limit {
		t.Fatalf("valeurObtenue %d takes, valeurAttendue %d", valeurObtenue, limit)
	}
	valeurObtenue, err := first.CountIndexerUsage(context.Background(), "torznab:a", "download", usageNow.Add(-24*time.Hour))
	if err != nil || valeurObtenue != limit {
		t.Fatalf("valeurObtenue %d rows (err %v), valeurAttendue %d", valeurObtenue, err, limit)
	}
}

func TestStore_takeIndexerUsageRefusesWithoutRecordingWhenFull(t *testing.T) {
	openedStore := openTestStore(t)
	ctx := context.Background()
	if taken, err := openedStore.TakeIndexerUsage(ctx, "torznab:a", "query", usageNow, usageNow.Add(-24*time.Hour), 1); err != nil || !taken {
		t.Fatalf("first take: taken %v err %v", taken, err)
	}

	taken, err := openedStore.TakeIndexerUsage(ctx, "torznab:a", "query", usageNow, usageNow.Add(-24*time.Hour), 1)

	valeurObtenue, countErr := openedStore.CountIndexerUsage(ctx, "torznab:a", "query", usageNow.Add(-24*time.Hour))
	if err != nil || countErr != nil || taken || valeurObtenue != 1 {
		t.Fatalf("taken %v rows %d err %v/%v, valeurAttendue a refusal that records nothing", taken, valeurObtenue, err, countErr)
	}
}
