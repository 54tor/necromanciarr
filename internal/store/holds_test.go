package store

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"necromanciarr/internal/i18n"
)

func saveVerifiedHoldCandidate(t *testing.T, openedStore *Store) int64 {
	t.Helper()
	mediaFile := insertTestMediaFile(t, openedStore, "/home/u/media/film.mkv", 100)
	candidateID, _ := saveTestCandidate(t, openedStore, "aa", mediaFile)
	markVerified(t, openedStore, candidateID)
	return candidateID
}

func TestCandidateHolds_saveListAndDeleteTheHeldVerdict(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	candidateID := saveVerifiedHoldCandidate(t, openedStore)
	expectedValue := CandidateHold{
		CandidateID: candidateID, Reason: "missing_pieces_above_cap", Kind: ProposalPartialAnnex,
		Warnings: []i18n.Message{{Key: "warning.test", Params: map[string]string{"count": "3"}}}, Details: json.RawMessage(`{"missing_pieces":3}`),
	}

	if err := openedStore.SaveCandidateHold(ctx, expectedValue, testNow); err != nil {
		t.Fatal(err)
	}
	gotValue, err := openedStore.ListCandidateHolds(ctx)
	if err != nil {
		t.Fatal(err)
	}

	isExpected := len(gotValue) == 1 && gotValue[0].CandidateID == candidateID && gotValue[0].Reason == expectedValue.Reason &&
		gotValue[0].Kind == expectedValue.Kind && reflect.DeepEqual(gotValue[0].Warnings, expectedValue.Warnings) &&
		string(gotValue[0].Details) == string(expectedValue.Details)
	if !isExpected {
		t.Fatalf("gotValue %+v, expectedValue %+v", gotValue, expectedValue)
	}
	if err := openedStore.DeleteCandidateHold(ctx, candidateID); err != nil {
		t.Fatal(err)
	}
	if remaining, err := openedStore.ListCandidateHolds(ctx); err != nil || len(remaining) != 0 {
		t.Fatalf("gotValue %+v %v, expectedValue no hold after delete", remaining, err)
	}
}

func TestCandidateHolds_aHoldOfAnOlderGenerationOrAStaleCandidateIsNotListed(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	candidateID := saveVerifiedHoldCandidate(t, openedStore)
	hold := CandidateHold{CandidateID: candidateID, Reason: "missing_pieces_above_cap", Kind: ProposalPartialPack}
	if err := openedStore.SaveCandidateHold(ctx, hold, testNow); err != nil {
		t.Fatal(err)
	}

	if _, err := openedStore.database.ExecContext(ctx, `UPDATE torrent_candidates SET generation = generation + 1`); err != nil {
		t.Fatal(err)
	}

	if gotValue, err := openedStore.ListCandidateHolds(ctx); err != nil || len(gotValue) != 0 {
		t.Fatalf("gotValue %+v %v, expectedValue no hold of a former generation", gotValue, err)
	}
}

func TestCandidateHolds_refusesAnUnknownKind(t *testing.T) {
	openedStore := openTestStore(t)
	candidateID := saveVerifiedHoldCandidate(t, openedStore)

	err := openedStore.SaveCandidateHold(context.Background(), CandidateHold{CandidateID: candidateID, Kind: "bogus"}, testNow)

	if err == nil {
		t.Fatal("gotValue nil, expectedValue an unknown kind error")
	}
}

func TestHoldWarnings_legacyStringRowsStillRender(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	candidateID := saveVerifiedHoldCandidate(t, openedStore)
	if err := openedStore.SaveCandidateHold(ctx, CandidateHold{CandidateID: candidateID, Reason: "missing_pieces_above_cap", Kind: ProposalPartialAnnex}, testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := openedStore.database.ExecContext(ctx, `UPDATE candidate_holds SET warnings = ?`, `["old hold text"]`); err != nil {
		t.Fatal(err)
	}

	holds, err := openedStore.ListCandidateHolds(ctx)
	if err != nil || len(holds) != 1 || len(holds[0].Warnings) != 1 {
		t.Fatalf("gotValue %+v %v, expectedValue one hold with one warning", holds, err)
	}
	for _, language := range []i18n.Language{i18n.French, i18n.English} {
		if gotValue := i18n.New(language, nil).Render(holds[0].Warnings[0]); gotValue != "old hold text" {
			t.Fatalf("language %s: gotValue %q, expectedValue the legacy text", language, gotValue)
		}
	}
}

func TestHoldWarnings_mixedLegacyAndStructuredArraysAreRead(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	candidateID := saveVerifiedHoldCandidate(t, openedStore)
	if err := openedStore.SaveCandidateHold(ctx, CandidateHold{CandidateID: candidateID, Reason: "missing_pieces_above_cap", Kind: ProposalPartialAnnex}, testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := openedStore.database.ExecContext(ctx, `UPDATE candidate_holds SET warnings = ?`, `[{"key":"warning.test"},"old hold text"]`); err != nil {
		t.Fatal(err)
	}
	expectedValue := []i18n.Message{{Key: "warning.test"}, {Key: i18n.KeyLegacyText, Params: map[string]string{"text": "old hold text"}}}

	holds, err := openedStore.ListCandidateHolds(ctx)
	if err != nil || len(holds) != 1 || !reflect.DeepEqual(holds[0].Warnings, expectedValue) {
		t.Fatalf("gotValue %+v %v, expectedValue %+v", holds, err, expectedValue)
	}
}
