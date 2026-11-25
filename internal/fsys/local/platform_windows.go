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
