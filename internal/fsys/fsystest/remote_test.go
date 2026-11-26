package fsystest_test

import (
	"errors"
	"testing"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/fsystest"
)

func TestRemoteVolume_conformance(t *testing.T) {
	skipOnWindows(t)
	fsystest.Run(t, func(t *testing.T) fsystest.Subject {
		volume := fsystest.NewRemoteVolume(t, "seedbox")
		volume.MkdirAll(t, "/srv/media")
		volume.MkdirAll(t, "/srv/links")
		hasSymlink := fsystest.WriteLibraryFixture(t, volume.LocalPath("/srv/media"))
		return fsystest.Subject{
			Volume: volume, LibraryRoot: "/srv/media", LinkDir: "/srv/links", LibraryHasSymlink: hasSymlink,
			PlantSymlink: func(t *testing.T, linkDirRelative, target string) {
				fsystest.PlantLocalSymlink(t, volume.LocalPath("/srv/links/"+linkDirRelative), volume.LocalPath(target))
			},
		}
	})
}

func TestRemoteVolume_unavailableRefusesEveryCallWithTheTypedError(t *testing.T) {
	volume := fsystest.NewRemoteVolume(t, "seedbox")
	volume.MkdirAll(t, "/srv/links")
	root, err := volume.OpenWriteRoot("/srv/links")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()

	volume.Unavailable.Store(true)

	_, statErr := volume.Stat("/srv/links")
	_, canonicalErr := volume.Canonical("/srv/links")
	mkdirErr := root.MkdirAll("x", 0o755)
	for _, valeurObtenue := range []error{statErr, canonicalErr, mkdirErr} {
		if !errors.Is(valeurObtenue, fsys.ErrRemoteUnavailable) {
			t.Fatalf("valeurAttendue ErrRemoteUnavailable, valeurObtenue %v", valeurObtenue)
		}
	}
}

func TestRemoteVolume_fileIdentityFollowsTheSwitch(t *testing.T) {
	skipOnWindows(t)
	volume := fsystest.NewRemoteVolume(t, "seedbox")
	volume.MkdirAll(t, "/srv")
	info, err := volume.Stat("/srv")
	if err != nil {
		t.Fatal(err)
	}
	_, knownBefore := volume.FileID(info)
	volume.FileIDKnown.Store(true)
	_, knownAfter := volume.FileID(info)

	if knownBefore || !knownAfter {
		t.Fatalf("valeurAttendue unknown then known, valeurObtenue %v then %v", knownBefore, knownAfter)
	}
}
