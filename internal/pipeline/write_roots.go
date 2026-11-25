package pipeline

import (
	"errors"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/local"
	"necromanciarr/internal/linker"
)

// writeRegistry rend Deps.Registry, seul accès aux racines d'écriture ; nil : le registre du disque local avec
// LibraryRoots, qui refuse toute racine d'écriture si LibraryRoots est vide.
func (deps Deps) writeRegistry() (*fsys.Registry, error) {
	if deps.Registry != nil {
		return deps.Registry, nil
	}
	return local.NewRegistry(deps.LibraryRoots...)
}

// probeLinkDir sonde un dossier de liens à travers sa racine d'écriture (défaut de Deps.ProbeLinks).
func (deps Deps) probeLinkDir(directory string) (linker.Capabilities, error) {
	registry, err := deps.writeRegistry()
	if err != nil {
		return linker.Capabilities{}, err
	}
	return linker.ProbeDirectory(registry, fsys.Location{Path: directory})
}

// prepareCopyUnder prépare une copie indépendante dans la racine d'écriture root (défaut de Deps.PrepareCopy).
func (deps Deps) prepareCopyUnder(source, destination, root string) (linker.PreparedCopy, error) {
	registry, err := deps.writeRegistry()
	if err != nil {
		return linker.PreparedCopy{}, err
	}
	return linker.PrepareCopyUnder(registry, fsys.Location{Path: source}, fsys.Location{Path: destination},
		fsys.Location{Path: root})
}

// openLinkRoot ouvre destRoot en racine d'écriture et rend la vue en lecture des sources du même volume.
func (deps Deps) openLinkRoot(destRoot string) (fsys.WriteRoot, fsys.ReadFS, error) {
	registry, err := deps.writeRegistry()
	if err != nil {
		return nil, nil, err
	}
	location := fsys.Location{Path: destRoot}
	root, err := registry.WriteRoot(location)
	if err != nil {
		return nil, nil, err
	}
	source, _, err := registry.Read(location)
	if err != nil {
		_ = root.Close()
		return nil, nil, err
	}
	return root, source, nil
}

// writeRootRefusalReason garde la cause d'un refus du registre : bibliothèque introuvable, dossier qui la recouvre,
// ou dossier de liens inutilisable.
func writeRootRefusalReason(err error) string {
	switch {
	case errors.Is(err, fsys.ErrCannotCanonicalize), errors.Is(err, fsys.ErrNoLibraryRoots):
		return ReasonLibraryRootUnavailable
	case errors.Is(err, fsys.ErrWriteInLibrary):
		return ReasonLinkDirOverlapsLibrary
	default:
		return ReasonLinkRootUnusable
	}
}

func localLocations(paths []string) []fsys.Location {
	locations := make([]fsys.Location, 0, len(paths))
	for _, path := range paths {
		locations = append(locations, fsys.Location{Path: path})
	}
	return locations
}
