package fsystest

import (
	"errors"
	"io/fs"
	"testing"

	"necromanciarr/internal/fsys"
)

// otherVolumeName désigne un volume étranger : aucune source de Hardlink ou CopyFrom ne peut en venir.
const otherVolumeName = "conformance-other-volume"

// caseWriteRootRootIsCanonical prouve que Root rend le volume et la forme canonique du dossier.
func caseWriteRootRootIsCanonical(t *testing.T, subject Subject) {
	canonical, err := subject.Volume.Canonical(subject.LinkDir)
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	expectedValue := fsys.Location{Volume: subject.Volume.Volume(), Path: canonical}
	gotValue := openWriteRoot(t, subject).Root()
	if gotValue != expectedValue {
		t.Errorf("Root() = %+v, want %+v", gotValue, expectedValue)
	}
}

// caseWriteRootRefusesEscapes prouve que chaque méthode à chemin relatif (Replace par ses deux arguments) refuse
// "..", les chemins absolus et le chemin vide (ErrUnconfined), sans rien écrire dans la bibliothèque.
func caseWriteRootRefusesEscapes(t *testing.T, subject Subject) {
	root := openWriteRoot(t, subject)
	writeThroughRoot(t, root, "kept", "kept")
	// "a/../b" reste lexicalement dedans mais ".." est refusé partout ; "" désignerait la racine elle-même.
	escapes := []string{
		"..", "../escape", "a/../b", "a/../../escape", "",
		subject.Volume.Paths().Join(subject.LibraryRoot, "escape"),
	}
	for _, relative := range escapes {
		assertEveryMethodUnconfined(t, subject, root, relative)
	}
	assertContent(t, subject.Volume, subject.Volume.Paths().Join(subject.LinkDir, "kept"), "kept")
	assertLibraryIntact(t, subject)
}

// caseWriteRootRefusesSymlinkedParent prouve qu'un dossier symbolique posé hors du volume dans la racine, et
// pointant vers la bibliothèque, ne sert jamais de parent : ni écriture, ni lecture par Lstat. Ce cas ne dépend
// d'aucune capacité du volume.
func caseWriteRootRefusesSymlinkedParent(t *testing.T, subject Subject) {
	subject.PlantSymlink(t, "escape", subject.LibraryRoot)
	root := openWriteRoot(t, subject)
	writeThroughRoot(t, root, "kept", "kept")
	for _, relative := range []string{"escape/" + FixtureNoteName, "escape/" + FixtureSubdirName + "/new"} {
		assertEveryMethodUnconfined(t, subject, root, relative)
	}
	assertLibraryIntact(t, subject)
}

// caseWriteRootRefusesSourceOfAnotherVolume prouve que Hardlink et CopyFrom refusent une source d'un autre volume
// ou à chemin relatif (§4.C.6 : source sur le même volume), avant toute écriture.
func caseWriteRootRefusesSourceOfAnotherVolume(t *testing.T, subject Subject) {
	root := openWriteRoot(t, subject)
	moviePath := subject.Volume.Paths().Join(subject.LibraryRoot, FixtureMovieName)
	sources := []fsys.Location{
		{Volume: otherVolumeName, Path: moviePath},
		{Volume: subject.Volume.Volume(), Path: FixtureMovieName},
	}
	for _, source := range sources {
		assertUnconfined(t, "Hardlink source", source.String(), root.Hardlink(source, "linked"))
		_, err := root.CopyFrom(t.Context(), source, "copied")
		assertUnconfined(t, "CopyFrom source", source.String(), err)
	}
	for _, leftover := range []string{"linked", "copied"} {
		if _, err := root.Lstat(leftover); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Lstat(%q) error = %v, want fs.ErrNotExist", leftover, err)
		}
	}
}

// caseCreateExclusiveRefusesExisting prouve O_EXCL : une entrée existante n'est jamais tronquée (fs.ErrExist).
func caseCreateExclusiveRefusesExisting(t *testing.T, subject Subject) {
	root := openWriteRoot(t, subject)
	writeThroughRoot(t, root, "file", "first")
	writer, err := root.CreateExclusive("file", fileMode)
	if writer != nil {
		closeQuietly(writer)
	}
	if !errors.Is(err, fs.ErrExist) {
		t.Errorf("second CreateExclusive error = %v, want fs.ErrExist", err)
	}
	assertContent(t, subject.Volume, subject.Volume.Paths().Join(subject.LinkDir, "file"), "first")
}

// caseReplaceIsAtomicOrRefused prouve que Replace remplace la cible si le volume l'annonce atomique, et sinon
// rend ErrNoAtomicReplace sans toucher aux deux entrées.
func caseReplaceIsAtomicOrRefused(t *testing.T, subject Subject) {
	capabilities := capabilitiesOf(t, subject.Volume)
	root := openWriteRoot(t, subject)
	writeThroughRoot(t, root, "old", "new content")
	writeThroughRoot(t, root, "new", "previous content")
	err := root.Replace("old", "new")
	newPath := subject.Volume.Paths().Join(subject.LinkDir, "new")
	if !capabilities.AtomicReplace {
		if !errors.Is(err, fsys.ErrNoAtomicReplace) {
			t.Fatalf("Replace error = %v, want ErrNoAtomicReplace", err)
		}
		assertContent(t, subject.Volume, newPath, "previous content")
		return
	}
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	assertContent(t, subject.Volume, newPath, "new content")
	if _, err := root.Lstat("old"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Lstat(old) error = %v, want fs.ErrNotExist", err)
	}
}

// caseRemoveDeletesEntry prouve que Remove supprime une entrée de la racine.
func caseRemoveDeletesEntry(t *testing.T, subject Subject) {
	root := openWriteRoot(t, subject)
	if err := root.MkdirAll("dir/nested", dirMode); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeThroughRoot(t, root, "dir/nested/file", "x")
	if err := root.Remove("dir/nested/file"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := root.Lstat("dir/nested/file"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Lstat after Remove error = %v, want fs.ErrNotExist", err)
	}
}

// caseSymlinkAndHardlinkFromLibrary prouve que les liens vers la bibliothèque se posent dans la racine sans
// modifier la bibliothèque.
func caseSymlinkAndHardlinkFromLibrary(t *testing.T, subject Subject) {
	volume := subject.Volume
	capabilities := capabilitiesOf(t, volume)
	root := openWriteRoot(t, subject)
	moviePath := volume.Paths().Join(subject.LibraryRoot, FixtureMovieName)
	if capabilities.Symlink {
		if err := root.Symlink(moviePath, "symlinked.mkv"); err != nil {
			t.Fatalf("Symlink: %v", err)
		}
		assertContent(t, volume, volume.Paths().Join(subject.LinkDir, "symlinked.mkv"), FixtureMovieContent)
	} else {
		t.Log("Symlink not offered by the volume: skipped")
	}
	if capabilities.Hardlink {
		source := fsys.Location{Volume: volume.Volume(), Path: moviePath}
		if err := root.Hardlink(source, "hardlinked.mkv"); err != nil {
			t.Fatalf("Hardlink: %v", err)
		}
		assertContent(t, volume, volume.Paths().Join(subject.LinkDir, "hardlinked.mkv"), FixtureMovieContent)
	} else {
		t.Log("Hardlink not offered by the volume: skipped")
	}
	assertLibraryIntact(t, subject)
}

// caseCopyFromLibrary prouve que CopyFrom rend une copie complète et compte ses octets.
func caseCopyFromLibrary(t *testing.T, subject Subject) {
	volume := subject.Volume
	root := openWriteRoot(t, subject)
	source := fsys.Location{Volume: volume.Volume(), Path: volume.Paths().Join(subject.LibraryRoot, FixtureMovieName)}
	copied, err := root.CopyFrom(t.Context(), source, "copy.mkv")
	if err != nil {
		t.Fatalf("CopyFrom: %v", err)
	}
	if copied != int64(len(FixtureMovieContent)) {
		t.Errorf("CopyFrom = %d bytes, want %d", copied, len(FixtureMovieContent))
	}
	assertContent(t, volume, volume.Paths().Join(subject.LinkDir, "copy.mkv"), FixtureMovieContent)
	assertLibraryIntact(t, subject)
}

// assertEveryMethodUnconfined appelle chaque méthode de WriteRoot avec relative en destination (et en second
// argument de Replace) ; "kept" existe dans la racine pour que Replace ne puisse pas échouer pour une autre raison.
func assertEveryMethodUnconfined(t *testing.T, subject Subject, root fsys.WriteRoot, relative string) {
	t.Helper()
	source := fsys.Location{
		Volume: subject.Volume.Volume(),
		Path:   subject.Volume.Paths().Join(subject.LibraryRoot, FixtureMovieName),
	}
	assertUnconfined(t, "MkdirAll", relative, root.MkdirAll(relative, dirMode))
	assertUnconfined(t, "Symlink", relative, root.Symlink(FixtureMovieName, relative))
	assertUnconfined(t, "Hardlink", relative, root.Hardlink(source, relative))
	_, err := root.CopyFrom(t.Context(), source, relative)
	assertUnconfined(t, "CopyFrom", relative, err)
	writer, err := root.CreateExclusive(relative, fileMode)
	if writer != nil {
		closeQuietly(writer)
	}
	assertUnconfined(t, "CreateExclusive", relative, err)
	assertUnconfined(t, "Replace old", relative, root.Replace(relative, "kept"))
	assertUnconfined(t, "Replace new", relative, root.Replace("kept", relative))
	assertUnconfined(t, "Remove", relative, root.Remove(relative))
	_, err = root.Lstat(relative)
	assertUnconfined(t, "Lstat", relative, err)
}
