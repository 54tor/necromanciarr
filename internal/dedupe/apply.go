package dedupe

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"necromanciarr/internal/linker"
	"necromanciarr/internal/store"
)

// temporaryMarker : le lien est d'abord créé sous « <fichier>.necromanciarr-tmp-<taille>-<aléatoire> » à côté du
// fichier torrent, puis renommé par-dessus ; la taille attendue dans le nom permet à Recover de reprendre sans base.
const (
	temporaryMarker    = ".necromanciarr-tmp-"
	temporarySeparator = "-"
)

// Erreurs typées d'Apply.
var (
	ErrChangedSincePlan    = errors.New("dedupe: file changed since plan")
	ErrNotIdentical        = errors.New("dedupe: new link does not resolve to the library file")
	ErrOutsideTorrentRoots = errors.New("dedupe: torrent path outside the torrent roots")
	ErrAmbiguous           = errors.New("dedupe: several identical library files, none chosen")
	ErrNoRecorder          = errors.New("dedupe: no link recorder")
)

// LinkRecorder enregistre chaque lien créé ; implémenté par store.Store.
type LinkRecorder interface {
	RecordLink(ctx context.Context, link store.Link) error
}

// Rechecker demande au client torrent de recontrôler le torrent qui contient path.
type Rechecker interface {
	RecheckPath(ctx context.Context, path string) error
}

// Outcome est le sort d'un Replacement : LinkTarget est la cible écrite (ou qui le serait en DryRun), Done dit que le
// lien remplace désormais le fichier torrent. Un échec du recontrôle est dans RecheckErr et ne rend pas Apply fautif.
type Outcome struct {
	Replacement Replacement
	LinkTarget  string
	Done        bool
	DryRun      bool
	Err         error
	RecheckErr  error
}

// Result donne un Outcome par Replacement, dans l'ordre reçu.
type Result struct {
	Outcomes []Outcome
}

// Apply remplace chaque fichier torrent par un lien vers le fichier de la bibliothèque, après avoir vérifié que ni
// l'un ni l'autre n'a changé depuis Plan. Le lien est créé sous un nom temporaire, contrôlé, puis renommé
// atomiquement par-dessus le fichier torrent ; en cas d'échec seul ce nom temporaire est retiré. DryRun fait toutes
// les vérifications sans rien créer. L'erreur renvoyée réunit les échecs des Outcomes.
func Apply(ctx context.Context, replacements []Replacement, options Options) (Result, error) {
	if options.Recorder == nil && !options.DryRun {
		return Result{}, ErrNoRecorder
	}
	if options.createLink == nil {
		options.createLink = linker.CreateLinkInRoot
	}
	roots, err := resolveRoots(options.TorrentRoots)
	if err != nil {
		return Result{}, err
	}
	result := Result{Outcomes: make([]Outcome, 0, len(replacements))}
	var failures []error
	for _, replacement := range replacements {
		if err := ctx.Err(); err != nil {
			return result, errors.Join(append(failures, err)...)
		}
		outcome := applyOne(ctx, replacement, options, roots)
		if outcome.Err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", replacement.TorrentPath, outcome.Err))
		}
		result.Outcomes = append(result.Outcomes, outcome)
	}
	return result, errors.Join(failures...)
}

func applyOne(ctx context.Context, replacement Replacement, options Options, roots []string) Outcome {
	outcome := Outcome{Replacement: replacement, DryRun: options.DryRun}
	root, libraryInfo, err := checkApplicable(replacement, roots)
	if err != nil {
		outcome.Err = err
		return outcome
	}
	target, relative, err := linkTarget(replacement, options.RelativeSymlinks)
	outcome.LinkTarget = target
	if err != nil || options.DryRun {
		outcome.Err = err
		return outcome
	}
	if err := replaceWithLink(replacement, options, root, target, libraryInfo); err != nil {
		outcome.Err = err
		return outcome
	}
	outcome.Done = true
	if err := options.Recorder.RecordLink(ctx, linkRecord(replacement, relative)); err != nil {
		outcome.Err = fmt.Errorf("dedupe: link created but not recorded: %w", err)
	}
	if options.RecheckAfter && options.Rechecker != nil {
		outcome.RecheckErr = options.Rechecker.RecheckPath(ctx, replacement.TorrentPath)
	}
	return outcome
}

func checkApplicable(replacement Replacement, roots []string) (string, fs.FileInfo, error) {
	if len(replacement.Ambiguous) > 0 {
		return "", nil, fmt.Errorf("%w: %v", ErrAmbiguous, replacement.Ambiguous)
	}
	if replacement.Kind == 0 {
		return "", nil, errors.Join(linker.ErrNoKind, replacement.Reason)
	}
	root, err := rootContaining(roots, replacement.TorrentPath)
	if err != nil {
		return "", nil, err
	}
	libraryInfo, err := checkUnchanged(replacement)
	return root, libraryInfo, err
}

func rootContaining(roots []string, torrentPath string) (string, error) {
	absolutePath, err := filepath.Abs(torrentPath)
	if err != nil {
		return "", err
	}
	for _, root := range roots {
		relative, err := filepath.Rel(root, absolutePath)
		if err == nil && relative != "." && filepath.IsLocal(relative) {
			return root, nil
		}
	}
	return "", fmt.Errorf("%w: %s", ErrOutsideTorrentRoots, torrentPath)
}

// checkUnchanged relit les deux fichiers sans les suivre : même taille, même date, toujours deux fichiers distincts ;
// l'inode de la bibliothèque n'est comparé que s'il est fiable (volume local).
func checkUnchanged(replacement Replacement) (fs.FileInfo, error) {
	torrentInfo, err := statUnchanged(replacement.TorrentPath, replacement.Size, replacement.TorrentModTime)
	if err != nil {
		return nil, err
	}
	libraryInfo, err := statUnchanged(replacement.LibraryPath, replacement.Size, replacement.LibraryModTime)
	if err != nil {
		return nil, err
	}
	if os.SameFile(torrentInfo, libraryInfo) {
		return nil, fmt.Errorf("%w: %s is already linked", ErrChangedSincePlan, replacement.TorrentPath)
	}
	if replacement.InodeReliable {
		device, inode, _ := fileIdentity(libraryInfo)
		if device != replacement.LibraryDevice || inode != replacement.LibraryInode {
			return nil, fmt.Errorf("%w: %s has another inode", ErrChangedSincePlan, replacement.LibraryPath)
		}
	}
	return libraryInfo, nil
}

func statUnchanged(path string, size int64, modTime time.Time) (fs.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrChangedSincePlan, err)
	}
	isSameFile := info.Mode().IsRegular() && info.Size() == size && info.ModTime().Equal(modTime)
	if !isSameFile {
		return nil, fmt.Errorf("%w: %s", ErrChangedSincePlan, path)
	}
	return info, nil
}

// linkTarget renvoie la source à passer au linker et si elle est relative ; un lien relatif impossible est une
// erreur, jamais un repli silencieux sur un chemin absolu.
func linkTarget(replacement Replacement, relativeSymlinks bool) (string, bool, error) {
	if replacement.Kind != linker.Symlink {
		return replacement.LibraryPath, false, nil
	}
	if relativeSymlinks {
		target, err := linker.RelativeSymlinkTarget(replacement.TorrentPath, replacement.LibraryPath)
		return target, true, err
	}
	target, err := filepath.Abs(replacement.LibraryPath)
	return target, false, err
}

func replaceWithLink(replacement Replacement, options Options, root, target string, libraryInfo fs.FileInfo) error {
	temporaryPath := temporaryPathFor(replacement.TorrentPath, replacement.Size)
	if err := options.createLink(replacement.Kind, target, temporaryPath, root); err != nil {
		return err
	}
	err := checkTemporaryLink(replacement, temporaryPath, libraryInfo)
	if err == nil {
		// Relu juste avant le renommage : une écriture pendant la création du lien serait perdue.
		_, err = statUnchanged(replacement.TorrentPath, replacement.Size, replacement.TorrentModTime)
	}
	if err == nil {
		err = os.Rename(temporaryPath, replacement.TorrentPath)
	}
	if err != nil {
		return errors.Join(err, removeTemporary(temporaryPath))
	}
	return nil
}

func temporaryPathFor(torrentPath string, size int64) string {
	return torrentPath + temporaryMarker + strconv.FormatInt(size, 10) + temporarySeparator + rand.Text()
}

// checkTemporaryLink : un symlink ou un hardlink doit mener au fichier même de la bibliothèque, un reflink à un
// fichier de même taille.
func checkTemporaryLink(replacement Replacement, temporaryPath string, libraryInfo fs.FileInfo) error {
	linkInfo, err := os.Stat(temporaryPath)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNotIdentical, err)
	}
	if !linkInfo.Mode().IsRegular() || linkInfo.Size() != replacement.Size {
		return fmt.Errorf("%w: %s", ErrNotIdentical, temporaryPath)
	}
	if replacement.Kind != linker.Reflink && !os.SameFile(linkInfo, libraryInfo) {
		return fmt.Errorf("%w: %s", ErrNotIdentical, temporaryPath)
	}
	return nil
}

// removeTemporary ne retire qu'un nom créé par cet appel : os.Remove ôte le lien, jamais le fichier visé.
func removeTemporary(temporaryPath string) error {
	err := os.Remove(temporaryPath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("dedupe: remove temporary link: %w", err)
	}
	return nil
}

func linkRecord(replacement Replacement, relative bool) store.Link {
	now := time.Now().UTC()
	link := store.Link{
		Kind:          replacement.Kind.String(),
		LinkPath:      replacement.TorrentPath,
		TargetPath:    replacement.LibraryPath,
		Relative:      relative,
		InodeReliable: replacement.InodeReliable,
		Size:          replacement.Size,
		Fingerprint:   replacement.Fingerprint,
		State:         store.LinkOK,
		CreatedAt:     now,
		VerifiedAt:    now,
	}
	if replacement.InodeReliable {
		link.TargetDevice, link.TargetInode = replacement.LibraryDevice, replacement.LibraryInode
	}
	return link
}
