// Package fsystest porte la suite de conformité commune à tous les volumes fsys (local, SFTP). Elle ne dépend
// d'aucune implémentation : chaque paquet de volume l'appelle depuis ses tests avec son propre constructeur.
package fsystest

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"necromanciarr/internal/fsys"
)

// Contenu de la bibliothèque de test, posé par WriteLibraryFixture et attendu par Run.
const (
	FixtureMovieName    = "movie.mkv"
	FixtureMovieContent = "conformance movie bytes"
	FixtureNoteName     = "a.txt"
	FixtureNoteContent  = "alpha"
	FixtureSubdirName   = "sub"
	FixtureLinkName     = "movie-link"
)

// fixtureEntries : ReadDir doit les rendre dans cet ordre (tri par nom, '-' avant '.').
var fixtureEntries = []string{FixtureNoteName, FixtureLinkName, FixtureMovieName, FixtureSubdirName}

// fileMode : droits des fichiers écrits par la suite.
const fileMode fs.FileMode = 0o644

// NewVolume construit un volume neuf pour un cas : la racine de bibliothèque (chemin du volume) contient la
// bibliothèque de WriteLibraryFixture, le dossier de liens existe, est vide et ne recouvre pas la bibliothèque.
type NewVolume func(t *testing.T) (volume fsys.Volume, libraryRoot, linkDir string)

// WriteLibraryFixture pose la bibliothèque de test dans un dossier local (celui que sert le volume, y compris un
// serveur SFTP de test). Sous Windows sans droit de lien symbolique, le test est sauté.
func WriteLibraryFixture(t *testing.T, localDir string) {
	t.Helper()
	writeFixtureFile(t, filepath.Join(localDir, FixtureMovieName), FixtureMovieContent)
	writeFixtureFile(t, filepath.Join(localDir, FixtureNoteName), FixtureNoteContent)
	if err := os.Mkdir(filepath.Join(localDir, FixtureSubdirName), 0o755); err != nil {
		t.Fatalf("fixture subdir: %v", err)
	}
	if err := os.Symlink(FixtureMovieName, filepath.Join(localDir, FixtureLinkName)); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlinks unavailable: %v", err)
		}
		t.Fatalf("fixture symlink: %v", err)
	}
}

func writeFixtureFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(content), fileMode); err != nil {
		t.Fatalf("fixture file: %v", err)
	}
}

// Run exécute tous les cas de conformité ; chaque cas appelle newVolume pour partir d'un état neuf.
func Run(t *testing.T, newVolume NewVolume) {
	t.Helper()
	cases := []struct {
		name string
		run  func(t *testing.T, newVolume NewVolume)
	}{
		{"OpenIsReadOnly", caseOpenIsReadOnly},
		{"LstatDoesNotFollowSymlink", caseLstatDoesNotFollowSymlink},
		{"ReadDirIsSortedByName", caseReadDirIsSortedByName},
		{"ReadlinkReturnsTarget", caseReadlinkReturnsTarget},
		{"MissingEntryIsNotExist", caseMissingEntryIsNotExist},
		{"WriteRootRootIsCanonical", caseWriteRootRootIsCanonical},
		{"WriteRootRefusesEscapes", caseWriteRootRefusesEscapes},
		{"WriteRootRefusesSymlinkedParent", caseWriteRootRefusesSymlinkedParent},
		{"CreateExclusiveRefusesExisting", caseCreateExclusiveRefusesExisting},
		{"ReplaceIsAtomicOrRefused", caseReplaceIsAtomicOrRefused},
		{"RemoveDeletesEntry", caseRemoveDeletesEntry},
		{"SymlinkAndHardlinkFromLibrary", caseSymlinkAndHardlinkFromLibrary},
		{"CopyFromLibrary", caseCopyFromLibrary},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) { testCase.run(t, newVolume) })
	}
}

// caseOpenIsReadOnly prouve qu'Open lit le contenu et que le fichier rendu refuse toute écriture.
func caseOpenIsReadOnly(t *testing.T, newVolume NewVolume) {
	volume, libraryRoot, _ := newVolume(t)
	moviePath := volume.Paths().Join(libraryRoot, FixtureMovieName)
	file, err := volume.Open(moviePath)
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
	assertLibraryIntact(t, volume, libraryRoot)
}

// caseLstatDoesNotFollowSymlink prouve que Lstat voit le lien et Stat sa cible.
func caseLstatDoesNotFollowSymlink(t *testing.T, newVolume NewVolume) {
	volume, libraryRoot, _ := newVolume(t)
	linkPath := volume.Paths().Join(libraryRoot, FixtureLinkName)
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
func caseReadDirIsSortedByName(t *testing.T, newVolume NewVolume) {
	volume, libraryRoot, _ := newVolume(t)
	gotValue := entryNames(t, volume, libraryRoot)
	if !slices.Equal(gotValue, fixtureEntries) {
		t.Errorf("ReadDir = %q, want %q", gotValue, fixtureEntries)
	}
}

// caseReadlinkReturnsTarget prouve que Readlink rend la cible brute, sans la résoudre.
func caseReadlinkReturnsTarget(t *testing.T, newVolume NewVolume) {
	volume, libraryRoot, _ := newVolume(t)
	gotValue, err := volume.Readlink(volume.Paths().Join(libraryRoot, FixtureLinkName))
	if err != nil || gotValue != FixtureMovieName {
		t.Errorf("Readlink = %q, %v, want %q", gotValue, err, FixtureMovieName)
	}
}

// caseMissingEntryIsNotExist prouve que les erreurs d'absence sont typées fs.ErrNotExist.
func caseMissingEntryIsNotExist(t *testing.T, newVolume NewVolume) {
	volume, libraryRoot, linkDir := newVolume(t)
	missingPath := volume.Paths().Join(libraryRoot, "missing")
	if _, err := volume.Stat(missingPath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Stat error = %v, want fs.ErrNotExist", err)
	}
	if _, err := volume.Open(missingPath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Open error = %v, want fs.ErrNotExist", err)
	}
	root := openWriteRoot(t, volume, linkDir)
	if err := root.Remove("missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Remove error = %v, want fs.ErrNotExist", err)
	}
}

// caseWriteRootRootIsCanonical prouve que Root rend le volume et la forme canonique du dossier.
func caseWriteRootRootIsCanonical(t *testing.T, newVolume NewVolume) {
	volume, _, linkDir := newVolume(t)
	canonical, err := volume.Canonical(linkDir)
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	expectedValue := fsys.Location{Volume: volume.Volume(), Path: canonical}
	gotValue := openWriteRoot(t, volume, canonical).Root()
	if gotValue != expectedValue {
		t.Errorf("Root() = %+v, want %+v", gotValue, expectedValue)
	}
}

// caseWriteRootRefusesEscapes prouve que chaque méthode d'écriture refuse ".." et les chemins absolus
// (ErrUnconfined), sans rien écrire dans la bibliothèque.
func caseWriteRootRefusesEscapes(t *testing.T, newVolume NewVolume) {
	volume, libraryRoot, linkDir := newVolume(t)
	root := openWriteRoot(t, volume, linkDir)
	// "a/../b" reste lexicalement dedans mais ".." est refusé partout ; "" désignerait la racine elle-même.
	escapes := []string{"..", "../escape", "a/../b", "a/../../escape", volume.Paths().Join(libraryRoot, "escape"), ""}
	for _, relative := range escapes {
		assertUnconfined(t, "MkdirAll", relative, root.MkdirAll(relative, 0o755))
		assertUnconfined(t, "Symlink", relative, root.Symlink(FixtureMovieName, relative))
		_, err := root.CreateExclusive(relative, fileMode)
		assertUnconfined(t, "CreateExclusive", relative, err)
		assertUnconfined(t, "Replace", relative, root.Replace(relative, "kept"))
		assertUnconfined(t, "Remove", relative, root.Remove(relative))
	}
	assertLibraryIntact(t, volume, libraryRoot)
}

// caseWriteRootRefusesSymlinkedParent prouve qu'un dossier symbolique posé dans la racine et pointant vers la
// bibliothèque ne sert jamais de parent à une écriture.
func caseWriteRootRefusesSymlinkedParent(t *testing.T, newVolume NewVolume) {
	volume, libraryRoot, linkDir := newVolume(t)
	skipWithout(t, volume, func(capabilities fsys.Capabilities) bool { return capabilities.Symlink })
	root := openWriteRoot(t, volume, linkDir)
	if err := root.Symlink(libraryRoot, "escape"); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	through := "escape/" + FixtureSubdirName
	assertUnconfined(t, "MkdirAll", through, root.MkdirAll(through+"/new", 0o755))
	assertUnconfined(t, "Symlink", through, root.Symlink(FixtureMovieName, through+"/link"))
	_, err := root.CreateExclusive("escape/new.txt", fileMode)
	assertUnconfined(t, "CreateExclusive", "escape/new.txt", err)
	assertUnconfined(t, "Remove", "escape/"+FixtureNoteName, root.Remove("escape/"+FixtureNoteName))
	assertLibraryIntact(t, volume, libraryRoot)
}

// caseCreateExclusiveRefusesExisting prouve O_EXCL : une entrée existante n'est jamais tronquée (fs.ErrExist).
func caseCreateExclusiveRefusesExisting(t *testing.T, newVolume NewVolume) {
	volume, _, linkDir := newVolume(t)
	root := openWriteRoot(t, volume, linkDir)
	writeThroughRoot(t, root, "file", "first")
	if _, err := root.CreateExclusive("file", fileMode); !errors.Is(err, fs.ErrExist) {
		t.Errorf("second CreateExclusive error = %v, want fs.ErrExist", err)
	}
	assertContent(t, volume, volume.Paths().Join(linkDir, "file"), "first")
}

// caseReplaceIsAtomicOrRefused prouve que Replace remplace la cible si le volume l'annonce atomique, et sinon
// rend ErrNoAtomicReplace sans toucher aux deux entrées.
func caseReplaceIsAtomicOrRefused(t *testing.T, newVolume NewVolume) {
	volume, _, linkDir := newVolume(t)
	capabilities := capabilitiesOf(t, volume)
	root := openWriteRoot(t, volume, linkDir)
	writeThroughRoot(t, root, "old", "new content")
	writeThroughRoot(t, root, "new", "previous content")
	err := root.Replace("old", "new")
	newPath := volume.Paths().Join(linkDir, "new")
	if !capabilities.AtomicReplace {
		if !errors.Is(err, fsys.ErrNoAtomicReplace) {
			t.Fatalf("Replace error = %v, want ErrNoAtomicReplace", err)
		}
		assertContent(t, volume, newPath, "previous content")
		return
	}
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	assertContent(t, volume, newPath, "new content")
	if _, err := root.Lstat("old"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Lstat(old) error = %v, want fs.ErrNotExist", err)
	}
}

// caseRemoveDeletesEntry prouve que Remove supprime une entrée de la racine.
func caseRemoveDeletesEntry(t *testing.T, newVolume NewVolume) {
	volume, _, linkDir := newVolume(t)
	root := openWriteRoot(t, volume, linkDir)
	if err := root.MkdirAll("dir/nested", 0o755); err != nil {
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
func caseSymlinkAndHardlinkFromLibrary(t *testing.T, newVolume NewVolume) {
	volume, libraryRoot, linkDir := newVolume(t)
	capabilities := capabilitiesOf(t, volume)
	root := openWriteRoot(t, volume, linkDir)
	moviePath := volume.Paths().Join(libraryRoot, FixtureMovieName)
	if capabilities.Symlink {
		if err := root.Symlink(moviePath, "symlinked.mkv"); err != nil {
			t.Fatalf("Symlink: %v", err)
		}
		assertContent(t, volume, volume.Paths().Join(linkDir, "symlinked.mkv"), FixtureMovieContent)
	}
	if capabilities.Hardlink {
		source := fsys.Location{Volume: volume.Volume(), Path: moviePath}
		if err := root.Hardlink(source, "hardlinked.mkv"); err != nil {
			t.Fatalf("Hardlink: %v", err)
		}
		assertContent(t, volume, volume.Paths().Join(linkDir, "hardlinked.mkv"), FixtureMovieContent)
	}
	assertLibraryIntact(t, volume, libraryRoot)
}

// caseCopyFromLibrary prouve que CopyFrom rend une copie complète et compte ses octets.
func caseCopyFromLibrary(t *testing.T, newVolume NewVolume) {
	volume, libraryRoot, linkDir := newVolume(t)
	root := openWriteRoot(t, volume, linkDir)
	source := fsys.Location{Volume: volume.Volume(), Path: volume.Paths().Join(libraryRoot, FixtureMovieName)}
	copied, err := root.CopyFrom(t.Context(), source, "copy.mkv")
	if err != nil {
		t.Fatalf("CopyFrom: %v", err)
	}
	if copied != int64(len(FixtureMovieContent)) {
		t.Errorf("CopyFrom = %d bytes, want %d", copied, len(FixtureMovieContent))
	}
	assertContent(t, volume, volume.Paths().Join(linkDir, "copy.mkv"), FixtureMovieContent)
	assertLibraryIntact(t, volume, libraryRoot)
}

func openWriteRoot(t *testing.T, volume fsys.Volume, dir string) fsys.WriteRoot {
	t.Helper()
	root, err := volume.OpenWriteRoot(dir)
	if err != nil {
		t.Fatalf("OpenWriteRoot: %v", err)
	}
	t.Cleanup(func() { closeQuietly(root) })
	return root
}

func capabilitiesOf(t *testing.T, volume fsys.Volume) fsys.Capabilities {
	t.Helper()
	capabilities, err := volume.Capabilities(t.Context())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	return capabilities
}

func skipWithout(t *testing.T, volume fsys.Volume, has func(fsys.Capabilities) bool) {
	t.Helper()
	if !has(capabilitiesOf(t, volume)) {
		t.Skip("capability not offered by the volume")
	}
}

func writeThroughRoot(t *testing.T, root fsys.WriteRoot, relative, content string) {
	t.Helper()
	writer, err := root.CreateExclusive(relative, fileMode)
	if err != nil {
		t.Fatalf("CreateExclusive(%q): %v", relative, err)
	}
	if _, err := io.WriteString(writer, content); err != nil {
		t.Fatalf("write %q: %v", relative, err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close %q: %v", relative, err)
	}
}

func assertUnconfined(t *testing.T, method, relative string, err error) {
	t.Helper()
	if !errors.Is(err, fsys.ErrUnconfined) {
		t.Errorf("%s(%q) error = %v, want ErrUnconfined", method, relative, err)
	}
}

func assertContent(t *testing.T, volume fsys.Volume, name, expectedValue string) {
	t.Helper()
	file, err := volume.Open(name)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer closeQuietly(file)
	info, err := file.Stat()
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if gotValue := readAllAt(t, file, info.Size()); gotValue != expectedValue {
		t.Errorf("content = %q, want %q", gotValue, expectedValue)
	}
}

// assertLibraryIntact vérifie que la bibliothèque a gardé exactement ses entrées et ses contenus.
func assertLibraryIntact(t *testing.T, volume fsys.Volume, libraryRoot string) {
	t.Helper()
	if gotValue := entryNames(t, volume, libraryRoot); !slices.Equal(gotValue, fixtureEntries) {
		t.Errorf("library entries = %q, want %q", gotValue, fixtureEntries)
	}
	subdirEntries := entryNames(t, volume, volume.Paths().Join(libraryRoot, FixtureSubdirName))
	if len(subdirEntries) != 0 {
		t.Errorf("library subdir entries = %q, want none", subdirEntries)
	}
	assertContent(t, volume, volume.Paths().Join(libraryRoot, FixtureMovieName), FixtureMovieContent)
	assertContent(t, volume, volume.Paths().Join(libraryRoot, FixtureNoteName), FixtureNoteContent)
}

func entryNames(t *testing.T, volume fsys.Volume, dir string) []string {
	t.Helper()
	entries, err := volume.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func readAllAt(t *testing.T, reader io.ReaderAt, size int64) string {
	t.Helper()
	var buffer bytes.Buffer
	if _, err := io.Copy(&buffer, io.NewSectionReader(reader, 0, size+1)); err != nil {
		t.Fatalf("read: %v", err)
	}
	return buffer.String()
}

func closeQuietly(closer io.Closer) {
	_ = closer.Close()
}
