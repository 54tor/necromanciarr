package pipeline

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"necromanciarr/internal/crossseed"
	"necromanciarr/internal/matcher"
	"necromanciarr/internal/store"
	"necromanciarr/internal/torrentcheck"
)

// ProposalDetails est le contenu de Proposal.Details pour les propositions créées par Verify. MissingPieces compte les
// pièces non vérifiées ; Candidates liste tous les candidats d'un groupe en conflit, sans en choisir aucun.
type ProposalDetails struct {
	Percent           float64         `json:"percent"`
	VerifiedPieces    int             `json:"verified_pieces"`
	MissingPieces     int             `json:"missing_pieces"`
	TotalPieces       int             `json:"total_pieces"`
	MissingMediaFiles []string        `json:"missing_media_files,omitempty"`
	AmbiguousFiles    []AmbiguousFile `json:"ambiguous_files,omitempty"`
	Candidates        []GroupMember   `json:"candidates,omitempty"`
}

// AmbiguousFile est un fichier du torrent que le hash n'a pas pu attribuer à un seul fichier local.
type AmbiguousFile struct {
	TorrentPath string   `json:"torrent_path"`
	LocalFiles  []string `json:"local_files"`
}

// GroupMember décrit un candidat d'un groupe en conflit et la proposition qui le porte.
type GroupMember struct {
	CandidateID int64                `json:"candidate_id"`
	Name        string               `json:"name"`
	Tracker     string               `json:"tracker"`
	State       store.CandidateState `json:"state"`
	Kind        store.ProposalKind   `json:"kind,omitempty"`
	ProposalID  int64                `json:"proposal_id,omitempty"`
	Percent     float64              `json:"percent"`
}

// verdict est la proposition à créer pour un candidat vérifié ; kind vide = aucune (média non vérifié).
type verdict struct {
	kind     store.ProposalKind
	warnings []string
	details  ProposalDetails
}

// classify range un candidat vérifié : un fichier ambigu est un conflit (jamais départagé ici), un média absent de la
// bibliothèque un pack partiel, 100 % des pièces un torrent complet, un média complet dont seules des pièces
// d'annexes manquent un partial_annex ; sinon aucune proposition.
func classify(torrent *torrentcheck.Torrent, assignment matcher.Assignment, report torrentcheck.Report) verdict {
	details := ProposalDetails{
		Percent: percentOf(report.VerifiedPieces, report.TotalPieces), VerifiedPieces: report.VerifiedPieces,
		MissingPieces: len(report.MissingPieces), TotalPieces: report.TotalPieces,
	}
	if len(assignment.Ambiguous) > 0 {
		details.AmbiguousFiles = ambiguousFiles(torrent, assignment)
		return verdict{kind: store.ProposalConflict, details: details}
	}
	if missing := missingMediaFiles(torrent, assignment); len(missing) > 0 {
		details.MissingMediaFiles = missing
		warning := fmt.Sprintf("%d media file(s) of this pack are absent from the library: ignored by default", len(missing))
		return verdict{kind: store.ProposalPartialPack, warnings: []string{warning}, details: details}
	}
	if report.TotalPieces > 0 && len(report.MissingPieces) == 0 {
		return verdict{kind: store.ProposalFull, details: details}
	}
	if report.MediaComplete {
		warning := fmt.Sprintf("will be added paused: %d piece(s) overlap annex files missing from the library, and the "+
			"client would rewrite those pieces across the linked media; it starts only with an explicit Force", len(report.MissingPieces))
		return verdict{kind: store.ProposalPartialAnnex, warnings: []string{warning}, details: details}
	}
	return verdict{}
}

func ambiguousFiles(torrent *torrentcheck.Torrent, assignment matcher.Assignment) []AmbiguousFile {
	var files []AmbiguousFile
	for _, fileIndex := range slices.Sorted(maps.Keys(assignment.Ambiguous)) {
		ambiguous := AmbiguousFile{TorrentPath: torrent.Files[fileIndex].Path}
		for _, local := range assignment.Ambiguous[fileIndex] {
			ambiguous.LocalFiles = append(ambiguous.LocalFiles, local.Path)
		}
		files = append(files, ambiguous)
	}
	return files
}

func missingMediaFiles(torrent *torrentcheck.Torrent, assignment matcher.Assignment) []string {
	var missing []string
	for fileIndex, file := range torrent.Files {
		_, assigned := assignment.ByTorrentFile[fileIndex]
		if file.Size > 0 && torrentcheck.IsMediaPath(file.Path) && !assigned {
			missing = append(missing, file.Path)
		}
	}
	return missing
}

// groupOf donne le groupe du candidat ; un candidat jamais regroupé forme son propre groupe.
func groupOf(candidate store.Candidate) int64 {
	if candidate.GroupID != 0 {
		return candidate.GroupID
	}
	return candidate.ID
}

// propose crée (ou met à jour si elle attend encore) la proposition du candidat vérifié.
func (run candidateRun) propose(ctx context.Context, candidate store.Candidate, torrent *torrentcheck.Torrent,
	assignment matcher.Assignment, report torrentcheck.Report,
) error {
	decided := classify(torrent, assignment, report)
	switch decided.kind {
	case "":
		run.result.count(ReasonMediaNotVerified, 1)
		return nil
	case store.ProposalPartialAnnex:
		run.result.PartialAnnex++
	case store.ProposalPartialPack:
		run.result.PartialPack++
	}
	created, err := upsertProposal(ctx, run.deps, store.Proposal{
		CandidateID: candidate.ID, GroupID: groupOf(candidate), Kind: decided.kind, Warnings: decided.warnings,
	}, decided.details)
	if created && decided.kind == store.ProposalConflict {
		run.result.Conflicts++
	}
	if created {
		run.result.ProposalsCreated++
	}
	return err
}

func upsertProposal(ctx context.Context, deps Deps, proposal store.Proposal, details ProposalDetails) (bool, error) {
	encoded, err := json.Marshal(details)
	if err != nil {
		return false, fmt.Errorf("encode proposal details: %w", err)
	}
	proposal.Details = encoded
	_, created, err := deps.Store.UpsertProposal(ctx, proposal, deps.now())
	return created, err
}

// proposalGroup rassemble les candidats d'un groupe et leurs propositions non closes.
type proposalGroup struct {
	groupID int64
	members []store.Candidate
	open    map[int64][]store.Proposal
}

// refreshGroups recalcule, pour chaque groupe, la proposition principale par défaut, le conflit qui liste tous les
// candidats et, en mode natif, les propositions cross_seed ; rejouable sans doublon.
func refreshGroups(ctx context.Context, deps Deps, result *Result) error {
	candidates, err := deps.Store.ListCandidatesByState(ctx)
	if err != nil {
		return err
	}
	proposals, err := deps.Store.ListProposals(ctx, "")
	if err != nil {
		return err
	}
	open := map[int64][]store.Proposal{}
	for _, proposal := range proposals {
		if proposal.Decision != store.ProposalSuperseded && proposal.CandidateID != 0 {
			open[proposal.CandidateID] = append(open[proposal.CandidateID], proposal)
		}
	}
	membersByGroup := map[int64][]store.Candidate{}
	for _, candidate := range candidates {
		if candidate.State != store.CandidateStale && candidate.State != store.CandidateFailed {
			membersByGroup[groupOf(candidate)] = append(membersByGroup[groupOf(candidate)], candidate)
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
	primaryCandidate, err := group.ensurePrimary(ctx, deps)
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

// mainProposal est la proposition qui porte le verdict du candidat (pas le conflit de groupe ni un cross_seed).
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

// parseDetails lit les détails ; des détails illisibles valent des détails vides (pourcentage nul, jamais principal).
func parseDetails(proposal store.Proposal) ProposalDetails {
	var details ProposalDetails
	_ = json.Unmarshal(proposal.Details, &details)
	return details
}

// syncGroupIDs rattache au groupe courant les propositions en attente regroupées depuis leur création.
func (group proposalGroup) syncGroupIDs(ctx context.Context, deps Deps) error {
	for _, member := range group.members {
		for _, proposal := range group.open[member.ID] {
			if proposal.Decision != store.ProposalPending || proposal.GroupID == group.groupID {
				continue
			}
			proposal.GroupID = group.groupID
			if _, _, err := deps.Store.UpsertProposal(ctx, proposal, deps.now()); err != nil {
				return err
			}
		}
	}
	return nil
}

// ensurePrimary garde la principale existante (choix éventuel de l'opérateur) ; sinon marque la proposition full ou
// partial_annex non rejetée au plus fort pourcentage, à égalité le plus petit candidat. Renvoie son candidat (0 si aucune).
func (group proposalGroup) ensurePrimary(ctx context.Context, deps Deps) (int64, error) {
	var best store.Proposal
	var bestPercent float64
	for _, member := range group.members {
		proposal, details, found := group.mainProposal(member.ID)
		isSeedable := found && (proposal.Kind == store.ProposalFull || proposal.Kind == store.ProposalPartialAnnex) &&
			proposal.Decision != store.ProposalRejected
		if !isSeedable {
			continue
		}
		if proposal.Primary {
			return proposal.CandidateID, nil
		}
		isBetter := best.ID == 0 || details.Percent > bestPercent ||
			(details.Percent == bestPercent && proposal.CandidateID < best.CandidateID)
		if isBetter {
			best, bestPercent = proposal, details.Percent
		}
	}
	if best.ID == 0 {
		return 0, nil
	}
	return best.CandidateID, deps.Store.SetGroupPrimary(ctx, group.groupID, best.ID)
}

// ensureConflict liste tous les candidats d'un groupe dès que deux d'entre eux ont une proposition ; le conflit est
// porté par le conflit de groupe existant, sinon par la principale, sinon par le premier candidat non ambigu.
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
	anchor := group.conflictAnchor(members, primaryCandidate)
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

func (group proposalGroup) conflictAnchor(members []GroupMember, primaryCandidate int64) int64 {
	for _, member := range members {
		for _, proposal := range group.open[member.CandidateID] {
			if proposal.Kind == store.ProposalConflict && len(parseDetails(proposal).Candidates) > 0 {
				return member.CandidateID
			}
		}
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
		isExtraFull := found && proposal.Kind == store.ProposalFull && proposal.Decision != store.ProposalRejected &&
			member.ID != primaryCandidate
		if !isExtraFull {
			continue
		}
		created, err := upsertProposal(ctx, deps, store.Proposal{CandidateID: member.ID, GroupID: group.groupID, Kind: store.ProposalCrossSeed},
			ProposalDetails{Percent: details.Percent, VerifiedPieces: details.VerifiedPieces, TotalPieces: details.TotalPieces})
		if err != nil {
			return err
		}
		if created {
			result.ProposalsCreated++
		}
	}
	return nil
}
