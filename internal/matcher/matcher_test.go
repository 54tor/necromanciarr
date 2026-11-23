package matcher

import (
	"context"
	"errors"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"

	"necromanciarr/internal/libraryprovider"
	"necromanciarr/internal/store"
)

const (
	pathA     = "/home/u/library/Show/a.mkv"
	pathB     = "/home/u/library/Show/b.mkv"
	pathExtra = "/home/u/library/Show/extra.mkv"
	pathNfo   = "/home/u/library/Show/show.nfo"
)

// twinEpisodes : deux épisodes de 4 pièces et de même taille, plus un fichier de taille unique.
func twinEpisodes() []testFile {
	return []testFile{
		{path: "e01.mkv", content: deterministicBytes(1, 4*testPieceLength)},
		{path: "e02.mkv", content: deterministicBytes(2, 4*testPieceLength)},
		{path: "extra.mkv", content: deterministicBytes(3, 5*testPieceLength)},
	}
}

// twinLibrary range volontairement e02 sous le chemin trié en premier : l'ordre ne doit rien décider.
func twinLibrary(files []testFile) *fakeLibrary {
	return newFakeLibrary(map[string][]byte{
		pathA:     files[1].content,
		pathB:     files[0].content,
		pathExtra: files[2].content,
	})
}

// tinyFileLayout : tiny.mkv tient dans la pièce 0, qu'elle partage avec show.nfo ; aucune pièce ne lui est propre.
func tinyFileLayout() []testFile {
	return []testFile{
		{path: "show.nfo", content: deterministicBytes(4, 11)},
		{path: "tiny.mkv", content: deterministicBytes(5, 5)},
		{path: "big.mkv", content: deterministicBytes(6, 2*testPieceLength)},
	}
}

func TestCandidates_keepsOnlyExactSize(t *testing.T) {
	release := libraryprovider.Release{Files: []libraryprovider.ReleaseFile{
		{Path: "Show/e01.mkv", Size: 100},
		{Path: "Show/e02.mkv", Size: 200},
		{Path: "Show/e03.mkv", Size: 300},
	}}
	local := []store.MediaFile{
		{Path: pathB, Size: 100},
		{Path: pathA, Size: 100},
		{Path: "/home/u/library/Show/near.mkv", Size: 199},
		{Path: pathExtra, Size: 300},
	}

	gotValue := Candidates(release, local)

	expectedValue := []FileMatch{
		{ReleaseFileIndex: 0, LocalFiles: []store.MediaFile{{Path: pathA, Size: 100}, {Path: pathB, Size: 100}}},
		{ReleaseFileIndex: 2, LocalFiles: []store.MediaFile{{Path: pathExtra, Size: 300}}},
	}
	if !reflect.DeepEqual(gotValue, expectedValue) {
		t.Fatalf("Candidates = %+v, want %+v", gotValue, expectedValue)
	}
}

func TestAssign_twinEpisodesSameSizeResolvedByHash(t *testing.T) {
	files := twinEpisodes()
	library := twinLibrary(files)

	gotValue, err := Assign(buildTorrent(t, files), library.mediaFiles(), library.open)
	if err != nil {
		t.Fatalf("Assign: %v", err)
	}

	expectedValue := map[int]string{0: pathB, 1: pathA, 2: pathExtra}
	gotPaths := map[int]string{}
	for torrentFileIndex, mediaFile := range gotValue.ByTorrentFile {
		gotPaths[torrentFileIndex] = mediaFile.Path
	}
	if !reflect.DeepEqual(gotPaths, expectedValue) {
		t.Fatalf("ByTorrentFile = %v, want %v", gotPaths, expectedValue)
	}
	if len(gotValue.Ambiguous) != 0 {
		t.Fatalf("Ambiguous = %v, want none", gotValue.Ambiguous)
	}
}

func TestAssign_reportsAmbiguousWhenHashesCannotSeparate(t *testing.T) {
	files := tinyFileLayout()
	library := newFakeLibrary(map[string][]byte{
		pathA: files[1].content,
		pathB: deterministicBytes(7, 5),
	})

	gotValue, err := Assign(buildTorrent(t, files), library.mediaFiles(), library.open)
	if err != nil {
		t.Fatalf("Assign: %v", err)
	}

	expectedValue := []string{pathA, pathB}
	if gotPaths := paths(gotValue.Ambiguous[1]); !slices.Equal(gotPaths, expectedValue) {
		t.Fatalf("Ambiguous[1] = %v, want %v", gotPaths, expectedValue)
	}
	if len(gotValue.ByTorrentFile) != 0 {
		t.Fatalf("ByTorrentFile = %v, want none", gotValue.ByTorrentFile)
	}
	if library.totalBytesRead() != 0 {
		t.Fatalf("read %d bytes, want 0: no piece could separate the candidates", library.totalBytesRead())
	}
}

func TestAssign_resolvesTinyFileThroughUniquelyAssignedNeighbour(t *testing.T) {
	files := tinyFileLayout()
	library := newFakeLibrary(map[string][]byte{
		pathNfo: files[0].content,
		pathA:   deterministicBytes(7, 5),
		pathB:   files[1].content,
	})

	gotValue, err := Assign(buildTorrent(t, files), library.mediaFiles(), library.open)
	if err != nil {
		t.Fatalf("Assign: %v", err)
	}

	expectedValue := pathB
	if gotValue := gotValue.ByTorrentFile[1].Path; gotValue != expectedValue {
		t.Fatalf("ByTorrentFile[1] = %q, want %q", gotValue, expectedValue)
	}
}

func TestAssign_readsOnlyUsefulPieces(t *testing.T) {
	files := twinEpisodes()
	library := twinLibrary(files)

	if _, err := Assign(buildTorrent(t, files), library.mediaFiles(), library.open); err != nil {
		t.Fatalf("Assign: %v", err)
	}

	// Une pièce par candidat du premier épisode, plus une pour écarter le gagnant du second épisode.
	const expectedMaxBytes = 3 * testPieceLength
	if gotValue := library.totalBytesRead(); gotValue > expectedMaxBytes {
		t.Fatalf("read %d bytes, want at most %d", gotValue, expectedMaxBytes)
	}
	if gotValue := library.openCount[pathExtra]; gotValue != 0 {
		t.Fatalf("unique-size file opened %d times, want 0", gotValue)
	}
	for path, gotValue := range library.openCount {
		if gotValue > 1 {
			t.Fatalf("%s opened %d times, want at most once", path, gotValue)
		}
	}
}

func TestAssign_identicalLocalCopiesAreAmbiguous(t *testing.T) {
	episode := deterministicBytes(1, 3*testPieceLength)
	library := newFakeLibrary(map[string][]byte{pathA: episode, pathB: episode})

	gotValue, err := Assign(buildTorrent(t, []testFile{{path: "e01.mkv", content: episode}}), library.mediaFiles(), library.open)
	if err != nil {
		t.Fatalf("Assign: %v", err)
	}

	expectedValue := []string{pathA, pathB}
	if gotPaths := paths(gotValue.Ambiguous[0]); !slices.Equal(gotPaths, expectedValue) {
		t.Fatalf("Ambiguous[0] = %v, want %v", gotPaths, expectedValue)
	}
	if len(gotValue.ByTorrentFile) != 0 {
		t.Fatalf("ByTorrentFile = %v, want none", gotValue.ByTorrentFile)
	}
}

func TestAssign_neverAssignsOneLocalFileToTwoTorrentFiles(t *testing.T) {
	episode := deterministicBytes(1, 3*testPieceLength)
	files := []testFile{{path: "e01.mkv", content: episode}, {path: "e01.copy.mkv", content: episode}}
	library := newFakeLibrary(map[string][]byte{pathA: episode})

	gotValue, err := Assign(buildTorrent(t, files), library.mediaFiles(), library.open)
	if err != nil {
		t.Fatalf("Assign: %v", err)
	}

	if len(gotValue.ByTorrentFile) != 0 {
		t.Fatalf("ByTorrentFile = %v, want none", gotValue.ByTorrentFile)
	}
	expectedValue := []string{pathA}
	for torrentFileIndex := range files {
		if gotPaths := paths(gotValue.Ambiguous[torrentFileIndex]); !slices.Equal(gotPaths, expectedValue) {
			t.Fatalf("Ambiguous[%d] = %v, want %v", torrentFileIndex, gotPaths, expectedValue)
		}
	}
}

func TestAssign_leavesTorrentFileOutWhenNoCandidateVerifies(t *testing.T) {
	files := []testFile{{path: "e01.mkv", content: deterministicBytes(1, 2*testPieceLength)}}
	library := newFakeLibrary(map[string][]byte{
		pathA: deterministicBytes(8, 2*testPieceLength),
		pathB: deterministicBytes(9, 2*testPieceLength),
	})

	gotValue, err := Assign(buildTorrent(t, files), library.mediaFiles(), library.open)
	if err != nil {
		t.Fatalf("Assign: %v", err)
	}

	if len(gotValue.ByTorrentFile) != 0 || len(gotValue.Ambiguous) != 0 {
		t.Fatalf("Assign = %+v, want empty assignment", gotValue)
	}
}

func TestAssign_ignoresFilesWithoutSizeMatch(t *testing.T) {
	files := twinEpisodes()
	library := newFakeLibrary(map[string][]byte{
		pathExtra: files[2].content,
		pathNfo:   deterministicBytes(4, 7),
	})

	gotValue, err := Assign(buildTorrent(t, files), library.mediaFiles(), library.open)
	if err != nil {
		t.Fatalf("Assign: %v", err)
	}

	expectedValue := map[int]store.MediaFile{2: {Path: pathExtra, Size: int64(len(files[2].content))}}
	if !reflect.DeepEqual(gotValue.ByTorrentFile, expectedValue) || len(gotValue.Ambiguous) != 0 {
		t.Fatalf("Assign = %+v, want only %v", gotValue, expectedValue)
	}
}

func TestAssign_stopsAtReadBudgetAndReportsRemainingAsAmbiguous(t *testing.T) {
	files := twinEpisodes()
	library := twinLibrary(files)
	options := Options{Budget: testPieceLength}

	gotValue, err := options.Assign(context.Background(), buildTorrent(t, files), library.mediaFiles(), library.open)

	if !errors.Is(err, ErrReadBudgetExceeded) {
		t.Fatalf("err = %v, want ErrReadBudgetExceeded", err)
	}
	if gotRead := library.totalBytesRead(); gotRead > options.Budget {
		t.Fatalf("read %d bytes, budget %d", gotRead, options.Budget)
	}
	expectedValue := []string{pathA, pathB}
	for torrentFileIndex := range 2 {
		if gotPaths := paths(gotValue.Ambiguous[torrentFileIndex]); !slices.Equal(gotPaths, expectedValue) {
			t.Fatalf("Ambiguous[%d] = %v, want %v", torrentFileIndex, gotPaths, expectedValue)
		}
	}
	if gotValue.ByTorrentFile[2].Path != pathExtra {
		t.Fatalf("ByTorrentFile = %v, want unique-size file kept", gotValue.ByTorrentFile)
	}
}

func TestAssign_returnsContextErrorWhenCancelled(t *testing.T) {
	files := twinEpisodes()
	library := twinLibrary(files)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	gotValue, err := Options{}.Assign(ctx, buildTorrent(t, files), library.mediaFiles(), library.open)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if library.totalBytesRead() != 0 {
		t.Fatalf("read %d bytes after cancellation", library.totalBytesRead())
	}
	if len(gotValue.Ambiguous) != 2 {
		t.Fatalf("Ambiguous = %v, want both twin episodes", gotValue.Ambiguous)
	}
}

func TestAssign_isDeterministicAcrossShuffledInputOrder(t *testing.T) {
	files := append(twinEpisodes(), tinyFileLayout()...)
	contents := map[string][]byte{
		pathA:                          files[1].content,
		pathB:                          files[0].content,
		pathExtra:                      files[2].content,
		"/home/u/library/Show/t1.mkv":  files[4].content,
		"/home/u/library/Show/t2.mkv":  deterministicBytes(7, 5),
		"/home/u/library/Show/big.mkv": files[5].content,
	}
	torrent := buildTorrent(t, files)
	expectedValue, err := Assign(torrent, newFakeLibrary(contents).mediaFiles(), newFakeLibrary(contents).open)
	if err != nil {
		t.Fatalf("Assign: %v", err)
	}

	const runCount = 20
	for runIndex := range runCount {
		library := newFakeLibrary(contents)
		local := library.mediaFiles()
		shuffler := rand.New(rand.NewPCG(uint64(runIndex), 1)) // #nosec G404 -- mélange de test reproductible
		shuffler.Shuffle(len(local), func(left, right int) { local[left], local[right] = local[right], local[left] })

		gotValue, err := Assign(torrent, local, library.open)
		if err != nil {
			t.Fatalf("run %d: Assign: %v", runIndex, err)
		}
		if !reflect.DeepEqual(gotValue, expectedValue) {
			t.Fatalf("run %d: Assign = %+v, want %+v", runIndex, gotValue, expectedValue)
		}
	}
}
