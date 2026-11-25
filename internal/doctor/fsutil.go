package doctor

import (
	"errors"
	"io/fs"

	"necromanciarr/internal/config"
	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/local"
	"necromanciarr/internal/linker"
)

// registryFor rend le registre du diagnostic : celui de Deps, sinon le disque local et les racines de bibliothèque de
// la configuration ; une racine qui ne s'y déclare pas (volume inconnu) laisse un registre en lecture seule, dont
// toute racine d'écriture est refusée (fsys.ErrNoLibraryRoots).
func (deps Deps) registryFor(settings config.Config) *fsys.Registry {
	if deps.Registry != nil {
		return deps.Registry
	}
	libraryRoots := make([]fsys.Location, 0, len(settings.LibraryRoots))
	for _, root := range settings.LibraryRoots {
		location, err := fsys.StoredLocation(root)
		if err != nil {
			continue
		}
		libraryRoots = append(libraryRoots, location)
	}
	volumes := map[string]fsys.Volume{"": local.New()}
	registry, err := fsys.NewRegistry(volumes, libraryRoots)
	if err != nil {
		registry, _ = fsys.NewRegistry(volumes, nil)
	}
	return registry
}

// readerOf rend la vue en lecture seule du volume de path et le nom à lui passer.
func readerOf(registry *fsys.Registry, path string) (fsys.ReadFS, string, error) {
	location, err := fsys.StoredLocation(path)
	if err != nil {
		return nil, "", err
	}
	return registry.Read(location)
}

func statThrough(registry *fsys.Registry, path string) (fs.FileInfo, error) {
	reader, name, err := readerOf(registry, path)
	if err != nil {
		return nil, err
	}
	return reader.Stat(name)
}

// canonicalThrough rend la forme canonique de path calculée par son volume (liens symboliques résolus), qualifiée
// par son volume : un chemin distant ne se compare jamais à un chemin local de même texte.
func canonicalThrough(registry *fsys.Registry, path string) (string, error) {
	reader, name, err := readerOf(registry, path)
	if err != nil {
		return "", err
	}
	canonical, err := reader.Canonical(name)
	if err != nil {
		return "", err
	}
	return fsys.Location{Volume: reader.Volume(), Path: canonical}.String(), nil
}

func pathExists(registry *fsys.Registry, path string) bool {
	if path == "" {
		return false
	}
	_, err := statThrough(registry, path)
	return err == nil
}

func isDirectory(registry *fsys.Registry, path string) bool {
	info, err := statThrough(registry, path)
	return err == nil && info.IsDir()
}

// sameVolumeIn s'appuie sur linker.ChooseLinkDir, qui compare les volumes lus par le registre sans rien créer.
func sameVolumeIn(registry *fsys.Registry) func(firstPath, secondPath string) (bool, error) {
	return func(firstPath, secondPath string) (bool, error) {
		first, err := fsys.StoredLocation(firstPath)
		if err != nil {
			return false, err
		}
		second, err := fsys.StoredLocation(secondPath)
		if err != nil {
			return false, err
		}
		_, err = linker.ChooseLinkDir(first, []fsys.Location{second}, registry)
		if err == nil {
			return true, nil
		}
		if isOnlyDifferentDevice(err) {
			return false, nil
		}
		return false, err
	}
}

// isOnlyDifferentDevice distingue « autre volume » d'une erreur de lecture, que ChooseLinkDir joint à la première.
func isOnlyDifferentDevice(err error) bool {
	joined, isJoined := err.(interface{ Unwrap() []error })
	return errors.Is(err, linker.ErrNoLinkDirOnSameDevice) && isJoined && len(joined.Unwrap()) == 1
}

// registryProbe sonde un dossier de liens à travers la racine d'écriture du registre : un dossier qui recouvre une
// racine de bibliothèque est refusé avant toute entrée d'essai, et les entrées d'essai sont créées puis retirées
// dans cette racine.
func registryProbe(registry *fsys.Registry) func(directory string) (linker.Capabilities, error) {
	return func(directory string) (linker.Capabilities, error) {
		location, err := fsys.StoredLocation(directory)
		if err != nil {
			return linker.Capabilities{}, err
		}
		return linker.ProbeDirectory(registry, location)
	}
}

// insideLibraryRoot résout les liens symboliques des deux côtés avant de comparer : un lien ou un montage ne doit pas
// masquer un dossier situé dans la bibliothèque. Un dossier introuvable est jugé dehors (l'appelant le signale).
func insideLibraryRoot(registry *fsys.Registry, directory string, roots []string) (string, bool) {
	resolvedDirectory, err := canonicalThrough(registry, directory)
	if err != nil {
		return "", false
	}
	for _, root := range roots {
		resolvedRoot, err := canonicalThrough(registry, root)
		if err == nil && config.PathsOverlap(resolvedDirectory, resolvedRoot) {
			return root, true
		}
	}
	return "", false
}
