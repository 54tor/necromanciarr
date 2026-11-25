package fsys_test

import (
	"errors"
	"testing"

	"necromanciarr/internal/fsys"
)

// Forme canonique d'un partage UNC : la racine du partage garde sa barre finale (filepath.Clean) et sert de
// point fixe à Dir ; un autre partage du même serveur ne recouvre pas la bibliothèque.
func TestRegistry_uncShareRootIsAParentOfItsLibrary(t *testing.T) {
	local := newFakeVolume("")
	local.paths = fsys.LocalPaths()
	local.canonical = func(name string) (string, error) { return fsys.LocalPaths().Clean(name), nil }
	registry, err := fsys.NewRegistry(map[string]fsys.Volume{"": local},
		[]fsys.Location{{Volume: "", Path: `\\server\share\media`}})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	for _, dir := range []string{`\\server\share\`, `\\server\share`, `\\SERVER\Share\Media\links`} {
		if _, err := registry.WriteRoot(fsys.Location{Volume: "", Path: dir}); !errors.Is(err, fsys.ErrWriteInLibrary) {
			t.Errorf("WriteRoot(%q) error = %v, want ErrWriteInLibrary", dir, err)
		}
	}
	for _, dir := range []string{`\\server\share\links`, `\\server\archive\media`} {
		if _, err := registry.WriteRoot(fsys.Location{Volume: "", Path: dir}); err != nil {
			t.Errorf("WriteRoot(%q): %v", dir, err)
		}
	}
}
