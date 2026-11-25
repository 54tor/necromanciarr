package local_test

import (
	"os"
	"path/filepath"
	"testing"

	"necromanciarr/internal/fsys/fsystest"
	"necromanciarr/internal/fsys/local"
)

// newLocalSubject pose une bibliothèque et un dossier de liens frères dans un dossier temporaire.
func newLocalSubject(t *testing.T) fsystest.Subject {
	baseDir := t.TempDir()
	libraryRoot := filepath.Join(baseDir, "library")
	linkDir := filepath.Join(baseDir, "links")
	for _, dir := range []string{libraryRoot, linkDir} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	return fsystest.Subject{
		Volume:            local.New(),
		LibraryRoot:       libraryRoot,
		LinkDir:           linkDir,
		LibraryHasSymlink: fsystest.WriteLibraryFixture(t, libraryRoot),
		PlantSymlink: func(t *testing.T, linkDirRelative, target string) {
			fsystest.PlantLocalSymlink(t, filepath.Join(linkDir, linkDirRelative), target)
		},
	}
}

func TestLocal_conformance(t *testing.T) {
	fsystest.Run(t, newLocalSubject)
}
