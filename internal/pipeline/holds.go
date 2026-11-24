package pipeline

import (
	"context"
	"encoding/json"
	"fmt"

	"necromanciarr/internal/store"
)

// isCappedKind dit si la nature est soumise au plafond de pièces manquantes : seuls les torrents partiels le sont.
func isCappedKind(kind store.ProposalKind) bool {
	return kind == store.ProposalPartialAnnex || kind == store.ProposalPartialPack
}

// aboveCap dit si les pièces manquantes dépassent le plafond ; égal au plafond reste permis.
func (deps Deps) aboveCap(kind store.ProposalKind, details ProposalDetails) bool {
	if deps.PartialMaxMissingPercent == nil || !isCappedKind(kind) || details.TotalPieces == 0 {
		return false
	}
	missingPercent := float64(details.MissingPieces) * 100 / float64(details.TotalPieces)
	return missingPercent > *deps.PartialMaxMissingPercent
}

func holdVerdict(ctx context.Context, deps Deps, candidateID int64, kind store.ProposalKind, warnings []string, details ProposalDetails) error {
	encoded, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("encode held details: %w", err)
	}
	hold := store.CandidateHold{
		CandidateID: candidateID, Reason: ReasonMissingPiecesAboveCap, Kind: kind, Warnings: warnings, Details: encoded,
	}
	return deps.Store.SaveCandidateHold(ctx, hold, deps.now())
}

// reevaluateCap applique le plafond courant à ce qui attend une décision, pour qu'un changement de plafond suivi d'un
// Verify ne laisse aucun verdict périmé : un verdict mis de côté revenu sous le plafond devient une proposition, une
// proposition partielle encore en attente passée au-dessus est retirée (superseded) et mise de côté. Une proposition
// déjà décidée n'est jamais touchée. Chaque verdict encore mis de côté est compté sous sa raison.
func reevaluateCap(ctx context.Context, deps Deps, result *Result) error {
	verified, err := deps.Store.ListCandidatesByState(ctx, store.CandidateVerified)
	if err != nil {
		return err
	}
	candidates := make(map[int64]store.Candidate, len(verified))
	for _, candidate := range verified {
		candidates[candidate.ID] = candidate
	}
	if err := withdrawPendingAboveCap(ctx, deps, candidates); err != nil {
		return err
	}
	return releaseHoldsUnderCap(ctx, deps, candidates, result)
}

func withdrawPendingAboveCap(ctx context.Context, deps Deps, candidates map[int64]store.Candidate) error {
	pending, err := deps.Store.ListProposals(ctx, store.ProposalPending)
	if err != nil {
		return err
	}
	for _, proposal := range pending {
		candidate, known := candidates[proposal.CandidateID]
		isCurrent := known && candidate.Generation == proposal.Generation
		details := parseDetails(proposal)
		if !isCurrent || !deps.aboveCap(proposal.Kind, details) {
			continue
		}
		if err := deps.Store.SetProposalDecision(ctx, proposal.ID, store.ProposalSuperseded, deps.now()); err != nil {
			return err
		}
		if err := holdVerdict(ctx, deps, proposal.CandidateID, proposal.Kind, proposal.Warnings, details); err != nil {
			return err
		}
	}
	return nil
}

func releaseHoldsUnderCap(ctx context.Context, deps Deps, candidates map[int64]store.Candidate, result *Result) error {
	holds, err := deps.Store.ListCandidateHolds(ctx)
	if err != nil {
		return err
	}
	for _, hold := range holds {
		details := parseDetails(store.Proposal{Details: hold.Details})
		if deps.aboveCap(hold.Kind, details) {
			result.count(hold.Reason, 1)
			continue
		}
		proposal := store.Proposal{
			CandidateID: hold.CandidateID, GroupID: GroupOf(candidates[hold.CandidateID]), Kind: hold.Kind, Warnings: hold.Warnings,
		}
		created, err := upsertProposal(ctx, deps, proposal, details)
		if err != nil {
			return err
		}
		if created {
			result.ProposalsCreated++
		}
		if err := deps.Store.DeleteCandidateHold(ctx, hold.CandidateID); err != nil {
			return err
		}
	}
	return nil
}
