package repair

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"necromanciarr/internal/dedupe"
	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/local"
	"necromanciarr/internal/linker"
	"necromanciarr/internal/store"
)

// recordTimeout borne l'enregistrement d'un lien déjà remplacé, fait même après une annulation.
const recordTimeout = 30 * time.Second

// Erreurs typées d'Apply.
var (
	// ErrChangedSinceScan : le lien n'est plus le lien symbolique vu par Scan.
	ErrChangedSinceScan = errors.New("repair: link changed since scan")
	// ErrOutsideRoots : le lien n'est sous aucune des racines autorisées.
	ErrOutsideRoots = errors.New("repair: link outside the allowed roots")
	// ErrTargetInvalid : le nouveau fichier manque, n'est pas un fichier ordinaire ou n'a plus la taille attendue.
	ErrTargetInvalid = errors.New("repair: new target is not the expected regular file")
)

// LinkUpdater enregistre le résultat d'une réparation ; implémenté par store.Store. Un lien inconnu de la base (trouvé
// dans un dossier indiqué) est toléré.
type LinkUpdater interface {
	UpdateLinkTarget(ctx context.Context, linkPath, targetPath string) error
	SetLinkState(ctx context.Context, linkPath string, state store.LinkState, verifiedAt time.Time) error
}

// ApplyOptions règle Apply : Roots borne les liens modifiables, DryRun fait toutes les vérifications sans rien créer,
// Store nil désactive la mise à jour de la base. Rechecker (nil = aucun) fait recontrôler par le client le torrent
// du lien remplacé, sous la garde pause-recontrôle-reprise : un torrent incomplet reste en pause.
type ApplyOptions struct {
	Roots     []string
	DryRun    bool
	Store     LinkUpdater
	Rechecker dedupe.Rechecker
	// Registry ouvre les racines d'écriture des liens ; nil : registre du disque local dont la bibliothèque est la
	// nouvelle cible de chaque réparation.
	Registry *fsys.Registry

	renameFile func(temporaryPath, linkPath string) error
}

// Outcome est le sort d'une Repair : LinkTarget est le texte écrit (ou qui le serait en DryRun), Done dit que le lien a
// été remplacé. Un échec de la base est dans Err alors que Done reste vrai ; l'échec du recontrôle est dans RecheckErr
// et ne rend pas Apply fautif.
type Outcome struct {
	Repair     Repair
	LinkTarget string
	Done       bool
	DryRun     bool
	Err        error
	RecheckErr error
}

// Result donne un Outcome par Repair, dans l'ordre reçu.
type Result struct {
	Outcomes []Outcome
}

// Apply remplace chaque lien cassé par un lien vers sa nouvelle cible : racine autorisée, lien toujours symbolique et
// toujours dirigé vers OldTarget, nouveau fichier ordinaire de la taille attendue. Le lien est créé sous un nom
// temporaire voisin puis renommé par-dessus l'ancien (atomique) ; en cas d'échec seul ce nom temporaire est retiré. Le
// fichier de la bibliothèque n'est jamais ouvert en écriture. L'erreur renvoyée réunit les échecs des Outcomes.
func Apply(ctx context.Context, repairs []Repair, options ApplyOptions) (Result, error) {
	if options.renameFile == nil {
		options.renameFile = os.Rename
	}
	roots, err := resolveRoots(options.Roots)
	if err != nil {
		return Result{}, err
	}
	result := Result{Outcomes: make([]Outcome, 0, len(repairs))}
	var failures []error
	for _, repair := range repairs {
		if err := ctx.Err(); err != nil {
			return result, errors.Join(append(failures, err)...)
		}
		outcome := applyOne(ctx, repair, options, roots)
		if outcome.Err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", repair.LinkPath, outcome.Err))
		}
		result.Outcomes = append(result.Outcomes, outcome)
	}
	return result, errors.Join(failures...)
}

func resolveRoots(roots []string) ([]string, error) {
	resolved := make([]string, 0, len(roots))
	for _, root := range roots {
		absoluteRoot, err := filepath.Abs(root)
		if err != nil {
			return nil, err
		}
		realRoot, err := filepath.EvalSymlinks(absoluteRoot)
		if err != nil {
			return nil, err
		}
		resolved = append(resolved, realRoot)
	}
	return resolved, nil
}

func applyOne(ctx context.Context, repair Repair, options ApplyOptions, roots []string) Outcome {
	outcome := Outcome{Repair: repair, DryRun: options.DryRun}
	root, err := rootOfLink(roots, repair.LinkPath)
	if err == nil {
		err = checkLinkUnchanged(repair)
	}
	if err == nil {
		err = checkNewTarget(repair)
	}
	if err == nil {
		outcome.LinkTarget, err = linkText(repair)
	}
	if err != nil || options.DryRun {
		outcome.Err = err
		return outcome
	}
	if err := replaceLink(repair, options, root, outcome.LinkTarget); err != nil {
		outcome.Err = err
		return outcome
	}
	outcome.Done = true
	// Le lien est remplacé : l'enregistrement doit suivre même si l'appelant annule.
	recordCtx, cancelRecord := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer cancelRecord()
	outcome.Err = recordRepair(recordCtx, repair, options.Store)
	if options.Rechecker != nil {
		outcome.RecheckErr = options.Rechecker.RecheckPath(ctx, repair.LinkPath)
	}
	return outcome
}

// rootOfLink résout le dossier du lien (pas le lien lui-même) avant de le comparer aux racines.
func rootOfLink(roots []string, linkPath string) (string, error) {
	absolutePath, err := filepath.Abs(linkPath)
	if err != nil {
		return "", err
	}
	realDirectory, err := filepath.EvalSymlinks(filepath.Dir(absolutePath))
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrChangedSinceScan, err)
	}
	realPath := filepath.Join(realDirectory, filepath.Base(absolutePath))
	for _, root := range roots {
		relative, err := filepath.Rel(root, realPath)
		if err == nil && filepath.IsLocal(relative) {
			return root, nil
		}
	}
	return "", fmt.Errorf("%w: %s", ErrOutsideRoots, linkPath)
}

func checkLinkUnchanged(repair Repair) error {
	info, err := os.Lstat(repair.LinkPath)
	if err != nil || info.Mode()&fs.ModeSymlink == 0 {
		return fmt.Errorf("%w: %s is no longer a symbolic link", ErrChangedSinceScan, repair.LinkPath)
	}
	currentTarget, err := os.Readlink(repair.LinkPath)
	if err != nil || currentTarget != repair.OldTarget {
		return fmt.Errorf("%w: %s now points elsewhere", ErrChangedSinceScan, repair.LinkPath)
	}
	// Un ancien chemin revenu (ou un état inconnu) ne justifie plus de remplacer le lien.
	if _, err := os.Stat(repair.LinkPath); !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s is no longer broken", ErrChangedSinceScan, repair.LinkPath)
	}
	return nil
}

func checkNewTarget(repair Repair) error {
	info, err := os.Stat(repair.NewTarget)
	if err != nil || !info.Mode().IsRegular() || info.Size() != repair.Size {
		return fmt.Errorf("%w: %s", ErrTargetInvalid, repair.NewTarget)
	}
	return nil
}

// linkText garde la forme du lien d'origine : relatif reste relatif, absolu reste absolu.
func linkText(repair Repair) (string, error) {
	if repair.Relative {
		return linker.RelativeSymlinkTarget(repair.LinkPath, repair.NewTarget)
	}
	return filepath.Abs(repair.NewTarget)
}

func replaceLink(repair Repair, options ApplyOptions, root, text string) error {
	temporaryPath := repair.LinkPath + dedupe.TemporaryMarker + rand.Text()
	registry, err := linkRegistry(options.Registry, repair.NewTarget)
	if err != nil {
		return err
	}
	if err := linker.CreateLinkUnder(registry, linker.Symlink, text, fsys.Location{Path: temporaryPath}, fsys.Location{Path: root}); err != nil {
		return err
	}
	err = checkNewTarget(repair)
	if err == nil {
		// Relu juste avant le renommage : un lien remplacé entre-temps par quelqu'un d'autre ne doit pas être écrasé.
		err = checkLinkUnchanged(repair)
	}
	if err == nil {
		err = checkTemporaryResolves(temporaryPath, repair.Size)
	}
	if err == nil {
		err = options.renameFile(temporaryPath, repair.LinkPath)
	}
	if err != nil {
		return errors.Join(err, removeTemporary(temporaryPath))
	}
	return nil
}

// linkRegistry rend le registre des racines d'écriture ; sans lui, la nouvelle cible tient lieu de bibliothèque :
// une racine qui la contient est refusée.
func linkRegistry(registry *fsys.Registry, newTarget string) (*fsys.Registry, error) {
	if registry != nil {
		return registry, nil
	}
	return local.NewRegistry(newTarget)
}

func checkTemporaryResolves(temporaryPath string, size int64) error {
	info, err := os.Stat(temporaryPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		return fmt.Errorf("%w: %s", ErrTargetInvalid, temporaryPath)
	}
	return nil
}

// removeTemporary ne retire qu'un nom créé par cet appel : os.Remove ôte le lien, jamais le fichier visé.
func removeTemporary(temporaryPath string) error {
	err := os.Remove(temporaryPath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("repair: remove temporary link: %w", err)
	}
	return nil
}

func recordRepair(ctx context.Context, repair Repair, recorder LinkUpdater) error {
	if recorder == nil {
		return nil
	}
	err := recorder.UpdateLinkTarget(ctx, repair.LinkPath, repair.NewTarget)
	if err == nil {
		err = recorder.SetLinkState(ctx, repair.LinkPath, store.LinkOK, time.Now().UTC())
	}
	if errors.Is(err, store.ErrLinkNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("repair: link replaced but not recorded: %w", err)
	}
	return nil
}
