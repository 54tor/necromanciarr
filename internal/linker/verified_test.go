package linker

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"necromanciarr/internal/torrentcheck"
)

var verifiedContent = []byte("verified nfo content\r\n")

func TestWriteVerifiedFile_createsParentsAndWritesFileUnderRoot(t *testing.T) {
	root := t.TempDir()
	if err := WriteVerifiedFile(openRoot(t, root), "Movie.2020/Movie.2020.nfo", verifiedContent); err != nil {
		t.Fatal(err)
	}
	written := filepath.Join(root, "Movie.2020", "Movie.2020.nfo")
	if gotValue := readFile(t, written); !bytes.Equal(gotValue, verifiedContent) {
		t.Fatalf("gotValue %q, expectedValue %q", gotValue, verifiedContent)
	}
	info, err := os.Stat(written)
	if err != nil {
		t.Fatal(err)
	}
	const expectedValue os.FileMode = 0o644
	// Windows ne porte pas les bits de permission Unix.
	if gotValue := info.Mode().Perm(); runtime.GOOS != "windows" && gotValue != expectedValue {
		t.Fatalf("gotValue %v, expectedValue %v", gotValue, expectedValue)
	}
}

func TestWriteVerifiedFile_refusesEscapeAttempts(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	if err := os.Mkdir(root, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, relativePath := range []string{"../outside.nfo", "a/../../outside.nfo", "/etc/outside.nfo", ""} {
		err := WriteVerifiedFile(openRoot(t, root), relativePath, verifiedContent)
		if !errors.Is(err, ErrDestinationEscapesRoot) {
			t.Fatalf("%q: gotValue %v, expectedValue ErrDestinationEscapesRoot", relativePath, err)
		}
	}
	assertExists(t, filepath.Join(parent, "outside.nfo"), false)
}

func TestWriteVerifiedFile_refusesSymlinkedParent(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "Movie.2020")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	err := WriteVerifiedFile(openRoot(t, root), "Movie.2020/Movie.2020.nfo", verifiedContent)
	if !errors.Is(err, ErrDestinationEscapesRoot) {
		t.Fatalf("gotValue %v, expectedValue ErrDestinationEscapesRoot", err)
	}
	assertExists(t, filepath.Join(outside, "Movie.2020.nfo"), false)
}

func TestWriteVerifiedFile_identicalExistingFileIsDone(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "Movie.2020", "Movie.2020.nfo")
	writeFile(t, existing, verifiedContent)
	before, err := os.Stat(existing)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteVerifiedFile(openRoot(t, root), "Movie.2020/Movie.2020.nfo", verifiedContent); err != nil {
		t.Fatalf("gotValue %v, expectedValue nil", err)
	}
	after, err := os.Stat(existing)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("identical file was rewritten")
	}
}

func TestWriteVerifiedFile_refusesOverwriteOfDifferentContent(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "Movie.2020.nfo")
	expectedValue := []byte("another content")
	writeFile(t, existing, expectedValue)
	err := WriteVerifiedFile(openRoot(t, root), "Movie.2020.nfo", verifiedContent)
	if !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("gotValue %v, expectedValue ErrDestinationExists", err)
	}
	if gotValue := readFile(t, existing); !bytes.Equal(gotValue, expectedValue) {
		t.Fatalf("existing file changed: gotValue %q", gotValue)
	}
}

func TestWriteVerifiedFile_refusesSymlinkAtDestination(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "target.nfo")
	writeFile(t, target, verifiedContent)
	if err := os.Symlink(target, filepath.Join(root, "Movie.2020.nfo")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := WriteVerifiedFile(openRoot(t, root), "Movie.2020.nfo", verifiedContent); !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("gotValue %v, expectedValue ErrDestinationExists", err)
	}
}

var errInjectedWrite = errors.New("injected write failure")

func TestWriteVerifiedFile_partialWriteFailureRemovesOnlyWhatItCreated(t *testing.T) {
	root := t.TempDir()
	keptDir := filepath.Join(root, "Movie.2020")
	if err := os.Mkdir(keptDir, 0o750); err != nil {
		t.Fatal(err)
	}
	failAfterHalf := func(writer io.Writer, data []byte) error {
		if _, err := writer.Write(data[:len(data)/2]); err != nil {
			return err
		}
		return errInjectedWrite
	}
	err := writeVerifiedFile(openRoot(t, root), "Movie.2020/Extras/Movie.2020.nfo", verifiedContent, failAfterHalf)
	if !errors.Is(err, errInjectedWrite) {
		t.Fatalf("gotValue %v, expectedValue errInjectedWrite", err)
	}
	assertExists(t, filepath.Join(keptDir, "Extras"), false)
	assertExists(t, keptDir, true)
}

func TestWriteVerifiedFile_failureLeavesNoTemporaryNorTruncatedFile(t *testing.T) {
	root := t.TempDir()
	failAfterHalf := func(writer io.Writer, data []byte) error {
		_, _ = writer.Write(data[:len(data)/2])
		return errInjectedWrite
	}
	if err := writeVerifiedFile(openRoot(t, root), "Movie.2020.nfo", verifiedContent, failAfterHalf); !errors.Is(err, errInjectedWrite) {
		t.Fatalf("gotValue %v, expectedValue errInjectedWrite", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("gotValue %v %v, expectedValue an empty root", entries, err)
	}
}

func TestRelativeDestination_matchesPlanLayout(t *testing.T) {
	multiFile := &torrentcheck.Torrent{Root: "Movie.2020", Files: []torrentcheck.File{{Path: "Movie.2020.mkv"}, {Path: "Extras/Movie.2020.nfo"}}}
	singleFile := &torrentcheck.Torrent{Root: "Movie.2020.mkv", Files: []torrentcheck.File{{Path: "Movie.2020.mkv"}}}
	cases := []struct {
		torrent       *torrentcheck.Torrent
		fileIndex     int
		expectedValue string
	}{
		{multiFile, 1, "Movie.2020/Extras/Movie.2020.nfo"},
		{singleFile, 0, "Movie.2020.mkv"},
	}
	for _, testCase := range cases {
		gotValue, err := RelativeDestination(testCase.torrent, testCase.fileIndex)
		if err != nil || gotValue != testCase.expectedValue {
			t.Fatalf("gotValue %q (%v), expectedValue %q", gotValue, err, testCase.expectedValue)
		}
	}
	if _, err := RelativeDestination(multiFile, 2); err == nil {
		t.Fatal("gotValue nil, expectedValue an error for an unknown file index")
	}
}
