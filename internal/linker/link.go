package linker

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Kind est une sorte de lien ; la valeur zéro n'en désigne aucune.
type Kind int

// Sortes de liens : Hardlink et Reflink exigent le même système de fichiers, Symlink vise un chemin absolu.
const (
	Hardlink Kind = iota + 1
	Reflink
	Symlink
	// Copy est une copie complète et indépendante, posée seulement par PrepareCopy (jamais par Apply).
	Copy
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
	case Copy:
		return "copy"
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
	// ErrDestinationEscapesRoot signale une destination hors de Op.Root, directement ou par un dossier symbolique.
	ErrDestinationEscapesRoot = errors.New("linker: destination escapes the link root")
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
	rename  func(oldPath, newPath string) error
}

// Apply crée les liens des ops en essayant les sortes dans l'ordre de preferred, sans jamais en utiliser une autre.
// Une destination déjà liée à la même source compte comme faite ; toute autre destination existante est refusée.
// En cas d'échec, seuls les liens et dossiers créés par cet appel sont retirés, en ordre inverse.
func Apply(ops []Op, preferred []Kind) (Result, error) {
	return systemApplier().apply(ops, preferred)
}

// CreateLinkInRoot crée un seul lien de sorte kind à destination, sous root, avec le même confinement qu'Apply :
// destination sous root, aucun composant symbolique sous root, jamais d'entrée existante remplacée. Pour un Symlink,
// source est le texte de la cible (relatif accepté) ; les dossiers créés sont retirés en cas d'échec.
func CreateLinkInRoot(kind Kind, source, destination, root string) error {
	return systemApplier().createInRoot(kind, source, destination, root)
}

func (linkApplier applier) createInRoot(kind Kind, source, destination, root string) error {
	linker, known := linkApplier.linkers[kind]
	if !known {
		return fmt.Errorf("%w: %v", ErrUnsupported, kind)
	}
	run := &transaction{resolvedRoots: map[string]string{}}
	resolvedRoot, relative, err := run.confine(Op{Destination: destination, Root: root})
	if err != nil {
		return err
	}
	confinedDestination := filepath.Join(resolvedRoot, relative)
	err = run.ensureConfinedDir(resolvedRoot, filepath.Dir(relative))
	if err == nil {
		err = checkParentInRoot(resolvedRoot, confinedDestination)
	}
	if err == nil {
		err = linker(source, confinedDestination)
	}
	if err != nil {
		return errors.Join(err, run.rollback())
	}
	return nil
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
	run := &transaction{resolvedRoots: map[string]string{}}
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
	resolvedRoot, relative, err := run.confine(operation)
	if err != nil {
		return 0, err
	}
	destination := filepath.Join(resolvedRoot, relative)
	if err := run.ensureConfinedDir(resolvedRoot, filepath.Dir(relative)); err != nil {
		return 0, err
	}
	existingKind, alreadyLinked, err := existingLink(destination, source, sourceInfo, preferred)
	if err != nil || alreadyLinked {
		return existingKind, err
	}
	// Re-vérifié juste avant le lien : un dossier remplacé par un symlink depuis la création serait suivi par os.Link.
	if err := checkParentInRoot(resolvedRoot, destination); err != nil {
		return 0, err
	}
	return linkApplier.link(run, source, destination, preferred)
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

// transaction mémorise, dans l'ordre de création, les liens et dossiers créés par un appel d'Apply, et les racines
// déjà résolues.
type transaction struct {
	created       []string
	resolvedRoots map[string]string
}

// confine renvoie la racine résolue (une racine elle-même symbolique est légitime) et le chemin relatif de la
// destination sous cette racine.
func (run *transaction) confine(operation Op) (string, string, error) {
	resolvedRoot, err := run.resolveRoot(operation.Root)
	if err != nil {
		return "", "", err
	}
	absoluteRoot, rootErr := filepath.Abs(operation.Root)
	absoluteDestination, destinationErr := filepath.Abs(operation.Destination)
	if rootErr != nil || destinationErr != nil {
		return "", "", fmt.Errorf("%w: %s", ErrDestinationEscapesRoot, operation.Destination)
	}
	relative, err := filepath.Rel(absoluteRoot, absoluteDestination)
	if err != nil || relative == "." || !filepath.IsLocal(relative) {
		return "", "", fmt.Errorf("%w: %s", ErrDestinationEscapesRoot, operation.Destination)
	}
	return resolvedRoot, relative, nil
}

func (run *transaction) resolveRoot(root string) (string, error) {
	if resolvedRoot, found := run.resolvedRoots[root]; found {
		return resolvedRoot, nil
	}
	if root == "" {
		return "", fmt.Errorf("%w: empty root", ErrDestinationEscapesRoot)
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	resolvedRoot, err := filepath.EvalSymlinks(absoluteRoot)
	if err != nil {
		return "", fmt.Errorf("linker: link root %s: %w", root, err)
	}
	run.resolvedRoots[root] = resolvedRoot
	return resolvedRoot, nil
}

// ensureConfinedDir crée les dossiers manquants sous la racine en refusant tout composant symbolique, qui pourrait
// mener hors de la racine.
func (run *transaction) ensureConfinedDir(resolvedRoot, relativeDir string) error {
	if relativeDir == "." {
		return nil
	}
	current := resolvedRoot
	for _, segment := range strings.Split(relativeDir, string(filepath.Separator)) {
		current = filepath.Join(current, segment)
		if err := run.ensureConfinedSegment(current); err != nil {
			return err
		}
	}
	return nil
}

func (run *transaction) ensureConfinedSegment(directory string) error {
	info, err := os.Lstat(directory)
	if errors.Is(err, fs.ErrNotExist) {
		mkdirErr := os.Mkdir(directory, dirMode) // #nosec G301 -- le client torrent doit pouvoir lire l'arborescence
		if mkdirErr == nil {
			run.created = append(run.created, directory)
			return nil
		}
		if !errors.Is(mkdirErr, fs.ErrExist) {
			return mkdirErr
		}
		info, err = os.Lstat(directory)
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

// checkParentInRoot ne ferme pas la course entre ce contrôle et la création du lien ; os.Root ne peut pas servir ici
// car la source du lien est hors de la racine.
func checkParentInRoot(resolvedRoot, destination string) error {
	resolvedParent, err := filepath.EvalSymlinks(filepath.Dir(destination))
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(resolvedRoot, resolvedParent)
	if err != nil || !filepath.IsLocal(relative) {
		return fmt.Errorf("%w: %s", ErrDestinationEscapesRoot, destination)
	}
	return nil
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
