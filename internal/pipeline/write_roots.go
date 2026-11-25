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

// probeLinkDir sonde un dossier de liens (forme enregistrée) à travers sa racine d'écriture (défaut de Deps.ProbeLinks).
func (deps Deps) probeLinkDir(directory string) (linker.Capabilities, error) {
	location, err := locationOfStoredPath(directory)
	if err != nil {
		return linker.Capabilities{}, err
	}
	registry, err := deps.writeRegistry()
	if err != nil {
		return linker.Capabilities{}, err
	}
	return linker.ProbeDirectory(registry, location)
}

// prepareCopyUnder prépare une copie indépendante dans la racine d'écriture root (défaut de Deps.PrepareCopy) ; les
// trois chemins sont sous leur forme enregistrée.
func (deps Deps) prepareCopyUnder(source, destination, root string) (linker.PreparedCopy, error) {
	locations := make([]fsys.Location, 0, 3)
	for _, stored := range []string{source, destination, root} {
		location, err := locationOfStoredPath(stored)
		if err != nil {
			return linker.PreparedCopy{}, err
		}
		locations = append(locations, location)
	}
	registry, err := deps.writeRegistry()
	if err != nil {
		return linker.PreparedCopy{}, err
	}
	return linker.PrepareCopyUnder(registry, locations[0], locations[1], locations[2])
}

// openWriteRoot ouvre dir en racine d'écriture, seul accès en écriture de Link.
func (deps Deps) openWriteRoot(dir fsys.Location) (fsys.WriteRoot, error) {
	registry, err := deps.writeRegistry()
	if err != nil {
		return nil, err
	}
	return registry.WriteRoot(dir)
}

// openLinkRoot ouvre dir en racine d'écriture et rend la vue en lecture du même volume.
func (deps Deps) openLinkRoot(dir fsys.Location) (fsys.WriteRoot, fsys.ReadFS, error) {
	root, err := deps.openWriteRoot(dir)
	if err != nil {
		return nil, nil, err
	}
	source, _, err := readRegistryOrLocal(deps.Registry).Read(dir)
	if err != nil {
		_ = root.Close()
		return nil, nil, err
	}
	return root, source, nil
}

// writeRootRefusalReason garde la cause d'un refus du registre : volume distant coupé, bibliothèque introuvable,
// dossier qui la recouvre, ou dossier de liens inutilisable.
func writeRootRefusalReason(err error) string {
	switch {
	case isRemoteUnavailable(err):
		return ReasonRemoteUnavailable
	case errors.Is(err, fsys.ErrCannotCanonicalize), errors.Is(err, fsys.ErrNoLibraryRoots):
		return ReasonLibraryRootUnavailable
	case errors.Is(err, fsys.ErrWriteInLibrary):
		return ReasonLinkDirOverlapsLibrary
	default:
		return ReasonLinkRootUnusable
	}
}
