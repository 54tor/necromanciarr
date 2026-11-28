package repair

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"necromanciarr/internal/dedupe"
	"necromanciarr/internal/fsys"
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
	// Registry lit les liens et ouvre leurs racines d'écriture ; nil : lecture du seul disque local, toute écriture
	// refusée (fsys.ErrNoLibraryRoots).
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
// toujours dirigé vers OldTarget, nouveau fichier ordinaire de la taille attendue. Toute écriture passe par la racine
// d'écriture de la racine du lien, ouverte par le registre : le lien est créé sous un nom temporaire voisin puis
// remplace l'ancien par WriteRoot.Replace ; sans remplacement atomique annoncé par le volume, rien n'est écrit
// (fsys.ErrNoAtomicReplace). En cas d'échec seul ce nom temporaire est retiré. Le fichier de la bibliothèque n'est
// jamais ouvert en écriture. L'erreur renvoyée réunit les échecs des Outcomes.
func Apply(ctx context.Context, repairs []Repair, options ApplyOptions) (Result, error) {
	files := dedupe.NewFiles(options.Registry)
	roots, err := resolveRoots(files, options.Roots)
	if err != nil {
		return Result{}, err
	}
	result := Result{Outcomes: make([]Outcome, 0, len(repairs))}
	var failures []error
	for _, repair := range repairs {
		if err := ctx.Err(); err != nil {
			return result, errors.Join(append(failures, err)...)
		}
		outcome := applyOne(ctx, files, repair, options, roots)
		if outcome.Err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", repair.LinkPath, outcome.Err))
		}
		result.Outcomes = append(result.Outcomes, outcome)
	}
	return result, errors.Join(failures...)
}

func resolveRoots(files dedupe.Files, roots []string) ([]string, error) {
	resolved := make([]string, 0, len(roots))
	for _, root := range roots {
		realRoot, err := files.Canonical(root)
		if err != nil {
			return nil, err
		}
		resolved = append(resolved, realRoot)
	}
	return resolved, nil
}

func applyOne(ctx context.Context, files dedupe.Files, repair Repair, options ApplyOptions, roots []string) Outcome {
	outcome := Outcome{Repair: repair, DryRun: options.DryRun}
	root, realLinkPath, err := rootOfLink(files, roots, repair.LinkPath)
	if err == nil {
		err = checkLinkUnchanged(files, repair)
	}
	if err == nil {
		err = checkNewTarget(files, repair)
	}
	if err == nil {
		outcome.LinkTarget, err = files.SymlinkText(repair.LinkPath, repair.NewTarget, repair.Relative)
	}
	if err != nil || options.DryRun {
		outcome.Err = err
		return outcome
	}
	if err := replaceLink(ctx, files, repair, options, root, realLinkPath, outcome.LinkTarget); err != nil {
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

// rootOfLink résout le dossier du lien (pas le lien lui-même) avant de le comparer aux racines ; rend aussi le
// chemin canonique du lien, seul comparable à une racine canonique (noms courts 8.3 de Windows, liens).
func rootOfLink(files dedupe.Files, roots []string, linkPath string) (string, string, error) {
	realDirectory, err := files.Canonical(dedupe.Dir(linkPath))
	if err != nil {
		return "", "", fmt.Errorf("%w: %w", ErrChangedSinceScan, err)
	}
	realPath := dedupe.Join(realDirectory, dedupe.Base(linkPath))
	for _, root := range roots {
		if _, err := dedupe.Below(root, realPath); err == nil {
			return root, realPath, nil
		}
	}
	return "", "", fmt.Errorf("%w: %s", ErrOutsideRoots, linkPath)
}

func checkLinkUnchanged(files dedupe.Files, repair Repair) error {
	info, err := files.Lstat(repair.LinkPath)
	if err != nil || info.Mode()&fs.ModeSymlink == 0 {
		return fmt.Errorf("%w: %s is no longer a symbolic link", ErrChangedSinceScan, repair.LinkPath)
	}
	currentTarget, err := files.Readlink(repair.LinkPath)
	if err != nil || currentTarget != repair.OldTarget {
		return fmt.Errorf("%w: %s now points elsewhere", ErrChangedSinceScan, repair.LinkPath)
	}
	// Un ancien chemin revenu (ou un état inconnu) ne justifie plus de remplacer le lien.
	if _, err := files.Stat(repair.LinkPath); !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s is no longer broken", ErrChangedSinceScan, repair.LinkPath)
	}
	return nil
}

func checkNewTarget(files dedupe.Files, repair Repair) error {
	info, err := files.Stat(repair.NewTarget)
	if err != nil || !info.Mode().IsRegular() || info.Size() != repair.Size {
		return fmt.Errorf("%w: %s", ErrTargetInvalid, repair.NewTarget)
	}
	return nil
}

// replaceLink n'écrit que dans la racine d'écriture de root, ouverte par le registre : lien temporaire voisin,
// contrôles, puis WriteRoot.Replace ; sans remplacement atomique annoncé, rien n'est créé.
func replaceLink(ctx context.Context, files dedupe.Files, repair Repair, options ApplyOptions, root, realLinkPath, text string) error {
	writeRoot, err := files.WriteRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = writeRoot.Close() }()
	if err := requireAtomicReplace(ctx, files, root); err != nil {
		return err
	}
	temporarySuffix := dedupe.TemporaryMarker + rand.Text()
	temporaryPath := repair.LinkPath + temporarySuffix
	temporaryRelative, err := dedupe.Below(root, realLinkPath+temporarySuffix)
	if err != nil {
		return err
	}
	linkRelative, err := dedupe.Below(root, realLinkPath)
	if err != nil {
		return err
	}
	if err := linker.CreateLinkInRoot(writeRoot, linker.Symlink, text, temporaryRelative); err != nil {
		return err
	}
	rename := options.renameFile
	if rename == nil {
		rename = func(string, string) error { return writeRoot.Replace(temporaryRelative, linkRelative) }
	}
	err = checkNewTarget(files, repair)
	if err == nil {
		// Relu juste avant le renommage : un lien remplacé entre-temps par quelqu'un d'autre ne doit pas être écrasé.
		err = checkLinkUnchanged(files, repair)
	}
	if err == nil {
		err = checkTemporaryResolves(files, temporaryPath, repair.Size)
	}
	if err == nil {
		err = rename(temporaryPath, repair.LinkPath)
	}
	if err != nil {
		return errors.Join(err, removeTemporary(writeRoot, temporaryRelative))
	}
	return nil
}

// requireAtomicReplace : jamais de repli sur un renommage non atomique ou une suppression puis création.
func requireAtomicReplace(ctx context.Context, files dedupe.Files, root string) error {
	capabilities, err := files.Capabilities(ctx, root)
	if err != nil {
		return err
	}
	if !capabilities.AtomicReplace {
		return fsys.ErrNoAtomicReplace
	}
	return nil
}

func checkTemporaryResolves(files dedupe.Files, temporaryPath string, size int64) error {
	info, err := files.Stat(temporaryPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		return fmt.Errorf("%w: %s", ErrTargetInvalid, temporaryPath)
	}
	return nil
}

// removeTemporary ne retire qu'un nom créé par cet appel : Remove ôte le lien, jamais le fichier visé.
func removeTemporary(writeRoot fsys.WriteRoot, temporaryRelative string) error {
	err := writeRoot.Remove(temporaryRelative)
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
