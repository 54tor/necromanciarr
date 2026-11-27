//go:build windows

package local

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// ClassifyLinkError traduit les refus de Windows : un lien symbolique sans le privilège ou le mode développeur donne
// ERROR_PRIVILEGE_NOT_HELD, un volume FAT sans liens durs ERROR_INVALID_FUNCTION.
func ClassifyLinkError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrExist):
		return fmt.Errorf("%w: %w", ErrDestinationExists, err)
	case errors.Is(err, windows.ERROR_NOT_SAME_DEVICE):
		return fmt.Errorf("%w: %w", ErrCrossDevice, err)
	case errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD), errors.Is(err, windows.ERROR_INVALID_FUNCTION):
		return fmt.Errorf("%w: %w", ErrLinkRefused, err)
	default:
		return err
	}
}

// DeviceKey compare les volumes : un point de montage dans un dossier n'est pas vu, Windows le refusera au lien.
func DeviceKey(path string) (string, error) {
	if _, err := os.Stat(path); err != nil {
		return "", err
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return strings.ToUpper(filepath.VolumeName(absolutePath)), nil
}

const (
	ntPathPrefix    = `\??\`
	ntUNCPathPrefix = `\??\UNC\`
	uncPathPrefix   = `\\`
)

// symlinkTarget préfixe d'un « \??\ » une cible absolue : os.Root.Symlink écrit le texte reçu tel quel comme nom
// de substitution, que le noyau ne sait ouvrir qu'avec ce préfixe, là où os.Symlink (CreateSymbolicLinkW) l'ajoute
// seul. os.Readlink retire le préfixe, la cible relue reste celle d'origine.
func symlinkTarget(target string) string {
	target = filepath.FromSlash(target)
	switch {
	case strings.HasPrefix(target, ntPathPrefix):
		return target
	case strings.HasPrefix(target, uncPathPrefix+"?"+`\`) || strings.HasPrefix(target, uncPathPrefix+"."+`\`):
		return ntPathPrefix + target[len(uncPathPrefix)+2:]
	case strings.HasPrefix(target, uncPathPrefix):
		return ntUNCPathPrefix + target[len(uncPathPrefix):]
	case filepath.IsAbs(target):
		return ntPathPrefix + target
	default:
		return target
	}
}
