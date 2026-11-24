package app

import (
	"context"
	"encoding/json"
	"testing"

	"necromanciarr/internal/crossseed"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/store"
)

func TestStatus_listsTheVerdictsHeldAboveTheMissingPiecesCap(t *testing.T) {
	fixture := newDecisionFixture(t)
	candidate := fixture.verifiedCandidate("aa")
	hold := store.CandidateHold{
		CandidateID: candidate.ID, Reason: "missing_pieces_above_cap", Kind: store.ProposalPartialPack,
		Details: json.RawMessage(`{"percent":40,"missing_pieces":6,"total_pieces":10}`),
	}
	if err := fixture.store.SaveCandidateHold(context.Background(), hold, fixedNow); err != nil {
		t.Fatal(err)
	}
	expectedValue := HeldCandidate{
		CandidateID: candidate.ID, CandidateName: candidate.Name, Tracker: candidate.Tracker, Kind: store.ProposalPartialPack,
		Reason: "missing_pieces_above_cap", Label: i18n.New(i18n.French, nil).ReasonLabel("missing_pieces_above_cap"), Percent: 40, MissingPieces: 6, TotalPieces: 10,
	}

	status, err := fixture.service(crossseed.Delegate).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(status.Held) != 1 || status.Held[0] != expectedValue {
		t.Fatalf("gotValue %+v, expectedValue [%+v]", status.Held, expectedValue)
	}
}
