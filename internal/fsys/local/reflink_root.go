package local

import (
	"errors"
	"io/fs"
	"os"

	"necromanciarr/internal/fsys"
)

// Reflink clone source (même volume, chemin absolu) vers relative, créé en exclusif ; sans clonage possible,
// rien ne reste et l'erreur classée (ErrCrossDevice, ErrUnsupported) permet de passer à la sorte suivante.
func (root *writeRoot) Reflink(source fsys.Location, relative string) error {
	if err := checkSource(source); err != nil {
		return err
	}
	destination, err := root.resolve(relative)
	if err != nil {
		return err
	}
	defer destination.close()
	sourceFile, _, err := openRegularReadOnly(source.Path)
	if err != nil {
		return err
	}
	defer func() { _ = sourceFile.Close() }()
	destinationFile, err := destination.parent.OpenFile(destination.name, exclusiveCreateFlags, copyFileMode)
	if err != nil {
		return ClassifyLinkError(err)
	}
	cloneErr := cloneFile(destinationFile, sourceFile)
	closeErr := destinationFile.Close()
	if cloneErr == nil && closeErr == nil {
		return nil
	}
	return errors.Join(cloneErr, closeErr, destination.parent.Remove(destination.name))
}

// SameFile compare deux informations du disque local, rendues par ce volume ou par os : os.SameFile sur
// l'information d'origine. Hors du disque local, faux.
func SameFile(first, second fs.FileInfo) bool {
	return os.SameFile(untagged(first), untagged(second))
}

func untagged(info fs.FileInfo) fs.FileInfo {
	if tagged, isTagged := info.(localInfo); isTagged {
		return tagged.FileInfo
	}
	return info
}

// NewRegistry construit le registre du seul disque local avec ces racines de bibliothèque (chemins absolus).
// Sans racine, il ne sert qu'à lire : WriteRoot y est refusé (fsys.ErrNoLibraryRoots).
func NewRegistry(libraryRoots ...string) (*fsys.Registry, error) {
	roots := make([]fsys.Location, 0, len(libraryRoots))
	for _, libraryRoot := range libraryRoots {
		roots = append(roots, fsys.Location{Path: libraryRoot})
	}
	return fsys.NewRegistry(map[string]fsys.Volume{"": New()}, roots)
}
