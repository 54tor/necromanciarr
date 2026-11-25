//go:build linux

package local

import (
	"os"

	"golang.org/x/sys/unix"
)

// cloneFile partage les blocs de source dans destination (ioctl FICLONE) entre deux handles déjà ouverts.
func cloneFile(destination, source *os.File) error {
	return classifyCloneError(unix.IoctlFileClone(int(destination.Fd()), int(source.Fd()))) // #nosec G115 -- un descripteur tient dans un int
}
