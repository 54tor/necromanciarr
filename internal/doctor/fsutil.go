package doctor

import (
	"errors"
	"os"
	"path/filepath"

	"necromanciarr/internal/config"

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

// sameVolume s'appuie sur linker.ChooseLinkDir, qui compare les périphériques sans rien créer.
func sameVolume(firstPath, secondPath string) (bool, error) {
	_, err := linker.ChooseLinkDir(firstPath, []string{secondPath})
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
