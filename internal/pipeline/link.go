package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"necromanciarr/internal/dedupe"
	"necromanciarr/internal/linker"
	"necromanciarr/internal/matcher"
	"necromanciarr/internal/store"
	"necromanciarr/internal/torrentcheck"
)

// ErrNoLinkDirs signale une configuration sans linkDirs : Link n'a nulle part où reconstituer les torrents.
var ErrNoLinkDirs = errors.New("pipeline: no link directory configured")

// Raisons propres à Link.
const (
	ReasonAlreadyLinked         = "already_linked"
	ReasonNoVerifiedAssignment  = "no_verified_assignment"
	ReasonAssignmentMismatch    = "assignment_mismatch"
	ReasonNoLinkDirOnSameDevice = "no_link_dir_on_same_device"
	ReasonLinkKindRefused       = "link_kind_refused"
	ReasonLinkRootUnusable      = "link_root_unusable"
	ReasonCrossDevice           = "cross_device"
	ReasonDestinationExists     = "destination_exists"
	ReasonSourceChanged         = "source_changed"
	ReasonLinkFailed            = "link_failed"
	ReasonAnnexWriteFailed      = "annex_write_failed"
)

// linkRootMode : le dossier du torrent doit rester lisible par le client torrent, qui tourne souvent sous un autre compte.
const linkRootMode fs.FileMode = 0o755

// shortHashLength : 8 caractères d'infohash suffisent à séparer deux torrents de même nom racine (autre tracker).
const shortHashLength = 8

// runLink reconstitue, une proposition acceptée à la fois, l'arborescence de chaque torrent par des liens vers
// l'association prouvée, puis écrit ses annexes vérifiées ; rien n'est jamais écrit dans la bibliothèque.
func runLink(ctx context.Context, deps Deps) (Result, error) {
	var result Result
	if len(deps.LinkDirs) == 0 {
		return result, ErrNoLinkDirs
	}
	work, err := loadAcceptedWork(ctx, deps)
	if err != nil {
		return result, err
	}
	if err := work.checkGroups(); err != nil {
		return result, err
	}
	run := linkRun{deps: deps, result: &result, probes: map[string]linker.Capabilities{}}
	linkedCandidates := map[int64]bool{}
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
		if err := run.linkProposal(ctx, proposal, candidate); err != nil {
			return result, err
		}
	}
	return result, nil
}

type linkRun struct {
	deps   Deps
	result *Result
	probes map[string]linker.Capabilities
}

// linkProposal ne renvoie une erreur que pour arrêter l'étape (store, annulation) ; un refus passe la proposition à
// failed avec sa raison.
func (run linkRun) linkProposal(ctx context.Context, proposal store.Proposal, candidate store.Candidate) error {
	assignment, found, err := run.deps.Store.LoadVerifiedAssignment(ctx, candidate.ID)
	switch {
	case errors.Is(err, store.ErrAssignmentMismatch):
		return failProposal(ctx, run.deps, run.result, proposal.ID, ReasonAssignmentMismatch)
	case err != nil:
		return err
	case !found:
		return failProposal(ctx, run.deps, run.result, proposal.ID, ReasonNoVerifiedAssignment)
	}
	if assignedFilesChanged(assignment.Files) {
		run.result.Skipped++
		run.result.count(ReasonChangedFile, 1)
		return run.deps.Store.SetCandidateState(ctx, candidate.ID, store.CandidateStale, run.deps.now())
	}
	raw, err := run.deps.Store.CandidateTorrent(ctx, candidate.ID)
	if err != nil {
		return err
	}
	torrent, err := torrentcheck.Parse(raw)
	if err != nil {
		return failProposal(ctx, run.deps, run.result, proposal.ID, ReasonInvalidTorrent)
	}
	return run.linkTorrent(ctx, proposal, candidate, torrent, provenAssignment(torrent, assignment, excludedPaths(proposal)))
}

// assignedFilesChanged compare chaque fichier associé à son instantané de vérification (taille, date à la nanoseconde).
func assignedFilesChanged(files []store.AssignedFile) bool {
	for _, file := range files {
		info, err := os.Stat(file.Path)
		if err != nil || info.Size() != file.Size || !info.ModTime().Equal(file.ModTime) {
			return true
		}
	}
	return false
}

func excludedPaths(proposal store.Proposal) map[string]bool {
	var details ProposalDetails
	excluded := map[string]bool{}
	if json.Unmarshal(proposal.Details, &details) != nil {
		return excluded
	}
	for _, file := range details.ExcludedFiles {
		excluded[file.TorrentPath] = true
	}
	return excluded
}

// provenAssignment reprend exactement l'association prouvée (jamais de nouvel appel à matcher.Assign), sans les
// fichiers écartés par Verify.
func provenAssignment(torrent *torrentcheck.Torrent, assignment store.VerifiedAssignment, excluded map[string]bool) matcher.Assignment {
	proven := matcher.Assignment{ByTorrentFile: map[int]store.MediaFile{}}
	for _, file := range assignment.Files {
		isKnownIndex := file.TorrentFileIndex >= 0 && file.TorrentFileIndex < len(torrent.Files)
		if isKnownIndex && excluded[torrent.Files[file.TorrentFileIndex].Path] {
			continue
		}
		proven.ByTorrentFile[file.TorrentFileIndex] = store.MediaFile{ID: file.MediaFileID, Path: file.Path, Size: file.Size, ModTime: file.ModTime}
	}
	return proven
}

func (run linkRun) linkTorrent(ctx context.Context, proposal store.Proposal, candidate store.Candidate, torrent *torrentcheck.Torrent, assignment matcher.Assignment) error {
	if len(assignment.ByTorrentFile) == 0 {
		return failProposal(ctx, run.deps, run.result, proposal.ID, ReasonNoVerifiedAssignment)
	}
	linkDir, kinds, reason := run.chooseLinkDir(firstSource(assignment))
	if reason != "" {
		return failProposal(ctx, run.deps, run.result, proposal.ID, reason)
	}
	destRoot, err := torrentLinkRoot(linkDir, torrent, candidate.Infohash)
	if err != nil {
		return failProposal(ctx, run.deps, run.result, proposal.ID, ReasonLinkRootUnusable)
	}
	ops, err := linker.Plan(torrent, assignment, destRoot)
	if err != nil {
		return failProposal(ctx, run.deps, run.result, proposal.ID, ReasonLinkRootUnusable)
	}
	placement := &linkPlacement{destRoot: destRoot}
	reason, err = run.place(ctx, proposal.ID, candidate, torrent, ops, kinds, placement)
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

// place crée le dossier du torrent, les liens, les annexes, enregistre chaque lien puis l'étape linked. Une raison non
// vide est un refus ; une erreur arrête l'étape. Dans les deux cas l'appelant défait ce que placement a créé.
func (run linkRun) place(ctx context.Context, proposalID int64, candidate store.Candidate, torrent *torrentcheck.Torrent, ops []linker.Op, kinds []linker.Kind, placement *linkPlacement) (string, error) {
	rootUsable := placement.ensureRoot() == nil
	if !rootUsable {
		return ReasonLinkRootUnusable, nil
	}
	pending, err := run.pendingOps(ctx, ops)
	if err != nil {
		return "", err
	}
	placement.notePreexisting(pending)
	applied, err := linker.Apply(pending, kinds)
	if err != nil {
		return applyFailureReason(err), nil
	}
	placement.links = pending
	if reason := run.writeAnnexes(ctx, candidate.ID, torrent, placement); reason != "" {
		return reason, nil
	}
	for opIndex, operation := range pending {
		if err := run.recordLink(ctx, operation, applied.Used[opIndex]); err != nil {
			return "", err
		}
	}
	progress := store.ProposalProgress{ProposalID: proposalID, Step: store.StepLinked, LinkRoot: placement.destRoot}
	return "", run.deps.Store.SaveProposalProgress(ctx, progress, run.deps.now())
}

// pendingOps écarte les liens déjà enregistrés vers la même source et encore présents : un reflink ne se reconnaît pas
// d'une copie, il ne doit jamais être recréé.
func (run linkRun) pendingOps(ctx context.Context, ops []linker.Op) ([]linker.Op, error) {
	links, err := run.deps.Store.ListLinks(ctx, "")
	if err != nil {
		return nil, err
	}
	recorded := make(map[string]string, len(links))
	for _, link := range links {
		recorded[link.LinkPath] = link.TargetPath
	}
	pending := make([]linker.Op, 0, len(ops))
	for _, operation := range ops {
		target, known := recorded[operation.Destination]
		_, statErr := os.Lstat(operation.Destination)
		if known && target == operation.Source && statErr == nil {
			continue
		}
		pending = append(pending, operation)
	}
	return pending, nil
}

func applyFailureReason(err error) string {
	switch {
	case errors.Is(err, linker.ErrCrossDevice):
		return ReasonCrossDevice
	case errors.Is(err, linker.ErrLinkRefused), errors.Is(err, linker.ErrUnsupported):
		return ReasonLinkKindRefused
	case errors.Is(err, linker.ErrDestinationExists):
		return ReasonDestinationExists
	case errors.Is(err, linker.ErrSizeMismatch), errors.Is(err, linker.ErrInvalidSource):
		return ReasonSourceChanged
	default:
		return ReasonLinkFailed
	}
}

func (run linkRun) writeAnnexes(ctx context.Context, candidateID int64, torrent *torrentcheck.Torrent, placement *linkPlacement) string {
	annexes, err := run.deps.Store.ListVerifiedFiles(ctx, candidateID)
	if err != nil {
		return ReasonAnnexWriteFailed
	}
	for _, annex := range annexes {
		relative, err := linker.RelativeDestination(torrent, annex.FileIndex)
		if err != nil {
			return ReasonAnnexWriteFailed
		}
		destination := filepath.Join(placement.destRoot, filepath.FromSlash(relative))
		_, statErr := os.Lstat(destination)
		if err := linker.WriteVerifiedFile(placement.destRoot, relative, annex.Data); err != nil {
			return ReasonAnnexWriteFailed
		}
		if errors.Is(statErr, fs.ErrNotExist) {
			placement.annexes = append(placement.annexes, destination)
		}
	}
	return ""
}

func (run linkRun) recordLink(ctx context.Context, operation linker.Op, kind linker.Kind) error {
	info, err := os.Stat(operation.Source)
	if err != nil {
		return err
	}
	fingerprint, _, err := dedupe.HeadTailFingerprint(ctx, run.deps.Store, run.deps.ReadLimiter, operation.Source, info.Size(), info.ModTime(), true)
	if err != nil {
		return err
	}
	filesystem, _ := linker.FilesystemKindOf(operation.Source)
	now := run.deps.now()
	link := store.Link{
		Kind: kind.String(), LinkPath: operation.Destination, TargetPath: operation.Source, Size: info.Size(),
		Fingerprint: fingerprint, State: store.LinkOK, CreatedAt: now, VerifiedAt: now, ModTime: info.ModTime(),
		InodeReliable: filesystem == linker.Local,
	}
	if link.InodeReliable {
		link.TargetDevice, link.TargetInode, link.InodeReliable = dedupe.FileIdentity(info)
	}
	return run.deps.Store.RecordLink(ctx, link)
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

// torrentLinkRoot est le dossier propre au torrent sous linkDir, nommé d'après son nom racine et le début de son
// infohash : deux torrents de même nom (autre tracker) n'y partagent jamais un dossier.
func torrentLinkRoot(linkDir string, torrent *torrentcheck.Torrent, infohash string) (string, error) {
	name := fmt.Sprintf("%s [%s]", torrent.Root, infohash[:min(shortHashLength, len(infohash))])
	if strings.ContainsAny(name, `/\`) || !filepath.IsLocal(name) {
		return "", fmt.Errorf("%w: %q", linker.ErrUnsafePath, name)
	}
	return filepath.Join(linkDir, name), nil
}

// linkPlacement retient ce que cette exécution a créé pour un torrent, afin de le défaire en cas d'échec.
type linkPlacement struct {
	destRoot    string
	createdRoot bool
	preexisting map[string]bool
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

func (placement *linkPlacement) notePreexisting(ops []linker.Op) {
	placement.preexisting = map[string]bool{}
	for _, operation := range ops {
		if _, err := os.Lstat(operation.Destination); err == nil {
			placement.preexisting[operation.Destination] = true
		}
	}
}

// rollback retire ce que l'exécution a créé : le dossier du torrent entier s'il est neuf (il ne contient que des liens
// et des annexes posés ici ; RemoveAll ne suit pas les liens), sinon les seuls liens et annexes nouveaux.
func (placement *linkPlacement) rollback() {
	if placement.createdRoot {
		_ = os.RemoveAll(placement.destRoot)
		return
	}
	for _, operation := range placement.links {
		if !placement.preexisting[operation.Destination] {
			_ = os.Remove(operation.Destination)
		}
	}
	for _, annex := range placement.annexes {
		_ = os.Remove(annex)
	}
}
