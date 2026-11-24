package app

import (
	"context"

	"necromanciarr/internal/crossseed"
	"necromanciarr/internal/store"
)

// Status résume l'état persisté : torrents par état, propositions par décision, propositions appliquées par étape,
// mode cross-seed effectif et son avertissement éventuel.
type Status struct {
	Candidates    map[store.CandidateState]int   `json:"candidates"`
	Proposals     map[store.ProposalDecision]int `json:"proposals"`
	Steps         map[store.ProposalStep]int     `json:"steps"`
	CrossSeedMode string                         `json:"cross_seed_mode"`
	Warnings      []string                       `json:"warnings,omitempty"`
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
	status := Status{
		Candidates: map[store.CandidateState]int{}, Proposals: map[store.ProposalDecision]int{}, Steps: map[store.ProposalStep]int{},
		CrossSeedMode: behaviorNames[svc.deps.CrossSeed],
	}
	if svc.deps.CrossSeedWarning != "" {
		status.Warnings = []string{svc.deps.CrossSeedWarning}
	}
	for _, candidate := range state.candidates {
		status.Candidates[candidate.State]++
	}
	for _, proposal := range state.proposals {
		status.Proposals[proposal.Decision]++
	}
	for _, progress := range state.progress {
		status.Steps[progress.Step]++
	}
	return status, nil
}
