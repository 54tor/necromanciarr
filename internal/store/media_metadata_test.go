package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestStore_upgradesMediaFilesWithMetadataColumns(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "necromanciarr.db")
	database, err := sql.Open("sqlite", dataSourceName(databasePath))
	if err != nil {
		t.Fatal(err)
	}
	legacy := v1Schema(t)
	for _, addedColumn := range []string{"imdb_id TEXT", "title TEXT", "year INTEGER", "season INTEGER", "episode INTEGER"} {
		if strings.Contains(legacy, addedColumn) {
			t.Fatalf("v1 fixture already holds %q", addedColumn)
		}
	}
	if _, err := database.Exec(legacy + `INSERT INTO media_files (path, size, mtime, tmdb_id) VALUES ('/home/u/media/a.mkv', 10, 1, 603);`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	for openingIndex := range 2 {
		openedStore, err := Open(databasePath)
		if err != nil {
			t.Fatalf("opening %d: %v", openingIndex, err)
		}
		files, err := openedStore.ListMediaFiles(ctx)
		if err != nil || len(files) != 1 {
			t.Fatalf("opening %d: gotValue %+v (err %v)", openingIndex, files, err)
		}
		gotValue := files[0]
		if gotValue.Path != "/home/u/media/a.mkv" || gotValue.Size != 10 || gotValue.TMDBID != 603 ||
			gotValue.IMDBID != "" || gotValue.Title != "" || gotValue.Year != 0 || gotValue.Season != 0 || gotValue.Episode != 0 {
			t.Fatalf("opening %d: gotValue %+v, expectedValue the v1 row intact with empty metadata", openingIndex, gotValue)
		}
		if err := openedStore.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStore_setMediaFileMetadataReplacesIdsAndMetadata(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	if err := openedStore.UpsertMediaFile(ctx, MediaFile{Path: "/home/u/media/a.mkv", Size: 1}); err != nil {
		t.Fatal(err)
	}
	files, err := openedStore.ListMediaFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expectedValue := MediaMetadata{TMDBID: 1, TVDBID: 2, IMDBID: "tt1", Title: "Show", Year: 2008, Season: 2, Episode: 3}

	if err := openedStore.SetMediaFileMetadata(ctx, files[0].ID, expectedValue); err != nil {
		t.Fatal(err)
	}

	files, err = openedStore.ListMediaFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gotValue := MediaMetadata{files[0].TMDBID, files[0].TVDBID, files[0].IMDBID, files[0].Title, files[0].Year, files[0].Season, files[0].Episode}
	if gotValue != expectedValue {
		t.Fatalf("expectedValue %+v, gotValue %+v", expectedValue, gotValue)
	}
}
