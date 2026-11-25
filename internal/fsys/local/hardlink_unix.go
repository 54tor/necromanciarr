//go:build unix

package local

import (
	"os"

	"golang.org/x/sys/unix"
)

// linkInto crée le lien dur par linkat relatif au handle du dossier parent : aucun chemin de destination n'est
// résolu par nom. os.Root.Link ne convient pas, il exige une source sous la racine.
func linkInto(destination confinedPath, sourcePath string) error {
	parentDir, err := destination.parent.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = parentDir.Close() }()
	err = unix.Linkat(unix.AT_FDCWD, sourcePath, int(parentDir.Fd()), destination.name, 0) // #nosec G115 -- un descripteur tient dans un int
	if err != nil {
		return &os.LinkError{Op: "link", Old: sourcePath, New: destination.name, Err: err}
	}
	return nil
}
