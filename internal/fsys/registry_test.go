package fsys_test

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"strings"
	"sync"
	"testing"

	"necromanciarr/internal/fsys"
)

// fakeVolume est un volume minimal : seul Canonical a un comportement, réglable par test (liens symboliques,
// casse repliée, refus). Il sert au registre, pas à la suite de conformité.
type fakeVolume struct {
	name      string
	canonical func(name string) (string, error)
}

// posixCanonical imite un volume distant : nettoyage POSIX et alias symboliques résolus par préfixe.
func posixCanonical(symlinks map[string]string) func(string) (string, error) {
	return func(name string) (string, error) {
		cleaned := fsys.RemotePaths().Clean(name)
		for alias, target := range symlinks {
			if cleaned == alias || strings.HasPrefix(cleaned, alias+"/") {
				cleaned = target + strings.TrimPrefix(cleaned, alias)
			}
		}
		return cleaned, nil
	}
}

func newFakeVolume(name string) *fakeVolume {
	return &fakeVolume{name: name, canonical: posixCanonical(nil)}
}

type fakeWriteRoot struct{ root fsys.Location }

var errFake = errors.New("fake: not implemented")

func (volume *fakeVolume) Volume() string                         { return volume.name }
func (volume *fakeVolume) Open(string) (fsys.File, error)         { return nil, errFake }
func (volume *fakeVolume) Stat(string) (fs.FileInfo, error)       { return nil, errFake }
func (volume *fakeVolume) Lstat(string) (fs.FileInfo, error)      { return nil, errFake }
func (volume *fakeVolume) ReadDir(string) ([]fs.DirEntry, error)  { return nil, errFake }
func (volume *fakeVolume) Readlink(string) (string, error)        { return "", errFake }
func (volume *fakeVolume) Canonical(name string) (string, error)  { return volume.canonical(name) }
func (volume *fakeVolume) VolumeID(string) (string, bool, error)  { return "", false, errFake }
func (volume *fakeVolume) FileID(fs.FileInfo) (fsys.FileID, bool) { return fsys.FileID{}, false }
func (volume *fakeVolume) FreeSpace(string) (uint64, error)       { return 0, errFake }
func (volume *fakeVolume) Paths() fsys.PathSyntax                 { return fsys.RemotePaths() }
func (volume *fakeVolume) Capabilities(context.Context) (fsys.Capabilities, error) {
	return fsys.Capabilities{}, nil
}

func (volume *fakeVolume) OpenWriteRoot(dir string) (fsys.WriteRoot, error) {
	return &fakeWriteRoot{root: fsys.Location{Volume: volume.name, Path: dir}}, nil
}

func (root *fakeWriteRoot) Root() fsys.Location                  { return root.root }
func (root *fakeWriteRoot) MkdirAll(string, fs.FileMode) error   { return errFake }
func (root *fakeWriteRoot) Symlink(string, string) error         { return errFake }
func (root *fakeWriteRoot) Hardlink(fsys.Location, string) error { return errFake }
func (root *fakeWriteRoot) Replace(string, string) error         { return errFake }
func (root *fakeWriteRoot) Remove(string) error                  { return errFake }
func (root *fakeWriteRoot) Lstat(string) (fs.FileInfo, error)    { return nil, errFake }
func (root *fakeWriteRoot) Close() error                         { return nil }
func (root *fakeWriteRoot) CopyFrom(context.Context, fsys.Location, string) (int64, error) {
	return 0, errFake
}

func (root *fakeWriteRoot) CreateExclusive(string, fs.FileMode) (io.WriteCloser, error) {
	return nil, errFake
}

func newTestRegistry(t *testing.T, volumes ...*fakeVolume) *fsys.Registry {
	t.Helper()
	byName := map[string]fsys.Volume{}
	for _, volume := range volumes {
		byName[volume.name] = volume
	}
	registry, err := fsys.NewRegistry(byName, []fsys.Location{
		{Volume: "seedbox", Path: "/srv/media"},
		{Volume: "seedbox", Path: "/srv/other-library/"},
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return registry
}

func TestRegistry_refusesWriteRootOverlappingLibrary(t *testing.T) {
	registry := newTestRegistry(t, newFakeVolume("seedbox"), newFakeVolume("other"))
	refused := []string{
		"/srv/media",
		"/srv/media/",
		"/srv/media/links",
		"/srv/media/a/b/c",
		"/srv",
		"/",
		"/srv/other-library",
		"/srv/media/../media/x",
		"/srv//media",
		"//srv/media/x",
		"/srv/./media/x/.",
		"/srv/links/../media",
	}
	for _, dir := range refused {
		_, err := registry.WriteRoot(fsys.Location{Volume: "seedbox", Path: dir})
		if !errors.Is(err, fsys.ErrWriteInLibrary) {
			t.Errorf("WriteRoot(%q) error = %v, want ErrWriteInLibrary", dir, err)
		}
	}
	accepted := []fsys.Location{
		{Volume: "seedbox", Path: "/srv/links"},
		{Volume: "seedbox", Path: "/srv/media-links"},
		{Volume: "seedbox", Path: "/srv/medi"},
		{Volume: "other", Path: "/srv/media"},
		{Volume: "other", Path: "/srv/media/x"},
	}
	for _, dir := range accepted {
		root, err := registry.WriteRoot(dir)
		if err != nil {
			t.Errorf("WriteRoot(%+v): %v", dir, err)
			continue
		}
		if root.Root() != dir {
			t.Errorf("Root() = %+v, want %+v", root.Root(), dir)
		}
	}
}

func TestRegistry_refusesWriteRootReachingLibraryThroughSymlink(t *testing.T) {
	volume := newFakeVolume("seedbox")
	volume.canonical = posixCanonical(map[string]string{"/srv/alias": "/srv/media", "/srv/up": "/srv"})
	registry := newTestRegistry(t, volume)
	for _, dir := range []string{"/srv/alias", "/srv/alias/links", "/srv/up"} {
		_, err := registry.WriteRoot(fsys.Location{Volume: "seedbox", Path: dir})
		if !errors.Is(err, fsys.ErrWriteInLibrary) {
			t.Errorf("WriteRoot(%q) error = %v, want ErrWriteInLibrary", dir, err)
		}
	}
}

func TestRegistry_refusesWriteRootDifferingOnlyByCase(t *testing.T) {
	volume := newFakeVolume("seedbox")
	volume.canonical = func(name string) (string, error) {
		return strings.ToLower(fsys.RemotePaths().Clean(name)), nil
	}
	registry, err := fsys.NewRegistry(map[string]fsys.Volume{"seedbox": volume},
		[]fsys.Location{{Volume: "seedbox", Path: "/Srv/Media"}})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	for _, dir := range []string{"/SRV/MEDIA/links", "/srv/media", "/SRV"} {
		_, err := registry.WriteRoot(fsys.Location{Volume: "seedbox", Path: dir})
		if !errors.Is(err, fsys.ErrWriteInLibrary) {
			t.Errorf("WriteRoot(%q) error = %v, want ErrWriteInLibrary", dir, err)
		}
	}
}

func TestRegistry_writeRootReceivesCanonicalDirectory(t *testing.T) {
	volume := newFakeVolume("seedbox")
	volume.canonical = posixCanonical(map[string]string{"/srv/alias": "/data/links"})
	registry := newTestRegistry(t, volume)
	root, err := registry.WriteRoot(fsys.Location{Volume: "seedbox", Path: "/srv/alias/"})
	if err != nil {
		t.Fatalf("WriteRoot: %v", err)
	}
	expectedValue := fsys.Location{Volume: "seedbox", Path: "/data/links"}
	gotValue := root.Root()
	if gotValue != expectedValue {
		t.Errorf("Root() = %+v, want %+v", gotValue, expectedValue)
	}
}

func TestRegistry_refusesWhenVolumeCannotCanonicalize(t *testing.T) {
	volume := newFakeVolume("seedbox")
	registry := newTestRegistry(t, volume)
	volume.canonical = func(string) (string, error) { return "", fsys.ErrCannotCanonicalize }
	_, err := registry.WriteRoot(fsys.Location{Volume: "seedbox", Path: "/srv/links"})
	if !errors.Is(err, fsys.ErrCannotCanonicalize) {
		t.Errorf("WriteRoot error = %v, want ErrCannotCanonicalize", err)
	}

	broken := newFakeVolume("seedbox")
	broken.canonical = func(string) (string, error) { return "", fsys.ErrCannotCanonicalize }
	_, err = fsys.NewRegistry(map[string]fsys.Volume{"seedbox": broken},
		[]fsys.Location{{Volume: "seedbox", Path: "/srv/media"}})
	if !errors.Is(err, fsys.ErrCannotCanonicalize) {
		t.Errorf("NewRegistry error = %v, want ErrCannotCanonicalize", err)
	}
}

// Un volume bogué qui rend une forme non canonique (barre finale, "..", relative) ne doit pas fausser le contrôle.
func TestRegistry_refusesNonCanonicalVolumeOutput(t *testing.T) {
	for _, output := range []string{"/srv/links/", "/srv/../srv/links", "srv/links", ""} {
		volume := newFakeVolume("seedbox")
		registry := newTestRegistry(t, volume)
		volume.canonical = func(string) (string, error) { return output, nil }
		_, err := registry.WriteRoot(fsys.Location{Volume: "seedbox", Path: "/srv/links"})
		if !errors.Is(err, fsys.ErrCannotCanonicalize) {
			t.Errorf("canonical output %q: error = %v, want ErrCannotCanonicalize", output, err)
		}
	}
}

func TestRegistry_canonicalErrorKeepsCauseWithoutPath(t *testing.T) {
	volume := newFakeVolume("seedbox")
	registry := newTestRegistry(t, volume)
	volume.canonical = func(name string) (string, error) {
		return "", &fs.PathError{Op: "realpath", Path: name, Err: fs.ErrNotExist}
	}
	_, err := registry.WriteRoot(fsys.Location{Volume: "seedbox", Path: "/home/u/links"})
	if !errors.Is(err, fsys.ErrCannotCanonicalize) || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("error = %v, want ErrCannotCanonicalize wrapping fs.ErrNotExist", err)
	}
	if strings.Contains(err.Error(), "/home/u") {
		t.Errorf("error %q contains the path", err)
	}
}

func TestRegistry_unknownVolume(t *testing.T) {
	registry := newTestRegistry(t, newFakeVolume("seedbox"))
	if _, _, err := registry.Read(fsys.Location{Volume: "nope", Path: "/x"}); !errors.Is(err, fsys.ErrUnknownVolume) {
		t.Errorf("Read error = %v, want ErrUnknownVolume", err)
	}
	if _, err := registry.WriteRoot(fsys.Location{Volume: "nope", Path: "/x"}); !errors.Is(err, fsys.ErrUnknownVolume) {
		t.Errorf("WriteRoot error = %v, want ErrUnknownVolume", err)
	}
	_, err := fsys.NewRegistry(map[string]fsys.Volume{}, []fsys.Location{{Volume: "seedbox", Path: "/srv"}})
	if !errors.Is(err, fsys.ErrUnknownVolume) {
		t.Errorf("NewRegistry error = %v, want ErrUnknownVolume", err)
	}
}

func TestRegistry_readReturnsVolumeAndPath(t *testing.T) {
	volume := newFakeVolume("seedbox")
	registry := newTestRegistry(t, volume)
	readFS, name, err := registry.Read(fsys.Location{Volume: "seedbox", Path: "/srv/media/film.mkv"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if readFS.Volume() != "seedbox" || name != "/srv/media/film.mkv" {
		t.Errorf("Read = %q, %q, want seedbox, /srv/media/film.mkv", readFS.Volume(), name)
	}
}

func TestRegistry_concurrentUseIsRaceFree(t *testing.T) {
	registry := newTestRegistry(t, newFakeVolume("seedbox"), newFakeVolume("other"))
	const workerCount = 8
	var workers sync.WaitGroup
	for workerIndex := range workerCount {
		workers.Go(func() {
			for iteration := range 50 {
				if _, _, err := registry.Read(fsys.Location{Volume: "seedbox", Path: "/srv/media"}); err != nil {
					t.Errorf("worker %d iteration %d Read: %v", workerIndex, iteration, err)
				}
				if _, err := registry.WriteRoot(fsys.Location{Volume: "other", Path: "/srv/media"}); err != nil {
					t.Errorf("worker %d iteration %d WriteRoot: %v", workerIndex, iteration, err)
				}
			}
		})
	}
	workers.Wait()
}
