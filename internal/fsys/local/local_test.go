package local

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"necromanciarr/internal/fsys"
)

const (
	testMovieName    = "movie.mkv"
	testMovieContent = "library bytes"
)

// testTree est une bibliothèque à un fichier et un dossier de liens frère, ouvert en racine d'écriture.
type testTree struct {
	libraryDir, linkDir string
	moviePath           string
	root                *writeRoot
}

func newTestTree(t *testing.T, atomicReplace bool) testTree {
	t.Helper()
	baseDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tree := testTree{libraryDir: filepath.Join(baseDir, "library"), linkDir: filepath.Join(baseDir, "links")}
	for _, dir := range []string{tree.libraryDir, tree.linkDir} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tree.moviePath = filepath.Join(tree.libraryDir, testMovieName)
	if err := os.WriteFile(tree.moviePath, []byte(testMovieContent), 0o644); err != nil {
		t.Fatal(err)
	}
	tree.root, err = openWriteRoot(tree.linkDir, atomicReplace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tree.root.Close() })
	return tree
}

func (tree testTree) movieSource() fsys.Location { return fsys.Location{Path: tree.moviePath} }

func (tree testTree) assertLibraryUntouched(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(tree.libraryDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != testMovieName {
		t.Fatalf("library entries changed: %v", entries)
	}
	content, err := os.ReadFile(tree.moviePath)
	if err != nil || string(content) != testMovieContent {
		t.Fatalf("library file changed: %q, %v", content, err)
	}
}

func writeThrough(t *testing.T, root fsys.WriteRoot, relative, content string) {
	t.Helper()
	writer, err := root.CreateExclusive(relative, 0o644)
	if err != nil {
		t.Fatalf("CreateExclusive(%q): %v", relative, err)
	}
	if _, err := io.WriteString(writer, content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertUnconfined(t *testing.T, method string, err error) {
	t.Helper()
	if !errors.Is(err, fsys.ErrUnconfined) {
		t.Errorf("%s error = %v, want ErrUnconfined", method, err)
	}
}

func skipWithoutSymlinks(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlink planting needs a privilege on Windows")
	}
}

func TestOpen_hidesTheOsFile(t *testing.T) {
	tree := newTestTree(t, true)
	file, err := New().Open(tree.moviePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	var opened any = file
	if _, isOsFile := opened.(*os.File); isOsFile {
		t.Error("Open returned *os.File")
	}
	if _, hasChmod := opened.(interface{ Chmod(fs.FileMode) error }); hasChmod {
		t.Error("Open result exposes Chmod")
	}
	if _, hasFd := opened.(interface{ Fd() uintptr }); hasFd {
		t.Error("Open result exposes Fd")
	}
	if _, isWriter := opened.(io.Writer); isWriter {
		t.Error("Open result exposes Write")
	}
	if _, isSeeker := opened.(io.ReadSeeker); !isSeeker {
		t.Error("Open result lost Read and Seek")
	}
}

func TestHardlink_acceptsSourceOutsideTheRootAndRefusesUnconfinedSources(t *testing.T) {
	tree := newTestTree(t, true)
	if err := tree.root.Hardlink(tree.movieSource(), "linked.mkv"); err != nil {
		t.Fatalf("Hardlink from the library: %v", err)
	}
	linkedInfo, err := os.Stat(filepath.Join(tree.linkDir, "linked.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	movieInfo, err := os.Stat(tree.moviePath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(linkedInfo, movieInfo) {
		t.Error("hardlink does not share the library inode")
	}
	refusedSources := []fsys.Location{
		{Path: filepath.Join("..", "library", testMovieName)},
		{Path: testMovieName},
		{Volume: "seedbox", Path: tree.moviePath},
	}
	for _, source := range refusedSources {
		assertUnconfined(t, "Hardlink "+source.String(), tree.root.Hardlink(source, "refused.mkv"))
	}
	if _, err := os.Lstat(filepath.Join(tree.linkDir, "refused.mkv")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("refused hardlink left an entry: %v", err)
	}
	tree.assertLibraryUntouched(t)
}

func TestWriteRoot_refusesSymlinkedParentPlantedBetweenOperations(t *testing.T) {
	skipWithoutSymlinks(t)
	tree := newTestTree(t, true)
	if err := tree.root.MkdirAll("dir", 0o755); err != nil {
		t.Fatal(err)
	}
	writeThrough(t, tree.root, "dir/first", "first")
	movedDir := filepath.Join(tree.linkDir, "moved")
	if err := os.Rename(filepath.Join(tree.linkDir, "dir"), movedDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(tree.libraryDir, filepath.Join(tree.linkDir, "dir")); err != nil {
		t.Fatal(err)
	}
	_, err := tree.root.CreateExclusive("dir/second", 0o644)
	assertUnconfined(t, "CreateExclusive", err)
	assertUnconfined(t, "Hardlink", tree.root.Hardlink(tree.movieSource(), "dir/linked"))
	assertUnconfined(t, "Symlink", tree.root.Symlink(tree.moviePath, "dir/symlinked"))
	assertUnconfined(t, "MkdirAll", tree.root.MkdirAll("dir/sub", 0o755))
	assertUnconfined(t, "Remove", tree.root.Remove("dir/"+testMovieName))
	tree.assertLibraryUntouched(t)
}

func TestWriteRoot_refusesEveryOperationOnceTheRootIsReplaced(t *testing.T) {
	skipOnWindows(t)
	tree := newTestTree(t, true)
	writeThrough(t, tree.root, "kept", "kept")
	if err := os.Rename(tree.linkDir, tree.linkDir+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(tree.linkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := tree.root.CreateExclusive("new", 0o644)
	assertUnconfined(t, "CreateExclusive", err)
	assertUnconfined(t, "MkdirAll", tree.root.MkdirAll("dir", 0o755))
	assertUnconfined(t, "Hardlink", tree.root.Hardlink(tree.movieSource(), "linked"))
	assertUnconfined(t, "Remove", tree.root.Remove("kept"))
	_, err = tree.root.Lstat("kept")
	assertUnconfined(t, "Lstat", err)
	if _, err := os.Lstat(filepath.Join(tree.linkDir+"-moved", "new")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("write reached the moved root: %v", err)
	}
}

func TestOpenWriteRoot_refusesANonCanonicalDirectory(t *testing.T) {
	tree := newTestTree(t, true)
	refusedDirs := []string{
		"links",
		tree.linkDir + string(filepath.Separator),
		tree.linkDir + string(filepath.Separator) + ".." + string(filepath.Separator) + "links",
	}
	for _, dir := range refusedDirs {
		if _, err := New().OpenWriteRoot(dir); !errors.Is(err, fsys.ErrUnconfined) {
			t.Errorf("OpenWriteRoot(%q) error = %v, want ErrUnconfined", dir, err)
		}
	}
	if runtime.GOOS == "windows" {
		return
	}
	aliasDir := filepath.Join(filepath.Dir(tree.linkDir), "alias")
	if err := os.Symlink(tree.linkDir, aliasDir); err != nil {
		t.Fatal(err)
	}
	if _, err := New().OpenWriteRoot(aliasDir); !errors.Is(err, fsys.ErrUnconfined) {
		t.Errorf("OpenWriteRoot(symlink) error = %v, want ErrUnconfined", err)
	}
}

func TestCopyFrom_cancelledContextLeavesNothing(t *testing.T) {
	tree := newTestTree(t, true)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := tree.root.CopyFrom(ctx, tree.movieSource(), "copy.mkv"); !errors.Is(err, context.Canceled) {
		t.Fatalf("CopyFrom error = %v, want context.Canceled", err)
	}
	if _, err := tree.root.Lstat("copy.mkv"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("cancelled copy left an entry: %v", err)
	}
}

// cancellingReader annule le contexte après la première lecture.
type cancellingReader struct {
	reader io.Reader
	cancel context.CancelFunc
}

func (reader cancellingReader) Read(buffer []byte) (int, error) {
	defer reader.cancel()
	return reader.reader.Read(buffer[:1])
}

func TestStreamCopy_stopsWhenTheContextIsCancelledMidway(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	source := cancellingReader{reader: strings.NewReader(testMovieContent), cancel: cancel}
	var destination bytes.Buffer
	_, err := streamCopy(ctx, &destination, source, int64(len(testMovieContent)))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("streamCopy error = %v, want context.Canceled", err)
	}
	if gotValue := destination.Len(); gotValue != 1 {
		t.Errorf("copied %d bytes after cancellation, want 1", gotValue)
	}
}

func TestStreamCopy_refusesASourceThatGrew(t *testing.T) {
	var destination bytes.Buffer
	_, err := streamCopy(t.Context(), &destination, strings.NewReader(testMovieContent), 3)
	if !errors.Is(err, errSourceChanged) {
		t.Fatalf("streamCopy error = %v, want errSourceChanged", err)
	}
}

func TestReadDir_sortsEntriesCreatedOutOfOrder(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"c", "a", "b"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := New().ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	gotValue := make([]string, 0, len(entries))
	for _, entry := range entries {
		gotValue = append(gotValue, entry.Name())
	}
	if expectedValue := []string{"a", "b", "c"}; !slices.Equal(gotValue, expectedValue) {
		t.Errorf("ReadDir = %q, want %q", gotValue, expectedValue)
	}
}

func TestReplace_withoutAtomicCapabilityLeavesBothEntries(t *testing.T) {
	tree := newTestTree(t, false)
	writeThrough(t, tree.root, "old", "old content")
	writeThrough(t, tree.root, "new", "new content")
	if err := tree.root.Replace("old", "new"); !errors.Is(err, fsys.ErrNoAtomicReplace) {
		t.Fatalf("Replace error = %v, want ErrNoAtomicReplace", err)
	}
	for name, expectedValue := range map[string]string{"old": "old content", "new": "new content"} {
		content, err := os.ReadFile(filepath.Join(tree.linkDir, name))
		if err != nil || string(content) != expectedValue {
			t.Errorf("%s = %q, %v, want %q", name, content, err, expectedValue)
		}
	}
}

func TestReplace_movesAcrossDirectoriesOfTheRoot(t *testing.T) {
	tree := newTestTree(t, true)
	if err := tree.root.MkdirAll("from", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := tree.root.MkdirAll("to", 0o755); err != nil {
		t.Fatal(err)
	}
	writeThrough(t, tree.root, "from/file", "moved")
	writeThrough(t, tree.root, "to/file", "replaced")
	if err := tree.root.Replace("from/file", "to/file"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(tree.linkDir, "to", "file"))
	if err != nil || string(content) != "moved" {
		t.Errorf("to/file = %q, %v, want %q", content, err, "moved")
	}
}

func TestSplitRelative_keepsNonASCIIRunesInsideASegment(t *testing.T) {
	segments, err := splitRelative("aįb")
	if err != nil {
		t.Fatal(err)
	}
	if expectedValue := []string{"aįb"}; !slices.Equal(segments, expectedValue) {
		t.Errorf("segments = %q, want %q", segments, expectedValue)
	}
}

func TestCanonical_missingPathWrapsErrCannotCanonicalize(t *testing.T) {
	_, err := New().Canonical(filepath.Join(t.TempDir(), "missing"))
	if !errors.Is(err, fsys.ErrCannotCanonicalize) || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Canonical error = %v, want ErrCannotCanonicalize and fs.ErrNotExist", err)
	}
}

func TestCheckOpenedChild_refusesAHandleThatIsNotTheCheckedEntry(t *testing.T) {
	tree := newTestTree(t, true)
	for _, name := range []string{"checked", "swapped"} {
		if err := tree.root.MkdirAll(name, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	checkedInfo, err := tree.root.root.Lstat("checked")
	if err != nil {
		t.Fatal(err)
	}
	swappedHandle, err := tree.root.root.OpenRoot("swapped")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = swappedHandle.Close() }()
	assertUnconfined(t, "checkOpenedChild", tree.root.checkOpenedChild(swappedHandle, checkedInfo))
}

// probeTime : date ancienne posée par les tests de Chtimes.
var probeTime = time.Unix(1000000000, 0)

func assertMovieTimeUnchanged(t *testing.T, tree testTree, before fs.FileInfo) {
	t.Helper()
	after, err := os.Stat(tree.moviePath)
	if err != nil || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("expectedValue the library file times unchanged, gotValue %v (%v)", after, err)
	}
}

func TestChtimes_setsTheTimesOfAnEntryThisRootCreated(t *testing.T) {
	tree := newTestTree(t, true)
	writeThrough(t, tree.root, "probe", "x")

	if err := tree.root.Chtimes("probe", probeTime, probeTime); err != nil {
		t.Fatal(err)
	}

	info, err := tree.root.Lstat("probe")
	if err != nil || !info.ModTime().Equal(probeTime) {
		t.Fatalf("expectedValue modification time %v, gotValue %v (%v)", probeTime, info, err)
	}
	assertUnconfined(t, "Chtimes ../escape", tree.root.Chtimes("../escape", probeTime, probeTime))
}

func TestChtimes_refusesAHardlinkToAFileOutsideTheRoot(t *testing.T) {
	tree := newTestTree(t, true)
	before, err := os.Stat(tree.moviePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := tree.root.Hardlink(tree.movieSource(), "linked.mkv"); err != nil {
		t.Fatal(err)
	}
	writeThrough(t, tree.root, "swapped.mkv", "x")
	if err := tree.root.Remove("swapped.mkv"); err != nil {
		t.Fatal(err)
	}
	if err := tree.root.Hardlink(tree.movieSource(), "swapped.mkv"); err != nil {
		t.Fatal(err)
	}

	assertUnconfined(t, "Chtimes on a hardlink", tree.root.Chtimes("linked.mkv", probeTime, probeTime))
	assertUnconfined(t, "Chtimes on a created name now a hardlink", tree.root.Chtimes("swapped.mkv", probeTime, probeTime))
	assertMovieTimeUnchanged(t, tree, before)
}

func TestChtimes_refusesAnEntryCreatedByAPreviousOpening(t *testing.T) {
	tree := newTestTree(t, true)
	writeThrough(t, tree.root, "old-probe", "x")
	reopened, err := openWriteRoot(tree.linkDir, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	assertUnconfined(t, "Chtimes from a reopened root", reopened.Chtimes("old-probe", probeTime, probeTime))
}

func TestChtimes_neverFollowsASymlinkToTheLibrary(t *testing.T) {
	skipWithoutSymlinks(t)
	tree := newTestTree(t, true)
	before, err := os.Stat(tree.moviePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := tree.root.Symlink(tree.moviePath, "to-library"); err != nil {
		t.Fatal(err)
	}

	assertUnconfined(t, "Chtimes on a symlink", tree.root.Chtimes("to-library", probeTime, probeTime))
	assertMovieTimeUnchanged(t, tree, before)
}
