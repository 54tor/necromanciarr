package app

import (
	"errors"
	"testing"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/sftpfs"
)

// mirrorName est un second fournisseur sftp : un autre compte sur le même serveur, en lecture et écriture.
const mirrorName = "mirror"

func mirrorLocation(remotePath string) fsys.Location {
	return fsys.Location{Volume: mirrorName, Path: remotePath}
}

// Régression du finding F1 C1 : la bibliothèque déclarée sous un nom de fournisseur ne doit pas être joignable en
// écriture sous un autre nom de même adresse, ni par le registre, ni par la défense propre du volume.
func TestBuildRegistry_refusesALinkDirInTheLibraryThroughASecondProviderOfTheSameAddress(t *testing.T) {
	server := startSeedbox(t)
	settings := seedboxSettings(t, server.Provider(t, seedboxName))
	settings.FSProviders = append(settings.FSProviders, server.Provider(t, mirrorName))
	settings.LinkDirs = []string{"sftp://" + mirrorName + seedboxLibrary}
	registry, volumes := buildTestRegistry(t, settings, sftpfs.Deps{})

	root, err := registry.WriteRoot(mirrorLocation(seedboxLibrary))

	if root != nil || !errors.Is(err, fsys.ErrWriteInLibrary) {
		t.Fatalf("valeurObtenue %v, valeurAttendue fsys.ErrWriteInLibrary through the aliased provider", err)
	}
	careless, err := fsys.NewRegistry(map[string]fsys.Volume{mirrorName: volumes.volumes[mirrorName]},
		[]fsys.Location{mirrorLocation(seedboxLinks)})
	if err != nil {
		t.Fatal(err)
	}
	root, err = careless.WriteRoot(mirrorLocation(seedboxLibrary))
	if root != nil || !errors.Is(err, fsys.ErrWriteInLibrary) {
		t.Fatalf("valeurObtenue %v, valeurAttendue fsys.ErrWriteInLibrary from the aliased volume's own roots", err)
	}
	if writes := writeRequestsUnder(server, seedboxLibrary); len(writes) != 0 {
		t.Fatalf("valeurObtenue %+v, valeurAttendue no write request under the library, probes included", writes)
	}
}

// Deux adresses différentes ne partagent rien : le dossier /library du second serveur n'est pas une bibliothèque.
func TestBuildRegistry_keepsLibraryRootsApartBetweenDifferentAddresses(t *testing.T) {
	server := startSeedbox(t)
	other := startSeedbox(t)
	settings := seedboxSettings(t, server.Provider(t, seedboxName))
	settings.FSProviders = append(settings.FSProviders, other.Provider(t, mirrorName))
	settings.LinkDirs = []string{"sftp://" + mirrorName + seedboxLibrary}
	registry, _ := buildTestRegistry(t, settings, sftpfs.Deps{})

	root, err := registry.WriteRoot(mirrorLocation(seedboxLibrary))
	if err != nil {
		t.Fatalf("valeurObtenue %v, valeurAttendue a write root on the other server", err)
	}
	_ = root.Close()
	if writes := writeRequestsUnder(server, seedboxLibrary); len(writes) != 0 {
		t.Fatalf("valeurObtenue %+v, valeurAttendue no write on the library server", writes)
	}
}
