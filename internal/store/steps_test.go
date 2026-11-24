package store

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func acceptedTestProposal(t *testing.T, openedStore *Store) (candidateID, proposalID int64) {
	t.Helper()
	candidateID, _ = saveTestCandidate(t, openedStore, "aa", insertTestMediaFile(t, openedStore, "/home/u/media/a.mkv", 10))
	markVerified(t, openedStore, candidateID)
	proposalID = proposalsByDecision(t, openedStore, candidateID, map[string][]ProposalDecision{"accepted": {ProposalAccepted}})["accepted"]
	return candidateID, proposalID
}

func TestProposalProgress_roundTripsAndRefusesUnknownStep(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	_, proposalID := acceptedTestProposal(t, openedStore)
	expectedValue := ProposalProgress{ProposalID: proposalID, Step: StepLinked, LinkRoot: "/home/u/links/Movie [aabbccdd]", UpdatedAt: testNow}
	if err := openedStore.SaveProposalProgress(ctx, expectedValue, testNow); err != nil {
		t.Fatal(err)
	}
	progress, err := openedStore.ListProposalProgress(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if gotValue := progress[proposalID]; gotValue != expectedValue {
		t.Fatalf("gotValue %+v, expectedValue %+v", gotValue, expectedValue)
	}
	err = openedStore.SaveProposalProgress(ctx, ProposalProgress{ProposalID: proposalID, Step: "started"}, testNow)
	if !errors.Is(err, ErrUnknownProposalStep) {
		t.Fatalf("gotValue %v, expectedValue ErrUnknownProposalStep", err)
	}
}

func TestApplyProposal_setsAppliedAndStepTogetherOrNothing(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	_, proposalID := acceptedTestProposal(t, openedStore)
	if _, err := openedStore.database.ExecContext(ctx, `UPDATE torrent_candidates SET generation = generation + 1`); err != nil {
		t.Fatal(err)
	}
	err := openedStore.ApplyProposal(ctx, proposalID, StepSeeded, "/home/u/links/a", testNow)
	if !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("gotValue %v, expectedValue ErrStaleGeneration", err)
	}
	progress, err := openedStore.ListProposalProgress(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, written := progress[proposalID]; written {
		t.Fatal("step written although the decision was refused")
	}
}

func TestApplyProposal_recordsAppliedDecisionAndStep(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	_, proposalID := acceptedTestProposal(t, openedStore)
	if err := openedStore.ApplyProposal(ctx, proposalID, StepSeededPausedIncomplete, "/home/u/links/a", testNow); err != nil {
		t.Fatal(err)
	}
	if gotValue := decisionsByID(t, openedStore)[proposalID]; gotValue != ProposalApplied {
		t.Fatalf("gotValue %s, expectedValue applied", gotValue)
	}
	progress, err := openedStore.ListProposalProgress(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if gotValue := progress[proposalID]; gotValue.Step != StepSeededPausedIncomplete || gotValue.LinkRoot != "/home/u/links/a" {
		t.Fatalf("gotValue %+v", gotValue)
	}
}

func TestAddProposalWarning_appendsEachWarningOnce(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	_, proposalID := acceptedTestProposal(t, openedStore)
	for range 2 {
		if err := openedStore.AddProposalWarning(ctx, proposalID, "cross-seed notification failed"); err != nil {
			t.Fatal(err)
		}
	}
	proposals, err := openedStore.ListProposals(ctx, ProposalAccepted)
	if err != nil {
		t.Fatal(err)
	}
	expectedValue := []string{"cross-seed notification failed"}
	if gotValue := proposals[0].Warnings; !slices.Equal(gotValue, expectedValue) {
		t.Fatalf("gotValue %v, expectedValue %v", gotValue, expectedValue)
	}
	if err := openedStore.AddProposalWarning(ctx, proposalID+100, "x"); !errors.Is(err, ErrProposalNotFound) {
		t.Fatalf("gotValue %v, expectedValue ErrProposalNotFound", err)
	}
}

func TestAcceptProposals_acceptsAllOrNothing(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	candidateID, _ := saveTestCandidate(t, openedStore, "aa", insertTestMediaFile(t, openedStore, "/home/u/media/a.mkv", 10))
	markVerified(t, openedStore, candidateID)
	identifiers := proposalsByDecision(t, openedStore, candidateID, map[string][]ProposalDecision{
		"pending": nil, "failed": {ProposalAccepted, ProposalFailed}, "rejected": {ProposalRejected},
	})

	err := openedStore.AcceptProposals(ctx, []int64{identifiers["pending"], identifiers["rejected"]}, testNow)
	if !errors.Is(err, ErrInvalidTransition) || decisionsByID(t, openedStore)[identifiers["pending"]] != ProposalPending {
		t.Fatalf("gotValue %v, expectedValue a refusal that leaves the pending proposal untouched", err)
	}
	if err := openedStore.AcceptProposals(ctx, []int64{identifiers["pending"], identifiers["failed"]}, testNow); err != nil {
		t.Fatal(err)
	}
	decisions := decisionsByID(t, openedStore)
	if decisions[identifiers["pending"]] != ProposalAccepted || decisions[identifiers["failed"]] != ProposalAccepted {
		t.Fatalf("gotValue %v, expectedValue both accepted", decisions)
	}
}

func TestDeleteProposalProgress_removesTheStep(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	_, proposalID := acceptedTestProposal(t, openedStore)
	if err := openedStore.SaveProposalProgress(ctx, ProposalProgress{ProposalID: proposalID, Step: StepLinked}, testNow); err != nil {
		t.Fatal(err)
	}
	if err := openedStore.DeleteProposalProgress(ctx, proposalID); err != nil {
		t.Fatal(err)
	}
	progress, err := openedStore.ListProposalProgress(ctx)
	if _, present := progress[proposalID]; err != nil || present {
		t.Fatalf("gotValue %+v %v, expectedValue no step", progress, err)
	}
}
