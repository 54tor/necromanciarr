// Package dedupe remplace, côté torrent, les copies d'un fichier de la bibliothèque par des liens vers ce fichier.
// Le fichier de la bibliothèque n'est jamais ouvert en écriture, déplacé, tronqué ni supprimé.
package dedupe

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"necromanciarr/internal/config"
	"necromanciarr/internal/fsys"
	"necromanciarr/internal/hashsource"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/linker"
	"necromanciarr/internal/store"
)

// Verification choisit la preuve d'identité du contenu.
type Verification string

// VerifyFull compare tout le contenu octet par octet (défaut). VerifySampled ne compare que début, fin et 8 blocs de
// 1 Mio : bien moins de lecture sur un montage réseau, mais une différence hors de ces blocs passe inaperçue.
const (
	VerifyFull    Verification = "full"
	VerifySampled Verification = "sampled"
	// VerifyAuto (défaut) compare par empreinte du stockage quand il en fournit, sinon relit en entier ; jamais par
	// échantillons sans demande. VerifyHash exige l'empreinte : ErrHashUnavailable plutôt qu'une lecture.
	VerifyAuto Verification = "auto"
	VerifyHash Verification = "hash"
)

// Avertissements portés par un Replacement dont un des fichiers est sur un volume FUSE, réseau ou inconnu.
var (
	warningUnreliableInode = i18n.Message{Key: i18n.KeyWarningDedupeUnreliableInode}
	warningFullNetworkRead = i18n.Message{Key: i18n.KeyWarningDedupeFullNetworkRead}
	warningSampledVerify   = i18n.Message{Key: i18n.KeyWarningDedupeSampledVerify}
	warningRenameNotAtomic = i18n.Message{Key: i18n.KeyWarningDedupeRenameNotAtomic}
)

// toolEntryMarker repère les entrées temporaires de necromanciarr (sonde, lien en cours) : jamais candidates.
const toolEntryMarker = ".necromanciarr-"

// Erreurs typées de Plan, reprises par Apply.
var (
	// ErrUnknownVerification signale une valeur d'Options.Verify inconnue.
	ErrUnknownVerification = errors.New("dedupe: unknown verification mode")
	// ErrTorrentRootOverlapsLibrary signale une racine torrent égale à un dossier de la bibliothèque, qui le contient
	// ou qui y est contenue : un lien y remplacerait un fichier de la bibliothèque.
	ErrTorrentRootOverlapsLibrary = errors.New("dedupe: torrent root overlaps the library")
)

// FingerprintCache conserve l'empreinte début et fin des fichiers de la bibliothèque ; implémenté par store.Store.
type FingerprintCache interface {
	LookupFingerprint(ctx context.Context, path string, size int64, modTime time.Time) (string, bool, error)
	RecordFingerprint(ctx context.Context, entry store.CachedFingerprint) error
}

// Options règle Plan et Apply. TorrentRoots ne sert qu'à Apply, qui refuse tout chemin hors de ces racines ; Plan
// reçoit les siennes en argument. Limiter, Fingerprints et Rechecker nil désactivent plafond, cache et recontrôle.
type Options struct {
	Kinds            []linker.Kind
	RelativeSymlinks bool
	MinFileAge       time.Duration
	RecheckAfter     bool
	DryRun           bool
	Verify           Verification
	TorrentRoots     []string
	Limiter          Limiter
	Fingerprints     FingerprintCache
	Recorder         LinkRecorder
	Rechecker        Rechecker
	// Backend donne les empreintes du stockage distant (hashsource.NewRclone) ; nil : aucune, donc lecture. Il est
	// interrogé sur les chemins locaux des deux fichiers. HashAlgorithms vide : MD5 seul.
	Backend        hashsource.HashSource
	HashAlgorithms []hashsource.Algorithm
	// Registry ouvre les racines d'écriture (sonde des dossiers torrent, liens) ; nil : toute écriture est refusée
	// (fsys.ErrNoLibraryRoots).
	Registry *fsys.Registry

	probe          func(directory string) (linker.Capabilities, error)
	filesystemKind func(path string) (linker.FilesystemKind, error)
	createLink     func(kind linker.Kind, source, destination, root string) error
}

// Replacement propose de remplacer TorrentPath par un lien de sorte Kind vers LibraryPath : un candidat de même taille
// et de même empreinte début et fin, dont Apply fera la preuve d'identité. Kind vaut 0 quand aucun remplacement n'est
// possible : Reason en donne la cause, ou Ambiguous liste les fichiers de la bibliothèque entre lesquels aucun choix
// n'est fait. BytesToRead estime, avant toute lecture, les octets que la preuve d'Apply lira : 0 si le stockage donne
// les deux empreintes, la taille d'un fichier s'il faut en lire un seul, les deux fichiers sinon. Method est la preuve
// prévue ; HashAlgorithm n'est renseigné que pour MethodHash.
// Les dates, l'inode et les empreintes servent à Apply pour refuser un fichier changé.
type Replacement struct {
	TorrentPath        string
	LibraryPath        string
	Size               int64
	Kind               linker.Kind
	SavedBytes         int64
	Ambiguous          []string
	Reason             error
	Warnings           []i18n.Message
	BytesToRead        int64
	Method             Method
	HashAlgorithm      hashsource.Algorithm
	Fingerprint        string
	LibraryFingerprint string
	TorrentModTime     time.Time
	LibraryModTime     time.Time
	LibraryDevice      uint64
	LibraryInode       uint64
	InodeReliable      bool
}

type libraryFile struct {
	path string
	info fs.FileInfo
}

type planRun struct {
	ctx                 context.Context
	options             Options
	youngerThan         time.Time
	capabilities        map[string]probeOutcome
	volumes             map[string]linker.FilesystemKind
	libraryFingerprints map[string]string
}

type probeOutcome struct {
	capabilities linker.Capabilities
	err          error
}

// Plan cherche, sous torrentRoots, les fichiers à remplacer : même taille et même empreinte début et fin qu'un
// fichier de library (environ 2 Mio lus par fichier, empreinte de la bibliothèque en cache). La preuve d'identité est
// faite par Apply. Liens symboliques, fichiers déjà liés au même inode et fichiers modifiés depuis moins de MinFileAge
// (en cours d'écriture) sont ignorés ; une racine qui recouvre un dossier de library est refusée. Rien n'est modifié,
// hors les entrées d'essai de la sonde de liens, créées puis supprimées dans les dossiers torrent.
func Plan(ctx context.Context, torrentRoots []string, library []store.MediaFile, options Options) ([]Replacement, error) {
	libraryPaths := make([]string, 0, len(library))
	for _, mediaFile := range library {
		libraryPaths = append(libraryPaths, mediaFile.Path)
	}
	if options.probe == nil {
		options.probe = registryProbe(options.Registry)
	}
	run, err := newPlanRun(ctx, options)
	if err != nil {
		return nil, err
	}
	resolvedRoots, err := resolveRoots(torrentRoots)
	if err != nil {
		return nil, err
	}
	if err := checkNoOverlap(resolvedRoots, libraryPaths); err != nil {
		return nil, err
	}
	librarySizes := indexLibrary(library)
	var replacements []Replacement
	for _, root := range resolvedRoots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			replacement, found, err := run.consider(path, entry, librarySizes)
			if found {
				replacements = append(replacements, replacement)
			}
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	return replacements, nil
}

func normalizeVerification(verification Verification) (Verification, error) {
	switch verification {
	case "":
		return VerifyAuto, nil
	case VerifyFull, VerifySampled, VerifyAuto, VerifyHash:
		return verification, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnknownVerification, verification)
	}
}

func newPlanRun(ctx context.Context, options Options) (*planRun, error) {
	verification, err := normalizeVerification(options.Verify)
	if err != nil {
		return nil, err
	}
	options.Verify = verification
	if options.filesystemKind == nil {
		options.filesystemKind = linker.FilesystemKindOf
	}
	return &planRun{
		ctx:                 ctx,
		options:             options,
		youngerThan:         time.Now().Add(-options.MinFileAge),
		capabilities:        map[string]probeOutcome{},
		volumes:             map[string]linker.FilesystemKind{},
		libraryFingerprints: map[string]string{},
	}, nil
}

// resolveRoots : une racine elle-même symbolique est légitime ; WalkDir ne suivrait pas un lien passé en racine.
func resolveRoots(roots []string) ([]string, error) {
	resolvedRoots := make([]string, 0, len(roots))
	for _, root := range roots {
		if root == "" {
			return nil, errors.New("dedupe: empty torrent root")
		}
		absoluteRoot, err := filepath.Abs(root)
		if err != nil {
			return nil, err
		}
		resolvedRoot, err := filepath.EvalSymlinks(absoluteRoot)
		if err != nil {
			return nil, fmt.Errorf("dedupe: torrent root %s: %w", root, err)
		}
		resolvedRoots = append(resolvedRoots, resolvedRoot)
	}
	return resolvedRoots, nil
}

// checkNoOverlap compare chaque racine aux dossiers des fichiers de la bibliothèque, tels que donnés et résolus.
func checkNoOverlap(roots, libraryPaths []string) error {
	libraryDirs := map[string]bool{}
	for _, libraryPath := range libraryPaths {
		for _, directory := range directoryForms(libraryPath) {
			libraryDirs[directory] = true
		}
	}
	for _, root := range roots {
		for directory := range libraryDirs {
			if config.PathsOverlap(root, directory) {
				return fmt.Errorf("%w: %s", ErrTorrentRootOverlapsLibrary, root)
			}
		}
	}
	return nil
}

func directoryForms(path string) []string {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return []string{filepath.Dir(path)}
	}
	directory := filepath.Dir(absolutePath)
	resolvedDirectory, err := filepath.EvalSymlinks(directory)
	if err != nil || resolvedDirectory == directory {
		return []string{directory}
	}
	return []string{directory, resolvedDirectory}
}

// indexLibrary ne garde que les fichiers réguliers encore présents : la cible d'un lien doit être un vrai fichier.
func indexLibrary(library []store.MediaFile) map[int64][]libraryFile {
	librarySizes := map[int64][]libraryFile{}
	for _, mediaFile := range library {
		info, err := os.Lstat(mediaFile.Path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		librarySizes[info.Size()] = append(librarySizes[info.Size()], libraryFile{path: mediaFile.Path, info: info})
	}
	return librarySizes
}

func (run *planRun) consider(path string, entry fs.DirEntry, librarySizes map[int64][]libraryFile) (Replacement, bool, error) {
	if !entry.Type().IsRegular() || strings.Contains(entry.Name(), toolEntryMarker) {
		return Replacement{}, false, nil
	}
	info, err := entry.Info()
	if errors.Is(err, fs.ErrNotExist) {
		return Replacement{}, false, nil
	}
	if err != nil {
		return Replacement{}, false, err
	}
	candidates := librarySizes[info.Size()]
	isEmpty := info.Size() == 0
	isBeingWritten := info.ModTime().After(run.youngerThan)
	if isEmpty || isBeingWritten || len(candidates) == 0 || alreadyLinked(info, candidates) {
		return Replacement{}, false, nil
	}
	replacement := Replacement{TorrentPath: path, Size: info.Size(), TorrentModTime: info.ModTime()}
	readMeter := &meter{ctx: run.ctx, limiter: run.options.Limiter}
	matching, err := run.matchingCandidates(readMeter, &replacement, candidates)
	if err != nil || len(matching) == 0 {
		return Replacement{}, false, err
	}
	hashRefusal := run.decideProof(&replacement, matching)
	if errors.Is(hashRefusal, ErrHashUnavailable) {
		run.describe(&replacement, info, matching)
		replacement.Kind, replacement.SavedBytes, replacement.Reason = 0, 0, hashRefusal
		return replacement, true, nil
	}
	if hashRefusal != nil {
		return Replacement{}, false, hashRefusal
	}
	run.describe(&replacement, info, matching)
	return replacement, true, nil
}

// decideProof choisit la preuve : par empreinte pour un candidat unique, sinon (ambigu) la lecture, que seul Apply
// refusera de toute façon.
func (run *planRun) decideProof(replacement *Replacement, matching []libraryFile) error {
	if len(matching) != 1 {
		run.useReadMethod(replacement)
		return nil
	}
	return run.chooseMethod(replacement, matching[0].path)
}

func alreadyLinked(info fs.FileInfo, candidates []libraryFile) bool {
	return slices.ContainsFunc(candidates, func(candidate libraryFile) bool { return os.SameFile(info, candidate.info) })
}

// proofBytes estime la lecture de la preuve d'Apply : les plages comparées, dans les deux fichiers.
func proofBytes(size int64, verification Verification) int64 {
	var total int64
	for _, span := range comparisonRanges(size, verification) {
		total += span.length
	}
	return 2 * total
}

func (run *planRun) matchingCandidates(readMeter *meter, replacement *Replacement, candidates []libraryFile) ([]libraryFile, error) {
	torrentFingerprint, err := fingerprintFile(readMeter, replacement.TorrentPath, replacement.Size)
	if err != nil {
		return nil, err
	}
	replacement.Fingerprint = torrentFingerprint
	var matching []libraryFile
	for _, candidate := range candidates {
		libraryFingerprint, err := run.libraryFingerprint(readMeter, candidate)
		if err != nil {
			return nil, err
		}
		if libraryFingerprint == torrentFingerprint {
			matching = append(matching, candidate)
		}
	}
	return matching, nil
}

// libraryFingerprint lit l'empreinte en mémoire, puis dans le cache en base (même taille, même date), sinon la
// calcule et l'enregistre.
func (run *planRun) libraryFingerprint(readMeter *meter, candidate libraryFile) (string, error) {
	if fingerprint, found := run.libraryFingerprints[candidate.path]; found {
		return fingerprint, nil
	}
	size, modTime := candidate.info.Size(), candidate.info.ModTime()
	cache := run.options.Fingerprints
	if cache != nil {
		fingerprint, found, err := cache.LookupFingerprint(run.ctx, candidate.path, size, modTime)
		if err != nil {
			return "", err
		}
		if found {
			run.libraryFingerprints[candidate.path] = fingerprint
			return fingerprint, nil
		}
	}
	fingerprint, err := fingerprintFile(readMeter, candidate.path, size)
	if err != nil {
		return "", err
	}
	run.libraryFingerprints[candidate.path] = fingerprint
	if cache == nil {
		return fingerprint, nil
	}
	entry := store.CachedFingerprint{Path: candidate.path, Size: size, ModTime: modTime, HeadTailHash: fingerprint, VerifiedAt: time.Now()}
	return fingerprint, cache.RecordFingerprint(run.ctx, entry)
}

func (run *planRun) describe(replacement *Replacement, torrentInfo fs.FileInfo, identical []libraryFile) {
	libraryPaths := make([]string, 0, len(identical))
	for _, libraryEntry := range identical {
		libraryPaths = append(libraryPaths, libraryEntry.path)
	}
	replacement.Warnings = run.warnings(replacement.TorrentPath, libraryPaths, replacement.Method)
	if len(identical) > 1 {
		slices.Sort(libraryPaths)
		replacement.Ambiguous = libraryPaths
		return
	}
	chosen := identical[0]
	replacement.LibraryPath = chosen.path
	replacement.LibraryFingerprint = run.libraryFingerprints[chosen.path]
	replacement.LibraryModTime = chosen.info.ModTime()
	device, inode, identityKnown := fileIdentity(chosen.info)
	replacement.LibraryDevice, replacement.LibraryInode = device, inode
	replacement.InodeReliable = identityKnown && run.volumeOf(chosen.path) == linker.Local
	torrentDir := filepath.Dir(replacement.TorrentPath)
	replacement.Kind, replacement.Reason = run.chooseKind(torrentDir, torrentInfo, chosen.info)
	if replacement.Kind == 0 {
		return
	}
	replacement.SavedBytes = replacement.Size
	if run.renameIsUncertain(torrentDir, replacement.TorrentPath) {
		replacement.Warnings = append(replacement.Warnings, warningRenameNotAtomic)
	}
}

// renameIsUncertain : un VFS FUSE ou réseau peut renommer par copie puis suppression, ou suppression puis renommage ;
// une coupure laisse alors un fichier temporaire que Recover reprend.
func (run *planRun) renameIsUncertain(torrentDir, torrentPath string) bool {
	volume := run.volumeOf(torrentPath)
	isRemoteVolume := volume == linker.Fuse || volume == linker.Network
	return isRemoteVolume || !run.capabilities[torrentDir].capabilities.RenameOverwrite
}

func (run *planRun) warnings(torrentPath string, libraryPaths []string, method Method) []i18n.Message {
	var warnings []i18n.Message
	libraryIsLocal := !slices.ContainsFunc(libraryPaths, func(path string) bool { return run.volumeOf(path) != linker.Local })
	allLocal := libraryIsLocal && run.volumeOf(torrentPath) == linker.Local
	if !libraryIsLocal {
		warnings = append(warnings, warningUnreliableInode)
	}
	if !allLocal && method == MethodFull {
		warnings = append(warnings, warningFullNetworkRead)
	}
	if method == MethodSampled {
		warnings = append(warnings, warningSampledVerify)
	}
	return warnings
}

// volumeOf classe le dossier du fichier, une fois par dossier ; une erreur compte comme volume inconnu.
func (run *planRun) volumeOf(path string) linker.FilesystemKind {
	directory := filepath.Dir(path)
	if kind, found := run.volumes[directory]; found {
		return kind
	}
	kind, err := run.options.filesystemKind(directory)
	if err != nil {
		kind = linker.Unknown
	}
	run.volumes[directory] = kind
	return kind
}

// chooseKind retient la première sorte de Kinds que la sonde du dossier torrent confirme ; hardlink et reflink
// exigent en plus le même périphérique que le fichier de la bibliothèque, sans écrire dans le dossier de celle-ci.
func (run *planRun) chooseKind(torrentDir string, torrentInfo, libraryInfo fs.FileInfo) (linker.Kind, error) {
	if len(run.options.Kinds) == 0 {
		return 0, linker.ErrNoKind
	}
	probed, found := run.capabilities[torrentDir]
	if !found {
		probed.capabilities, probed.err = run.options.probe(torrentDir)
		run.capabilities[torrentDir] = probed
	}
	if probed.err != nil {
		return 0, probed.err
	}
	refusals := make([]error, 0, len(run.options.Kinds))
	for _, kind := range run.options.Kinds {
		if err := kindRefusal(probed.capabilities, kind); err != nil {
			refusals = append(refusals, fmt.Errorf("%v: %w", kind, err))
			continue
		}
		if kind != linker.Symlink && !sameDevice(torrentInfo, libraryInfo) {
			refusals = append(refusals, fmt.Errorf("%v: %w", kind, linker.ErrCrossDevice))
			continue
		}
		return kind, nil
	}
	return 0, errors.Join(refusals...)
}

func kindRefusal(capabilities linker.Capabilities, kind linker.Kind) error {
	supported := map[linker.Kind]bool{
		linker.Hardlink: capabilities.Hardlink, linker.Reflink: capabilities.Reflink, linker.Symlink: capabilities.Symlink,
	}[kind]
	if supported {
		return nil
	}
	if reason := capabilities.Reasons[kind]; reason != nil {
		return reason
	}
	return linker.ErrUnsupported
}

// sameDevice : sans identité connue (Windows), on laisse la création du lien trancher, elle échoue proprement.
func sameDevice(first, second fs.FileInfo) bool {
	firstDevice, _, firstKnown := fileIdentity(first)
	secondDevice, _, secondKnown := fileIdentity(second)
	return !firstKnown || !secondKnown || firstDevice == secondDevice
}
