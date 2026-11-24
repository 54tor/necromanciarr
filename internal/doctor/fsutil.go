package doctor

import (
	"errors"
	"os"

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
