package dedupe

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"time"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/hashsource"
	"necromanciarr/internal/linker"
	"necromanciarr/internal/store"
)

// temporaryMarker : le lien est d'abord créé sous « <fichier>.necromanciarr-tmp-<taille>-<aléatoire> » à côté du
// fichier torrent, puis renommé par-dessus ; la taille attendue dans le nom permet à Recover de reprendre sans base.
const (
	temporaryMarker    = ".necromanciarr-tmp-"
	temporarySeparator = "-"
)

// recordTimeout borne l'enregistrement d'un lien déjà posé, fait même après une annulation.
const recordTimeout = 30 * time.Second

// Erreurs typées d'Apply.
var (
	ErrChangedSincePlan    = errors.New("dedupe: file changed since plan")
	ErrNotIdentical        = errors.New("dedupe: new link does not resolve to the library file")
	ErrOutsideTorrentRoots = errors.New("dedupe: torrent path outside the torrent roots")
	ErrAmbiguous           = errors.New("dedupe: several identical library files, none chosen")
	ErrNoRecorder          = errors.New("dedupe: no link recorder")
	// ErrKindNotAllowed signale une proposition (enregistrée ou modifiée) dont la sorte de lien n'est pas dans Kinds.
	ErrKindNotAllowed = errors.New("dedupe: link kind not allowed by the options")
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
// lien remplace désormais le fichier torrent, BytesRead compte les octets lus par la preuve d'identité. Un échec du
// recontrôle est dans RecheckErr et ne rend pas Apply fautif.
type Outcome struct {
	Replacement Replacement
	LinkTarget  string
	BytesRead   int64
	Done        bool
	DryRun      bool
	Err         error
	RecheckErr  error
}

// Result donne un Outcome par Replacement, dans l'ordre reçu.
type Result struct {
	Outcomes []Outcome
}

// Apply remplace chaque fichier torrent par un lien vers le fichier de la bibliothèque. Avant toute création : sorte
// autorisée par Kinds, racine sans recouvrement de la bibliothèque, remplacement atomique annoncé par le volume
// (fsys.ErrNoAtomicReplace sinon, rien n'est écrit), taille et date inchangées, empreintes début et fin identiques à
// celles de Plan, puis preuve d'identité selon Verify (octet par octet, débit plafonné, annulable). Le lien est créé
// sous un nom temporaire dans la racine d'écriture du dossier torrent, contrôlé, puis remplace le fichier torrent par
// WriteRoot.Replace ; en cas d'échec seul ce nom temporaire est retiré. DryRun fait toutes ces vérifications sans
// rien créer. L'erreur renvoyée réunit les échecs des Outcomes.
func Apply(ctx context.Context, replacements []Replacement, options Options) (Result, error) {
	if options.Recorder == nil && !options.DryRun {
		return Result{}, ErrNoRecorder
	}
	verification, err := normalizeVerification(options.Verify)
	if err != nil {
		return Result{}, err
	}
	options.Verify = verification
	files := NewFiles(options.Registry)
	roots, err := resolveRoots(files, options.TorrentRoots)
	if err != nil {
		return Result{}, err
	}
	result := Result{Outcomes: make([]Outcome, 0, len(replacements))}
	var failures []error
	for _, replacement := range replacements {
		if err := ctx.Err(); err != nil {
			return result, errors.Join(append(failures, err)...)
		}
		outcome := applyOne(ctx, files, replacement, options, roots)
		if outcome.Err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", replacement.TorrentPath, outcome.Err))
		}
		result.Outcomes = append(result.Outcomes, outcome)
	}
	return result, errors.Join(failures...)
}

func applyOne(ctx context.Context, files Files, replacement Replacement, options Options, roots []string) Outcome {
	outcome := Outcome{Replacement: replacement, DryRun: options.DryRun}
	root, libraryInfo, err := checkApplicable(ctx, files, replacement, options.Kinds, roots)
	var proven proof
	if err == nil {
		proven, err = proveIdentity(ctx, files, replacement, options)
		outcome.BytesRead, outcome.Replacement.Method = proven.bytesRead, proven.method
	}
	if err != nil {
		outcome.Err = err
		return outcome
	}
	target, relative, err := linkTarget(files, replacement, options.RelativeSymlinks)
	outcome.LinkTarget = target
	if err != nil || options.DryRun {
		outcome.Err = err
		return outcome
	}
	if err := replaceWithLink(ctx, files, replacement, options, root, target, libraryInfo); err != nil {
		outcome.Err = err
		return outcome
	}
	outcome.Done = true
	// Le renommage est fait : l'enregistrement doit suivre même si l'appelant annule, sinon la base ignore le lien.
	recordCtx, cancelRecord := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer cancelRecord()
	if err := options.Recorder.RecordLink(recordCtx, linkRecord(replacement, relative, proven)); err != nil {
		outcome.Err = fmt.Errorf("dedupe: link created but not recorded: %w", err)
	}
	if options.RecheckAfter && options.Rechecker != nil {
		outcome.RecheckErr = options.Rechecker.RecheckPath(ctx, replacement.TorrentPath)
	}
	return outcome
}

func checkApplicable(ctx context.Context, files Files, replacement Replacement, allowedKinds []linker.Kind, roots []string) (string, fs.FileInfo, error) {
	if len(replacement.Ambiguous) > 0 {
		return "", nil, fmt.Errorf("%w: %v", ErrAmbiguous, replacement.Ambiguous)
	}
	if replacement.Kind == 0 {
		return "", nil, errors.Join(linker.ErrNoKind, replacement.Reason)
	}
	if !slices.Contains(allowedKinds, replacement.Kind) {
		return "", nil, fmt.Errorf("%w: %v", ErrKindNotAllowed, replacement.Kind)
	}
	root, err := rootContaining(roots, replacement.TorrentPath)
	if err != nil {
		return "", nil, err
	}
	if err := checkNoOverlap(files, []string{root}, []string{replacement.LibraryPath}); err != nil {
		return "", nil, err
	}
	if err := atomicReplaceRefusal(ctx, files, root); err != nil {
		return "", nil, err
	}
	libraryInfo, err := checkUnchanged(files, replacement)
	return root, libraryInfo, err
}

func rootContaining(roots []string, torrentPath string) (string, error) {
	for _, root := range roots {
		if _, err := Below(root, torrentPath); err == nil {
			return root, nil
		}
	}
	return "", fmt.Errorf("%w: %s", ErrOutsideTorrentRoots, torrentPath)
}

// checkUnchanged relit les deux fichiers sans les suivre : même taille, même date, toujours deux fichiers distincts ;
// l'inode de la bibliothèque n'est comparé que s'il est fiable (volume local).
func checkUnchanged(files Files, replacement Replacement) (fs.FileInfo, error) {
	torrentInfo, err := statUnchanged(files, replacement.TorrentPath, replacement.Size, replacement.TorrentModTime)
	if err != nil {
		return nil, err
	}
	libraryInfo, err := statUnchanged(files, replacement.LibraryPath, replacement.Size, replacement.LibraryModTime)
	if err != nil {
		return nil, err
	}
	if files.SameFile(replacement.TorrentPath, torrentInfo, replacement.LibraryPath, libraryInfo) {
		return nil, fmt.Errorf("%w: %s is already linked", ErrChangedSincePlan, replacement.TorrentPath)
	}
	if replacement.InodeReliable {
		identity, _ := ReliableID(replacement.LibraryPath, libraryInfo)
		if identity.Device != replacement.LibraryDevice || identity.Inode != replacement.LibraryInode {
			return nil, fmt.Errorf("%w: %s has another inode", ErrChangedSincePlan, replacement.LibraryPath)
		}
	}
	return libraryInfo, nil
}

// proveIdentity relit les empreintes de Plan (un contenu réécrit à taille et date égales est refusé), puis prouve
// l'identité selon la méthode : empreinte du stockage (jamais sans ce contrôle préalable), ou comparaison des deux
// fichiers ; rien n'est créé avant qu'elle réussisse. proof.method est renseigné même en cas d'échec.
func proveIdentity(ctx context.Context, files Files, replacement Replacement, options Options) (proof, error) {
	method := methodFor(replacement, options)
	readMeter := &meter{ctx: ctx, limiter: options.Limiter}
	proven := proof{method: method}
	finish := func(err error) (proof, error) {
		proven.bytesRead = readMeter.bytesRead
		return proven, err
	}
	if options.Verify == VerifyHash && method != MethodHash {
		return finish(ErrHashUnavailable)
	}
	if err := checkFingerprints(readMeter, files, replacement); err != nil {
		return finish(err)
	}
	verification := options.Verify
	if method == MethodHash {
		hashProof, err := proveByHash(ctx, replacement, options, readMeter)
		// Empreinte distante en échec en cours d'étape : en auto, la preuve devient une lecture complète (jamais par
		// échantillons) ; en hash, aucune lecture n'est faite (ErrHashUnavailable).
		fallsBackToFullRead := errors.Is(err, hashsource.ErrRemoteExecFailed) && options.Verify == VerifyAuto
		if !fallsBackToFullRead {
			proven.algorithm, proven.hash = hashProof.algorithm, hashProof.hash
			return finish(err)
		}
		proven.method, verification = MethodFull, VerifyFull
	}
	equal, err := sameBytes(readMeter, files, replacement.TorrentPath, replacement.LibraryPath, comparisonRanges(replacement.Size, verification))
	if err == nil && !equal {
		err = fmt.Errorf("%w: %s differs from %s", ErrNotIdentical, replacement.TorrentPath, replacement.LibraryPath)
	}
	return finish(err)
}

// checkFingerprints : l'égalité des empreintes début et fin de Plan ne remplace jamais ce contrôle, même si le
// stockage donne une empreinte de contenu.
func checkFingerprints(readMeter *meter, files Files, replacement Replacement) error {
	torrentFingerprint, err := fingerprintFile(readMeter, files, replacement.TorrentPath, replacement.Size)
	if err != nil {
		return err
	}
	libraryFingerprint, err := fingerprintFile(readMeter, files, replacement.LibraryPath, replacement.Size)
	if err != nil {
		return err
	}
	if torrentFingerprint != replacement.Fingerprint || libraryFingerprint != replacement.LibraryFingerprint {
		return fmt.Errorf("%w: head or tail fingerprint differs", ErrChangedSincePlan)
	}
	return nil
}

func statUnchanged(files Files, path string, size int64, modTime time.Time) (fs.FileInfo, error) {
	info, err := files.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrChangedSincePlan, err)
	}
	isSameFile := info.Mode().IsRegular() && info.Size() == size && info.ModTime().Equal(modTime)
	if !isSameFile {
		return nil, fmt.Errorf("%w: %s", ErrChangedSincePlan, path)
	}
	return info, nil
}

// linkTarget renvoie la source à passer au linker (chemin du volume) et si elle est relative ; un lien relatif
// impossible est une erreur, jamais un repli silencieux sur un chemin absolu.
func linkTarget(files Files, replacement Replacement, relativeSymlinks bool) (string, bool, error) {
	if replacement.Kind != linker.Symlink {
		return VolumePath(replacement.LibraryPath), false, nil
	}
	target, err := files.SymlinkText(replacement.TorrentPath, replacement.LibraryPath, relativeSymlinks)
	return target, relativeSymlinks, err
}

// replaceWithLink écrit seulement dans la racine d'écriture du dossier torrent, ouverte par le registre (qui refuse
// tout recouvrement de la bibliothèque) : lien temporaire, contrôle, puis WriteRoot.Replace atomique.
func replaceWithLink(ctx context.Context, files Files, replacement Replacement, options Options, root, target string, libraryInfo fs.FileInfo) error {
	writeRoot, err := files.WriteRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = writeRoot.Close() }()
	temporaryPath := temporaryPathFor(replacement.TorrentPath, replacement.Size)
	temporaryRelative, err := Below(root, temporaryPath)
	if err != nil {
		return err
	}
	torrentRelative, err := Below(root, replacement.TorrentPath)
	if err != nil {
		return err
	}
	createLink := options.createLink
	if createLink == nil {
		createLink = func(kind linker.Kind, source, _, _ string) error {
			return linker.CreateLinkInRoot(writeRoot, kind, source, temporaryRelative)
		}
	}
	if err := createLink(replacement.Kind, target, temporaryPath, root); err != nil {
		return err
	}
	err = checkTemporaryLink(&meter{ctx: ctx, limiter: options.Limiter}, files, replacement, temporaryPath, libraryInfo)
	if err == nil {
		// Relu juste avant le remplacement : une écriture pendant la création du lien serait perdue.
		_, err = statUnchanged(files, replacement.TorrentPath, replacement.Size, replacement.TorrentModTime)
	}
	if err == nil {
		err = writeRoot.Replace(temporaryRelative, torrentRelative)
	}
	if err != nil {
		return errors.Join(err, removeTemporary(writeRoot, temporaryRelative))
	}
	return nil
}

func temporaryPathFor(torrentPath string, size int64) string {
	return torrentPath + temporaryMarker + strconv.FormatInt(size, 10) + temporarySeparator + rand.Text()
}

// checkTemporaryLink : un reflink doit être un fichier de même taille ; un symlink ou un hardlink doit mener au
// fichier même de la bibliothèque, prouvé par l'identité stricte (ReliableID) des deux côtés, ou, faute d'identité
// connue, par l'empreinte début et fin du fichier de la bibliothèque relue à travers le lien.
func checkTemporaryLink(readMeter *meter, files Files, replacement Replacement, temporaryPath string, libraryInfo fs.FileInfo) error {
	linkInfo, err := files.Stat(temporaryPath)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNotIdentical, err)
	}
	if !linkInfo.Mode().IsRegular() || linkInfo.Size() != replacement.Size {
		return fmt.Errorf("%w: %s", ErrNotIdentical, temporaryPath)
	}
	if replacement.Kind == linker.Reflink {
		return nil
	}
	linkIdentity, linkKnown := ReliableID(temporaryPath, linkInfo)
	libraryIdentity, libraryKnown := ReliableID(replacement.LibraryPath, libraryInfo)
	if linkKnown && libraryKnown {
		if linkIdentity != libraryIdentity {
			return fmt.Errorf("%w: %s", ErrNotIdentical, temporaryPath)
		}
		return nil
	}
	fingerprint, err := fingerprintFile(readMeter, files, temporaryPath, replacement.Size)
	if err != nil || fingerprint != replacement.LibraryFingerprint {
		return errors.Join(fmt.Errorf("%w: %s", ErrNotIdentical, temporaryPath), err)
	}
	return nil
}

// removeTemporary ne retire qu'un nom créé par cet appel : Remove ôte le lien, jamais le fichier visé.
func removeTemporary(writeRoot fsys.WriteRoot, temporaryRelative string) error {
	err := writeRoot.Remove(temporaryRelative)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("dedupe: remove temporary link: %w", err)
	}
	return nil
}

func linkRecord(replacement Replacement, relative bool, proven proof) store.Link {
	now := time.Now().UTC()
	link := store.Link{
		Kind:          replacement.Kind.String(),
		Volume:        volumeOf(replacement.TorrentPath),
		LinkPath:      replacement.TorrentPath,
		TargetPath:    replacement.LibraryPath,
		Relative:      relative,
		InodeReliable: replacement.InodeReliable,
		Size:          replacement.Size,
		Fingerprint:   replacement.Fingerprint,
		State:         store.LinkOK,
		CreatedAt:     now,
		VerifiedAt:    now,
		ModTime:       replacement.LibraryModTime,
		HashAlgo:      string(proven.algorithm),
		Hash:          proven.hash,
	}
	if replacement.InodeReliable {
		link.TargetDevice, link.TargetInode = replacement.LibraryDevice, replacement.LibraryInode
	}
	return link
}
