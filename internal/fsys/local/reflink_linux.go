//go:build linux

package local

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// classifyCloneError range un refus de FICLONE parmi les erreurs de lien qui permettent une autre sorte.
func classifyCloneError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, unix.EXDEV):
		return fmt.Errorf("%w: %w", ErrCrossDevice, err)
	case errors.Is(err, unix.EOPNOTSUPP), errors.Is(err, unix.EINVAL), errors.Is(err, unix.ENOTTY),
		errors.Is(err, unix.ENOSYS):
		return fmt.Errorf("%w: %w", ErrUnsupported, err)
	default:
		return err
	}
}
