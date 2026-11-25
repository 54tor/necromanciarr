package dedupe

import (
	"necromanciarr/internal/fsys"
	"necromanciarr/internal/linker"
)

// writeRegistry rend Options.Registry, seul accès aux racines d'écriture ; nil : refus (fsys.ErrNoLibraryRoots),
// rien ne prouve alors qu'un dossier torrent ne recouvre pas la bibliothèque.
func writeRegistry(registry *fsys.Registry) (*fsys.Registry, error) {
	if registry == nil {
		return nil, fsys.ErrNoLibraryRoots
	}
	return registry, nil
}

// registryProbe sonde un dossier torrent à travers la racine d'écriture que donne le registre.
func registryProbe(registry *fsys.Registry) func(directory string) (linker.Capabilities, error) {
	return func(directory string) (linker.Capabilities, error) {
		probeRegistry, err := writeRegistry(registry)
		if err != nil {
			return linker.Capabilities{}, err
		}
		return linker.ProbeDirectory(probeRegistry, fsys.Location{Path: directory})
	}
}

// registryLink crée le lien temporaire dans la racine torrent ouverte par le registre.
func registryLink(registry *fsys.Registry) func(kind linker.Kind, source, destination, root string) error {
	return func(kind linker.Kind, source, destination, root string) error {
		linkRegistry, err := writeRegistry(registry)
		if err != nil {
			return err
		}
		return linker.CreateLinkUnder(linkRegistry, kind, source, fsys.Location{Path: destination}, fsys.Location{Path: root})
	}
}
