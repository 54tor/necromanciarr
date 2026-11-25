package linker

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"necromanciarr/internal/config"
	"necromanciarr/internal/fsys"
	"necromanciarr/internal/matcher"
	"necromanciarr/internal/store"
	"necromanciarr/internal/torrentcheck"
)

func writeFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path) // #nosec G304 -- chemin de test sous t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func assertExists(t *testing.T, path string, expectedExists bool) {
	t.Helper()
	_, err := os.Lstat(path)
	gotExists := err == nil
	if gotExists != expectedExists {
		t.Fatalf("exists(%s) = %v, want %v (err %v)", path, gotExists, expectedExists, err)
	}
}

func packTorrent() *torrentcheck.Torrent {
	return &torrentcheck.Torrent{
		Root: "Show.S01",
		Files: []torrentcheck.File{
			{Path: "Season 1/e01.mkv", Size: 10, Offset: 0},
			{Path: "Season 1/e02.mkv", Size: 10, Offset: 10},
			{Path: "Extras/making-of/part.mkv", Size: 5, Offset: 20},
			{Path: "show.nfo", Size: 3, Offset: 25},
		},
	}
}

func TestPlan_reproducesRootFolderAndRelativePaths(t *testing.T) {
	destRoot := t.TempDir()
	assignment := matcher.Assignment{ByTorrentFile: map[int]store.MediaFile{
		0: {Path: "/home/u/library/Show/S01E01.mkv", Size: 10},
		2: {Path: "/home/u/library/Show/extras/making.mkv", Size: 5},
	}}
	gotValue, err := Plan(packTorrent(), assignment, destRoot, fsys.LocalPaths())
	if err != nil {
		t.Fatal(err)
	}
	expectedValue := []Op{
		{
			Source:      "/home/u/library/Show/S01E01.mkv",
			Destination: filepath.Join(destRoot, "Show.S01", "Season 1", "e01.mkv"),
			Root:        destRoot,
			Size:        10,
		},
		{
			Source:      "/home/u/library/Show/extras/making.mkv",
			Destination: filepath.Join(destRoot, "Show.S01", "Extras", "making-of", "part.mkv"),
			Root:        destRoot,
			Size:        5,
		},
	}
	if !reflect.DeepEqual(gotValue, expectedValue) {
		t.Fatalf("Plan = %+v, want %+v", gotValue, expectedValue)
	}
}

func TestPlan_skipsAmbiguousAndAbsentFiles(t *testing.T) {
	assignment := matcher.Assignment{
		ByTorrentFile: map[int]store.MediaFile{3: {Path: "/home/u/library/show.nfo", Size: 3}},
		Ambiguous: map[int][]store.MediaFile{
			0: {{Path: "/home/u/library/a.mkv", Size: 10}, {Path: "/home/u/library/b.mkv", Size: 10}},
		},
	}
	gotValue, err := Plan(packTorrent(), assignment, t.TempDir(), fsys.LocalPaths())
	if err != nil {
		t.Fatal(err)
	}
	expectedLength := 1
	if len(gotValue) != expectedLength || gotValue[0].Source != "/home/u/library/show.nfo" {
		t.Fatalf("Plan = %+v, want only the nfo", gotValue)
	}
}

func TestPlan_singleFileTorrentLinksUnderDestRoot(t *testing.T) {
	destRoot := t.TempDir()
	torrent := &torrentcheck.Torrent{Root: "Movie.2020.mkv", Files: []torrentcheck.File{{Path: "Movie.2020.mkv", Size: 7}}}
	assignment := matcher.Assignment{ByTorrentFile: map[int]store.MediaFile{0: {Path: "/home/u/library/Movie (2020).mkv", Size: 7}}}
	gotValue, err := Plan(torrent, assignment, destRoot, fsys.LocalPaths())
	if err != nil {
		t.Fatal(err)
	}
	expectedDestination := filepath.Join(destRoot, "Movie.2020.mkv")
	if len(gotValue) != 1 || gotValue[0].Destination != expectedDestination {
		t.Fatalf("Plan = %+v, want destination %s", gotValue, expectedDestination)
	}
}

func TestPlan_rejectsDestinationOutsideRoot(t *testing.T) {
	forgedPaths := []string{"../escape.mkv", "/etc/passwd", "a/../../escape.mkv", ""}
	for _, forgedPath := range forgedPaths {
		torrent := &torrentcheck.Torrent{Root: "Pack", Files: []torrentcheck.File{{Path: forgedPath, Size: 1}}}
		assignment := matcher.Assignment{ByTorrentFile: map[int]store.MediaFile{0: {Path: "/home/u/library/x.mkv", Size: 1}}}
		_, err := Plan(torrent, assignment, t.TempDir(), fsys.LocalPaths())
		if !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("Plan(%q) error = %v, want ErrUnsafePath", forgedPath, err)
		}
	}
	forgedRoot := &torrentcheck.Torrent{Root: "..", Files: []torrentcheck.File{{Path: "x.mkv", Size: 1}}}
	assignment := matcher.Assignment{ByTorrentFile: map[int]store.MediaFile{0: {Path: "/home/u/library/x.mkv", Size: 1}}}
	if _, err := Plan(forgedRoot, assignment, t.TempDir(), fsys.LocalPaths()); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Plan(root ..) error = %v, want ErrUnsafePath", err)
	}
}

func TestApply_hardlinkSameDevice(t *testing.T) {
	workDir := t.TempDir()
	source := filepath.Join(workDir, "library", "a.mkv")
	content := []byte("episode-one")
	writeFile(t, source, content)
	destination := filepath.Join(workDir, "links", "Pack", "Season 1", "e01.mkv")
	operation := Op{Source: source, Destination: destination, Root: workDir, Size: int64(len(content))}
	result, err := applyOps(t, []Op{operation}, []Kind{Hardlink})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Used, []Kind{Hardlink}) {
		t.Fatalf("Used = %v, want [Hardlink]", result.Used)
	}
	sourceInfo, _ := os.Stat(source)
	destinationInfo, _ := os.Stat(destination)
	if !os.SameFile(sourceInfo, destinationInfo) {
		t.Fatal("destination is not a hardlink to the source")
	}
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits of the created parent are not meaningful on Windows")
	}
	expectedMode := os.FileMode(0o755)
	parentInfo, _ := os.Stat(filepath.Dir(destination))
	if parentInfo.Mode().Perm()&^expectedMode != 0 {
		t.Fatalf("parent mode = %v, want at most %v", parentInfo.Mode().Perm(), expectedMode)
	}
}

func TestApply_symlinkUsesAbsoluteTarget(t *testing.T) {
	workDir := t.TempDir()
	source := filepath.Join(workDir, "library", "a.mkv")
	writeFile(t, source, []byte("abc"))
	destination := filepath.Join(workDir, "links", "a.mkv")
	result, err := applyOps(t, []Op{{Source: source, Destination: destination, Root: workDir, Size: 3}}, []Kind{Symlink})
	if err != nil {
		t.Fatal(err)
	}
	gotTarget, err := os.Readlink(destination)
	if err != nil {
		t.Fatal(err)
	}
	expectedTarget, err := filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(gotTarget) || gotTarget != expectedTarget || result.Used[0] != Symlink {
		t.Fatalf("target = %q used %v, want absolute %q via Symlink", gotTarget, result.Used, expectedTarget)
	}
}

type fakeLinker struct {
	calls map[Kind]int
	errs  map[Kind]error
}

func newFakeApplier(errs map[Kind]error) (applier, *fakeLinker) {
	fake := &fakeLinker{calls: map[Kind]int{}, errs: errs}
	linkers := map[Kind]linkFunc{}
	for _, kind := range []Kind{Hardlink, Reflink, Symlink} {
		linkers[kind] = func(root fsys.WriteRoot, source fsys.Location, relative string) error {
			fake.calls[kind]++
			if fakeError := fake.errs[kind]; fakeError != nil {
				return fakeError
			}
			return root.Symlink(source.Path, relative)
		}
	}
	return applier{linkers: linkers}, fake
}

func singleOp(t *testing.T) Op {
	t.Helper()
	workDir := t.TempDir()
	source := filepath.Join(workDir, "library", "a.mkv")
	writeFile(t, source, []byte("abcd"))
	return Op{Source: source, Destination: filepath.Join(workDir, "links", "Pack", "a.mkv"), Root: workDir, Size: 4}
}

func TestApply_fallsBackAndReportsKindUsed(t *testing.T) {
	fallbackErrors := []error{ErrCrossDevice, ErrUnsupported, ErrLinkRefused}
	for _, fallbackError := range fallbackErrors {
		fakeApplier, fake := newFakeApplier(map[Kind]error{Reflink: ErrUnsupported, Hardlink: fallbackError})
		result, err := fakeApplier.applyOps(t, []Op{singleOp(t)}, []Kind{Reflink, Hardlink, Symlink})
		if err != nil {
			t.Fatalf("%v: %v", fallbackError, err)
		}
		expectedUsed := []Kind{Symlink}
		if !reflect.DeepEqual(result.Used, expectedUsed) {
			t.Fatalf("%v: Used = %v, want %v", fallbackError, result.Used, expectedUsed)
		}
		expectedCalls := map[Kind]int{Reflink: 1, Hardlink: 1, Symlink: 1}
		if !reflect.DeepEqual(fake.calls, expectedCalls) {
			t.Fatalf("%v: calls = %v, want %v", fallbackError, fake.calls, expectedCalls)
		}
	}
}

func TestApply_crossDeviceReturnsTypedError(t *testing.T) {
	fakeApplier, fake := newFakeApplier(map[Kind]error{Hardlink: ErrCrossDevice})
	operation := singleOp(t)
	_, err := fakeApplier.applyOps(t, []Op{operation}, []Kind{Hardlink})
	if !errors.Is(err, ErrCrossDevice) {
		t.Fatalf("error = %v, want ErrCrossDevice", err)
	}
	if fake.calls[Symlink] != 0 || fake.calls[Reflink] != 0 {
		t.Fatalf("calls = %v, an unlisted kind was tried", fake.calls)
	}
	assertExists(t, filepath.Dir(operation.Destination), false)
}

func TestApply_unexpectedErrorStopsWithoutFallback(t *testing.T) {
	unexpectedError := errors.New("disk on fire")
	fakeApplier, fake := newFakeApplier(map[Kind]error{Hardlink: unexpectedError})
	_, err := fakeApplier.applyOps(t, []Op{singleOp(t)}, []Kind{Hardlink, Symlink})
	if !errors.Is(err, unexpectedError) {
		t.Fatalf("error = %v, want the unexpected error", err)
	}
	if fake.calls[Symlink] != 0 {
		t.Fatal("fell back after an error that is not a link refusal")
	}
}

func TestApply_rejectsEmptyKindList(t *testing.T) {
	if _, err := applyOps(t, []Op{singleOp(t)}, nil); !errors.Is(err, ErrNoKind) {
		t.Fatalf("error = %v, want ErrNoKind", err)
	}
}

func TestApply_rollsBackOnPartialFailure(t *testing.T) {
	workDir := t.TempDir()
	firstSource := filepath.Join(workDir, "library", "a.mkv")
	secondSource := filepath.Join(workDir, "library", "b.mkv")
	firstContent := []byte("first-file")
	writeFile(t, firstSource, firstContent)
	writeFile(t, secondSource, []byte("second-file-grown"))
	existingDir := filepath.Join(workDir, "links")
	if err := os.Mkdir(existingDir, 0o750); err != nil {
		t.Fatal(err)
	}
	firstDestination := filepath.Join(existingDir, "Pack", "Season 1", "a.mkv")
	secondDestination := filepath.Join(existingDir, "Pack", "Season 2", "b.mkv")
	ops := []Op{
		{Source: firstSource, Destination: firstDestination, Root: existingDir, Size: int64(len(firstContent))},
		{Source: secondSource, Destination: secondDestination, Root: existingDir, Size: 11},
	}
	_, err := applyOps(t, ops, []Kind{Hardlink})
	if !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("error = %v, want ErrSizeMismatch", err)
	}
	assertExists(t, firstDestination, false)
	assertExists(t, filepath.Join(existingDir, "Pack"), false)
	assertExists(t, existingDir, true)
	if !bytes.Equal(readFile(t, firstSource), firstContent) {
		t.Fatal("library file changed during rollback")
	}
}

func TestApply_refusesSizeMismatch(t *testing.T) {
	operation := singleOp(t)
	for _, wrongSize := range []int64{3, 5} {
		operation.Size = wrongSize
		_, err := applyOps(t, []Op{operation}, []Kind{Hardlink})
		if !errors.Is(err, ErrSizeMismatch) {
			t.Fatalf("size %d: error = %v, want ErrSizeMismatch", wrongSize, err)
		}
		assertExists(t, operation.Destination, false)
	}
}

func TestApply_refusesDirectoryOrDanglingSource(t *testing.T) {
	workDir := t.TempDir()
	danglingSource := filepath.Join(workDir, "dangling.mkv")
	if err := os.Symlink(filepath.Join(workDir, "nowhere.mkv"), danglingSource); err != nil {
		t.Fatal(err)
	}
	for _, badSource := range []string{workDir, danglingSource} {
		operation := Op{Source: badSource, Destination: filepath.Join(workDir, "links", "x.mkv"), Root: workDir, Size: 0}
		_, err := applyOps(t, []Op{operation}, []Kind{Hardlink, Symlink})
		if !errors.Is(err, ErrInvalidSource) {
			t.Fatalf("%s: error = %v, want ErrInvalidSource", badSource, err)
		}
		assertExists(t, operation.Destination, false)
	}
}

func TestApply_isIdempotentForSameLink(t *testing.T) {
	for _, kind := range []Kind{Hardlink, Symlink} {
		operation := singleOp(t)
		if _, err := applyOps(t, []Op{operation}, []Kind{kind}); err != nil {
			t.Fatal(err)
		}
		result, err := applyOps(t, []Op{operation}, []Kind{kind})
		if err != nil {
			t.Fatalf("%v second apply: %v", kind, err)
		}
		if result.Used[0] != kind {
			t.Fatalf("Used = %v, want %v", result.Used, kind)
		}
	}
}

func TestApply_refusesExistingDifferentDestination(t *testing.T) {
	operation := singleOp(t)
	otherContent := []byte("not-the-source")
	writeFile(t, operation.Destination, otherContent)
	_, err := applyOps(t, []Op{operation}, []Kind{Hardlink, Symlink})
	if !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("error = %v, want ErrDestinationExists", err)
	}
	if !bytes.Equal(readFile(t, operation.Destination), otherContent) {
		t.Fatal("existing destination was overwritten")
	}
	assertExists(t, operation.Destination, true)
}

func TestApply_existingLinkOfUnlistedKindIsRefused(t *testing.T) {
	operation := singleOp(t)
	if _, err := applyOps(t, []Op{operation}, []Kind{Symlink}); err != nil {
		t.Fatal(err)
	}
	if _, err := applyOps(t, []Op{operation}, []Kind{Hardlink}); !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("error = %v, want ErrDestinationExists", err)
	}
}

func TestApply_reflinkCopiesOrReportsUnsupported(t *testing.T) {
	operation := singleOp(t)
	_, err := applyOps(t, []Op{operation}, []Kind{Reflink})
	if err == nil {
		if !bytes.Equal(readFile(t, operation.Destination), readFile(t, operation.Source)) {
			t.Fatal("reflink content differs from source")
		}
		return
	}
	if !errors.Is(err, ErrUnsupported) && !errors.Is(err, ErrCrossDevice) {
		t.Fatalf("error = %v, want ErrUnsupported or ErrCrossDevice", err)
	}
	assertExists(t, operation.Destination, false)
}

func TestTranslate_longestRuleWins(t *testing.T) {
	mapper := NewMapper([]config.PathRule{
		{From: "/data", To: "/mnt/data"},
		{From: "/data/tv/", To: "/srv/series"},
	})
	gotValue, err := mapper.Translate("/data/tv/Show/e01.mkv")
	if err != nil {
		t.Fatal(err)
	}
	expectedValue := "/srv/series/Show/e01.mkv"
	if gotValue != expectedValue {
		t.Fatalf("Translate = %q, want %q", gotValue, expectedValue)
	}
}

func TestTranslate_matchesOnSegmentBoundaryOnly(t *testing.T) {
	mapper := NewMapper([]config.PathRule{{From: "/data/tv", To: "/srv/series"}})
	if _, err := mapper.Translate("/data/tvshows/x.mkv"); !errors.Is(err, ErrNoRule) {
		t.Fatalf("error = %v, want ErrNoRule", err)
	}
	gotValue, err := mapper.Translate("/data/tv")
	if err != nil || gotValue != "/srv/series" {
		t.Fatalf("Translate = %q, %v, want /srv/series", gotValue, err)
	}
}

func TestTranslate_cleansTraversalBeforeMatching(t *testing.T) {
	mapper := NewMapper([]config.PathRule{{From: "/data/tv", To: "/srv/series"}})
	if _, err := mapper.Translate("/data/tv/../secret/x"); !errors.Is(err, ErrNoRule) {
		t.Fatalf("error = %v, want ErrNoRule", err)
	}
}

func TestReverse_mapsToBackToFrom(t *testing.T) {
	mapper := NewMapper([]config.PathRule{{From: "/data/tv", To: "/srv/series"}})
	gotValue, err := mapper.Reverse("/srv/series/Show/e01.mkv")
	if err != nil {
		t.Fatal(err)
	}
	expectedValue := "/data/tv/Show/e01.mkv"
	if gotValue != expectedValue {
		t.Fatalf("Reverse = %q, want %q", gotValue, expectedValue)
	}
}

func TestTranslate_keepsWindowsDriveLetters(t *testing.T) {
	mapper := NewMapper([]config.PathRule{{From: "/data/movies", To: `D:\Media\Movies`}})
	gotValue, err := mapper.Translate("/data/movies/Film (2020)/film.mkv")
	if err != nil {
		t.Fatal(err)
	}
	expectedValue := `D:\Media\Movies\Film (2020)\film.mkv`
	if gotValue != expectedValue {
		t.Fatalf("Translate = %q, want %q", gotValue, expectedValue)
	}
	reversed, err := mapper.Reverse(`d:\media\movies\Film (2020)\film.mkv`)
	if err != nil {
		t.Fatal(err)
	}
	expectedReversed := "/data/movies/Film (2020)/film.mkv"
	if reversed != expectedReversed {
		t.Fatalf("Reverse = %q, want %q", reversed, expectedReversed)
	}
}

func TestChooseLinkDir_returnsFirstOnSameDevice(t *testing.T) {
	devices := map[string]string{"/home/u/library/a.mkv": "dev1", "/l1": "dev2", "/l2": "dev1", "/l3": "dev1"}
	fakeDevice := func(location fsys.Location) (string, bool, error) { return devices[location.Path], true, nil }
	gotLocation, err := chooseLinkDir(localLocation("/home/u/library/a.mkv"), localLocations("/l1", "/l2", "/l3"), fakeDevice)
	if err != nil {
		t.Fatal(err)
	}
	gotValue := gotLocation.Path
	expectedValue := "/l2"
	if gotValue != expectedValue {
		t.Fatalf("chooseLinkDir = %q, want %q", gotValue, expectedValue)
	}
}

func TestChooseLinkDir_reportsNoneOnSameDevice(t *testing.T) {
	devices := map[string]string{"/home/u/library/a.mkv": "dev1", "/l1": "dev2"}
	fakeDevice := func(location fsys.Location) (string, bool, error) {
		device, found := devices[location.Path]
		if !found {
			return "", false, os.ErrNotExist
		}
		return device, true, nil
	}
	_, err := chooseLinkDir(localLocation("/home/u/library/a.mkv"), localLocations("/l1", "/missing"), fakeDevice)
	if !errors.Is(err, ErrNoLinkDirOnSameDevice) {
		t.Fatalf("error = %v, want ErrNoLinkDirOnSameDevice", err)
	}
}

func TestChooseLinkDir_realFilesystem(t *testing.T) {
	workDir := t.TempDir()
	source := filepath.Join(workDir, "a.mkv")
	writeFile(t, source, []byte("x"))
	firstDir := filepath.Join(workDir, "first")
	secondDir := filepath.Join(workDir, "second")
	for _, linkDir := range []string{firstDir, secondDir} {
		if err := os.Mkdir(linkDir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	gotLocation, err := ChooseLinkDir(localLocation(source), localLocations(filepath.Join(workDir, "missing"), firstDir, secondDir), testRegistry(t))
	gotValue := gotLocation.Path
	if err != nil || gotValue != firstDir {
		t.Fatalf("ChooseLinkDir = %q, %v, want %q", gotValue, err, firstDir)
	}
}

type confinementFixture struct {
	operation Op
	outside   string
	content   []byte
}

func newConfinementFixture(t *testing.T) confinementFixture {
	t.Helper()
	workDir := t.TempDir()
	source := filepath.Join(workDir, "library", "a.mkv")
	content := []byte("library-content")
	writeFile(t, source, content)
	destRoot := filepath.Join(workDir, "links")
	outside := filepath.Join(workDir, "outside")
	for _, directory := range []string{destRoot, outside} {
		if err := os.Mkdir(directory, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	operation := Op{
		Source:      source,
		Destination: filepath.Join(destRoot, "Pack", "Season 1", "a.mkv"),
		Root:        destRoot,
		Size:        int64(len(content)),
	}
	return confinementFixture{operation: operation, outside: outside, content: content}
}

func (fixture confinementFixture) assertNothingOutside(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(fixture.outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("created %d entries outside the link root", len(entries))
	}
	if !bytes.Equal(readFile(t, fixture.operation.Source), fixture.content) {
		t.Fatal("library file changed")
	}
}

func TestApply_refusesSymlinkedDirectoryLeavingRoot(t *testing.T) {
	fixture := newConfinementFixture(t)
	if err := os.Symlink(fixture.outside, filepath.Join(fixture.operation.Root, "Pack")); err != nil {
		t.Fatal(err)
	}
	_, err := applyOps(t, []Op{fixture.operation}, []Kind{Hardlink, Symlink})
	if !errors.Is(err, ErrDestinationEscapesRoot) {
		t.Fatalf("error = %v, want ErrDestinationEscapesRoot", err)
	}
	fixture.assertNothingOutside(t)
	entries, _ := os.ReadDir(fixture.operation.Root)
	expectedEntries := 1
	if len(entries) != expectedEntries {
		t.Fatalf("link root holds %d entries, want only the planted symlink", len(entries))
	}
}

func TestApply_refusesSymlinkSwappedInAfterPlanning(t *testing.T) {
	fixture := newConfinementFixture(t)
	torrent := &torrentcheck.Torrent{Root: "Pack", Files: []torrentcheck.File{{Path: "Season 1/a.mkv", Size: fixture.operation.Size}}}
	assignment := matcher.Assignment{ByTorrentFile: map[int]store.MediaFile{0: {Path: fixture.operation.Source}}}
	ops, err := Plan(torrent, assignment, fixture.operation.Root, fsys.LocalPaths())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fixture.outside, filepath.Join(fixture.operation.Root, "Pack")); err != nil {
		t.Fatal(err)
	}
	if _, err := applyOps(t, ops, []Kind{Hardlink}); !errors.Is(err, ErrDestinationEscapesRoot) {
		t.Fatalf("error = %v, want ErrDestinationEscapesRoot", err)
	}
	fixture.assertNothingOutside(t)
}

func TestCreate_refusesSwappedParent(t *testing.T) {
	fixture := newConfinementFixture(t)
	root := openRoot(t, fixture.operation.Root)
	parent := filepath.Join(root.Root().Path, "Pack")
	if err := os.Symlink(fixture.outside, parent); err != nil {
		t.Fatal(err)
	}
	err := systemApplier().create(root, Hardlink, localLocation(fixture.operation.Source), "Pack/a.mkv")
	if !errors.Is(err, ErrDestinationEscapesRoot) {
		t.Fatalf("error = %v, want ErrDestinationEscapesRoot", err)
	}
}

func TestApply_acceptsSymlinkedRoot(t *testing.T) {
	fixture := newConfinementFixture(t)
	symlinkedRoot := filepath.Join(filepath.Dir(fixture.operation.Root), "links-alias")
	if err := os.Symlink(fixture.operation.Root, symlinkedRoot); err != nil {
		t.Fatal(err)
	}
	operation := fixture.operation
	operation.Root = symlinkedRoot
	operation.Destination = filepath.Join(symlinkedRoot, "Pack", "Season 1", "a.mkv")
	if _, err := applyOps(t, []Op{operation}, []Kind{Hardlink}); err != nil {
		t.Fatal(err)
	}
	assertExists(t, fixture.operation.Destination, true)
	fixture.assertNothingOutside(t)
}

func TestApply_refusesDestinationOutsideOpRoot(t *testing.T) {
	fixture := newConfinementFixture(t)
	operation := fixture.operation
	operation.Destination = filepath.Join(fixture.outside, "a.mkv")
	if _, err := applyOps(t, []Op{operation}, []Kind{Hardlink}); !errors.Is(err, ErrDestinationEscapesRoot) {
		t.Fatalf("error = %v, want ErrDestinationEscapesRoot", err)
	}
	fixture.assertNothingOutside(t)
}
