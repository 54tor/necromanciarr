//go:build windows

package local

import (
	"os"
	"path/filepath"
)

// linkInto : Windows ne lie que par chemin. Le chemin du parent doit encore désigner le dossier ouvert juste
// avant le lien ; la course restante entre ce contrôle et CreateHardLink est celle de linker aujourd'hui.
func linkInto(destination confinedPath, sourcePath string) error {
	current, err := os.Lstat(destination.parentPath)
	if err != nil {
		return err
	}
	opened, err := destination.parent.Stat(".")
	if err != nil {
		return err
	}
	if !isPlainDirectory(current) || !os.SameFile(current, opened) {
		return unconfined("a parent of the path changed before linking")
	}
	return os.Link(sourcePath, filepath.Join(destination.parentPath, destination.name))
}
