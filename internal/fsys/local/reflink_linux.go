//go:build linux

package local

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// reflinkFileMode : la copie partagée doit rester lisible par le client torrent.
const reflinkFileMode os.FileMode = 0o644

// Reflink partage les blocs de la source (ioctl FICLONE) ; la destination, créée en exclusif, est retirée si le
// clonage échoue.
func Reflink(source, destination string) error {
	sourceFile, err := os.Open(source) // #nosec G304 -- source validée par checkSource
	if err != nil {
		return err
	}
	defer func() { _ = sourceFile.Close() }()
	destinationFile, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, reflinkFileMode) // #nosec G302 G304 -- destination sous la racine des liens, lisible par le client
	if err != nil {
		return ClassifyLinkError(err)
	}
	cloneErr := unix.IoctlFileClone(int(destinationFile.Fd()), int(sourceFile.Fd())) // #nosec G115 -- un descripteur tient dans un int
	closeErr := destinationFile.Close()
	if cloneErr == nil && closeErr == nil {
		return nil
	}
	return errors.Join(classifyCloneError(cloneErr), closeErr, os.Remove(destination))
}

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
