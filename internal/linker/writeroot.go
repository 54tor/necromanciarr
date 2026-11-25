package linker

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/local"
)

// relativeSeparator : les chemins passés à une WriteRoot sont toujours en « / », que le volume local comme le
// volume distant acceptent.
const relativeSeparator = "/"

// pathsOf rend la syntaxe des chemins du volume de location : filepath en local, POSIX à distance.
func pathsOf(location fsys.Location) fsys.PathSyntax {
	if location.Volume == "" {
		return fsys.LocalPaths()
	}
	return fsys.RemotePaths()
}

// slashRelative met en « / » un chemin relatif rendu par la syntaxe du volume ; à distance il l'est déjà, et un
// « \ » y est un caractère de nom ordinaire.
func slashRelative(location fsys.Location, relative string) string {
	if location.Volume == "" {
		return filepath.ToSlash(relative)
	}
	return relative
}

// relativeUnder rend destination relative à base (même volume), en « / » ; l'échappement éventuel est refusé par
// la WriteRoot elle-même.
func relativeUnder(base, destination fsys.Location) (string, error) {
	if base.Volume != destination.Volume {
		return "", fmt.Errorf("%w: %s", ErrDestinationEscapesRoot, destination)
	}
	relative, err := pathsOf(base).Rel(base.Path, destination.Path)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrDestinationEscapesRoot, destination)
	}
	return slashRelative(base, relative), nil
}

// absoluteIn rend le chemin complet, dans la syntaxe du volume, d'un chemin relatif de root.
func absoluteIn(root fsys.WriteRoot, relative string) string {
	location := root.Root()
	return pathsOf(location).Join(location.Path, relative)
}

// confined garde l'erreur typée du linker quand la WriteRoot refuse une sortie de la racine.
func confined(err error) error {
	if errors.Is(err, fsys.ErrUnconfined) && !errors.Is(err, ErrDestinationEscapesRoot) {
		return fmt.Errorf("%w: %w", ErrDestinationEscapesRoot, err)
	}
	return err
}

// directoryMaker crée sous une racine les dossiers manquants un à un et retient, dans l'ordre, ceux qu'il a créés.
type directoryMaker struct {
	created []string
}

// ensure refuse tout segment symbolique (il pourrait mener hors de la racine) et tout segment qui n'est pas un dossier.
func (maker *directoryMaker) ensure(root fsys.WriteRoot, relativeDir string) error {
	if relativeDir == "." || relativeDir == "" {
		return nil
	}
	current := ""
	for _, segment := range strings.Split(relativeDir, relativeSeparator) {
		current = path.Join(current, segment)
		if err := maker.ensureSegment(root, current); err != nil {
			return confined(err)
		}
	}
	return nil
}

func (maker *directoryMaker) ensureSegment(root fsys.WriteRoot, directory string) error {
	info, err := root.Lstat(directory)
	if errors.Is(err, fs.ErrNotExist) {
		mkdirErr := root.MkdirAll(directory, dirMode)
		if mkdirErr != nil {
			return mkdirErr
		}
		maker.created = append(maker.created, directory)
		info, err = root.Lstat(directory)
	}
	if err != nil {
		return err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s is a symlink", ErrDestinationEscapesRoot, directory)
	}
	if !info.IsDir() {
		return fmt.Errorf("linker: %s is not a directory", directory)
	}
	return nil
}

// rollback retire en ordre inverse les entrées créées ; Remove ne retire qu'un nom de lien ou un dossier vide,
// jamais le contenu d'un fichier de la bibliothèque.
func rollback(root fsys.WriteRoot, created []string) error {
	var failures []error
	for index := len(created) - 1; index >= 0; index-- {
		if err := removeIfPresent(root, created[index]); err != nil {
			failures = append(failures, fmt.Errorf("linker: rollback: %w", err))
		}
	}
	return errors.Join(failures...)
}

func removeIfPresent(root fsys.WriteRoot, relative string) error {
	if err := root.Remove(relative); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// readViewOf rend la vue en lecture du volume d'une racine quand on la connaît sans registre : celle du disque
// local. À distance, nil : les contrôles qui relisent un contenu ou suivent un lien s'en tiennent au Lstat.
func readViewOf(root fsys.WriteRoot) fsys.ReadFS {
	if root.Root().Volume == "" {
		return local.New()
	}
	return nil
}

// sameFile compare deux entrées par FileID quand le volume les identifie, sinon par l'identité du disque local
// (Windows, où FileInfo ne porte pas l'index du fichier).
func sameFile(view fsys.ReadFS, first, second fs.FileInfo) bool {
	if view != nil {
		firstID, firstKnown := view.FileID(first)
		secondID, secondKnown := view.FileID(second)
		if firstKnown && secondKnown {
			return firstID == secondID
		}
	}
	return local.SameFile(first, second)
}
