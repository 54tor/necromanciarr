package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"necromanciarr/internal/config"
	"necromanciarr/internal/crossseed"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/pipeline"
	"necromanciarr/internal/sources"
	"necromanciarr/internal/store"
)

// Proposal est une proposition telle que la CLI et le web l'affichent : nature, décision, étape d'application,
// candidat (nom, taille, tracker), groupe, principale, génération et détails (pourcentage, pièces manquantes,
// fichiers exclus, litige). Current dit qu'elle est de la génération courante d'un candidat vérifié ; une proposition
// dedupe ou repair, sans candidat, est toujours courante (une proposition périmée y passe à superseded).
type Proposal struct {
	ID            int64                  `json:"id"`
	CandidateID   int64                  `json:"candidate_id"`
	CandidateName string                 `json:"candidate_name"`
	CandidateSize int64                  `json:"candidate_size"`
	Tracker       string                 `json:"tracker"`
	GroupID       int64                  `json:"group_id"`
	Kind          store.ProposalKind     `json:"kind"`
	Decision      store.ProposalDecision `json:"decision"`
	Step          store.ProposalStep     `json:"step,omitempty"`
	Primary       bool                   `json:"primary"`
	Generation    int64                  `json:"generation"`
	Current       bool                   `json:"current"`
	// UpdatedAt est le plus récent des instants connus de la proposition (RFC 3339 UTC), vide si aucun.
	UpdatedAt string                   `json:"updated_at"`
	Warnings  []Warning                `json:"warnings"`
	Details   pipeline.ProposalDetails `json:"details"`
	// Maintenance porte le détail d'une proposition dedupe (DedupeDetails) ou repair (RepairDetails).
	Maintenance json.RawMessage `json:"maintenance,omitempty"`
}

// ProposalFilter restreint ListProposals : valeurs vides = pas de filtre ; CurrentOnly écarte les générations périmées ;
// Source garde les propositions dont le candidat vient de cette source (provider ou torznab:<nom>).
type ProposalFilter struct {
	Decision    store.ProposalDecision
	Kind        store.ProposalKind
	GroupID     int64
	CurrentOnly bool
	Source      string
}

// NewProposalFilter construit un filtre depuis des valeurs textuelles (requête web) sans dépendre du store.
func NewProposalFilter(decision, kind string, groupID int64, currentOnly bool, source string) ProposalFilter {
	return ProposalFilter{
		Decision: store.ProposalDecision(decision), Kind: store.ProposalKind(kind), GroupID: groupID, CurrentOnly: currentOnly,
		Source: source,
	}
}

// checkSource refuse un filtre de source qui ne peut nommer aucune source : une faute de frappe ne doit pas donner une
// liste vide.
func (filter ProposalFilter) checkSource() error {
	if filter.Source == "" || isSourceID(filter.Source) {
		return nil
	}
	return fmt.Errorf("%w: unknown source %q (valid: provider or torznab:<name>)", config.ErrUsage, filter.Source)
}

func (filter ProposalFilter) keeps(view Proposal) bool {
	rejectedByDecision := filter.Decision != "" && view.Decision != filter.Decision
	rejectedByKind := filter.Kind != "" && view.Kind != filter.Kind
	rejectedByGroup := filter.GroupID != 0 && view.GroupID != filter.GroupID
	rejectedAsStale := filter.CurrentOnly && !view.Current
	rejectedBySource := filter.Source != "" && view.Details.Source != filter.Source
	return !rejectedByDecision && !rejectedByKind && !rejectedByGroup && !rejectedAsStale && !rejectedBySource
}

// snapshot est l'état lu une fois par action : propositions, chaque candidat (tout état), étapes d'application et
// résolution des annonces d'après la configuration courante.
type snapshot struct {
	proposals  []store.Proposal
	candidates map[int64]store.Candidate
	progress   map[int64]store.ProposalProgress
	announces  sources.Resolver
}

func (svc *service) loadSnapshot(ctx context.Context) (snapshot, error) {
	proposals, err := svc.deps.Store.ListProposals(ctx, "")
	if err != nil {
		return snapshot{}, err
	}
	candidates, err := svc.deps.Store.ListCandidatesByState(ctx)
	if err != nil {
		return snapshot{}, err
	}
	announces := svc.announces()
	// Au mieux à la lecture : une base occupée ne doit pas priver l'opérateur de ses propositions ; l'annonce configurée
	// gagne de toute façon sur le scellé resté en place.
	if err := clearCoveredSealed(ctx, svc.deps.Store, announces, candidates); err != nil {
		svc.logLine(ctx, slog.LevelWarn, LogCodeSealedClearFailed, i18n.KeyLogSealedClearFailed, map[string]string{"error": err.Error()})
	}
	progress, err := svc.deps.Store.ListProposalProgress(ctx)
	if err != nil {
		return snapshot{}, err
	}
	byID := make(map[int64]store.Candidate, len(candidates))
	for _, candidate := range candidates {
		byID[candidate.ID] = candidate
	}
	return snapshot{proposals: proposals, candidates: byID, progress: progress, announces: announces}, nil
}

func (state snapshot) proposal(proposalID int64) (store.Proposal, error) {
	for _, proposal := range state.proposals {
		if proposal.ID == proposalID {
			return proposal, nil
		}
	}
	return store.Proposal{}, store.ErrProposalNotFound
}

// current dit si la proposition est de la génération courante d'un candidat vérifié.
func (state snapshot) current(proposal store.Proposal) bool {
	candidate, known := state.candidates[proposal.CandidateID]
	return known && candidate.State == store.CandidateVerified && candidate.Generation == proposal.Generation
}

// isOpenTorrentDecision : une proposition de torrent acceptée ou appliquée occupe son torrent (et, sans cross-seed,
// son groupe).
func isOpenTorrentDecision(proposal store.Proposal) bool {
	isTaken := proposal.Decision == store.ProposalAccepted || proposal.Decision == store.ProposalApplied
	return isTaken && pipeline.IsTorrentProposal(proposal.Kind)
}

func (state snapshot) view(translator i18n.Translator, proposal store.Proposal) Proposal {
	candidate := state.candidates[proposal.CandidateID]
	view := Proposal{
		ID: proposal.ID, CandidateID: proposal.CandidateID, CandidateName: candidate.Name, CandidateSize: candidate.TotalSize,
		Tracker: candidate.Tracker, GroupID: proposal.GroupID, Kind: proposal.Kind, Decision: proposal.Decision,
		Step: state.progress[proposal.ID].Step, Primary: proposal.Primary, Generation: proposal.Generation,
		Current: state.current(proposal), Warnings: warningViews(translator, proposal.Warnings),
		UpdatedAt: proposalUpdatedAt(proposal, state.progress[proposal.ID]),
	}
	if isMaintenanceKind(proposal.Kind) {
		view.Current, view.Maintenance = true, labelledMaintenance(translator, proposal)
		return view
	}
	_ = json.Unmarshal(proposal.Details, &view.Details)
	view.Details.Source = candidate.Source
	for position := range view.Details.ExcludedFiles {
		view.Details.ExcludedFiles[position].Label = translator.ReasonLabel(view.Details.ExcludedFiles[position].Reason)
	}
	return state.withAnnounce(translator, view)
}

// labelledMaintenance ajoute le libellé traduit du motif au détail dedupe ou repair ; un détail illisible reste tel quel.
func labelledMaintenance(translator i18n.Translator, proposal store.Proposal) json.RawMessage {
	var labelled any
	switch proposal.Kind {
	case store.ProposalDedupe:
		var details DedupeDetails
		if json.Unmarshal(proposal.Details, &details) != nil {
			return proposal.Details
		}
		labelled = details.labelled(translator)
	case store.ProposalRepair:
		var details RepairDetails
		if json.Unmarshal(proposal.Details, &details) != nil {
			return proposal.Details
		}
		labelled = details.labelled(translator)
	default:
		return proposal.Details
	}
	encoded, err := json.Marshal(labelled)
	if err != nil {
		return proposal.Details
	}
	return encoded
}

func (svc *service) ListProposals(ctx context.Context, filter ProposalFilter) ([]Proposal, error) {
	if err := filter.checkSource(); err != nil {
		return nil, err
	}
	state, err := svc.loadSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	var views []Proposal
	for _, proposal := range state.proposals {
		if view := state.view(svc.deps.Translator, proposal); filter.keeps(view) {
			views = append(views, view)
		}
	}
	return views, nil
}

func (svc *service) Accept(ctx context.Context, proposalID int64, options ...DecisionOption) error {
	state, err := svc.loadSnapshot(ctx)
	if err != nil {
		return err
	}
	proposal, err := state.proposal(proposalID)
	if err != nil {
		return err
	}
	if isMaintenanceKind(proposal.Kind) {
		return svc.acceptMaintenance(ctx, proposal)
	}
	if err := checkAcceptable(proposal, slices.Contains(options, AllowPartialPack)); err != nil {
		return err
	}
	behavior := svc.crossSeed(ctx).Behavior
	if proposal.Kind == store.ProposalCrossSeed && behavior != crossseed.Native {
		return ErrCrossSeedNotNative
	}
	if err := svc.checkFreeToAccept(state, proposal, behavior); err != nil {
		return err
	}
	return svc.accept(ctx, proposal)
}

func checkAcceptable(proposal store.Proposal, allowPartialPack bool) error {
	switch {
	case proposal.Kind == store.ProposalConflict:
		return ErrConflictNeedsChoose
	case proposal.Kind == store.ProposalPartialPack && !allowPartialPack:
		return ErrPartialPackNeedsForce
	case !pipeline.IsTorrentProposal(proposal.Kind):
		return ErrNotATorrentProposal
	}
	return nil
}

// checkFreeToAccept : génération courante, groupe cohérent, torrent pas déjà pris par une autre proposition, et sans
// cross-seed un seul torrent par groupe.
func (svc *service) checkFreeToAccept(state snapshot, proposal store.Proposal, behavior crossseed.Behavior) error {
	if !state.current(proposal) {
		return ErrStaleProposal
	}
	candidate := state.candidates[proposal.CandidateID]
	if proposal.GroupID != pipeline.GroupOf(candidate) {
		return fmt.Errorf("%w: proposal %d", pipeline.ErrGroupInconsistent, proposal.ID)
	}
	for _, other := range state.proposals {
		if other.ID == proposal.ID || !isOpenTorrentDecision(other) || !state.current(other) {
			continue
		}
		if other.CandidateID == proposal.CandidateID {
			return ErrCandidateAlreadyAccepted
		}
		if behavior == crossseed.Disabled && other.GroupID == proposal.GroupID {
			return ErrOneTorrentPerFile
		}
	}
	return nil
}

// accept passe à accepted (une proposition failed est d'abord remise en attente), tout ou rien, dans une transaction.
func (svc *service) accept(ctx context.Context, proposals ...store.Proposal) error {
	proposalIDs := make([]int64, 0, len(proposals))
	for _, proposal := range proposals {
		proposalIDs = append(proposalIDs, proposal.ID)
	}
	return mapStale(svc.deps.Store.AcceptProposals(ctx, proposalIDs, svc.now()))
}

func (svc *service) Reject(ctx context.Context, proposalID int64) error {
	return svc.deps.Store.SetProposalDecision(ctx, proposalID, store.ProposalRejected, svc.now())
}

func (svc *service) Choose(ctx context.Context, proposalID int64) error {
	state, err := svc.loadSnapshot(ctx)
	if err != nil {
		return err
	}
	proposal, err := state.proposal(proposalID)
	if err != nil {
		return err
	}
	if isMaintenanceKind(proposal.Kind) {
		return ErrNotATorrentProposal
	}
	if proposal.Kind != store.ProposalFull {
		return ErrNotFull
	}
	if !state.current(proposal) {
		return ErrStaleProposal
	}
	groupID := pipeline.GroupOf(state.candidates[proposal.CandidateID])
	if proposal.GroupID != groupID {
		return fmt.Errorf("%w: proposal %d", pipeline.ErrGroupInconsistent, proposal.ID)
	}
	return svc.deps.Store.SetGroupPrimary(ctx, groupID, proposalID, store.PrimaryOperator)
}

// lockNameForce nomme le verrou tenu par Force : deux Force simultanés ne contrôleraient pas la place libre ensemble.
const lockNameForce = "force"

func (svc *service) Force(ctx context.Context, proposalID int64) (ForceResult, error) {
	var result ForceResult
	err := svc.locked(ctx, lockNameForce, func(ctx context.Context) error {
		var forceErr error
		result, forceErr = svc.force(ctx, proposalID)
		return forceErr
	})
	if result.ProposalID == 0 {
		result.ProposalID = proposalID
	}
	return result, err
}

func (svc *service) force(ctx context.Context, proposalID int64) (ForceResult, error) {
	state, err := svc.loadSnapshot(ctx)
	if err != nil {
		return ForceResult{}, err
	}
	proposal, err := state.proposal(proposalID)
	if err != nil {
		return ForceResult{}, err
	}
	result := ForceResult{ProposalID: proposalID, Warnings: warningViews(svc.deps.Translator, proposal.Warnings)}
	progress := state.progress[proposal.ID]
	isPausedBySeed := proposal.Decision == store.ProposalApplied && progress.Step == store.StepSeededPausedIncomplete
	switch {
	case isMaintenanceKind(proposal.Kind):
		return result, ErrNotATorrentProposal
	case proposal.Kind == store.ProposalPartialPack:
		return result, ErrForceNotAllowed
	case proposal.Kind != store.ProposalPartialAnnex || !isPausedBySeed:
		return result, ErrNotForceable
	case !state.current(proposal):
		return result, ErrStaleProposal
	case svc.deps.Client == nil:
		return result, fmt.Errorf("%w: torrent client", ErrNotConfigured)
	}
	if err := svc.checkSourcesUnchanged(ctx, proposal.CandidateID); err != nil {
		return result, err
	}
	candidate := state.candidates[proposal.CandidateID]
	isolation, err := pipeline.IsolateStraddlingMedia(ctx, svc.deps.Stages, proposal, candidate, progress.LinkRoot)
	result.Replaced = isolation.Replaced
	if refused := forceRefusal(err); refused != nil {
		return result, refused
	}
	if errors.Is(err, pipeline.ErrIsolationImpossible) {
		return result, fmt.Errorf("%w: %w", ErrForceIsolationImpossible, err)
	}
	if err != nil {
		return result, err
	}
	if err := svc.deps.Client.Resume(ctx, candidate.Infohash); err != nil {
		return result, err
	}
	progress.Step = store.StepForced
	if err := svc.deps.Store.SaveProposalProgress(ctx, progress, svc.now()); err != nil {
		return result, err
	}
	result.Forced = true
	return result, nil
}

// forceRefusal range un refus de Force sur un volume distant sous son erreur d'action, avec le volume en paramètre
// du message ; nil pour toute autre erreur.
func forceRefusal(err error) error {
	var refusal *pipeline.ForceRefusal
	if !errors.As(err, &refusal) {
		return nil
	}
	typed := ErrForceServerCopyUnavailable
	if refusal.Reason == pipeline.ReasonFreeSpaceUnknown {
		typed = ErrForceFreeSpaceUnknown
	}
	return fmt.Errorf("%w: %w", typed, err)
}

// checkSourcesUnchanged refait le contrôle de Link avant de démarrer : un fichier source changé depuis la vérification
// rend le candidat stale et l'action est refusée.
func (svc *service) checkSourcesUnchanged(ctx context.Context, candidateID int64) error {
	assignment, found, err := svc.deps.Store.LoadVerifiedAssignment(ctx, candidateID)
	if err != nil && !errors.Is(err, store.ErrAssignmentMismatch) {
		return err
	}
	if found && err == nil {
		changed, statErr := pipeline.AssignedFilesChanged(svc.deps.Stages.Registry, assignment.Files)
		if statErr != nil {
			return statErr
		}
		if !changed {
			return nil
		}
	}
	if err := svc.deps.Store.SetCandidateState(ctx, candidateID, store.CandidateStale, svc.now()); err != nil {
		return err
	}
	return ErrStaleProposal
}

func (svc *service) AcceptGroup(ctx context.Context, groupID int64) ([]int64, error) {
	state, err := svc.loadSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	members, err := state.groupMembers(groupID)
	if err != nil {
		return nil, err
	}
	behavior := svc.crossSeed(ctx).Behavior
	targets, err := state.groupTargets(members, behavior)
	if err != nil {
		return nil, err
	}
	accepted := make([]int64, 0, len(targets))
	for _, target := range targets {
		if !isOpenTorrentDecision(target) {
			if err := svc.checkFreeToAccept(state, target, behavior); err != nil {
				return nil, err
			}
		}
		accepted = append(accepted, target.ID)
	}
	if err := svc.accept(ctx, targets...); err != nil {
		return nil, err
	}
	return accepted, nil
}

// groupMembers renvoie, triées par id, les propositions ouvertes de la génération courante de chaque candidat vérifié
// du groupe ; une proposition ouverte rattachée à un autre groupe que son candidat refuse tout.
func (state snapshot) groupMembers(groupID int64) ([]store.Proposal, error) {
	var members []store.Proposal
	groupKnown := false
	for _, proposal := range state.proposals {
		isOpen := proposal.Decision == store.ProposalPending || proposal.Decision == store.ProposalAccepted ||
			proposal.Decision == store.ProposalFailed || proposal.Decision == store.ProposalApplied
		if !isOpen || !state.current(proposal) || pipeline.GroupOf(state.candidates[proposal.CandidateID]) != groupID {
			continue
		}
		groupKnown = true
		if proposal.GroupID != groupID {
			return nil, fmt.Errorf("%w: proposal %d", pipeline.ErrGroupInconsistent, proposal.ID)
		}
		members = append(members, proposal)
	}
	if !groupKnown {
		return nil, ErrGroupNotFound
	}
	return members, nil
}

// groupTargets choisit les propositions à accepter : la principale toujours ; en Native seulement, pour chaque autre
// torrent vérifié, sa proposition cross_seed (à défaut sa full). Un torrent déjà pris n'est pas repris.
func (state snapshot) groupTargets(members []store.Proposal, behavior crossseed.Behavior) ([]store.Proposal, error) {
	primaryIndex := slices.IndexFunc(members, func(proposal store.Proposal) bool {
		isOpen := proposal.Decision == store.ProposalPending || proposal.Decision == store.ProposalAccepted
		return isOpen && proposal.Primary && proposal.Kind == store.ProposalFull
	})
	if primaryIndex < 0 {
		return nil, ErrNoPrimary
	}
	primary := members[primaryIndex]
	targets := []store.Proposal{primary}
	if behavior != crossseed.Native {
		return targets, nil
	}
	taken := map[int64]bool{primary.CandidateID: true}
	for _, proposal := range members {
		if isOpenTorrentDecision(proposal) {
			taken[proposal.CandidateID] = true
		}
	}
	for _, kind := range []store.ProposalKind{store.ProposalCrossSeed, store.ProposalFull} {
		for _, proposal := range members {
			isCandidateTarget := proposal.Kind == kind && proposal.Decision == store.ProposalPending && !taken[proposal.CandidateID]
			if isCandidateTarget {
				targets = append(targets, proposal)
				taken[proposal.CandidateID] = true
			}
		}
	}
	return targets, nil
}

// proposalUpdatedAt rend le plus récent de la création, de la décision et de la dernière étape d'application.
func proposalUpdatedAt(proposal store.Proposal, progress store.ProposalProgress) string {
	latest := proposal.CreatedAt
	for _, instant := range []time.Time{proposal.DecidedAt, progress.UpdatedAt} {
		if instant.After(latest) {
			latest = instant
		}
	}
	if latest.IsZero() {
		return ""
	}
	return latest.UTC().Format(time.RFC3339)
}
