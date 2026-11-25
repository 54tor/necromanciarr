package linker

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"path"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/local"
)

// Erreurs typées de PrepareCopy.
var (
	// ErrInsufficientSpace : le volume du dossier de liens n'a pas la place d'une copie complète, marge comprise.
	ErrInsufficientSpace = errors.New("linker: not enough free space for an independent copy")
	// ErrFreeSpaceUnknown : la place libre ne peut pas être lue sur cette plateforme ; aucune copie n'est tentée.
	ErrFreeSpaceUnknown = errors.New("linker: free space cannot be read")
)

// copyMarginBytes et copyMarginDivisor : marge laissée libre après une copie (64 Mio plus 1 % de la taille), pour
// ne jamais remplir le volume que le client torrent utilise aussi.
const (
	copyMarginBytes   = 64 << 20
	copyMarginDivisor = 100
)

// PreparedCopy est une copie indépendante écrite dans un temporaire à côté de sa destination ; Commit la met en place
// par un remplacement atomique, Discard la retire. Kind vaut Reflink ou Copy. Destination est le chemin complet de
// la destination dans la syntaxe de son volume.
type PreparedCopy struct {
	Kind        Kind
	Destination string
	root        fsys.WriteRoot
	relative    string
	temporary   string
	// ownsRoot : la racine a été ouverte pour cette copie (PrepareCopyUnder) et se ferme après Commit réussi ou Discard.
	ownsRoot bool
}

type copier struct {
	reflink   func(root fsys.WriteRoot, source fsys.Location, relative string) error
	freeBytes func(directory fsys.Location) (uint64, error)
}

func systemCopier() copier {
	return copier{reflink: reflink, freeBytes: localFreeBytes}
}

// localFreeBytes ne lit la place libre que du disque local : à distance elle reste inconnue (Force refusé).
func localFreeBytes(directory fsys.Location) (uint64, error) {
	if directory.Volume != "" {
		return 0, fmt.Errorf("%w: remote volume", errors.ErrUnsupported)
	}
	return local.FreeBytes(directory.Path)
}

// PrepareCopy écrit dans root, dans le dossier de relative, une copie de source (même volume) qui ne partage aucun
// bloc modifiable avec elle : reflink si le système de fichiers le permet (copie à l'écriture), sinon copie complète
// si la place libre suffit. Rien n'est remplacé avant Commit ; root reste à l'appelant.
func PrepareCopy(root fsys.WriteRoot, source fsys.Location, relative string) (PreparedCopy, error) {
	return systemCopier().prepare(root, source, relative)
}

// PrepareCopyUnder ouvre la racine d'écriture root par registry puis prépare la copie de source vers destination
// comme PrepareCopy ; la racine se ferme après Commit réussi ou Discard.
func PrepareCopyUnder(registry *fsys.Registry, source, destination, root fsys.Location) (PreparedCopy, error) {
	relative, err := relativeUnder(root, destination)
	if err != nil {
		return PreparedCopy{}, err
	}
	writeRoot, err := registry.WriteRoot(root)
	if err != nil {
		return PreparedCopy{}, err
	}
	prepared, err := PrepareCopy(writeRoot, source, relative)
	if err != nil {
		_ = writeRoot.Close()
		return PreparedCopy{}, err
	}
	prepared.ownsRoot = true
	return prepared, nil
}

func (fileCopier copier) prepare(root fsys.WriteRoot, source fsys.Location, relative string) (PreparedCopy, error) {
	relative = slashRelative(root.Root(), relative)
	if _, err := root.Lstat(relative); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return PreparedCopy{}, confined(err)
	}
	temporary := path.Join(path.Dir(relative), "."+path.Base(relative)+temporaryMarker+rand.Text())
	prepared := PreparedCopy{Destination: absoluteIn(root, relative), root: root, relative: relative, temporary: temporary}
	reflinkErr := confined(fileCopier.reflink(root, source, temporary))
	if reflinkErr == nil {
		prepared.Kind = Reflink
		return prepared, nil
	}
	if !isFallbackError(reflinkErr) {
		return PreparedCopy{}, reflinkErr
	}
	if err := fileCopier.copyFull(root, source, temporary); err != nil {
		return PreparedCopy{}, err
	}
	prepared.Kind = Copy
	return prepared, nil
}

// copyFull vérifie la place avant d'écrire ; CopyFrom borne la copie à la taille de la source et retire une copie
// incomplète.
func (fileCopier copier) copyFull(root fsys.WriteRoot, source fsys.Location, temporary string) error {
	size, err := sourceSize(root, source)
	if err != nil {
		return err
	}
	directory := fsys.Location{Volume: root.Root().Volume, Path: absoluteIn(root, path.Dir(temporary))}
	if err := fileCopier.checkSpace(directory, size); err != nil {
		return err
	}
	_, err = root.CopyFrom(context.Background(), source, temporary)
	return confined(err)
}

// sourceSize lit la taille de la source par la vue du volume ; sans elle, la place nécessaire est inconnue.
func sourceSize(root fsys.WriteRoot, source fsys.Location) (int64, error) {
	view := readViewOf(root)
	if view == nil {
		return 0, fmt.Errorf("%w: source size unknown on this volume", ErrFreeSpaceUnknown)
	}
	info, err := view.Stat(source.Path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func (fileCopier copier) checkSpace(directory fsys.Location, size int64) error {
	available, err := fileCopier.freeBytes(directory)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrFreeSpaceUnknown, err)
	}
	required := uint64(size) + copyMarginBytes + uint64(size)/copyMarginDivisor // #nosec G115 -- taille d'un fichier ouvert, positive
	if available < required {
		return fmt.Errorf("%w: %d bytes free, %d needed", ErrInsufficientSpace, available, required)
	}
	return nil
}

// Commit remplace la destination par la copie (dans le même dossier) : l'ancien lien disparaît, jamais le fichier
// qu'il désignait. Après un échec, Discard retire encore la copie.
func (prepared PreparedCopy) Commit() error {
	if err := prepared.root.Replace(prepared.temporary, prepared.relative); err != nil {
		return err
	}
	return prepared.closeOwnedRoot()
}

// Discard retire la copie préparée sans toucher à la destination.
func (prepared PreparedCopy) Discard() error {
	return errors.Join(removeIfPresent(prepared.root, prepared.temporary), prepared.closeOwnedRoot())
}

func (prepared PreparedCopy) closeOwnedRoot() error {
	if !prepared.ownsRoot {
		return nil
	}
	return prepared.root.Close()
}
