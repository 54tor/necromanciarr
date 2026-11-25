package fsystest

import (
	"testing"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/local"
)

// LocalRegistry construit le registre du seul disque local, partagé par les tests des paquets qui lisent la
// bibliothèque ; libraryRoots sont des chemins locaux absolus.
func LocalRegistry(t testing.TB, libraryRoots ...string) *fsys.Registry {
	t.Helper()
	roots := make([]fsys.Location, 0, len(libraryRoots))
	for _, libraryRoot := range libraryRoots {
		roots = append(roots, fsys.Location{Path: libraryRoot})
	}
	registry, err := fsys.NewRegistry(map[string]fsys.Volume{"": local.New()}, roots)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}
