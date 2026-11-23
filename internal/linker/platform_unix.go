//go:build unix

package linker

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"syscall"
)

// classifyLinkError traduit les refus du noyau : EPERM vient d'un système sans liens durs ou de protected_hardlinks,
// EMLINK d'un fichier qui a atteint son nombre maximal de liens.
func classifyLinkError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrExist):
		return fmt.Errorf("%w: %w", ErrDestinationExists, err)
	case errors.Is(err, syscall.EXDEV):
		return fmt.Errorf("%w: %w", ErrCrossDevice, err)
	case errors.Is(err, syscall.EPERM), errors.Is(err, syscall.EMLINK):
		return fmt.Errorf("%w: %w", ErrLinkRefused, err)
	default:
		return err
	}
}

func deviceKey(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("linker: no device id for %s", path)
	}
	return strconv.FormatUint(uint64(stat.Dev), 10), nil //nolint:unconvert // le type de Dev varie selon l'OS
}
