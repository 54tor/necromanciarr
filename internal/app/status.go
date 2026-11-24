package app

import (
	"context"
	"encoding/json"

	"necromanciarr/internal/crossseed"
	"necromanciarr/internal/pipeline"
	"necromanciarr/internal/store"
)

// Status résume l'état persisté : torrents par état, propositions par décision, propositions appliquées par étape,
// mode cross-seed effectif et son avertissement éventuel.
type Status struct {
	Candidates    map[store.CandidateState]int   `json:"candidates"`
	Proposals     map[store.ProposalDecision]int `json:"proposals"`
	Steps         map[store.ProposalStep]int     `json:"steps"`
	CrossSeedMode string                         `json:"cross_seed_mode"`
	// Language est la langue des textes rendus (fr ou en) : l'interface la lit avant tout rendu.
	Language string   `json:"language"`
	Warnings []string `json:"warnings,omitempty"`
	// Held liste les torrents vérifiés dont le verdict est mis de côté (aucune proposition), avec la raison.
	Held []HeldCandidate `json:"held"`
}

// HeldCandidate est un torrent vérifié non proposé : nature qu'il aurait eue, raison et pièces manquantes.
type HeldCandidate struct {
	CandidateID   int64              `json:"candidate_id"`
	CandidateName string             `json:"candidate_name"`
	Tracker       string             `json:"tracker"`
	Kind          store.ProposalKind `json:"kind"`
	Reason        string             `json:"reason"`
	Label         string             `json:"label"`
	Percent       float64            `json:"percent"`
	MissingPieces int                `json:"missing_pieces"`
	TotalPieces   int                `json:"total_pieces"`
}

// behaviorNames nomme le comportement cross-seed effectif pour l'affichage.
var behaviorNames = map[crossseed.Behavior]string{
	crossseed.Delegate: "delegate",
	crossseed.Native:   "native",
	crossseed.Disabled: "disabled",
}

func (svc *service) Status(ctx context.Context) (Status, error) {
	state, err := svc.loadSnapshot(ctx)
	if err != nil {
		return Status{}, err
	}
	resolution := svc.crossSeed(ctx)
	status := Status{
		Candidates: map[store.CandidateState]int{}, Proposals: map[store.ProposalDecision]int{}, Steps: map[store.ProposalStep]int{},
		CrossSeedMode: behaviorNames[resolution.Behavior], Language: string(svc.deps.Translator.Language()),
	}
	if resolution.Warning != "" {
		status.Warnings = []string{resolution.Warning}
	}
	for _, candidate := range state.candidates {
		status.Candidates[candidate.State]++
	}
	for _, proposal := range state.proposals {
		status.Proposals[proposal.Decision]++
	}
	for _, progress := range state.progress {
		if progress.Step != "" {
			status.Steps[progress.Step]++
		}
	}
	status.Held, err = svc.heldCandidates(ctx, state)
	return status, err
}

func (svc *service) heldCandidates(ctx context.Context, state snapshot) ([]HeldCandidate, error) {
	holds, err := svc.deps.Store.ListCandidateHolds(ctx)
	if err != nil {
		return nil, err
	}
	held := []HeldCandidate{}
	for _, hold := range holds {
		candidate := state.candidates[hold.CandidateID]
		var details pipeline.ProposalDetails
		_ = json.Unmarshal(hold.Details, &details)
		held = append(held, HeldCandidate{
			CandidateID: hold.CandidateID, CandidateName: candidate.Name, Tracker: candidate.Tracker, Kind: hold.Kind,
			Reason: hold.Reason, Label: svc.deps.Translator.ReasonLabel(hold.Reason), Percent: details.Percent, MissingPieces: details.MissingPieces, TotalPieces: details.TotalPieces,
		})
	}
	return held, nil
}
