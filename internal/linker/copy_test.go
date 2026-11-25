package linker

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"necromanciarr/internal/fsys"
)

const plentyOfSpace = 1 << 40

func refusingReflink(fsys.WriteRoot, fsys.Location, string) error { return ErrUnsupported }

func fixedFreeBytes(available uint64) func(fsys.Location) (uint64, error) {
	return func(fsys.Location) (uint64, error) { return available, nil }
}

// prepareIn appelle prepare avec les formes qu'attend PrepareCopy : racine ouverte, source en Location, chemin relatif.
func (fileCopier copier) prepareIn(t *testing.T, source, destination, root string) (PreparedCopy, error) {
	t.Helper()
	return fileCopier.prepare(openRoot(t, root), localLocation(source), relativeTo(t, root, destination))
}

// hardlinkedDestination pose sous un dossier de liens un lien dur vers un fichier « de bibliothèque ».
func hardlinkedDestination(t *testing.T) (source, destination, root string) {
	t.Helper()
	base := t.TempDir()
	source = filepath.Join(base, "library", "movie.mkv")
	writeFile(t, source, bytes.Repeat([]byte("library-bytes"), 64))
	root = filepath.Join(base, "links", "Movie [hash]")
	destination = filepath.Join(root, "Movie", "movie.mkv")
	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(source, destination); err != nil {
		t.Fatal(err)
	}
	return source, destination, root
}

func TestPrepareCopy_plainCopyReplacesHardlinkOnlyOnCommit(t *testing.T) {
	source, destination, root := hardlinkedDestination(t)
	expectedContent := readFile(t, source)
	fileCopier := copier{reflink: refusingReflink, freeBytes: fixedFreeBytes(plentyOfSpace)}

	prepared, err := fileCopier.prepareIn(t, source, destination, root)
	if err != nil {
		t.Fatal(err)
	}
	sourceInfo, _ := os.Stat(source)
	beforeCommit, _ := os.Stat(destination)
	if !os.SameFile(sourceInfo, beforeCommit) {
		t.Fatal("destination replaced before Commit")
	}
	if err := prepared.Commit(); err != nil {
		t.Fatal(err)
	}
	afterCommit, _ := os.Lstat(destination)
	if prepared.Kind != Copy || os.SameFile(sourceInfo, afterCommit) || !afterCommit.Mode().IsRegular() {
		t.Fatalf("kind %v, still shared with the source after Commit", prepared.Kind)
	}
	if gotContent := readFile(t, destination); !bytes.Equal(gotContent, expectedContent) {
		t.Fatal("copy content differs from the source")
	}
}

func TestPrepareCopy_prefersReflinkWhenAvailable(t *testing.T) {
	source, destination, root := hardlinkedDestination(t)
	cloned := false
	fakeReflink := func(writeRoot fsys.WriteRoot, from fsys.Location, to string) error {
		cloned = true
		writer, err := writeRoot.CreateExclusive(to, 0o600)
		if err != nil {
			return err
		}
		_, err = writer.Write(readFile(t, from.Path))
		return errors.Join(err, writer.Close())
	}
	fileCopier := copier{reflink: fakeReflink, freeBytes: fixedFreeBytes(0)}

	prepared, err := fileCopier.prepareIn(t, source, destination, root)
	if err != nil {
		t.Fatal(err)
	}
	if expectedKind := Reflink; prepared.Kind != expectedKind || !cloned {
		t.Fatalf("expected %v, got %v", expectedKind, prepared.Kind)
	}
	if err := prepared.Discard(); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareCopy_refusesWithoutEnoughFreeSpaceAndLeavesNothing(t *testing.T) {
	source, destination, root := hardlinkedDestination(t)
	testCases := map[string]struct {
		freeBytes     func(fsys.Location) (uint64, error)
		expectedError error
	}{
		"too little space": {freeBytes: fixedFreeBytes(1024), expectedError: ErrInsufficientSpace},
		"unknown space":    {freeBytes: func(fsys.Location) (uint64, error) { return 0, errors.ErrUnsupported }, expectedError: ErrFreeSpaceUnknown},
	}
	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			fileCopier := copier{reflink: refusingReflink, freeBytes: testCase.freeBytes}
			_, gotErr := fileCopier.prepareIn(t, source, destination, root)
			if !errors.Is(gotErr, testCase.expectedError) {
				t.Fatalf("expected %v, got %v", testCase.expectedError, gotErr)
			}
			entries, _ := os.ReadDir(filepath.Dir(destination))
			if expectedEntries := 1; len(entries) != expectedEntries {
				t.Fatalf("expected only the original link, got %d entries", len(entries))
			}
		})
	}
}

func TestPrepareCopy_refusesDestinationOutsideRoot(t *testing.T) {
	source, _, root := hardlinkedDestination(t)
	fileCopier := copier{reflink: refusingReflink, freeBytes: fixedFreeBytes(plentyOfSpace)}

	_, gotErr := fileCopier.prepareIn(t, source, filepath.Join(filepath.Dir(root), "elsewhere.mkv"), root)
	if !errors.Is(gotErr, ErrDestinationEscapesRoot) {
		t.Fatalf("expected ErrDestinationEscapesRoot, got %v", gotErr)
	}
}
