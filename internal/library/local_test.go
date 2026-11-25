package library

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/fsystest"
	"necromanciarr/internal/store"
)

// scanLocal parcourt un chemin local par le registre du disque local, comme le fait la production.
func scanLocal(t *testing.T, ctx context.Context, root string, mediaStore *store.Store) error {
	t.Helper()
	return Scan(ctx, fsys.Location{Path: root}, fsystest.LocalRegistry(t), mediaStore)
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	testStore, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = testStore.Close() })
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

	if err := scanLocal(t, context.Background(), root, testStore); err != nil {
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

	if err := scanLocal(t, context.Background(), root, testStore); err != nil {
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
	if err := scanLocal(t, context.Background(), root, testStore); err != nil {
		t.Fatal(err)
	}
	writeFile(t, movieFilePath, "123")

	if err := scanLocal(t, context.Background(), root, testStore); err != nil {
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

	if err := scanLocal(t, cancelledContext, root, openTestStore(t)); err == nil {
		t.Fatal("expectedValue a context error")
	}
}

func TestScan_followsSymlinkedRootAndKeepsRootPrefix(t *testing.T) {
	realRoot := t.TempDir()
	writeFile(t, filepath.Join(realRoot, "Film.mkv"), "12345")
	linkRoot := filepath.Join(t.TempDir(), "library")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Fatal(err)
	}
	testStore := openTestStore(t)

	if err := scanLocal(t, context.Background(), linkRoot, testStore); err != nil {
		t.Fatal(err)
	}

	gotFiles, err := testStore.ListMediaFiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expectedPath := filepath.Join(linkRoot, "Film.mkv")
	if len(gotFiles) != 1 || gotFiles[0].Path != expectedPath {
		t.Fatalf("expectedValue one file at %s, gotValue %+v", expectedPath, gotFiles)
	}
}

func TestScan_doesNotFollowSymlinksOutOfTheLibrary(t *testing.T) {
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "Elsewhere.mkv"), "1")
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "Inside.mkv"), "1")
	if err := os.Symlink(outside, filepath.Join(root, "dir-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "Elsewhere.mkv"), filepath.Join(root, "file-link.mkv")); err != nil {
		t.Fatal(err)
	}
	testStore := openTestStore(t)

	if err := scanLocal(t, context.Background(), root, testStore); err != nil {
		t.Fatal(err)
	}

	gotFiles, _ := testStore.ListMediaFiles(context.Background())
	if len(gotFiles) != 1 || gotFiles[0].Path != filepath.Join(root, "Inside.mkv") {
		t.Fatalf("valeurAttendue only Inside.mkv, valeurObtenue %+v", gotFiles)
	}
}
