package app

import (
	"strings"
	"testing"

	"necromanciarr/internal/config"
	"necromanciarr/internal/fsys"
)

func TestNewFilesystemRegistry_readsLocalLibraryRoots(t *testing.T) {
	libraryRoot := t.TempDir()

	registry, err := newFilesystemRegistry(config.Config{LibraryRoots: []string{libraryRoot}})
	if err != nil {
		t.Fatal(err)
	}

	readFS, path, err := registry.Read(fsys.Location{Path: libraryRoot})
	if err != nil || path != libraryRoot {
		t.Fatalf("valeurObtenue (%v, %q), valeurAttendue the local volume reading %s", err, path, libraryRoot)
	}
	if _, err := readFS.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestNewFilesystemRegistry_refusesARootThatIsNotALocation(t *testing.T) {
	_, err := newFilesystemRegistry(config.Config{LibraryRoots: []string{"relative/media"}})

	if err == nil || !strings.Contains(err.Error(), "libraryRoots[0]") {
		t.Fatalf("valeurObtenue %v, valeurAttendue an error naming libraryRoots[0]", err)
	}
}
