package dedupe

import (
	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/local"
	"necromanciarr/internal/linker"
)

// writeRegistry rend Options.Registry ; nil : le registre du disque local dont la bibliothèque est libraryPaths,
// les fichiers que l'appel lit : une racine torrent qui en contient un est refusée.
func writeRegistry(registry *fsys.Registry, libraryPaths []string) (*fsys.Registry, error) {
	if registry != nil {
		return registry, nil
	}
	return local.NewRegistry(libraryPaths...)
}

// registryProbe sonde un dossier torrent à travers la racine d'écriture que donne le registre.
func registryProbe(registry *fsys.Registry, libraryPaths []string) func(directory string) (linker.Capabilities, error) {
	// Un seul registre pour toute la planification : ses racines ne sont canonicalisées qu'une fois.
	probeRegistry, registryErr := writeRegistry(registry, libraryPaths)
	return func(directory string) (linker.Capabilities, error) {
		if registryErr != nil {
			return linker.Capabilities{}, registryErr
		}
		return linker.ProbeDirectory(probeRegistry, fsys.Location{Path: directory})
	}
}

// registryLink crée le lien temporaire dans la racine torrent ouverte par le registre.
func registryLink(registry *fsys.Registry, libraryPath string) func(kind linker.Kind, source, destination, root string) error {
	return func(kind linker.Kind, source, destination, root string) error {
		linkRegistry, err := writeRegistry(registry, []string{libraryPath})
		if err != nil {
			return err
		}
		return linker.CreateLinkUnder(linkRegistry, kind, source, fsys.Location{Path: destination}, fsys.Location{Path: root})
	}
}
