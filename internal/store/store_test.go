package store

import (
	"context"
	"os"
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
	defer func() { _ = testStore.Close() }()
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

func TestDataSourceName_escapesSpecialCharactersAndKeepsDriveLetter(t *testing.T) {
	testCases := map[string]string{
		"/home/u/a?b#c.db": "file:/home/u/a%3Fb%23c.db?" + pragmaQuery,
		"necromanciarr.db": "file:necromanciarr.db?" + pragmaQuery,
		"C:/data/x.db":     "file:C:/data/x.db?" + pragmaQuery,
	}
	for inputPath, expectedValue := range testCases {
		if gotValue := dataSourceName(inputPath); gotValue != expectedValue {
			t.Errorf("dataSourceName(%q) = %q, want %q", inputPath, gotValue, expectedValue)
		}
	}
}

func TestOpen_acceptsQuestionMarkAndHashInPath(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "dir?x#y", "necromanciarr.db")
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o755); err != nil {
		t.Fatal(err)
	}
	openedStore, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := openedStore.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(databasePath); err != nil {
		t.Fatalf("database not created at the literal path: %v", err)
	}
}
