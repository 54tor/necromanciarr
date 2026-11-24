package store

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"necromanciarr/internal/i18n"
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
		if err := openedStore.AddProposalWarning(ctx, proposalID, i18n.Message{Key: "warning.test", Params: map[string]string{"name": "a"}}); err != nil {
			t.Fatal(err)
		}
	}
	proposals, err := openedStore.ListProposals(ctx, ProposalAccepted)
	if err != nil {
		t.Fatal(err)
	}
	expectedValue := []i18n.Message{{Key: "warning.test", Params: map[string]string{"name": "a"}}}
	if gotValue := proposals[0].Warnings; !reflect.DeepEqual(gotValue, expectedValue) {
		t.Fatalf("gotValue %v, expectedValue %v", gotValue, expectedValue)
	}
	if err := openedStore.AddProposalWarning(ctx, proposalID+100, i18n.Message{Key: "warning.test"}); !errors.Is(err, ErrProposalNotFound) {
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

func TestClearProposalStep_removesTheStepButKeepsTheLinkRoot(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	_, proposalID := acceptedTestProposal(t, openedStore)
	expectedRoot := "/home/u/links/Movie [aa]"
	if err := openedStore.SaveProposalProgress(ctx, ProposalProgress{ProposalID: proposalID, Step: StepLinked, LinkRoot: expectedRoot}, testNow); err != nil {
		t.Fatal(err)
	}
	if err := openedStore.ClearProposalStep(ctx, proposalID, testNow); err != nil {
		t.Fatal(err)
	}
	progress, err := openedStore.ListProposalProgress(ctx)
	if gotValue := progress[proposalID]; err != nil || gotValue.Step != "" || gotValue.LinkRoot != expectedRoot {
		t.Fatalf("gotValue %+v %v, expectedValue no step and the link root kept", gotValue, err)
	}
}

func TestProposalWarnings_roundTripMessages(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	candidateID, _ := acceptedTestProposal(t, openedStore)
	expectedValue := []i18n.Message{
		{Key: "warning.test", Params: map[string]string{"count": "2"}},
		{Key: "warning.other"},
	}

	proposalID, err := openedStore.CreateProposal(ctx, Proposal{CandidateID: candidateID, GroupID: candidateID, Kind: ProposalFull, Warnings: expectedValue}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	proposals, err := openedStore.ListProposals(ctx, ProposalPending)
	if err != nil {
		t.Fatal(err)
	}

	var gotValue []i18n.Message
	for _, proposal := range proposals {
		if proposal.ID == proposalID {
			gotValue = proposal.Warnings
		}
	}
	if !reflect.DeepEqual(gotValue, expectedValue) {
		t.Fatalf("gotValue %+v, expectedValue %+v", gotValue, expectedValue)
	}
}

func TestProposalWarnings_legacyStringRowsStillRender(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	_, proposalID := acceptedTestProposal(t, openedStore)
	if _, err := openedStore.database.ExecContext(ctx, `UPDATE proposals SET warnings = ? WHERE id = ?`, `["old text"]`, proposalID); err != nil {
		t.Fatal(err)
	}
	expectedValue := []i18n.Message{{Key: i18n.KeyLegacyText, Params: map[string]string{"text": "old text"}}}

	proposals, err := openedStore.ListProposals(ctx, ProposalAccepted)
	if err != nil {
		t.Fatal(err)
	}
	if gotValue := proposals[0].Warnings; !reflect.DeepEqual(gotValue, expectedValue) {
		t.Fatalf("gotValue %+v, expectedValue %+v", gotValue, expectedValue)
	}
	for _, language := range []i18n.Language{i18n.French, i18n.English} {
		if gotValue := i18n.New(language, nil).Render(proposals[0].Warnings[0]); gotValue != "old text" {
			t.Fatalf("language %s: gotValue %q, expectedValue the legacy text", language, gotValue)
		}
	}
}

func TestAddProposalWarning_appendsToALegacyRowWithoutLosingIt(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	_, proposalID := acceptedTestProposal(t, openedStore)
	if _, err := openedStore.database.ExecContext(ctx, `UPDATE proposals SET warnings = ? WHERE id = ?`, `["old text"]`, proposalID); err != nil {
		t.Fatal(err)
	}

	if err := openedStore.AddProposalWarning(ctx, proposalID, i18n.Message{Key: "warning.test"}); err != nil {
		t.Fatal(err)
	}
	proposals, err := openedStore.ListProposals(ctx, ProposalAccepted)
	if err != nil {
		t.Fatal(err)
	}
	expectedValue := []i18n.Message{
		{Key: i18n.KeyLegacyText, Params: map[string]string{"text": "old text"}},
		{Key: "warning.test"},
	}
	if gotValue := proposals[0].Warnings; !reflect.DeepEqual(gotValue, expectedValue) {
		t.Fatalf("gotValue %+v, expectedValue %+v", gotValue, expectedValue)
	}
}
