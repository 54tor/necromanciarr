package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestOpen_createsTablesAndReopens(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "necromanciarr.db")

	firstStore, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}

	secondStore, err := Open(databasePath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if err := secondStore.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestUpsertMediaFile_isIdempotentAndUpdates(t *testing.T) {
	ctx := context.Background()
	testStore, err := Open(filepath.Join(t.TempDir(), "necromanciarr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer testStore.Close()
	modTime := time.Unix(1700000000, 0).UTC()
	file := MediaFile{Path: "/home/u/media/film.mkv", Size: 100, ModTime: modTime, TMDBID: 1}

	for range 2 {
		if err := testStore.UpsertMediaFile(ctx, file); err != nil {
			t.Fatal(err)
		}
	}
	file.Size = 200
	file.TVDBID = 7
	if err := testStore.UpsertMediaFile(ctx, file); err != nil {
		t.Fatal(err)
	}

	gotFiles, err := testStore.ListMediaFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotFiles) != 1 {
		t.Fatalf("got %d rows, want 1", len(gotFiles))
	}
	gotFile := gotFiles[0]
	if gotFile.Size != 200 || gotFile.TVDBID != 7 || gotFile.TMDBID != 1 || !gotFile.ModTime.Equal(modTime) || gotFile.ID == 0 {
		t.Fatalf("unexpected row: %+v", gotFile)
	}
}
