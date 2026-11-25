// Package fsystest porte la suite de conformité commune à tous les volumes fsys (local, SFTP). Elle ne dépend
// d'aucune implémentation : chaque paquet de volume l'appelle depuis ses tests avec son propre constructeur.
package fsystest

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
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

// fileMode et dirMode : droits des entrées écrites par la suite.
const (
	fileMode fs.FileMode = 0o644
	dirMode  fs.FileMode = 0o755
)

// Subject est ce que le constructeur d'un volume rend à chaque cas.
type Subject struct {
	Volume fsys.Volume
	// LibraryRoot contient la bibliothèque de WriteLibraryFixture ; LinkDir existe, est vide et ne la recouvre pas.
	// Les deux sont des chemins du volume.
	LibraryRoot, LinkDir string
	// LibraryHasSymlink rend la valeur de WriteLibraryFixture (faux sous Windows sans droit de lien symbolique).
	LibraryHasSymlink bool
	// PlantSymlink pose, HORS du volume (par le disque qui le sert), un lien symbolique LinkDir/linkDirRelative vers
	// target, chemin du volume : la suite ne dépend ainsi pas de WriteRoot.Symlink pour tester un parent piégé.
	// Il appelle t.Fatal si la pose échoue, t.Skip seulement quand la plateforme l'interdit (PlantLocalSymlink).
	PlantSymlink func(t *testing.T, linkDirRelative, target string)
	// CopyNeedsServerCopy : le volume ne copie que côté serveur (SFTP, où copier par le client ferait transiter tout
	// le média par le réseau). Sans Capabilities.ServerCopy, CopyFromLibrary attend alors ErrServerCopyUnavailable
	// et aucune entrée créée, au lieu d'une copie.
	CopyNeedsServerCopy bool
}

// NewVolume construit un volume neuf pour un cas.
type NewVolume func(t *testing.T) Subject

// WriteLibraryFixture pose la bibliothèque de test dans un dossier local (celui que sert le volume, y compris un
// serveur SFTP de test) et dit si le lien symbolique a pu être posé. Sous Windows sans droit de lien symbolique,
// le lien manque (signalé par t.Logf) et seuls les cas qui le lisent sont sautés.
func WriteLibraryFixture(t *testing.T, localDir string) (hasSymlink bool) {
	t.Helper()
	writeFixtureFile(t, filepath.Join(localDir, FixtureMovieName), FixtureMovieContent)
	writeFixtureFile(t, filepath.Join(localDir, FixtureNoteName), FixtureNoteContent)
	if err := os.Mkdir(filepath.Join(localDir, FixtureSubdirName), dirMode); err != nil { // #nosec G301 -- fixture de test, dossier temporaire
		t.Fatalf("fixture subdir: %v", err)
	}
	err := os.Symlink(FixtureMovieName, filepath.Join(localDir, FixtureLinkName))
	if err == nil {
		return true
	}
	if runtime.GOOS == "windows" {
		t.Logf("fixture symlink unavailable on this platform: %v", err)
		return false
	}
	t.Fatalf("fixture symlink: %v", err)
	return false
}

// PlantLocalSymlink pose un lien symbolique localPath -> target sur le disque local, pour les PlantSymlink des
// volumes servis par un dossier local ; sous Windows sans droit, seul le cas appelant est sauté.
func PlantLocalSymlink(t *testing.T, localPath, target string) {
	t.Helper()
	err := os.Symlink(target, localPath)
	if err == nil {
		return
	}
	if runtime.GOOS == "windows" {
		t.Skipf("symlink planting unavailable on this platform: %v", err)
	}
	t.Fatalf("plant symlink: %v", err)
}

func writeFixtureFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(content), fileMode); err != nil { // #nosec G306 -- fixture de test, dossier temporaire
		t.Fatalf("fixture file: %v", err)
	}
}

// ReadSubject est ce que le constructeur d'une vue en lecture rend à chaque cas de RunRead.
type ReadSubject struct {
	Volume fsys.ReadFS
	// LibraryRoot contient la bibliothèque de WriteLibraryFixture, chemin du volume.
	LibraryRoot       string
	LibraryHasSymlink bool
}

// NewReadVolume construit une vue en lecture neuve pour un cas.
type NewReadVolume func(t *testing.T) ReadSubject

func (subject Subject) readSubject() ReadSubject {
	return ReadSubject{Volume: subject.Volume, LibraryRoot: subject.LibraryRoot, LibraryHasSymlink: subject.LibraryHasSymlink}
}

type readCase struct {
	name string
	run  func(t *testing.T, subject ReadSubject)
}

var readCases = []readCase{
	{"OpenIsReadOnly", caseOpenIsReadOnly},
	{"LstatDoesNotFollowSymlink", caseLstatDoesNotFollowSymlink},
	{"ReadDirIsSortedByName", caseReadDirIsSortedByName},
	{"ReadlinkReturnsTarget", caseReadlinkReturnsTarget},
	{"MissingEntryIsNotExist", caseMissingEntryIsNotExist},
}

// RunRead exécute les seuls cas de lecture (ReadFS), pour un volume dont l'écriture n'existe pas encore ou est
// testée ailleurs ; chaque cas appelle newVolume pour partir d'un état neuf.
func RunRead(t *testing.T, newVolume NewReadVolume) {
	for _, testCase := range readCases {
		t.Run(testCase.name, func(t *testing.T) {
			subject := newVolume(t)
			if subject.Volume == nil {
				t.Fatalf("NewReadVolume must return a Volume")
			}
			testCase.run(t, subject)
		})
	}
}

// Run exécute tous les cas de conformité, lecture (RunRead) puis écriture ; chaque cas appelle newVolume pour
// partir d'un état neuf.
func Run(t *testing.T, newVolume NewVolume) {
	newCheckedVolume := func(t *testing.T) Subject {
		subject := newVolume(t)
		if subject.Volume == nil || subject.PlantSymlink == nil {
			t.Fatalf("NewVolume must return a Volume and a PlantSymlink hook")
		}
		return subject
	}
	RunRead(t, func(t *testing.T) ReadSubject { return newCheckedVolume(t).readSubject() })
	writeCases := []struct {
		name string
		run  func(t *testing.T, subject Subject)
	}{
		{"WriteRootMissingEntryIsNotExist", caseWriteRootMissingEntryIsNotExist},
		{"WriteRootRootIsCanonical", caseWriteRootRootIsCanonical},
		{"WriteRootRefusesEscapes", caseWriteRootRefusesEscapes},
		{"WriteRootRefusesSymlinkedParent", caseWriteRootRefusesSymlinkedParent},
		{"WriteRootRefusesSourceOfAnotherVolume", caseWriteRootRefusesSourceOfAnotherVolume},
		{"CreateExclusiveRefusesExisting", caseCreateExclusiveRefusesExisting},
		{"ReplaceIsAtomicOrRefused", caseReplaceIsAtomicOrRefused},
		{"RemoveDeletesEntry", caseRemoveDeletesEntry},
		{"SymlinkAndHardlinkFromLibrary", caseSymlinkAndHardlinkFromLibrary},
		{"CopyFromLibrary", caseCopyFromLibrary},
	}
	for _, testCase := range writeCases {
		t.Run(testCase.name, func(t *testing.T) {
			testCase.run(t, newCheckedVolume(t))
		})
	}
}
