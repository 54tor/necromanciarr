package fsys_test

import (
	"errors"
	"io/fs"
	"strings"
	"sync"
	"testing"

	"necromanciarr/internal/fsys"
)

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

// failingFor rend une canonicalisation qui n'échoue (ou ne ment) que pour un chemin donné.
func failingFor(failedPath, output string, err error) func(string) (string, error) {
	return func(name string) (string, error) {
		if name == failedPath {
			return output, err
		}
		return posixCanonical(nil)(name)
	}
}

func TestRegistry_refusesWhenVolumeCannotCanonicalize(t *testing.T) {
	volume := newFakeVolume("seedbox")
	volume.canonical = failingFor("/srv/links", "", fsys.ErrCannotCanonicalize)
	registry := newTestRegistry(t, volume)
	_, err := registry.WriteRoot(fsys.Location{Volume: "seedbox", Path: "/srv/links"})
	if !errors.Is(err, fsys.ErrCannotCanonicalize) {
		t.Errorf("WriteRoot error = %v, want ErrCannotCanonicalize", err)
	}

	broken := newFakeVolume("seedbox")
	broken.canonical = func(string) (string, error) { return "", fsys.ErrCannotCanonicalize }
	brokenRegistry := newTestRegistry(t, broken)
	_, err = brokenRegistry.WriteRoot(fsys.Location{Volume: "seedbox", Path: "/srv/links"})
	if !errors.Is(err, fsys.ErrCannotCanonicalize) {
		t.Errorf("WriteRoot on a volume whose library roots fail: error = %v, want ErrCannotCanonicalize", err)
	}
}

// Un volume bogué qui rend une forme non canonique (barre finale, "..", relative) ne doit pas fausser le contrôle.
func TestRegistry_refusesNonCanonicalVolumeOutput(t *testing.T) {
	for _, output := range []string{"/srv/links/", "/srv/../srv/links", "srv/links", ""} {
		volume := newFakeVolume("seedbox")
		volume.canonical = failingFor("/srv/links", output, nil)
		registry := newTestRegistry(t, volume)
		_, err := registry.WriteRoot(fsys.Location{Volume: "seedbox", Path: "/srv/links"})
		if !errors.Is(err, fsys.ErrCannotCanonicalize) {
			t.Errorf("canonical output %q: error = %v, want ErrCannotCanonicalize", output, err)
		}
	}
}

func TestRegistry_canonicalErrorKeepsCauseWithoutPath(t *testing.T) {
	volume := newFakeVolume("seedbox")
	volume.canonical = failingFor("/home/u/links", "",
		&fs.PathError{Op: "realpath", Path: "/home/u/links", Err: fs.ErrNotExist})
	registry := newTestRegistry(t, volume)
	_, err := registry.WriteRoot(fsys.Location{Volume: "seedbox", Path: "/home/u/links"})
	if !errors.Is(err, fsys.ErrCannotCanonicalize) || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("error = %v, want ErrCannotCanonicalize wrapping fs.ErrNotExist", err)
	}
	if strings.Contains(err.Error(), "/home/u") {
		t.Errorf("error %q contains the path", err)
	}
}

func TestRegistry_buildsAndServesOtherVolumesWhenOneIsUnreachable(t *testing.T) {
	unreachable := newFakeVolume("seedbox")
	unreachable.canonical = func(string) (string, error) { return "", fsys.ErrRemoteUnavailable }
	local := newFakeVolume("")
	registry, err := fsys.NewRegistry(map[string]fsys.Volume{"seedbox": unreachable, "": local}, []fsys.Location{
		{Volume: "seedbox", Path: "/srv/media"},
		{Volume: "", Path: "/data/library"},
	})
	if err != nil {
		t.Fatalf("NewRegistry with an unreachable volume: %v", err)
	}
	if _, err := registry.WriteRoot(fsys.Location{Volume: "", Path: "/data/links"}); err != nil {
		t.Errorf("local WriteRoot: %v", err)
	}
	if _, err := registry.WriteRoot(fsys.Location{Volume: "", Path: "/data/library/x"}); !errors.Is(err, fsys.ErrWriteInLibrary) {
		t.Errorf("local WriteRoot in library: error = %v, want ErrWriteInLibrary", err)
	}
	_, err = registry.WriteRoot(fsys.Location{Volume: "seedbox", Path: "/srv/links"})
	if !errors.Is(err, fsys.ErrRemoteUnavailable) || !errors.Is(err, fsys.ErrCannotCanonicalize) {
		t.Errorf("WriteRoot on the unreachable volume: error = %v, want ErrRemoteUnavailable", err)
	}
	if _, _, err := registry.Read(fsys.Location{Volume: "seedbox", Path: "/srv/media"}); !errors.Is(err, fsys.ErrRemoteUnavailable) {
		t.Errorf("Read on the unreachable volume: error = %v, want ErrRemoteUnavailable", err)
	}
}

func TestRegistry_canonicalizesLibraryRootsOnceOnFirstConcurrentUse(t *testing.T) {
	volume := newFakeVolume("seedbox")
	registry := newTestRegistry(t, volume)
	if gotValue := volume.callsFor("/srv/media"); gotValue != 0 {
		t.Fatalf("library root canonicalized %d times by NewRegistry, want 0 (lazy)", gotValue)
	}
	const workerCount = 8
	var workers sync.WaitGroup
	for range workerCount {
		workers.Go(func() {
			_, _ = registry.WriteRoot(fsys.Location{Volume: "seedbox", Path: "/srv/links"})
		})
	}
	workers.Wait()
	expectedValue := 1
	for _, libraryRoot := range []string{"/srv/media", "/srv/other-library/"} {
		if gotValue := volume.callsFor(libraryRoot); gotValue != expectedValue {
			t.Errorf("%s canonicalized %d times, want %d", libraryRoot, gotValue, expectedValue)
		}
	}
}

func TestRegistry_refusesVolumeNameDifferentFromItsKey(t *testing.T) {
	_, err := fsys.NewRegistry(map[string]fsys.Volume{"seedbox": newFakeVolume("other")}, nil)
	if !errors.Is(err, fsys.ErrUnknownVolume) {
		t.Errorf("NewRegistry error = %v, want ErrUnknownVolume", err)
	}
}

// Deux graphies d'un même dossier (montage lié, NFD, nom 8.3) passent le contrôle textuel : l'identité les attrape
// quand le volume la fournit.
func TestRegistry_refusesAliasSharingFileIdentity(t *testing.T) {
	for _, capabilities := range []fsys.Capabilities{{FileIdentity: true}, {RemoteStat: true}} {
		volume := newFakeVolume("seedbox")
		volume.capabilities = capabilities
		volume.sharedIDs = map[string]uint64{"/srv/media": 7, "/mnt/bind": 7, "/srv": 9, "/mnt/up": 9}
		registry := newTestRegistry(t, volume)
		for _, dir := range []string{"/mnt/bind", "/mnt/bind/links", "/mnt/up"} {
			_, err := registry.WriteRoot(fsys.Location{Volume: "seedbox", Path: dir})
			if !errors.Is(err, fsys.ErrWriteInLibrary) {
				t.Errorf("%+v WriteRoot(%q) error = %v, want ErrWriteInLibrary", capabilities, dir, err)
			}
		}
		if _, err := registry.WriteRoot(fsys.Location{Volume: "seedbox", Path: "/mnt/links"}); err != nil {
			t.Errorf("%+v WriteRoot(/mnt/links): %v", capabilities, err)
		}
	}
}

// Sans identité de fichier, seul le contrôle textuel tient : risque résiduel documenté (doctor avertira).
func TestRegistry_withoutIdentityOnlyTextIsCompared(t *testing.T) {
	volume := newFakeVolume("seedbox")
	volume.sharedIDs = map[string]uint64{"/srv/media": 7, "/mnt/bind": 7}
	registry := newTestRegistry(t, volume)
	if _, err := registry.WriteRoot(fsys.Location{Volume: "seedbox", Path: "/mnt/bind"}); err != nil {
		t.Errorf("WriteRoot(/mnt/bind) without identity: %v", err)
	}
}

func TestRegistry_identityFailureIsFailClosed(t *testing.T) {
	volume := newFakeVolume("seedbox")
	volume.capabilities = fsys.Capabilities{FileIdentity: true}
	volume.statError = map[string]error{"/mnt": fs.ErrPermission}
	registry := newTestRegistry(t, volume)
	_, err := registry.WriteRoot(fsys.Location{Volume: "seedbox", Path: "/mnt/links"})
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("WriteRoot error = %v, want a refusal wrapping fs.ErrPermission", err)
	}
}

// Volume local insensible à la casse (Windows, macOS) : la clé de comparaison est repliée par le registre, la
// forme sur disque rendue par Canonical est gardée pour Root.
func TestRegistry_foldsCaseOnlyForComparisonOnCaseInsensitiveLocalVolume(t *testing.T) {
	fsys.SetLocalCaseInsensitiveForTest(t, true)
	local := newFakeVolume("")
	remote := newFakeVolume("seedbox")
	registry, err := fsys.NewRegistry(map[string]fsys.Volume{"": local, "seedbox": remote}, []fsys.Location{
		{Volume: "", Path: "/Srv/Media"},
		{Volume: "seedbox", Path: "/Srv/Media"},
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	for _, dir := range []string{"/SRV/MEDIA/links", "/srv/media", "/SRV"} {
		if _, err := registry.WriteRoot(fsys.Location{Volume: "", Path: dir}); !errors.Is(err, fsys.ErrWriteInLibrary) {
			t.Errorf("local WriteRoot(%q) error = %v, want ErrWriteInLibrary", dir, err)
		}
	}
	root, err := registry.WriteRoot(fsys.Location{Volume: "", Path: "/Data/Links"})
	if err != nil {
		t.Fatalf("WriteRoot(/Data/Links): %v", err)
	}
	expectedValue := fsys.Location{Volume: "", Path: "/Data/Links"}
	if gotValue := root.Root(); gotValue != expectedValue {
		t.Errorf("Root() = %+v, want the on-disk form %+v", gotValue, expectedValue)
	}
	if _, err := registry.WriteRoot(fsys.Location{Volume: "seedbox", Path: "/srv/media"}); err != nil {
		t.Errorf("remote volumes are case-sensitive: WriteRoot(/srv/media): %v", err)
	}
}

func TestRegistry_keepsCaseOnCaseSensitiveLocalVolume(t *testing.T) {
	fsys.SetLocalCaseInsensitiveForTest(t, false)
	local := newFakeVolume("")
	registry, err := fsys.NewRegistry(map[string]fsys.Volume{"": local}, []fsys.Location{{Volume: "", Path: "/Srv/Media"}})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if _, err := registry.WriteRoot(fsys.Location{Volume: "", Path: "/srv/media"}); err != nil {
		t.Errorf("WriteRoot(/srv/media): %v", err)
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

func TestRegistry_readNeverExposesTheVolume(t *testing.T) {
	registry := newTestRegistry(t, newFakeVolume("seedbox"))
	readFS, _, err := registry.Read(fsys.Location{Volume: "seedbox", Path: "/srv/media"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if _, isVolume := readFS.(fsys.Volume); isVolume {
		t.Fatalf("Read returned a value usable as fsys.Volume")
	}
	if _, opensWriteRoot := readFS.(interface {
		OpenWriteRoot(string) (fsys.WriteRoot, error)
	}); opensWriteRoot {
		t.Fatalf("Read returned a value with OpenWriteRoot")
	}
	expectedValue := "seedbox"
	if gotValue := readFS.Volume(); gotValue != expectedValue {
		t.Errorf("Volume() = %q, want %q", gotValue, expectedValue)
	}
}
