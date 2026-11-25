package doctor

import (
	"errors"
	"os"
	"path/filepath"

	"necromanciarr/internal/config"
	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/local"
	"necromanciarr/internal/linker"
)

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return path != "" && err == nil
}

func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// sameVolume s'appuie sur linker.ChooseLinkDir, qui compare les périphériques sans rien créer (registre en lecture
// seule, sans racine de bibliothèque).
func sameVolume(firstPath, secondPath string) (bool, error) {
	readRegistry, err := local.NewRegistry()
	if err != nil {
		return false, err
	}
	_, err = linker.ChooseLinkDir(fsys.Location{Path: firstPath}, []fsys.Location{{Path: secondPath}}, readRegistry)
	if err == nil {
		return true, nil
	}
	if isOnlyDifferentDevice(err) {
		return false, nil
	}
	return false, err
}

// isOnlyDifferentDevice distingue « autre volume » d'une erreur de lecture, que ChooseLinkDir joint à la première.
func isOnlyDifferentDevice(err error) bool {
	joined, isJoined := err.(interface{ Unwrap() []error })
	return errors.Is(err, linker.ErrNoLinkDirOnSameDevice) && isJoined && len(joined.Unwrap()) == 1
}

// registryProbe sonde un dossier de liens à travers la racine d'écriture du registre des racines de bibliothèque :
// un dossier qui en recouvre une est refusé avant toute entrée d'essai.
func registryProbe(libraryRoots []string) func(directory string) (linker.Capabilities, error) {
	return func(directory string) (linker.Capabilities, error) {
		registry, err := local.NewRegistry(libraryRoots...)
		if err != nil {
			return linker.Capabilities{}, err
		}
		return linker.ProbeDirectory(registry, fsys.Location{Path: directory})
	}
}

// insideLibraryRoot résout les liens symboliques des deux côtés avant de comparer : un lien ou un montage ne doit pas
// masquer un dossier situé dans la bibliothèque. Un dossier introuvable est jugé dehors (l'appelant le signale).
func insideLibraryRoot(directory string, roots []string) (string, bool) {
	resolvedDirectory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return "", false
	}
	for _, root := range roots {
		resolvedRoot, err := filepath.EvalSymlinks(root)
		if err == nil && config.PathsOverlap(resolvedDirectory, resolvedRoot) {
			return root, true
		}
	}
	return "", false
}
