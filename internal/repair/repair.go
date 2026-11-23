// Package repair retrouve la nouvelle place d'un fichier de la bibliothèque renommé et propose de refaire le lien
// cassé ; seuls des liens sont remplacés, jamais un fichier de la bibliothèque.
package repair

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"necromanciarr/internal/dedupe"
	"necromanciarr/internal/store"
	"necromanciarr/internal/torrentcheck"
)

// RepairMethod nomme la méthode qui a retrouvé la nouvelle cible.
type RepairMethod string

// Méthodes de recherche, dans l'ordre où Scan les essaie.
const (
	ByInode          RepairMethod = "inode"
	BySizeAndModTime RepairMethod = "size-mtime"
	ByFingerprint    RepairMethod = "fingerprint"
	ByTorrentPieces  RepairMethod = "torrent-pieces"
)

const defaultMaxCandidatesToRead = 5

// Repair propose de faire pointer LinkPath vers NewTarget au lieu de OldTarget (texte du lien tel que lu). Size est la
// taille attendue du nouveau fichier. AutoApplicable n'est vrai que si Options.AutoApply est actif et que la méthode
// est assez sûre pour s'appliquer sans validation.
type Repair struct {
	LinkPath       string
	OldTarget      string
	NewTarget      string
	Size           int64
	Method         RepairMethod
	Candidates     []string
	Relative       bool
	AutoApplicable bool
}

// LinkStore est la part de la base utilisée par Scan ; implémenté par store.Store.
type LinkStore interface {
	ListLinks(ctx context.Context, state store.LinkState) ([]store.Link, error)
	SetLinkState(ctx context.Context, linkPath string, state store.LinkState, verifiedAt time.Time) error
}

// HashConfirmer confirme un candidat sans lire son contenu (somme MD5 du stockage distant) ; known est faux quand il
// ne sait pas répondre, Scan lit alors l'empreinte début et fin.
type HashConfirmer interface {
	Confirm(ctx context.Context, link store.Link, candidatePath string) (matches, known bool, err error)
}

// TorrentRef désigne un fichier d'un torrent du client : FileIndex indexe Torrent.Files.
type TorrentRef struct {
	Torrent   *torrentcheck.Torrent
	FileIndex int
}

// TorrentSource relie un lien inconnu à son torrent (chemin de sauvegarde et liste de fichiers du client).
type TorrentSource interface {
	TorrentForPath(ctx context.Context, path string) (TorrentRef, bool, error)
}

// Options règle Scan. HashConfirmer, TorrentSource, Store, Limiter et FingerprintCache nil désactivent la confirmation
// distante, la méthode par pièces, la mise à jour des états, le plafond de débit et le cache. RecordedModTimes donne la
// date de modification connue de chaque ancienne cible (clé : TargetPath), car la table links ne la conserve pas ;
// sans entrée, la méthode taille et date est sautée. Si LibraryRoots n'est pas vide, seuls les fichiers de
// l'inventaire situés dessous sont retenus. MaxCandidatesToRead plafonne les lectures par lien (défaut 5).
type Options struct {
	LibraryRoots        []string
	ScanDirectories     []string
	MaxCandidatesToRead int
	Limiter             dedupe.Limiter
	FingerprintCache    dedupe.FingerprintCache
	HashConfirmer       HashConfirmer
	TorrentSource       TorrentSource
	AutoApply           bool
	Store               LinkStore
	RecordedModTimes    map[string]time.Time
}

// AmbiguousLink est un lien cassé pour lequel plusieurs fichiers conviennent : aucun n'est choisi.
type AmbiguousLink struct {
	LinkPath   string
	Candidates []string
}

// Report liste, par chemin de lien trié, le sort de chaque lien examiné ; les effectifs sont les longueurs.
type Report struct {
	OK         []string
	Repairable []string
	Ambiguous  []AmbiguousLink
	Lost       []string
	Diverged   []string
}

// Scan examine les liens enregistrés (tous ceux de Options.Store si links est nil) puis, si TorrentSource est fourni,
// les liens cassés inconnus des ScanDirectories. Il ne modifie que les états de la base ; rien n'est réparé ici.
func Scan(ctx context.Context, options Options, links []store.Link, library []store.MediaFile) ([]Repair, Report, error) {
	run, err := newScanRun(ctx, options, library)
	if err != nil {
		return nil, Report{}, err
	}
	if links == nil && options.Store != nil {
		if links, err = options.Store.ListLinks(ctx, ""); err != nil {
			return nil, Report{}, err
		}
	}
	links = slices.Clone(links)
	sort.Slice(links, func(first, second int) bool { return links[first].LinkPath < links[second].LinkPath })
	recorded := map[string]struct{}{}
	for _, link := range links {
		recorded[link.LinkPath] = struct{}{}
		if err := run.examine(link); err != nil {
			return nil, Report{}, fmt.Errorf("%s: %w", link.LinkPath, err)
		}
	}
	if err := run.scanDirectories(recorded); err != nil {
		return nil, Report{}, err
	}
	sort.Slice(run.repairs, func(first, second int) bool { return run.repairs[first].LinkPath < run.repairs[second].LinkPath })
	run.report.sort()
	return run.repairs, run.report, nil
}

func (report *Report) sort() {
	sort.Strings(report.OK)
	sort.Strings(report.Repairable)
	sort.Strings(report.Lost)
	sort.Strings(report.Diverged)
	sort.Slice(report.Ambiguous, func(first, second int) bool {
		return report.Ambiguous[first].LinkPath < report.Ambiguous[second].LinkPath
	})
}

type scanRun struct {
	ctx     context.Context
	options Options
	bySize  map[int64][]store.MediaFile
	repairs []Repair
	report  Report
}

func newScanRun(ctx context.Context, options Options, library []store.MediaFile) (*scanRun, error) {
	if options.MaxCandidatesToRead <= 0 {
		options.MaxCandidatesToRead = defaultMaxCandidatesToRead
	}
	roots, err := absolutePaths(options.LibraryRoots)
	if err != nil {
		return nil, err
	}
	run := &scanRun{ctx: ctx, options: options, bySize: map[int64][]store.MediaFile{}}
	for _, file := range library {
		if len(roots) == 0 || isUnderAny(roots, file.Path) {
			run.bySize[file.Size] = append(run.bySize[file.Size], file)
		}
	}
	for _, sameSize := range run.bySize {
		sort.Slice(sameSize, func(first, second int) bool { return sameSize[first].Path < sameSize[second].Path })
	}
	return run, nil
}

func absolutePaths(paths []string) ([]string, error) {
	absolute := make([]string, 0, len(paths))
	for _, path := range paths {
		absolutePath, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		absolute = append(absolute, absolutePath)
	}
	return absolute, nil
}

func isUnderAny(roots []string, path string) bool {
	for _, root := range roots {
		relative, err := filepath.Rel(root, path)
		if err == nil && filepath.IsLocal(relative) {
			return true
		}
	}
	return false
}

func (run *scanRun) setState(link store.Link, state store.LinkState) error {
	if run.options.Store == nil {
		return nil
	}
	err := run.options.Store.SetLinkState(run.ctx, link.LinkPath, state, time.Now().UTC())
	if errors.Is(err, store.ErrLinkNotFound) {
		return nil
	}
	return err
}

// examine classe un lien enregistré ; seul un lien symbolique dont la cible a disparu est cherché dans la bibliothèque.
func (run *scanRun) examine(link store.Link) error {
	if err := run.ctx.Err(); err != nil {
		return err
	}
	linkInfo, err := os.Lstat(link.LinkPath)
	if errors.Is(err, os.ErrNotExist) {
		run.report.Lost = append(run.report.Lost, link.LinkPath)
		return run.setState(link, store.LinkLost)
	}
	if err != nil {
		return err
	}
	if linkInfo.Mode()&os.ModeSymlink == 0 {
		return run.examineNonSymlink(link, linkInfo)
	}
	if _, err := os.Stat(link.LinkPath); err == nil {
		run.report.OK = append(run.report.OK, link.LinkPath)
		return run.setState(link, store.LinkOK)
	}
	oldTarget, err := os.Readlink(link.LinkPath)
	if err != nil {
		return err
	}
	return run.repairBroken(link, oldTarget)
}

// examineNonSymlink : un hardlink ou une copie ne casse pas ; il diverge s'il ne partage plus l'inode du fichier.
func (run *scanRun) examineNonSymlink(link store.Link, linkInfo os.FileInfo) error {
	targetInfo, err := os.Stat(link.TargetPath)
	if err == nil && os.SameFile(linkInfo, targetInfo) {
		run.report.OK = append(run.report.OK, link.LinkPath)
		return run.setState(link, store.LinkOK)
	}
	run.report.Diverged = append(run.report.Diverged, link.LinkPath)
	return run.setState(link, store.LinkDiverged)
}

func (run *scanRun) repairBroken(link store.Link, oldTarget string) error {
	located, err := run.locate(link)
	if err != nil {
		return err
	}
	if err := run.setState(link, store.LinkBroken); err != nil {
		return err
	}
	run.conclude(link.LinkPath, oldTarget, link.Size, !filepath.IsAbs(oldTarget), located)
	return nil
}

// conclude range un résultat de recherche : un candidat unique devient une réparation, plusieurs sont signalés, aucun
// donne un lien perdu.
func (run *scanRun) conclude(linkPath, oldTarget string, size int64, relative bool, located located) {
	switch {
	case located.unique:
		run.repairs = append(run.repairs, Repair{
			LinkPath: linkPath, OldTarget: oldTarget, NewTarget: located.matches[0], Size: size, Method: located.method,
			Candidates: located.matches, Relative: relative, AutoApplicable: run.options.AutoApply && located.autoSafe,
		})
		run.report.Repairable = append(run.report.Repairable, linkPath)
	case len(located.matches) > 0:
		run.report.Ambiguous = append(run.report.Ambiguous, AmbiguousLink{LinkPath: linkPath, Candidates: located.matches})
	default:
		run.report.Lost = append(run.report.Lost, linkPath)
	}
}
