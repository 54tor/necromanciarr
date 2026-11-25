package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"necromanciarr/internal/config"
	"necromanciarr/internal/dedupe"
	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/local"
	"necromanciarr/internal/linker"
	"necromanciarr/internal/matcher"
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
	// ReasonSymlinkOrderUnverified : le serveur SFTP n'a pas prouvé l'ordre des arguments d'un lien symbolique ; les
	// liens symboliques y sont refusés, la proposition reste acceptée.
	ReasonSymlinkOrderUnverified = "symlink_order_unverified"
	// ReasonRemoteExecRequired : le volume du dossier de liens est réglé exec required et son exécution distante n'est
	// pas utilisable ; la proposition reste acceptée, reprise à la relance.
	ReasonRemoteExecRequired = "remote_exec_required"
)

// ReasonLibraryRootUnavailable : une racine de bibliothèque ne se résout pas (absente, non montée), le registre refuse
// alors toute racine d'écriture.
const ReasonLibraryRootUnavailable = "library_root_unavailable"

// linkRootMode : le dossier du torrent doit rester lisible par le client torrent, qui tourne souvent sous un autre compte.
const linkRootMode fs.FileMode = 0o755

// maxFolderNameBytes est la longueur maximale d'un nom de dossier sur les systèmes de fichiers courants (octets).
const maxFolderNameBytes = 255

// temporaryMarker est le motif des fichiers temporaires d'annexe laissés par un arrêt (linker.WriteVerifiedFile).
const temporaryMarker = ".necromanciarr-tmp-"

// runLink reconstitue, une proposition acceptée à la fois, l'arborescence de chaque torrent par des liens vers
// l'association prouvée, puis écrit ses annexes vérifiées ; rien n'est jamais écrit dans la bibliothèque. Une
// proposition sans annonce (no_announce) ou au .torrent scellé sans clé (secret_key_missing) est refusée avant toute
// écriture. Toute écriture passe par une racine d'écriture du registre, toute lecture par sa vue en lecture.
func runLink(ctx context.Context, deps Deps) (Result, error) {
	var result Result
	if len(deps.LinkDirs) == 0 {
		return result, ErrNoLinkDirs
	}
	if err := checkLinkDirsOutsideLibrary(readRegistryOrLocal(deps.Registry), deps.LinkDirs, deps.LibraryRoots); err != nil {
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

// checkLinkDirsOutsideLibrary compare les chemins résolus par leur volume (liens symboliques suivis) : un dossier de
// liens égal à une racine de bibliothèque, ancêtre ou descendant, y ferait écrire liens et annexes.
func checkLinkDirsOutsideLibrary(registry *fsys.Registry, linkDirs, libraryRoots []string) error {
	for linkIndex, linkDir := range linkDirs {
		for libraryIndex, libraryRoot := range libraryRoots {
			if config.PathsOverlap(resolvedPath(registry, linkDir), resolvedPath(registry, libraryRoot)) {
				return fmt.Errorf("%w: linkDirs[%d] and libraryRoots[%d]", ErrLinkDirOverlapsLibrary, linkIndex, libraryIndex)
			}
		}
	}
	return nil
}

// resolvedPath suit les liens symboliques par le volume du chemin ; un chemin absent ou illisible reste tel quel.
func resolvedPath(registry *fsys.Registry, stored string) string {
	location, err := locationOfStoredPath(stored)
	if err != nil {
		return stored
	}
	readFS, name, err := registry.Read(location)
	if err != nil {
		return stored
	}
	canonical, err := readFS.Canonical(name)
	if err != nil {
		return stored
	}
	return storedIn(location.Volume, canonical)
}

type linkRun struct {
	deps   Deps
	result *Result
	probes map[string]linker.Capabilities
}

// linkProposal ne renvoie une erreur que pour arrêter l'étape (store, annulation) ; un refus passe la proposition à
// failed avec sa raison, sauf remote_unavailable qui la laisse acceptée pour la prochaine exécution. recordedRoot est
// le dossier déjà enregistré pour la proposition (vide s'il n'y en a pas).
func (run linkRun) linkProposal(ctx context.Context, proposal store.Proposal, candidate store.Candidate, recordedRoot string) error {
	reason, err := run.linkChecked(ctx, proposal, candidate, recordedRoot)
	if isRemoteUnavailable(err) {
		reason, err = ReasonRemoteUnavailable, nil
	}
	switch {
	case err != nil:
		return err
	case reason == ReasonRemoteUnavailable, reason == ReasonRemoteExecRequired:
		run.result.Skipped++
		run.result.count(reason, 1)
		return nil
	case reason == ReasonChangedFile:
		run.result.Skipped++
		run.result.count(ReasonChangedFile, 1)
		return run.deps.Store.SetCandidateState(ctx, candidate.ID, store.CandidateStale, run.deps.now())
	case reason != "":
		return failProposal(ctx, run.deps, run.result, proposal.ID, reason)
	}
	run.result.Processed++
	run.result.Linked++
	return nil
}

// linkChecked rend la raison d'un refus (vide : liée) ; une erreur arrête l'étape ou, distante, reporte la proposition.
func (run linkRun) linkChecked(ctx context.Context, proposal store.Proposal, candidate store.Candidate, recordedRoot string) (string, error) {
	// Ce que Seed refuserait d'avance (sans annonce, scellé sans clé) : rien n'est sondé ni créé, aucun dossier orphelin.
	if reason := announceRefusal(run.deps, candidate); reason != "" {
		return reason, nil
	}
	proven, reason, err := loadProven(ctx, run.deps, proposal, candidate)
	if err != nil || reason != "" {
		return reason, err
	}
	source, sourceErr := locationOfStoredPath(firstSource(proven.assignment))
	if sourceKnown := sourceErr == nil; !sourceKnown {
		return ReasonLinkRootUnusable, nil
	}
	linkDir, kinds, reason := run.chooseLinkDir(source)
	if reason != "" {
		return reason, nil
	}
	destRoot, ops, name, layoutErr := linkLayout(proven, linkDir, recordedRoot, candidate.Infohash)
	if layoutKnown := layoutErr == nil; !layoutKnown {
		return ReasonLinkRootUnusable, nil
	}
	placement := &linkPlacement{linkDir: linkDir, destRoot: destRoot, name: name}
	return run.place(ctx, proposal.ID, proven, ops, kinds, placement)
}

// linkLayout choisit le dossier du torrent sous linkDir, ses liens, et son nom relatif au dossier de liens.
func linkLayout(proven provenTorrent, linkDir fsys.Location, recordedRoot, infohash string) (fsys.Location, []linker.Op, string, error) {
	destRoot, err := chooseLinkRoot(linkDir, recordedRoot, proven.torrent, infohash)
	if err != nil {
		return fsys.Location{}, nil, "", err
	}
	ops, err := planOps(proven, destRoot)
	if err != nil {
		return fsys.Location{}, nil, "", err
	}
	name, err := relativeIn(linkDir, destRoot)
	return destRoot, ops, name, err
}

// planOps rend les liens du torrent sous root, sources et destinations en chemins du volume de root ; une source d'un
// autre volume est refusée (aucun lien ne traverse deux volumes).
func planOps(proven provenTorrent, root fsys.Location) ([]linker.Op, error) {
	ops, err := linker.Plan(proven.torrent, proven.assignment, root.Path, pathsOf(root))
	if err != nil {
		return nil, err
	}
	for opIndex := range ops {
		source, err := locationOfStoredPath(ops[opIndex].Source)
		if err != nil {
			return nil, err
		}
		if source.Volume != root.Volume {
			return nil, fmt.Errorf("%w: source on another volume", linker.ErrUnsafePath)
		}
		ops[opIndex].Source = source.Path
	}
	return ops, nil
}

// place ouvre la racine d'écriture du dossier de liens, y crée le dossier du torrent, réconcilie ce qui s'y trouve
// déjà (reprise après un arrêt), crée les liens et les annexes, enregistre chaque lien puis l'étape linked. Une raison
// non vide est un refus ; une erreur arrête l'étape. Dans les deux cas ce que l'exécution a créé est défait.
func (run linkRun) place(ctx context.Context, proposalID int64, proven provenTorrent, ops []linker.Op, kinds []linker.Kind, placement *linkPlacement) (string, error) {
	linkDirRoot, readFS, err := run.deps.openLinkRoot(placement.linkDir)
	if err != nil {
		return writeRootRefusalReason(err), nil
	}
	defer func() { _ = linkDirRoot.Close() }()
	placement.linkDirRoot, placement.read = linkDirRoot, readFS
	reason, err := run.placeIn(ctx, proposalID, proven, ops, kinds, placement)
	if err != nil || reason != "" {
		placement.rollback()
	}
	return reason, err
}

func (run linkRun) placeIn(ctx context.Context, proposalID int64, proven provenTorrent, ops []linker.Op, kinds []linker.Kind, placement *linkPlacement) (string, error) {
	if err := placement.ensureRoot(); err != nil {
		if isRemoteUnavailable(err) {
			return "", err
		}
		return ReasonLinkRootUnusable, nil
	}
	reconciled, reason, err := run.reconcile(ctx, proven, ops, placement)
	if err != nil || reason != "" {
		return reason, err
	}
	root, err := run.deps.openWriteRoot(placement.destRoot)
	if err != nil {
		return writeRootRefusalReason(err), nil
	}
	defer func() { _ = root.Close() }()
	applied, err := linker.Apply(root, placement.read, reconciled.pending, kinds)
	if err != nil {
		return applyFailureReason(err), nil
	}
	placement.links = reconciled.pending
	if reason := run.writeAnnexes(root, proven, placement); reason != "" {
		return reason, nil
	}
	volume := placement.destRoot.Volume
	for opIndex, operation := range reconciled.pending {
		if err := recordLink(ctx, run.deps, volume, operation, applied.Used[opIndex]); err != nil {
			return "", err
		}
	}
	for _, adopted := range reconciled.adopted {
		if err := recordLink(ctx, run.deps, volume, adopted.operation, adopted.kind); err != nil {
			return "", err
		}
	}
	progress := store.ProposalProgress{ProposalID: proposalID, Step: store.StepLinked, LinkRoot: placement.destRoot.String()}
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
func (run linkRun) reconcile(ctx context.Context, proven provenTorrent, ops []linker.Op, placement *linkPlacement) (reconciled, string, error) {
	links, err := run.deps.Store.ListLinks(ctx, "")
	if err != nil {
		return reconciled{}, "", err
	}
	recorded := make(map[string]string, len(links))
	for _, link := range links {
		recorded[link.LinkPath] = link.TargetPath
	}
	volume := placement.destRoot.Volume
	var result reconciled
	var stale []string
	for _, operation := range ops {
		if _, statErr := placement.read.Lstat(operation.Destination); errors.Is(statErr, fs.ErrNotExist) {
			result.pending = append(result.pending, operation)
			continue
		}
		match, err := entryMatches(ctx, run.deps, placement.read, operation.Destination, operation.Source)
		if err != nil {
			return reconciled{}, "", err
		}
		storedDestination := storedIn(volume, operation.Destination)
		recordedTarget, known := recorded[storedDestination]
		switch {
		case match.matches && recordedTarget != storedIn(volume, operation.Source):
			result.adopted = append(result.adopted, adoptedLink{operation: operation, kind: match.kind})
		case match.matches:
		case known:
			stale = append(stale, operation.Destination)
			result.pending = append(result.pending, operation)
		default:
			return reconciled{}, ReasonDestinationConflict, nil
		}
	}
	expectedAnnexes := map[string]bool{}
	for _, annex := range proven.annexes {
		path, pathErr := annexPath(proven.torrent, placement.destRoot, annex)
		pathKnown := pathErr == nil
		if !pathKnown {
			return reconciled{}, ReasonAnnexWriteFailed, nil
		}
		expectedAnnexes[path] = true
		if _, statErr := placement.read.Lstat(path); statErr != nil {
			continue
		}
		matches, err := annexMatches(placement.read, path, annex)
		if err != nil {
			return reconciled{}, "", err
		}
		if !matches {
			stale = append(stale, path)
		}
	}
	return result, placement.removeStaleEntries(stale, recorded, expectedAnnexes), nil
}

// removeStaleEntries retire les entrées périmées et les temporaires d'annexe, seulement si le dossier du torrent ne
// contient que des entrées de cet outil (liens enregistrés, annexes attendues, temporaires) ; sinon litige.
func (placement *linkPlacement) removeStaleEntries(stale []string, recorded map[string]string, expectedAnnexes map[string]bool) string {
	if len(stale) == 0 {
		return ""
	}
	volume := placement.destRoot.Volume
	var temporaries []string
	onlyOurs := true
	walkErr := walkFiles(placement.read, placement.destRoot, func(entryPath, name string) {
		_, isRecorded := recorded[storedIn(volume, entryPath)]
		isTemporary := strings.Contains(name, temporaryMarker)
		if isTemporary {
			temporaries = append(temporaries, entryPath)
		}
		onlyOurs = onlyOurs && (isRecorded || expectedAnnexes[entryPath] || isTemporary)
	})
	if isRemoteUnavailable(walkErr) {
		return ReasonRemoteUnavailable
	}
	if walkErr != nil || !onlyOurs {
		return ReasonDestinationConflict
	}
	for _, entryPath := range append(stale, temporaries...) {
		if err := placement.remove(entryPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return ReasonDestinationConflict
		}
	}
	return ""
}

// walkFiles visite chaque entrée qui n'est pas un dossier sous directory (un lien symbolique n'est jamais suivi).
func walkFiles(readFS fsys.ReadFS, directory fsys.Location, visit func(entryPath, name string)) error {
	entries, err := readFS.ReadDir(directory.Path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		child := joinLocation(directory, entry.Name())
		if entry.IsDir() {
			if err := walkFiles(readFS, child, visit); err != nil {
				return err
			}
			continue
		}
		visit(child.Path, entry.Name())
	}
	return nil
}

func applyFailureReason(err error) string {
	switch {
	case isRemoteUnavailable(err):
		return ReasonRemoteUnavailable
	case errors.Is(err, fsys.ErrSymlinkOrderUnverified):
		return ReasonSymlinkOrderUnverified
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

func (run linkRun) writeAnnexes(root fsys.WriteRoot, proven provenTorrent, placement *linkPlacement) string {
	for _, annex := range proven.annexes {
		relative, err := linker.RelativeDestination(proven.torrent, annex.FileIndex)
		if err != nil {
			return ReasonAnnexWriteFailed
		}
		destination := absoluteOf(placement.destRoot, relative)
		_, statErr := placement.read.Lstat(destination)
		err = linker.WriteVerifiedFile(root, relative, annex.Data)
		switch {
		case isRemoteUnavailable(err), isRemoteUnavailable(statErr):
			return ReasonRemoteUnavailable
		case errors.Is(err, linker.ErrDestinationExists):
			return ReasonDestinationConflict
		case err != nil:
			return ReasonAnnexWriteFailed
		}
		if errors.Is(statErr, fs.ErrNotExist) {
			placement.annexes = append(placement.annexes, destination)
		}
	}
	return ""
}

// recordLink enregistre le lien posé (ou la copie qui le remplace) avec sa sorte, son volume et l'instantané de sa
// source, lue par le registre sous le limiteur de lecture. Un lien distant n'a jamais d'inode fiable.
func recordLink(ctx context.Context, deps Deps, volume string, operation linker.Op, kind linker.Kind) error {
	readFS, name, err := readRegistryOrLocal(deps.Registry).Read(fsys.Location{Volume: volume, Path: operation.Source})
	if err != nil {
		return err
	}
	info, err := readFS.Stat(name)
	if err != nil {
		return err
	}
	targetPath := storedIn(volume, operation.Source)
	open := func() (dedupe.ReadAtCloser, error) { return readFS.Open(name) }
	fingerprint, err := dedupe.HeadTailFingerprintOf(ctx, deps.Store, deps.ReadLimiter, open, targetPath, info.Size(), info.ModTime())
	if err != nil {
		return err
	}
	now := deps.now()
	link := store.Link{
		Kind: kind.String(), LinkPath: storedIn(volume, operation.Destination), TargetPath: targetPath, Size: info.Size(),
		Fingerprint: fingerprint, State: store.LinkOK, CreatedAt: now, VerifiedAt: now, ModTime: info.ModTime(), Volume: volume,
	}
	if volume == "" {
		identity, reliable := local.ReliableFileID(info)
		link.TargetDevice, link.TargetInode, link.InodeReliable = identity.Device, identity.Inode, reliable
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
// possible, et seulement s'il est permis, vers le premier linkDirs du volume de la source. Les sortes que le dossier
// refuse (sonde) sont écartées, jamais remplacées.
func (run linkRun) chooseLinkDir(source fsys.Location) (fsys.Location, []linker.Kind, string) {
	kinds := run.deps.LinkKinds
	if len(kinds) == 0 {
		kinds = []linker.Kind{linker.Hardlink}
	}
	linkDirs := storedLocations(run.deps.LinkDirs)
	linkDir, err := linker.ChooseLinkDir(source, linkDirs, readRegistryOrLocal(run.deps.Registry))
	if isRemoteUnavailable(err) {
		return fsys.Location{}, nil, ReasonRemoteUnavailable
	}
	if err != nil {
		kinds = keepKinds(kinds, func(kind linker.Kind) bool { return kind == linker.Symlink })
		fallback, found := firstOnVolume(linkDirs, source.Volume)
		if len(kinds) == 0 || !found {
			return fsys.Location{}, nil, ReasonNoLinkDirOnSameDevice
		}
		linkDir = fallback
	}
	capabilities, err := run.probe(linkDir)
	if err != nil {
		return fsys.Location{}, nil, writeRootRefusalReason(err)
	}
	kinds = keepKinds(kinds, func(kind linker.Kind) bool { return supportsKind(capabilities, kind) })
	if len(kinds) == 0 {
		return fsys.Location{}, nil, refusedKindsReason(capabilities)
	}
	return linkDir, kinds, ""
}

// refusedKindsReason nomme un symlink refusé faute d'ordre des arguments prouvé (SFTP), sinon le refus générique.
func refusedKindsReason(capabilities linker.Capabilities) string {
	if errors.Is(capabilities.Reasons[linker.Symlink], fsys.ErrSymlinkOrderUnverified) {
		return ReasonSymlinkOrderUnverified
	}
	return ReasonLinkKindRefused
}

// firstOnVolume : un symlink ne relie que deux chemins du même volume.
func firstOnVolume(linkDirs []fsys.Location, volume string) (fsys.Location, bool) {
	for _, linkDir := range linkDirs {
		if linkDir.Volume == volume {
			return linkDir, true
		}
	}
	return fsys.Location{}, false
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
func (run linkRun) probe(linkDir fsys.Location) (linker.Capabilities, error) {
	key := linkDir.String()
	if capabilities, known := run.probes[key]; known {
		return capabilities, nil
	}
	probe := run.deps.ProbeLinks
	if probe == nil {
		probe = run.deps.probeLinkDir
	}
	capabilities, err := probe(key)
	if err != nil {
		return linker.Capabilities{}, err
	}
	run.probes[key] = capabilities
	return capabilities, nil
}

// chooseLinkRoot reprend le dossier déjà enregistré pour la proposition s'il est sous linkDir (un dossier d'une
// version antérieure, nommé autrement, reste ainsi utilisé) ; sinon il le nomme.
func chooseLinkRoot(linkDir fsys.Location, recordedRoot string, torrent *torrentcheck.Torrent, infohash string) (fsys.Location, error) {
	paths := pathsOf(linkDir)
	recorded, err := locationOfStoredPath(recordedRoot)
	isRecordedUnderLinkDir := recordedRoot != "" && err == nil && recorded.Volume == linkDir.Volume &&
		paths.Dir(paths.Clean(recorded.Path)) == paths.Clean(linkDir.Path)
	if isRecordedUnderLinkDir {
		return recorded, nil
	}
	return torrentLinkRootIn(linkDir, torrent, infohash)
}

// torrentLinkRoot est torrentLinkRootIn pour un dossier de liens sous sa forme enregistrée.
func torrentLinkRoot(linkDir string, torrent *torrentcheck.Torrent, infohash string) (string, error) {
	location, err := locationOfStoredPath(linkDir)
	if err != nil {
		return "", err
	}
	root, err := torrentLinkRootIn(location, torrent, infohash)
	if err != nil {
		return "", err
	}
	return root.String(), nil
}

// torrentLinkRootIn est le dossier propre au torrent sous linkDir, nommé d'après son nom racine et son infohash
// complet : deux torrents de même nom (autre tracker) n'y partagent jamais un dossier. Un nom trop long pour le système
// de fichiers raccourcit le nom racine, jamais l'infohash.
func torrentLinkRootIn(linkDir fsys.Location, torrent *torrentcheck.Torrent, infohash string) (fsys.Location, error) {
	suffix := " [" + infohash + "]"
	name := truncateUTF8(torrent.Root, maxFolderNameBytes-len(suffix)) + suffix
	if strings.ContainsAny(name, `/\`) || !filepath.IsLocal(name) {
		return fsys.Location{}, fmt.Errorf("%w: %q", linker.ErrUnsafePath, name)
	}
	return joinLocation(linkDir, name), nil
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

// linkPlacement retient ce que cette exécution a créé pour un torrent, afin de le défaire en cas d'échec. Toute
// écriture passe par linkDirRoot (racine d'écriture du dossier de liens), toute lecture par read ; name est le
// dossier du torrent relatif au dossier de liens.
type linkPlacement struct {
	linkDir     fsys.Location
	destRoot    fsys.Location
	name        string
	linkDirRoot fsys.WriteRoot
	read        fsys.ReadFS
	createdRoot bool
	links       []linker.Op
	annexes     []string
}

// ensureRoot crée le dossier du torrent avant Apply, qui exige que Op.Root existe ; un lien symbolique est refusé.
func (placement *linkPlacement) ensureRoot() error {
	info, err := placement.linkDirRoot.Lstat(placement.name)
	if errors.Is(err, fs.ErrNotExist) {
		if err := placement.linkDirRoot.MkdirAll(placement.name, linkRootMode); err != nil {
			return err
		}
		placement.createdRoot = true
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s", linker.ErrDestinationEscapesRoot, placement.name)
	}
	return nil
}

// remove retire une entrée du dossier du torrent par la racine d'écriture du dossier de liens.
func (placement *linkPlacement) remove(entryPath string) error {
	relative, err := relativeIn(placement.linkDir, fsys.Location{Volume: placement.linkDir.Volume, Path: entryPath})
	if err != nil {
		return err
	}
	return placement.linkDirRoot.Remove(relative)
}

// rollback retire les seuls liens et annexes créés par l'exécution ; un dossier du torrent neuf perd en plus ses
// dossiers restés vides, lui compris. Rien n'est retiré qui n'ait été posé ici.
func (placement *linkPlacement) rollback() {
	for _, operation := range placement.links {
		_ = placement.remove(operation.Destination)
	}
	for _, annex := range placement.annexes {
		_ = placement.remove(annex)
	}
	if placement.createdRoot {
		placement.removeEmptyDirectories(placement.name)
	}
}

// removeEmptyDirectories retire relative et ses sous-dossiers vides, du plus profond au plus haut ; un dossier qui
// garde une entrée reste en place (Remove refuse un dossier non vide).
func (placement *linkPlacement) removeEmptyDirectories(relative string) {
	entries, err := placement.read.ReadDir(absoluteOf(placement.linkDir, relative))
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			placement.removeEmptyDirectories(relative + relativeSeparator + entry.Name())
		}
	}
	_ = placement.linkDirRoot.Remove(relative)
}
