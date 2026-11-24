package linker

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
// par un renommage atomique, Discard la retire. Kind vaut Reflink ou Copy.
type PreparedCopy struct {
	Kind        Kind
	Destination string
	temporary   string
}

type copier struct {
	reflink   func(source, destination string) error
	freeBytes func(directory string) (uint64, error)
}

func systemCopier() copier {
	return copier{reflink: reflink, freeBytes: freeBytes}
}

// PrepareCopy écrit, sous root et dans le dossier de destination, une copie de source qui ne partage aucun bloc
// modifiable avec elle : reflink si le système de fichiers le permet (copie à l'écriture), sinon copie complète si la
// place libre suffit. Rien n'est remplacé avant Commit.
func PrepareCopy(source, destination, root string) (PreparedCopy, error) {
	return systemCopier().prepare(source, destination, root)
}

func (fileCopier copier) prepare(source, destination, root string) (PreparedCopy, error) {
	run := &transaction{resolvedRoots: map[string]string{}}
	resolvedRoot, relative, err := run.confine(Op{Destination: destination, Root: root})
	if err != nil {
		return PreparedCopy{}, err
	}
	confined := filepath.Join(resolvedRoot, relative)
	if err := checkParentInRoot(resolvedRoot, confined); err != nil {
		return PreparedCopy{}, err
	}
	temporary := filepath.Join(filepath.Dir(confined), "."+filepath.Base(confined)+temporaryMarker+rand.Text())
	reflinkErr := fileCopier.reflink(source, temporary)
	if reflinkErr == nil {
		return PreparedCopy{Kind: Reflink, Destination: confined, temporary: temporary}, nil
	}
	if !isFallbackError(reflinkErr) {
		return PreparedCopy{}, reflinkErr
	}
	if err := fileCopier.copyFull(source, temporary); err != nil {
		return PreparedCopy{}, err
	}
	return PreparedCopy{Kind: Copy, Destination: confined, temporary: temporary}, nil
}

func (fileCopier copier) copyFull(source, temporary string) error {
	sourceFile, err := os.Open(source) // #nosec G304 -- source prouvée par la vérification
	if err != nil {
		return err
	}
	defer func() { _ = sourceFile.Close() }()
	info, err := sourceFile.Stat()
	if err != nil {
		return err
	}
	if err := fileCopier.checkSpace(filepath.Dir(temporary), info.Size()); err != nil {
		return err
	}
	destinationFile, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, verifiedFileMode) // #nosec G302 G304 -- nom aléatoire confiné sous la racine des liens
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(destinationFile, sourceFile)
	err = errors.Join(copyErr, destinationFile.Sync(), destinationFile.Close())
	if err != nil {
		return errors.Join(err, removeIfPresent(temporary))
	}
	return nil
}

func (fileCopier copier) checkSpace(directory string, size int64) error {
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

// Commit remplace la destination par la copie (renommage dans le même dossier) : l'ancien lien disparaît, jamais le
// fichier qu'il désignait.
func (prepared PreparedCopy) Commit() error {
	return os.Rename(prepared.temporary, prepared.Destination)
}

// Discard retire la copie préparée sans toucher à la destination.
func (prepared PreparedCopy) Discard() error {
	return removeIfPresent(prepared.temporary)
}
