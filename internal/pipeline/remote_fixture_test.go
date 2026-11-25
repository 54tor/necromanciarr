package pipeline

import (
	"context"
	"testing"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/fsystest"
	"necromanciarr/internal/fsys/local"
	"necromanciarr/internal/store"
)

// Adresses du volume distant factice : bibliothèque et dossier de liens sur le même hôte, comme une seedbox.
const (
	remoteVolumeName  = "seedbox"
	remoteLibraryPath = "/srv/media"
	remoteLinkDirPath = "/srv/links"
)

// newRemoteApplyFixture place la bibliothèque et le dossier de liens sur le volume distant factice ; fixture.root
// reste le dossier local qui sert la bibliothèque, pour le contrôle « bibliothèque intacte » de chaque étape.
func newRemoteApplyFixture(t *testing.T) (*applyFixture, *fsystest.RemoteVolume) {
	t.Helper()
	volume := fsystest.NewRemoteVolume(t, remoteVolumeName)
	volume.MkdirAll(t, remoteLibraryPath)
	volume.MkdirAll(t, remoteLinkDirPath)
	registry, err := fsys.NewRegistry(map[string]fsys.Volume{"": local.New(), remoteVolumeName: volume},
		[]fsys.Location{volume.Location(remoteLibraryPath)})
	if err != nil {
		t.Fatal(err)
	}
	fixture := newApplyFixture(t)
	fixture.root = volume.LocalPath(remoteLibraryPath)
	fixture.remoteLibrary = volume.Location(remoteLibraryPath).String()
	fixture.linkDir = volume.Location(remoteLinkDirPath).String()
	fixture.registry = registry
	fixture.productionOpener = true
	return fixture, volume
}

// linksOf rend les liens enregistrés.
func (fixture *applyFixture) linksOf() []store.Link {
	fixture.t.Helper()
	links, err := fixture.store.ListLinks(context.Background(), "")
	if err != nil {
		fixture.t.Fatal(err)
	}
	return links
}
