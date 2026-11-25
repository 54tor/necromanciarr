package linker

import (
	"path/filepath"
	"testing"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/fsystest"
)

// testRegistry déclare une racine de bibliothèque sans rapport avec les dossiers des cas : ces tests portent sur
// le linker, le refus des racines qui recouvrent une bibliothèque est éprouvé par fsys.
func testRegistry(t *testing.T) *fsys.Registry {
	t.Helper()
	return fsystest.LocalRegistry(t, t.TempDir())
}

// openRoot ouvre dir par le registre, seul constructeur de WriteRoot, et la ferme en fin de test.
func openRoot(t *testing.T, dir string) fsys.WriteRoot {
	t.Helper()
	root, err := testRegistry(t).WriteRoot(fsys.Location{Path: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

// localSource est la vue en lecture du disque local, d'où viennent les sources des liens.
func localSource(t *testing.T) fsys.ReadFS {
	t.Helper()
	readFS, _, err := testRegistry(t).Read(fsys.Location{})
	if err != nil {
		t.Fatal(err)
	}
	return readFS
}

// applyOps applique ops (même Root pour toutes) dans la racine d'écriture de leur Root.
func applyOps(t *testing.T, ops []Op, preferred []Kind) (Result, error) {
	t.Helper()
	return Apply(openRoot(t, ops[0].Root), localSource(t), ops, preferred)
}

func (linkApplier applier) applyOps(t *testing.T, ops []Op, preferred []Kind) (Result, error) {
	t.Helper()
	return linkApplier.apply(openRoot(t, ops[0].Root), localSource(t), ops, preferred)
}

func localLocation(path string) fsys.Location {
	return fsys.Location{Path: path}
}

func localLocations(paths ...string) []fsys.Location {
	locations := make([]fsys.Location, 0, len(paths))
	for _, path := range paths {
		locations = append(locations, localLocation(path))
	}
	return locations
}

// relativeTo rend destination relative à root, la forme qu'attend une WriteRoot.
func relativeTo(t *testing.T, root, destination string) string {
	t.Helper()
	relative, err := filepath.Rel(root, destination)
	if err != nil {
		t.Fatal(err)
	}
	return relative
}
