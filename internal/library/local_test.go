package library

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"necromanciarr/internal/store"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	testStore, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testStore.Close() })
	return testStore
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScan_recordsSizeAndPath(t *testing.T) {
	root := t.TempDir()
	movieFilePath := filepath.Join(root, "Movies", "Film (2020)", "Film.mkv")
	writeFile(t, movieFilePath, "12345")
	writeFile(t, filepath.Join(root, "Movies", "Film (2020)", "Film.nfo"), "x")
	testStore := openTestStore(t)

	if err := Scan(context.Background(), root, testStore); err != nil {
		t.Fatal(err)
	}

	gotFiles, err := testStore.ListMediaFiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(gotFiles) != 1 {
		t.Fatalf("expectedValue 1 media file, gotValue %d", len(gotFiles))
	}
	if gotFiles[0].Path != movieFilePath || gotFiles[0].Size != 5 || gotFiles[0].ModTime.IsZero() {
		t.Fatalf("unexpected row %+v", gotFiles[0])
	}
}

func TestScan_doesNotFollowSymlinkLoops(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a", "one.mkv"), "1")
	if err := os.Symlink(root, filepath.Join(root, "a", "loop")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "a"), filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	testStore := openTestStore(t)

	if err := Scan(context.Background(), root, testStore); err != nil {
		t.Fatal(err)
	}

	gotFiles, _ := testStore.ListMediaFiles(context.Background())
	if len(gotFiles) != 1 {
		t.Fatalf("expectedValue 1 media file, gotValue %d", len(gotFiles))
	}
}

func TestScan_isIdempotent(t *testing.T) {
	root := t.TempDir()
	movieFilePath := filepath.Join(root, "Film.mkv")
	writeFile(t, movieFilePath, "1")
	testStore := openTestStore(t)
	if err := Scan(context.Background(), root, testStore); err != nil {
		t.Fatal(err)
	}
	writeFile(t, movieFilePath, "123")

	if err := Scan(context.Background(), root, testStore); err != nil {
		t.Fatal(err)
	}

	gotFiles, _ := testStore.ListMediaFiles(context.Background())
	if len(gotFiles) != 1 || gotFiles[0].Size != 3 {
		t.Fatalf("expectedValue one row of size 3, gotValue %+v", gotFiles)
	}
}

func TestScan_stopsOnCancelledContext(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "Film.mkv"), "1")
	cancelledContext, cancel := context.WithCancel(context.Background())
	cancel()

	if err := Scan(cancelledContext, root, openTestStore(t)); err == nil {
		t.Fatal("expectedValue a context error")
	}
}
