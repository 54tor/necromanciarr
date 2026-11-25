package linker

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/local"
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
	ErrCrossDevice       = local.ErrCrossDevice
	ErrLinkRefused       = local.ErrLinkRefused
	ErrUnsupported       = local.ErrUnsupported
	ErrDestinationExists = local.ErrDestinationExists
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

// linkFunc crée relative dans root ; pour un Symlink, source.Path est le texte de la cible.
type linkFunc func(root fsys.WriteRoot, source fsys.Location, relative string) error

type applier struct {
	linkers map[Kind]linkFunc
	rename  func(root fsys.WriteRoot, oldRelative, newRelative string) error
}

func systemApplier() applier {
	return applier{linkers: map[Kind]linkFunc{Hardlink: hardlink, Reflink: reflink, Symlink: symlink}}
}

func hardlink(root fsys.WriteRoot, source fsys.Location, relative string) error {
	return root.Hardlink(source, relative)
}

func symlink(root fsys.WriteRoot, source fsys.Location, relative string) error {
	return root.Symlink(source.Path, relative)
}

// reflink n'emploie que le clonage du volume (fsys.Reflinker) : CopyFrom ferait une copie complète en silence.
func reflink(root fsys.WriteRoot, source fsys.Location, relative string) error {
	reflinker, supported := root.(fsys.Reflinker)
	if !supported {
		return fmt.Errorf("%w: reflink on this volume", ErrUnsupported)
	}
	err := reflinker.Reflink(source, relative)
	if errors.Is(err, fs.ErrExist) && !errors.Is(err, ErrDestinationExists) {
		return fmt.Errorf("%w: %w", ErrDestinationExists, err)
	}
	return err
}

// Apply crée dans root les liens des ops en essayant les sortes dans l'ordre de preferred, sans jamais en utiliser
// une autre ; les sources sont lues par source, du même volume que root. Op.Root doit désigner root (son chemin
// canonique), Op.Destination être dessous. Une destination déjà liée à la même source compte comme faite ; toute
// autre destination existante est refusée. En cas d'échec, seuls les liens et dossiers créés par cet appel sont
// retirés, en ordre inverse.
func Apply(root fsys.WriteRoot, source fsys.ReadFS, ops []Op, preferred []Kind) (Result, error) {
	return systemApplier().apply(root, source, ops, preferred)
}

// CreateLinkInRoot crée dans root un seul lien de sorte kind à relative, avec le même confinement qu'Apply : aucun
// composant symbolique, jamais d'entrée existante remplacée. Pour un Symlink, source est le texte de la cible
// (relatif accepté) ; sinon un chemin absolu du volume de root. Les dossiers créés sont retirés en cas d'échec.
func CreateLinkInRoot(root fsys.WriteRoot, kind Kind, source, relative string) error {
	return systemApplier().createInRoot(root, kind, source, relative)
}

func (linkApplier applier) createInRoot(root fsys.WriteRoot, kind Kind, source, relative string) error {
	relative = slashRelative(root.Root(), relative)
	maker := &directoryMaker{}
	err := maker.ensure(root, parentOf(relative))
	if err == nil {
		err = linkApplier.create(root, kind, fsys.Location{Volume: root.Root().Volume, Path: source}, relative)
	}
	if err != nil {
		return errors.Join(err, rollback(root, maker.created))
	}
	return nil
}

// create pose un seul lien ; la WriteRoot revérifie chaque parent au moment même de la création.
func (linkApplier applier) create(root fsys.WriteRoot, kind Kind, source fsys.Location, relative string) error {
	linker, known := linkApplier.linkers[kind]
	if !known {
		return fmt.Errorf("%w: %v", ErrUnsupported, kind)
	}
	return confined(linker(root, source, relative))
}

func (linkApplier applier) apply(root fsys.WriteRoot, source fsys.ReadFS, ops []Op, preferred []Kind) (Result, error) {
	if len(preferred) == 0 {
		return Result{}, ErrNoKind
	}
	run := &transaction{root: root, source: source, matchedRoots: map[string]bool{}}
	result := Result{Used: make([]Kind, 0, len(ops))}
	for _, operation := range ops {
		usedKind, err := linkApplier.applyOne(run, operation, preferred)
		if err != nil {
			return Result{}, errors.Join(err, rollback(root, run.created()))
		}
		result.Used = append(result.Used, usedKind)
	}
	return result, nil
}

func (linkApplier applier) applyOne(run *transaction, operation Op, preferred []Kind) (Kind, error) {
	sourceLocation, sourceInfo, err := run.checkSource(operation)
	if err != nil {
		return 0, err
	}
	relative, err := run.confine(operation)
	if err != nil {
		return 0, err
	}
	if err := run.directories.ensure(run.root, parentOf(relative)); err != nil {
		return 0, err
	}
	existingKind, alreadyLinked, err := run.existingLink(relative, sourceLocation.Path, sourceInfo, preferred)
	if err != nil || alreadyLinked {
		return existingKind, err
	}
	return linkApplier.link(run, sourceLocation, relative, preferred)
}

func (linkApplier applier) link(run *transaction, source fsys.Location, relative string, preferred []Kind) (Kind, error) {
	destination := absoluteIn(run.root, relative)
	var refusals []error
	for _, kind := range preferred {
		err := linkApplier.create(run.root, kind, source, relative)
		if err == nil {
			run.links = append(run.links, relative)
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

// parentOf rend le dossier d'un chemin relatif en « / » (« . » à la racine).
func parentOf(relative string) string {
	return fsys.RemotePaths().Dir(relative)
}

// transaction mémorise, dans l'ordre de création, les dossiers et liens créés par un appel d'Apply, et les Op.Root
// déjà reconnus comme la racine d'écriture.
type transaction struct {
	root         fsys.WriteRoot
	source       fsys.ReadFS
	directories  directoryMaker
	links        []string
	matchedRoots map[string]bool
}

// created rend tout ce que l'appel a créé, dans l'ordre : un lien vient toujours après ses dossiers.
func (run *transaction) created() []string {
	return append(slices.Clone(run.directories.created), run.links...)
}

// checkSource résout les liens symboliques de la source : un lien dur vers un symlink relatif serait cassé.
func (run *transaction) checkSource(operation Op) (fsys.Location, fs.FileInfo, error) {
	if run.source.Volume() != run.root.Root().Volume {
		return fsys.Location{}, nil, fmt.Errorf("%w: %s is on another volume", ErrInvalidSource, operation.Source)
	}
	resolvedSource, err := run.source.Canonical(operation.Source)
	if err != nil {
		return fsys.Location{}, nil, fmt.Errorf("%w: %s: %w", ErrInvalidSource, operation.Source, err)
	}
	sourceInfo, err := run.source.Stat(resolvedSource)
	if err != nil {
		return fsys.Location{}, nil, fmt.Errorf("%w: %s: %w", ErrInvalidSource, operation.Source, err)
	}
	if !sourceInfo.Mode().IsRegular() {
		return fsys.Location{}, nil, fmt.Errorf("%w: %s", ErrInvalidSource, operation.Source)
	}
	// Un fichier local plus grand pourrait être tronqué par le client une fois lié en dur.
	if sourceInfo.Size() != operation.Size {
		return fsys.Location{}, nil, fmt.Errorf("%w: %s is %d bytes, torrent expects %d", ErrSizeMismatch, operation.Source,
			sourceInfo.Size(), operation.Size)
	}
	return fsys.Location{Volume: run.source.Volume(), Path: resolvedSource}, sourceInfo, nil
}

// confine exige qu'Op.Root désigne la racine d'écriture (une racine elle-même symbolique est légitime) et rend
// le chemin de la destination relatif à Op.Root ; la WriteRoot refuse ensuite toute sortie.
func (run *transaction) confine(operation Op) (string, error) {
	if err := run.matchRoot(operation.Root); err != nil {
		return "", err
	}
	rootLocation := fsys.Location{Volume: run.root.Root().Volume, Path: operation.Root}
	relative, err := relativeUnder(rootLocation, fsys.Location{Volume: rootLocation.Volume, Path: operation.Destination})
	if err != nil || relative == "." {
		return "", fmt.Errorf("%w: %s", ErrDestinationEscapesRoot, operation.Destination)
	}
	return relative, nil
}

func (run *transaction) matchRoot(operationRoot string) error {
	if run.matchedRoots[operationRoot] {
		return nil
	}
	if operationRoot == "" {
		return fmt.Errorf("%w: empty root", ErrDestinationEscapesRoot)
	}
	resolvedRoot, err := run.source.Canonical(operationRoot)
	if err != nil {
		return fmt.Errorf("linker: link root %s: %w", operationRoot, err)
	}
	if resolvedRoot != run.root.Root().Path {
		return fmt.Errorf("%w: %s is not the write root", ErrDestinationEscapesRoot, operationRoot)
	}
	run.matchedRoots[operationRoot] = true
	return nil
}

// existingLink reconnaît un lien déjà posé vers la source, d'une sorte demandée ; un reflink déjà là ne se distingue
// pas d'une copie et reste donc refusé.
func (run *transaction) existingLink(relative, source string, sourceInfo fs.FileInfo, preferred []Kind) (Kind, bool, error) {
	destinationInfo, err := run.root.Lstat(relative)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, confined(err)
	}
	destination := absoluteIn(run.root, relative)
	isSymlink := destinationInfo.Mode()&fs.ModeSymlink != 0
	if isSymlink && slices.Contains(preferred, Symlink) {
		target, readErr := run.source.Readlink(destination)
		if readErr == nil && target == source {
			return Symlink, true, nil
		}
	}
	if !isSymlink && slices.Contains(preferred, Hardlink) && sameFile(run.source, sourceInfo, destinationInfo) {
		return Hardlink, true, nil
	}
	return 0, false, fmt.Errorf("%w: %s", ErrDestinationExists, destination)
}
