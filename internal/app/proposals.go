package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"necromanciarr/internal/crossseed"
	"necromanciarr/internal/pipeline"
	"necromanciarr/internal/store"
)

// Proposal est une proposition telle que la CLI et le web l'affichent : nature, décision, étape d'application,
// candidat (nom, taille, tracker), groupe, principale, génération et détails (pourcentage, pièces manquantes,
// fichiers exclus, litige). Current dit qu'elle est de la génération courante d'un candidat vérifié ; une proposition
// dedupe ou repair, sans candidat, est toujours courante (une proposition périmée y passe à superseded).
type Proposal struct {
	ID            int64                    `json:"id"`
	CandidateID   int64                    `json:"candidate_id"`
	CandidateName string                   `json:"candidate_name"`
	CandidateSize int64                    `json:"candidate_size"`
	Tracker       string                   `json:"tracker"`
	GroupID       int64                    `json:"group_id"`
	Kind          store.ProposalKind       `json:"kind"`
	Decision      store.ProposalDecision   `json:"decision"`
	Step          store.ProposalStep       `json:"step,omitempty"`
	Primary       bool                     `json:"primary"`
	Generation    int64                    `json:"generation"`
	Current       bool                     `json:"current"`
	Warnings      []string                 `json:"warnings"`
	Details       pipeline.ProposalDetails `json:"details"`
	// Maintenance porte le détail d'une proposition dedupe (DedupeDetails) ou repair (RepairDetails).
	Maintenance json.RawMessage `json:"maintenance,omitempty"`
}

// ProposalFilter restreint ListProposals : valeurs vides = pas de filtre ; CurrentOnly écarte les générations périmées.
type ProposalFilter struct {
	Decision    store.ProposalDecision
	Kind        store.ProposalKind
	GroupID     int64
	CurrentOnly bool
}

func (filter ProposalFilter) keeps(view Proposal) bool {
	rejectedByDecision := filter.Decision != "" && view.Decision != filter.Decision
	rejectedByKind := filter.Kind != "" && view.Kind != filter.Kind
	rejectedByGroup := filter.GroupID != 0 && view.GroupID != filter.GroupID
	rejectedAsStale := filter.CurrentOnly && !view.Current
	return !rejectedByDecision && !rejectedByKind && !rejectedByGroup && !rejectedAsStale
}

// snapshot est l'état lu une fois par action : propositions, chaque candidat (tout état) et étapes d'application.
type snapshot struct {
	proposals  []store.Proposal
	candidates map[int64]store.Candidate
	progress   map[int64]store.ProposalProgress
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
	progress, err := svc.deps.Store.ListProposalProgress(ctx)
	if err != nil {
		return snapshot{}, err
	}
	byID := make(map[int64]store.Candidate, len(candidates))
	for _, candidate := range candidates {
		byID[candidate.ID] = candidate
	}
	return snapshot{proposals: proposals, candidates: byID, progress: progress}, nil
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

func (state snapshot) view(proposal store.Proposal) Proposal {
	candidate := state.candidates[proposal.CandidateID]
	view := Proposal{
		ID: proposal.ID, CandidateID: proposal.CandidateID, CandidateName: candidate.Name, CandidateSize: candidate.TotalSize,
		Tracker: candidate.Tracker, GroupID: proposal.GroupID, Kind: proposal.Kind, Decision: proposal.Decision,
		Step: state.progress[proposal.ID].Step, Primary: proposal.Primary, Generation: proposal.Generation,
		Current: state.current(proposal), Warnings: proposal.Warnings,
	}
	if isMaintenanceKind(proposal.Kind) {
		view.Current, view.Maintenance = true, proposal.Details
		return view
	}
	_ = json.Unmarshal(proposal.Details, &view.Details)
	return view
}

func (svc *service) ListProposals(ctx context.Context, filter ProposalFilter) ([]Proposal, error) {
	state, err := svc.loadSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	var views []Proposal
	for _, proposal := range state.proposals {
		if view := state.view(proposal); filter.keeps(view) {
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
	if err := svc.checkFreeToAccept(state, proposal); err != nil {
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
func (svc *service) checkFreeToAccept(state snapshot, proposal store.Proposal) error {
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
		if svc.deps.CrossSeed == crossseed.Disabled && other.GroupID == proposal.GroupID {
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

func (svc *service) Force(ctx context.Context, proposalID int64) (ForceResult, error) {
	state, err := svc.loadSnapshot(ctx)
	if err != nil {
		return ForceResult{}, err
	}
	proposal, err := state.proposal(proposalID)
	if err != nil {
		return ForceResult{}, err
	}
	result := ForceResult{ProposalID: proposalID, Warnings: proposal.Warnings}
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
	if err := svc.deps.Client.Resume(ctx, state.candidates[proposal.CandidateID].Infohash); err != nil {
		return result, err
	}
	progress.Step = store.StepForced
	if err := svc.deps.Store.SaveProposalProgress(ctx, progress, svc.now()); err != nil {
		return result, err
	}
	result.Forced = true
	return result, nil
}

// checkSourcesUnchanged refait le contrôle de Link avant de démarrer : un fichier source changé depuis la vérification
// rend le candidat stale et l'action est refusée.
func (svc *service) checkSourcesUnchanged(ctx context.Context, candidateID int64) error {
	assignment, found, err := svc.deps.Store.LoadVerifiedAssignment(ctx, candidateID)
	if err != nil && !errors.Is(err, store.ErrAssignmentMismatch) {
		return err
	}
	if found && err == nil && !pipeline.AssignedFilesChanged(assignment.Files) {
		return nil
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
	targets, err := state.groupTargets(members, svc.deps.CrossSeed)
	if err != nil {
		return nil, err
	}
	accepted := make([]int64, 0, len(targets))
	for _, target := range targets {
		if !isOpenTorrentDecision(target) {
			if err := svc.checkFreeToAccept(state, target); err != nil {
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
