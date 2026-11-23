package linker

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

// Kind est une sorte de lien ; la valeur zéro n'en désigne aucune.
type Kind int

// Sortes de liens : Hardlink et Reflink exigent le même système de fichiers, Symlink vise un chemin absolu.
const (
	Hardlink Kind = iota + 1
	Reflink
	Symlink
)

// String nomme la sorte de lien.
func (kind Kind) String() string {
	switch kind {
	case Hardlink:
		return "hardlink"
	case Reflink:
		return "reflink"
	case Symlink:
		return "symlink"
	default:
		return fmt.Sprintf("kind(%d)", int(kind))
	}
}

// Erreurs typées d'Apply. ErrCrossDevice, ErrLinkRefused et ErrUnsupported déclenchent l'essai de la sorte suivante
// demandée ; les autres arrêtent l'appel.
var (
	ErrCrossDevice       = errors.New("linker: link across filesystems")
	ErrLinkRefused       = errors.New("linker: link refused by the system")
	ErrUnsupported       = errors.New("linker: link kind unsupported")
	ErrDestinationExists = errors.New("linker: destination already exists")
	ErrSizeMismatch      = errors.New("linker: source size differs from torrent file size")
	ErrInvalidSource     = errors.New("linker: source is not an existing regular file")
	ErrNoKind            = errors.New("linker: no link kind requested")
)

// dirMode : les dossiers de liens doivent rester lisibles par le client torrent, qui tourne souvent sous un autre compte.
const dirMode fs.FileMode = 0o755

// Result donne, pour chaque Op dans l'ordre, la sorte de lien effectivement utilisée.
type Result struct {
	Used []Kind
}

type linkFunc func(source, destination string) error

type applier struct {
	linkers map[Kind]linkFunc
}

// Apply crée les liens des ops en essayant les sortes dans l'ordre de preferred, sans jamais en utiliser une autre.
// Une destination déjà liée à la même source compte comme faite ; toute autre destination existante est refusée.
// En cas d'échec, seuls les liens et dossiers créés par cet appel sont retirés, en ordre inverse.
func Apply(ops []Op, preferred []Kind) (Result, error) {
	systemApplier := applier{linkers: map[Kind]linkFunc{Hardlink: hardlink, Reflink: reflink, Symlink: symlink}}
	return systemApplier.apply(ops, preferred)
}

func hardlink(source, destination string) error {
	return classifyLinkError(os.Link(source, destination))
}

func symlink(source, destination string) error {
	return classifyLinkError(os.Symlink(source, destination))
}

func (linkApplier applier) apply(ops []Op, preferred []Kind) (Result, error) {
	if len(preferred) == 0 {
		return Result{}, ErrNoKind
	}
	run := &transaction{}
	result := Result{Used: make([]Kind, 0, len(ops))}
	for _, operation := range ops {
		usedKind, err := linkApplier.applyOne(run, operation, preferred)
		if err != nil {
			return Result{}, errors.Join(err, run.rollback())
		}
		result.Used = append(result.Used, usedKind)
	}
	return result, nil
}

func (linkApplier applier) applyOne(run *transaction, operation Op, preferred []Kind) (Kind, error) {
	source, sourceInfo, err := checkSource(operation)
	if err != nil {
		return 0, err
	}
	existingKind, alreadyLinked, err := existingLink(operation.Destination, source, sourceInfo, preferred)
	if err != nil || alreadyLinked {
		return existingKind, err
	}
	if err := run.ensureDir(filepath.Dir(operation.Destination)); err != nil {
		return 0, err
	}
	return linkApplier.link(run, source, operation.Destination, preferred)
}

func (linkApplier applier) link(run *transaction, source, destination string, preferred []Kind) (Kind, error) {
	var refusals []error
	for _, kind := range preferred {
		linker, known := linkApplier.linkers[kind]
		if !known {
			refusals = append(refusals, fmt.Errorf("%w: %v", ErrUnsupported, kind))
			continue
		}
		err := linker(source, destination)
		if err == nil {
			run.created = append(run.created, destination)
			return kind, nil
		}
		refusals = append(refusals, fmt.Errorf("%v %s: %w", kind, destination, err))
		if !isFallbackError(err) {
			break
		}
	}
	return 0, errors.Join(refusals...)
}

func isFallbackError(err error) bool {
	return errors.Is(err, ErrCrossDevice) || errors.Is(err, ErrUnsupported) || errors.Is(err, ErrLinkRefused)
}

// checkSource résout les liens symboliques de la source : un lien dur vers un symlink relatif serait cassé.
func checkSource(operation Op) (string, fs.FileInfo, error) {
	absoluteSource, err := filepath.Abs(operation.Source)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %s: %w", ErrInvalidSource, operation.Source, err)
	}
	resolvedSource, err := filepath.EvalSymlinks(absoluteSource)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %s: %w", ErrInvalidSource, operation.Source, err)
	}
	sourceInfo, err := os.Stat(resolvedSource)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %s: %w", ErrInvalidSource, operation.Source, err)
	}
	if !sourceInfo.Mode().IsRegular() {
		return "", nil, fmt.Errorf("%w: %s", ErrInvalidSource, operation.Source)
	}
	// Un fichier local plus grand pourrait être tronqué par le client une fois lié en dur.
	if sourceInfo.Size() != operation.Size {
		return "", nil, fmt.Errorf("%w: %s is %d bytes, torrent expects %d", ErrSizeMismatch, operation.Source,
			sourceInfo.Size(), operation.Size)
	}
	return resolvedSource, sourceInfo, nil
}

// existingLink reconnaît un lien déjà posé vers la source, d'une sorte demandée ; un reflink déjà là ne se distingue
// pas d'une copie et reste donc refusé.
func existingLink(destination, source string, sourceInfo fs.FileInfo, preferred []Kind) (Kind, bool, error) {
	destinationInfo, err := os.Lstat(destination)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	isSymlink := destinationInfo.Mode()&fs.ModeSymlink != 0
	if isSymlink && slices.Contains(preferred, Symlink) {
		target, readErr := os.Readlink(destination)
		if readErr == nil && target == source {
			return Symlink, true, nil
		}
	}
	if !isSymlink && slices.Contains(preferred, Hardlink) && os.SameFile(sourceInfo, destinationInfo) {
		return Hardlink, true, nil
	}
	return 0, false, fmt.Errorf("%w: %s", ErrDestinationExists, destination)
}

// transaction mémorise, dans l'ordre de création, les liens et dossiers créés par un appel d'Apply.
type transaction struct {
	created []string
}

func (run *transaction) ensureDir(directory string) error {
	missing, err := missingAncestors(directory)
	if err != nil {
		return err
	}
	for index := len(missing) - 1; index >= 0; index-- {
		err := os.Mkdir(missing[index], dirMode) // #nosec G301 -- le client torrent doit pouvoir lire l'arborescence
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return err
		}
		run.created = append(run.created, missing[index])
	}
	return nil
}

func missingAncestors(directory string) ([]string, error) {
	var missing []string
	current := directory
	for {
		info, err := os.Stat(current)
		if err == nil {
			if !info.IsDir() {
				return nil, fmt.Errorf("linker: %s is not a directory", current)
			}
			return missing, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		missing = append(missing, current)
		parent := filepath.Dir(current)
		if parent == current {
			return missing, nil
		}
		current = parent
	}
}

// rollback retire en ordre inverse ce que l'appel a créé ; os.Remove ne supprime qu'un nom de lien ou un dossier
// vide, jamais le contenu d'un fichier de la bibliothèque.
func (run *transaction) rollback() error {
	var failures []error
	for index := len(run.created) - 1; index >= 0; index-- {
		err := os.Remove(run.created[index])
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			failures = append(failures, fmt.Errorf("linker: rollback: %w", err))
		}
	}
	return errors.Join(failures...)
}
