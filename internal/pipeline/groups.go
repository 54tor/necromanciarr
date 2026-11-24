package pipeline

import (
	"cmp"
	"context"
	"encoding/json"
	"maps"
	"slices"

	"necromanciarr/internal/crossseed"
	"necromanciarr/internal/store"
)

// proposalGroup rassemble chaque candidat d'un groupe et ses propositions non closes de sa génération courante.
type proposalGroup struct {
	groupID int64
	members []store.Candidate
	open    map[int64][]store.Proposal
}

// refreshGroups recalcule, pour chaque groupe : le rattachement des propositions regroupées, la principale (élue parmi
// les full, sauf choix de l'opérateur), le litige qui liste chaque torrent du groupe et, en mode natif, les propositions
// cross_seed ; rejouable sans doublon.
func refreshGroups(ctx context.Context, deps Deps, result *Result) error {
	candidates, err := deps.Store.ListCandidatesByState(ctx)
	if err != nil {
		return err
	}
	proposals, err := deps.Store.ListProposals(ctx, "")
	if err != nil {
		return err
	}
	generations := map[int64]int64{}
	membersByGroup := map[int64][]store.Candidate{}
	for _, candidate := range candidates {
		generations[candidate.ID] = candidate.Generation
		if candidate.State != store.CandidateStale && candidate.State != store.CandidateFailed {
			membersByGroup[GroupOf(candidate)] = append(membersByGroup[GroupOf(candidate)], candidate)
		}
	}
	open := map[int64][]store.Proposal{}
	for _, proposal := range proposals {
		isCurrent := proposal.CandidateID != 0 && proposal.Decision != store.ProposalSuperseded &&
			proposal.Generation == generations[proposal.CandidateID]
		if isCurrent {
			open[proposal.CandidateID] = append(open[proposal.CandidateID], proposal)
		}
	}
	for _, groupID := range slices.Sorted(maps.Keys(membersByGroup)) {
		group := proposalGroup{groupID: groupID, members: membersByGroup[groupID], open: open}
		if err := group.refresh(ctx, deps, result); err != nil {
			return err
		}
	}
	return nil
}

func (group proposalGroup) refresh(ctx context.Context, deps Deps, result *Result) error {
	if err := group.syncGroupIDs(ctx, deps); err != nil {
		return err
	}
	primaryCandidate, err := group.electPrimary(ctx, deps)
	if err != nil {
		return err
	}
	if err := group.ensureConflict(ctx, deps, result, primaryCandidate); err != nil {
		return err
	}
	if deps.CrossSeed != crossseed.Native || primaryCandidate == 0 {
		return nil
	}
	return group.ensureCrossSeeds(ctx, deps, result, primaryCandidate)
}

// isOpenDecision : pending ou accepted, les décisions qui comptent pour la principale (voir l'index du store).
func isOpenDecision(decision store.ProposalDecision) bool {
	return decision == store.ProposalPending || decision == store.ProposalAccepted
}

// syncGroupIDs rattache au groupe courant les propositions ouvertes regroupées depuis leur création (fusion de groupes) ;
// le store leur retire alors le rôle de principale, réélue ensuite.
func (group proposalGroup) syncGroupIDs(ctx context.Context, deps Deps) error {
	for _, member := range group.members {
		proposals := group.open[member.ID]
		for proposalIndex := range proposals {
			proposal := &proposals[proposalIndex]
			if !isOpenDecision(proposal.Decision) || proposal.GroupID == group.groupID {
				continue
			}
			if err := deps.Store.SetProposalGroup(ctx, proposal.ID, group.groupID); err != nil {
				return err
			}
			proposal.GroupID, proposal.Primary, proposal.PrimarySource = group.groupID, false, ""
		}
	}
	return nil
}

// mainProposal est la proposition qui porte le verdict du candidat (pas le litige de groupe ni un cross_seed).
func (group proposalGroup) mainProposal(candidateID int64) (store.Proposal, ProposalDetails, bool) {
	for _, proposal := range group.open[candidateID] {
		details := parseDetails(proposal)
		isVerdict := proposal.Kind == store.ProposalFull || proposal.Kind == store.ProposalPartialAnnex ||
			proposal.Kind == store.ProposalPartialPack || (proposal.Kind == store.ProposalConflict && len(details.AmbiguousFiles) > 0)
		if isVerdict {
			return proposal, details, true
		}
	}
	return store.Proposal{}, ProposalDetails{}, false
}

// parseDetails lit les détails ; des détails illisibles valent des détails vides (pourcentage nul).
func parseDetails(proposal store.Proposal) ProposalDetails {
	var details ProposalDetails
	_ = json.Unmarshal(proposal.Details, &details)
	return details
}

// electPrimary garde une principale choisie par l'opérateur tant qu'elle reste ouverte ; sinon élit la proposition full
// ouverte au plus fort pourcentage, à égalité le plus petit candidat. Un groupe sans full n'a pas de principale.
// Renvoie le candidat de la principale (0 si aucune).
func (group proposalGroup) electPrimary(ctx context.Context, deps Deps) (int64, error) {
	current := group.currentPrimary()
	if current.ID != 0 && current.PrimarySource == store.PrimaryOperator && current.Kind == store.ProposalFull {
		return current.CandidateID, nil
	}
	best := group.bestFull()
	if best.ID == 0 {
		if current.ID == 0 {
			return 0, nil
		}
		return 0, deps.Store.ClearGroupPrimary(ctx, group.groupID)
	}
	if current.ID == best.ID {
		return best.CandidateID, nil
	}
	return best.CandidateID, deps.Store.SetGroupPrimary(ctx, group.groupID, best.ID, store.PrimaryAuto)
}

func (group proposalGroup) currentPrimary() store.Proposal {
	for _, member := range group.members {
		for _, proposal := range group.open[member.ID] {
			if proposal.Primary && isOpenDecision(proposal.Decision) && proposal.GroupID == group.groupID {
				return proposal
			}
		}
	}
	return store.Proposal{}
}

func (group proposalGroup) bestFull() store.Proposal {
	var best store.Proposal
	var bestPercent float64
	for _, member := range group.members {
		proposal, details, found := group.mainProposal(member.ID)
		if !found || proposal.Kind != store.ProposalFull || !isOpenDecision(proposal.Decision) {
			continue
		}
		isBetter := best.ID == 0 || details.Percent > bestPercent ||
			(details.Percent == bestPercent && proposal.CandidateID < best.CandidateID)
		if isBetter {
			best, bestPercent = proposal, details.Percent
		}
	}
	return best
}

// ensureConflict liste chaque torrent d'un groupe dès que deux d'entre eux ont une proposition. Un litige en attente
// est mis à jour ; un litige déjà décidé (accepté ou rejeté) dont la composition a changé est clos et recréé.
func (group proposalGroup) ensureConflict(ctx context.Context, deps Deps, result *Result, primaryCandidate int64) error {
	members := group.describeMembers()
	withProposal := 0
	for _, member := range members {
		if member.ProposalID != 0 {
			withProposal++
		}
	}
	if withProposal < 2 {
		return nil
	}
	existing, found := group.groupConflict()
	if found && existing.Decision != store.ProposalPending {
		replaceable := existing.Decision == store.ProposalAccepted || existing.Decision == store.ProposalRejected
		if sameMembership(parseDetails(existing).Candidates, members) || !replaceable {
			return nil
		}
		if err := deps.Store.SetProposalDecision(ctx, existing.ID, store.ProposalSuperseded, deps.now()); err != nil {
			return err
		}
	}
	anchor := group.conflictAnchor(members, primaryCandidate, existing, found)
	if anchor == 0 {
		return nil
	}
	created, err := upsertProposal(ctx, deps, store.Proposal{CandidateID: anchor, GroupID: group.groupID, Kind: store.ProposalConflict},
		ProposalDetails{Candidates: members})
	if created {
		result.Conflicts++
		result.ProposalsCreated++
	}
	return err
}

func (group proposalGroup) groupConflict() (store.Proposal, bool) {
	for _, member := range group.members {
		for _, proposal := range group.open[member.ID] {
			if proposal.Kind == store.ProposalConflict && len(parseDetails(proposal).Candidates) > 0 {
				return proposal, true
			}
		}
	}
	return store.Proposal{}, false
}

func sameMembership(listed, current []GroupMember) bool {
	identifiers := func(members []GroupMember) []int64 {
		var candidateIDs []int64
		for _, member := range members {
			candidateIDs = append(candidateIDs, member.CandidateID)
		}
		slices.Sort(candidateIDs)
		return candidateIDs
	}
	return slices.Equal(identifiers(listed), identifiers(current))
}

func (group proposalGroup) describeMembers() []GroupMember {
	members := make([]GroupMember, 0, len(group.members))
	for _, candidate := range group.members {
		member := GroupMember{CandidateID: candidate.ID, Name: candidate.Name, Tracker: candidate.Tracker, State: candidate.State}
		if proposal, details, found := group.mainProposal(candidate.ID); found {
			member.Kind, member.ProposalID, member.Percent = proposal.Kind, proposal.ID, details.Percent
		}
		members = append(members, member)
	}
	slices.SortFunc(members, func(left, right GroupMember) int { return cmp.Compare(left.CandidateID, right.CandidateID) })
	return members
}

// conflictAnchor : le porteur du litige déjà présent, sinon la principale, sinon le premier candidat non ambigu.
func (group proposalGroup) conflictAnchor(members []GroupMember, primaryCandidate int64, existing store.Proposal, found bool) int64 {
	if found {
		return existing.CandidateID
	}
	if primaryCandidate != 0 {
		return primaryCandidate
	}
	for _, member := range members {
		if member.ProposalID != 0 && member.Kind != store.ProposalConflict {
			return member.CandidateID
		}
	}
	return 0
}

// ensureCrossSeeds propose, en mode natif seulement, chaque autre torrent complet du groupe en cross_seed de la principale.
func (group proposalGroup) ensureCrossSeeds(ctx context.Context, deps Deps, result *Result, primaryCandidate int64) error {
	for _, member := range group.members {
		proposal, details, found := group.mainProposal(member.ID)
		isExtraFull := found && proposal.Kind == store.ProposalFull && isOpenDecision(proposal.Decision) && member.ID != primaryCandidate
		if !isExtraFull {
			continue
		}
		created, err := upsertProposal(ctx, deps, store.Proposal{CandidateID: member.ID, GroupID: group.groupID, Kind: store.ProposalCrossSeed},
			ProposalDetails{
				Percent: details.Percent, VerifiedPieces: details.VerifiedPieces, TotalPieces: details.TotalPieces,
				ExcludedFiles: details.ExcludedFiles,
			})
		if err != nil {
			return err
		}
		if created {
			result.ProposalsCreated++
		}
	}
	return nil
}
