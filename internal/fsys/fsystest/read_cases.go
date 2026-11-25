package fsystest

import (
	"errors"
	"io"
	"io/fs"
	"slices"
	"testing"
)

// caseOpenIsReadOnly prouve qu'Open lit le contenu et que le fichier rendu n'offre aucune écriture : ni Write qui
// réussit, ni Chmod/Truncate atteignables par assertion de type (*os.File interdit).
func caseOpenIsReadOnly(t *testing.T, subject ReadSubject) {
	volume := subject.Volume
	file, err := volume.Open(volume.Paths().Join(subject.LibraryRoot, FixtureMovieName))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer closeQuietly(file)
	if gotValue := readAllAt(t, file, int64(len(FixtureMovieContent))); gotValue != FixtureMovieContent {
		t.Errorf("content = %q, want %q", gotValue, FixtureMovieContent)
	}
	if writer, isWriter := file.(io.Writer); isWriter {
		if _, err := writer.Write([]byte("x")); err == nil {
			t.Errorf("Write on a file from Open succeeded, want a refusal")
		}
	}
	if _, hasChmod := file.(interface{ Chmod(fs.FileMode) error }); hasChmod {
		t.Errorf("file from Open exposes Chmod")
	}
	if _, hasTruncate := file.(interface{ Truncate(int64) error }); hasTruncate {
		t.Errorf("file from Open exposes Truncate")
	}
	assertLibraryIntact(t, subject)
}

// caseLstatDoesNotFollowSymlink prouve que Lstat voit le lien et Stat sa cible.
func caseLstatDoesNotFollowSymlink(t *testing.T, subject ReadSubject) {
	skipWithoutFixtureLink(t, subject)
	volume := subject.Volume
	linkPath := volume.Paths().Join(subject.LibraryRoot, FixtureLinkName)
	linkInfo, err := volume.Lstat(linkPath)
	if err != nil {
		t.Fatalf("Lstat: %v", err)
	}
	if linkInfo.Mode()&fs.ModeSymlink == 0 {
		t.Errorf("Lstat mode = %v, want a symlink", linkInfo.Mode())
	}
	targetInfo, err := volume.Stat(linkPath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !targetInfo.Mode().IsRegular() || targetInfo.Size() != int64(len(FixtureMovieContent)) {
		t.Errorf("Stat = %v size %d, want the regular target", targetInfo.Mode(), targetInfo.Size())
	}
}

// caseReadDirIsSortedByName prouve que ReadDir rend toutes les entrées, triées par nom.
func caseReadDirIsSortedByName(t *testing.T, subject ReadSubject) {
	expectedValue := fixtureEntries(subject)
	gotValue := entryNames(t, subject.Volume, subject.LibraryRoot)
	if !slices.Equal(gotValue, expectedValue) {
		t.Errorf("ReadDir = %q, want %q", gotValue, expectedValue)
	}
}

// caseReadlinkReturnsTarget prouve que Readlink rend la cible brute, sans la résoudre.
func caseReadlinkReturnsTarget(t *testing.T, subject ReadSubject) {
	skipWithoutFixtureLink(t, subject)
	volume := subject.Volume
	gotValue, err := volume.Readlink(volume.Paths().Join(subject.LibraryRoot, FixtureLinkName))
	if err != nil || gotValue != FixtureMovieName {
		t.Errorf("Readlink = %q, %v, want %q", gotValue, err, FixtureMovieName)
	}
}

// caseMissingEntryIsNotExist prouve que les erreurs d'absence sont typées fs.ErrNotExist.
func caseMissingEntryIsNotExist(t *testing.T, subject ReadSubject) {
	volume := subject.Volume
	missingPath := volume.Paths().Join(subject.LibraryRoot, "missing")
	if _, err := volume.Stat(missingPath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Stat error = %v, want fs.ErrNotExist", err)
	}
	if _, err := volume.Open(missingPath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Open error = %v, want fs.ErrNotExist", err)
	}
	if _, err := volume.Lstat(missingPath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Lstat error = %v, want fs.ErrNotExist", err)
	}
}

func skipWithoutFixtureLink(t *testing.T, subject ReadSubject) {
	t.Helper()
	if !subject.LibraryHasSymlink {
		t.Skip("fixture symlink unavailable on this platform")
	}
}
