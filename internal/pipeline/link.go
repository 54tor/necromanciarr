package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"necromanciarr/internal/config"
	"necromanciarr/internal/dedupe"
	"necromanciarr/internal/linker"
	"necromanciarr/internal/matcher"
	"necromanciarr/internal/sources"
	"necromanciarr/internal/store"
	"necromanciarr/internal/torrentcheck"
)

// Erreurs typées de Link.
var (
	// ErrNoLinkDirs signale une configuration sans linkDirs : Link n'a nulle part où reconstituer les torrents.
	ErrNoLinkDirs = errors.New("pipeline: no link directory configured")
	// ErrLinkDirOverlapsLibrary : un dossier de liens est dans une racine de bibliothèque, ou la contient (chemins
	// résolus) ; rien n'est créé.
	ErrLinkDirOverlapsLibrary = errors.New("pipeline: a link directory overlaps a library root")
)

// Raisons propres à Link.
const (
	ReasonAlreadyLinked          = "already_linked"
	ReasonNoVerifiedAssignment   = "no_verified_assignment"
	ReasonAssignmentMismatch     = "assignment_mismatch"
	ReasonNoLinkDirOnSameDevice  = "no_link_dir_on_same_device"
	ReasonLinkKindRefused        = "link_kind_refused"
	ReasonLinkRootUnusable       = "link_root_unusable"
	ReasonCrossDevice            = "cross_device"
	ReasonDestinationConflict    = "destination_conflict"
	ReasonSourceChanged          = "source_changed"
	ReasonLinkFailed             = "link_failed"
	ReasonAnnexWriteFailed       = "annex_write_failed"
	ReasonLinkDirOverlapsLibrary = "link_dir_overlaps_library"
)

// linkRootMode : le dossier du torrent doit rester lisible par le client torrent, qui tourne souvent sous un autre compte.
const linkRootMode fs.FileMode = 0o755

// maxFolderNameBytes est la longueur maximale d'un nom de dossier sur les systèmes de fichiers courants (octets).
const maxFolderNameBytes = 255

// temporaryMarker est le motif des fichiers temporaires d'annexe laissés par un arrêt (linker.WriteVerifiedFile).
const temporaryMarker = ".necromanciarr-tmp-"

// runLink reconstitue, une proposition acceptée à la fois, l'arborescence de chaque torrent par des liens vers
// l'association prouvée, puis écrit ses annexes vérifiées ; rien n'est jamais écrit dans la bibliothèque. Une
// proposition sans annonce (no_announce) est refusée avant toute écriture.
func runLink(ctx context.Context, deps Deps) (Result, error) {
	var result Result
	if len(deps.LinkDirs) == 0 {
		return result, ErrNoLinkDirs
	}
	if err := checkLinkDirsOutsideLibrary(deps.LinkDirs, deps.LibraryRoots); err != nil {
		result.count(ReasonLinkDirOverlapsLibrary, 1)
		return result, err
	}
	work, err := loadAcceptedWork(ctx, deps)
	if err != nil {
		return result, err
	}
	if err := work.checkGroups(); err != nil {
		return result, err
	}
	run := linkRun{deps: deps, result: &result, probes: map[string]linker.Capabilities{}}
	linkedCandidates := work.linkedCandidates()
	for _, proposal := range work.proposals {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		candidate, reason := work.eligibility(proposal)
		if reason == "" && work.progress[proposal.ID].Step != "" {
			reason = ReasonAlreadyLinked
		}
		if reason == "" && linkedCandidates[candidate.ID] {
			reason = ReasonCandidateAlreadyLinked
		}
		if reason != "" {
			result.Skipped++
			result.count(reason, 1)
			continue
		}
		linkedCandidates[candidate.ID] = true
		if err := run.linkProposal(ctx, proposal, candidate, work.progress[proposal.ID].LinkRoot); err != nil {
			return result, err
		}
	}
	return result, nil
}

// checkLinkDirsOutsideLibrary compare les chemins résolus (liens symboliques suivis) : un dossier de liens égal à une
// racine de bibliothèque, ancêtre ou descendant, y ferait écrire liens et annexes.
func checkLinkDirsOutsideLibrary(linkDirs, libraryRoots []string) error {
	for linkIndex, linkDir := range linkDirs {
		for libraryIndex, libraryRoot := range libraryRoots {
			if config.PathsOverlap(resolvedPath(linkDir), resolvedPath(libraryRoot)) {
				return fmt.Errorf("%w: linkDirs[%d] and libraryRoots[%d]", ErrLinkDirOverlapsLibrary, linkIndex, libraryIndex)
			}
		}
	}
	return nil
}

// resolvedPath suit les liens symboliques ; un chemin absent reste tel quel, rendu absolu.
func resolvedPath(original string) string {
	absolute, err := filepath.Abs(original)
	if err != nil {
		return filepath.Clean(original)
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return resolved
	}
	return absolute
}

type linkRun struct {
	deps   Deps
	result *Result
	probes map[string]linker.Capabilities
}

// linkProposal ne renvoie une erreur que pour arrêter l'étape (store, annulation) ; un refus passe la proposition à
// failed avec sa raison. recordedRoot est le dossier déjà enregistré pour la proposition (vide s'il n'y en a pas).
func (run linkRun) linkProposal(ctx context.Context, proposal store.Proposal, candidate store.Candidate, recordedRoot string) error {
	// Sans annonce, Seed refuserait : rien n'est sondé ni créé, pour ne laisser aucun dossier de liens orphelin.
	if AnnounceSourceOf(run.deps.Announces, candidate) == sources.AnnounceNone {
		return failProposal(ctx, run.deps, run.result, proposal.ID, ReasonNoAnnounce)
	}
	proven, reason, err := loadProven(ctx, run.deps, proposal, candidate)
	switch {
	case err != nil:
		return err
	case reason == ReasonChangedFile:
		run.result.Skipped++
		run.result.count(ReasonChangedFile, 1)
		return run.deps.Store.SetCandidateState(ctx, candidate.ID, store.CandidateStale, run.deps.now())
	case reason != "":
		return failProposal(ctx, run.deps, run.result, proposal.ID, reason)
	}
	linkDir, kinds, reason := run.chooseLinkDir(firstSource(proven.assignment))
	if reason != "" {
		return failProposal(ctx, run.deps, run.result, proposal.ID, reason)
	}
	destRoot, err := chooseLinkRoot(linkDir, recordedRoot, proven.torrent, candidate.Infohash)
	if err != nil {
		return failProposal(ctx, run.deps, run.result, proposal.ID, ReasonLinkRootUnusable)
	}
	ops, err := linker.Plan(proven.torrent, proven.assignment, destRoot)
	if err != nil {
		return failProposal(ctx, run.deps, run.result, proposal.ID, ReasonLinkRootUnusable)
	}
	placement := &linkPlacement{destRoot: destRoot}
	reason, err = run.place(ctx, proposal.ID, proven, ops, kinds, placement)
	if err != nil || reason != "" {
		placement.rollback()
	}
	if err != nil {
		return err
	}
	if reason != "" {
		return failProposal(ctx, run.deps, run.result, proposal.ID, reason)
	}
	run.result.Processed++
	run.result.Linked++
	return nil
}

// place crée le dossier du torrent, réconcilie ce qui s'y trouve déjà (reprise après un arrêt), crée les liens et les
// annexes, enregistre chaque lien puis l'étape linked. Une raison non vide est un refus ; une erreur arrête l'étape.
// Dans les deux cas l'appelant défait ce que placement a créé.
func (run linkRun) place(ctx context.Context, proposalID int64, proven provenTorrent, ops []linker.Op, kinds []linker.Kind, placement *linkPlacement) (string, error) {
	rootUsable := placement.ensureRoot() == nil
	if !rootUsable {
		return ReasonLinkRootUnusable, nil
	}
	reconciled, reason, err := run.reconcile(ctx, proven, ops, placement.destRoot)
	if err != nil || reason != "" {
		return reason, err
	}
	applied, err := linker.Apply(reconciled.pending, kinds)
	if err != nil {
		return applyFailureReason(err), nil
	}
	placement.links = reconciled.pending
	if reason := run.writeAnnexes(proven, placement); reason != "" {
		return reason, nil
	}
	for opIndex, operation := range reconciled.pending {
		if err := recordLink(ctx, run.deps, operation, applied.Used[opIndex]); err != nil {
			return "", err
		}
	}
	for _, adopted := range reconciled.adopted {
		if err := recordLink(ctx, run.deps, adopted.operation, adopted.kind); err != nil {
			return "", err
		}
	}
	progress := store.ProposalProgress{ProposalID: proposalID, Step: store.StepLinked, LinkRoot: placement.destRoot}
	return "", run.deps.Store.SaveProposalProgress(ctx, progress, run.deps.now())
}

// reconciled sépare les liens à créer de ceux déjà présents avec le bon contenu (adoptés, à enregistrer).
type reconciled struct {
	pending []linker.Op
	adopted []adoptedLink
}

type adoptedLink struct {
	operation linker.Op
	kind      linker.Kind
}

// reconcile examine chaque destination déjà présente dans le dossier du torrent (le nôtre, nommé d'après l'infohash) :
// contenu identique à la source → adoptée sans nouvel Apply (un reflink interrompu avant son enregistrement compris) ;
// entrée différente mais enregistrée par cet outil, ou annexe différente → périmée ; toute autre entrée → litige.
// Les entrées périmées ne sont retirées que si le dossier ne contient que des entrées de cet outil.
func (run linkRun) reconcile(ctx context.Context, proven provenTorrent, ops []linker.Op, destRoot string) (reconciled, string, error) {
	links, err := run.deps.Store.ListLinks(ctx, "")
	if err != nil {
		return reconciled{}, "", err
	}
	recorded := make(map[string]string, len(links))
	for _, link := range links {
		recorded[link.LinkPath] = link.TargetPath
	}
	var result reconciled
	var stale []string
	for _, operation := range ops {
		if _, statErr := os.Lstat(operation.Destination); errors.Is(statErr, fs.ErrNotExist) {
			result.pending = append(result.pending, operation)
			continue
		}
		kind, matches, err := entryMatches(ctx, run.deps, operation.Destination, operation.Source)
		if err != nil {
			return reconciled{}, "", err
		}
		_, known := recorded[operation.Destination]
		switch {
		case matches && recorded[operation.Destination] != operation.Source:
			result.adopted = append(result.adopted, adoptedLink{operation: operation, kind: kind})
		case matches:
		case known:
			stale = append(stale, operation.Destination)
			result.pending = append(result.pending, operation)
		default:
			return reconciled{}, ReasonDestinationConflict, nil
		}
	}
	expectedAnnexes := map[string]bool{}
	for _, annex := range proven.annexes {
		path, pathErr := annexPath(proven.torrent, destRoot, annex)
		pathKnown := pathErr == nil
		if !pathKnown {
			return reconciled{}, ReasonAnnexWriteFailed, nil
		}
		expectedAnnexes[path] = true
		if _, statErr := os.Lstat(path); statErr == nil && !annexMatches(path, annex) {
			stale = append(stale, path)
		}
	}
	return result, removeStaleEntries(destRoot, stale, recorded, expectedAnnexes), nil
}

// removeStaleEntries retire les entrées périmées et les temporaires d'annexe, seulement si le dossier du torrent ne
// contient que des entrées de cet outil (liens enregistrés, annexes attendues, temporaires) ; sinon litige.
func removeStaleEntries(destRoot string, stale []string, recorded map[string]string, expectedAnnexes map[string]bool) string {
	if len(stale) == 0 {
		return ""
	}
	var temporaries []string
	onlyOurs := true
	walkErr := filepath.WalkDir(destRoot, func(entryPath string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		_, isRecorded := recorded[entryPath]
		isTemporary := strings.Contains(entry.Name(), temporaryMarker)
		if isTemporary {
			temporaries = append(temporaries, entryPath)
		}
		onlyOurs = onlyOurs && (isRecorded || expectedAnnexes[entryPath] || isTemporary)
		return nil
	})
	if walkErr != nil || !onlyOurs {
		return ReasonDestinationConflict
	}
	for _, entryPath := range append(stale, temporaries...) {
		if err := os.Remove(entryPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return ReasonDestinationConflict
		}
	}
	return ""
}

func applyFailureReason(err error) string {
	switch {
	case errors.Is(err, linker.ErrCrossDevice):
		return ReasonCrossDevice
	case errors.Is(err, linker.ErrLinkRefused), errors.Is(err, linker.ErrUnsupported):
		return ReasonLinkKindRefused
	case errors.Is(err, linker.ErrDestinationExists):
		return ReasonDestinationConflict
	case errors.Is(err, linker.ErrSizeMismatch), errors.Is(err, linker.ErrInvalidSource):
		return ReasonSourceChanged
	default:
		return ReasonLinkFailed
	}
}

func (run linkRun) writeAnnexes(proven provenTorrent, placement *linkPlacement) string {
	for _, annex := range proven.annexes {
		relative, err := linker.RelativeDestination(proven.torrent, annex.FileIndex)
		if err != nil {
			return ReasonAnnexWriteFailed
		}
		destination := filepath.Join(placement.destRoot, filepath.FromSlash(relative))
		_, statErr := os.Lstat(destination)
		err = linker.WriteVerifiedFile(placement.destRoot, relative, annex.Data)
		if errors.Is(err, linker.ErrDestinationExists) {
			return ReasonDestinationConflict
		}
		if err != nil {
			return ReasonAnnexWriteFailed
		}
		if errors.Is(statErr, fs.ErrNotExist) {
			placement.annexes = append(placement.annexes, destination)
		}
	}
	return ""
}

// recordLink enregistre le lien posé (ou la copie qui le remplace) avec sa sorte et l'instantané de sa source.
func recordLink(ctx context.Context, deps Deps, operation linker.Op, kind linker.Kind) error {
	info, err := os.Stat(operation.Source)
	if err != nil {
		return err
	}
	fingerprint, _, err := dedupe.HeadTailFingerprint(ctx, deps.Store, deps.ReadLimiter, operation.Source, info.Size(), info.ModTime(), true)
	if err != nil {
		return err
	}
	filesystem, _ := linker.FilesystemKindOf(operation.Source)
	now := deps.now()
	link := store.Link{
		Kind: kind.String(), LinkPath: operation.Destination, TargetPath: operation.Source, Size: info.Size(),
		Fingerprint: fingerprint, State: store.LinkOK, CreatedAt: now, VerifiedAt: now, ModTime: info.ModTime(),
		InodeReliable: filesystem == linker.Local,
	}
	if link.InodeReliable {
		link.TargetDevice, link.TargetInode, link.InodeReliable = dedupe.FileIdentity(info)
	}
	return deps.Store.RecordLink(ctx, link)
}

func firstSource(assignment matcher.Assignment) string {
	firstIndex := -1
	for fileIndex := range assignment.ByTorrentFile {
		if firstIndex < 0 || fileIndex < firstIndex {
			firstIndex = fileIndex
		}
	}
	return assignment.ByTorrentFile[firstIndex].Path
}

// chooseLinkDir prend le premier linkDirs du même système de fichiers que la source ; sans lui, seul un symlink reste
// possible, et seulement s'il est permis. Les sortes que le dossier refuse (sonde) sont écartées, jamais remplacées.
func (run linkRun) chooseLinkDir(source string) (string, []linker.Kind, string) {
	kinds := run.deps.LinkKinds
	if len(kinds) == 0 {
		kinds = []linker.Kind{linker.Hardlink}
	}
	linkDir, err := linker.ChooseLinkDir(source, run.deps.LinkDirs)
	if err != nil {
		kinds = keepKinds(kinds, func(kind linker.Kind) bool { return kind == linker.Symlink })
		if len(kinds) == 0 {
			return "", nil, ReasonNoLinkDirOnSameDevice
		}
		linkDir = run.deps.LinkDirs[0]
	}
	capabilities, err := run.probe(linkDir)
	if err != nil {
		return "", nil, ReasonLinkRootUnusable
	}
	kinds = keepKinds(kinds, func(kind linker.Kind) bool { return supportsKind(capabilities, kind) })
	if len(kinds) == 0 {
		return "", nil, ReasonLinkKindRefused
	}
	return linkDir, kinds, ""
}

func supportsKind(capabilities linker.Capabilities, kind linker.Kind) bool {
	switch kind {
	case linker.Hardlink:
		return capabilities.Hardlink
	case linker.Reflink:
		return capabilities.Reflink
	case linker.Symlink:
		return capabilities.Symlink
	default:
		return false
	}
}

func keepKinds(kinds []linker.Kind, keep func(linker.Kind) bool) []linker.Kind {
	var kept []linker.Kind
	for _, kind := range kinds {
		if keep(kind) {
			kept = append(kept, kind)
		}
	}
	return kept
}

// probe sonde une fois par exécution chaque dossier de liens (fichiers d'essai créés puis supprimés dans ce dossier).
func (run linkRun) probe(linkDir string) (linker.Capabilities, error) {
	if capabilities, known := run.probes[linkDir]; known {
		return capabilities, nil
	}
	probe := run.deps.ProbeLinks
	if probe == nil {
		probe = linker.Probe
	}
	capabilities, err := probe(linkDir)
	if err != nil {
		return linker.Capabilities{}, err
	}
	run.probes[linkDir] = capabilities
	return capabilities, nil
}

// chooseLinkRoot reprend le dossier déjà enregistré pour la proposition s'il est sous linkDir (un dossier d'une
// version antérieure, nommé autrement, reste ainsi utilisé) ; sinon il le nomme.
func chooseLinkRoot(linkDir, recordedRoot string, torrent *torrentcheck.Torrent, infohash string) (string, error) {
	isRecordedUnderLinkDir := recordedRoot != "" && filepath.Dir(filepath.Clean(recordedRoot)) == filepath.Clean(linkDir)
	if isRecordedUnderLinkDir {
		return recordedRoot, nil
	}
	return torrentLinkRoot(linkDir, torrent, infohash)
}

// torrentLinkRoot est le dossier propre au torrent sous linkDir, nommé d'après son nom racine et son infohash
// complet : deux torrents de même nom (autre tracker) n'y partagent jamais un dossier. Un nom trop long pour le système
// de fichiers raccourcit le nom racine, jamais l'infohash.
func torrentLinkRoot(linkDir string, torrent *torrentcheck.Torrent, infohash string) (string, error) {
	suffix := " [" + infohash + "]"
	name := truncateUTF8(torrent.Root, maxFolderNameBytes-len(suffix)) + suffix
	if strings.ContainsAny(name, `/\`) || !filepath.IsLocal(name) {
		return "", fmt.Errorf("%w: %q", linker.ErrUnsafePath, name)
	}
	return filepath.Join(linkDir, name), nil
}

// truncateUTF8 coupe text à maxBytes octets au plus sans couper un caractère, puis retire espaces et points finaux
// (refusés en fin de nom par Windows).
func truncateUTF8(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	cut := max(maxBytes, 0)
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return strings.TrimRight(text[:cut], " .")
}

// linkPlacement retient ce que cette exécution a créé pour un torrent, afin de le défaire en cas d'échec.
type linkPlacement struct {
	destRoot    string
	createdRoot bool
	links       []linker.Op
	annexes     []string
}

// ensureRoot crée le dossier du torrent avant Apply, qui exige que Op.Root existe ; un lien symbolique est refusé.
func (placement *linkPlacement) ensureRoot() error {
	info, err := os.Lstat(placement.destRoot)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.Mkdir(placement.destRoot, linkRootMode); err != nil { // #nosec G301 -- lisible par le client torrent
			return err
		}
		placement.createdRoot = true
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s", linker.ErrDestinationEscapesRoot, placement.destRoot)
	}
	return nil
}

// rollback retire ce que l'exécution a créé : le dossier du torrent entier s'il est neuf (il ne contient que des liens
// et des annexes posés ici ; RemoveAll ne suit pas les liens), sinon les seuls liens et annexes créés par l'exécution.
func (placement *linkPlacement) rollback() {
	if placement.createdRoot {
		_ = os.RemoveAll(placement.destRoot)
		return
	}
	for _, operation := range placement.links {
		_ = os.Remove(operation.Destination)
	}
	for _, annex := range placement.annexes {
		_ = os.Remove(annex)
	}
}
