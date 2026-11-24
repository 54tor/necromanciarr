package app

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"necromanciarr/internal/i18n"
	"necromanciarr/internal/pipeline"
	"necromanciarr/internal/store"
)

func listedWarnings(t *testing.T, fixture *decisionFixture, language i18n.Language) []Warning {
	t.Helper()
	service := New(Deps{Store: fixture.store, Translator: i18n.New(language, nil)})
	proposals, err := service.ListProposals(t.Context(), ProposalFilter{})
	if err != nil || len(proposals) != 1 {
		t.Fatalf("language %s: gotValue %+v %v, expectedValue one proposal", language, proposals, err)
	}
	return proposals[0].Warnings
}

func TestProposalWarnings_areRenderedWithAStableCode(t *testing.T) {
	fixture := newDecisionFixture(t)
	proposalID := fixture.proposal(fixture.verifiedCandidate("aa"), store.ProposalPartialAnnex)
	if err := fixture.store.AddProposalWarning(t.Context(), proposalID, i18n.Message{Key: i18n.KeyWarningSourcesChanged}); err != nil {
		t.Fatal(err)
	}

	french, english := listedWarnings(t, fixture, i18n.French), listedWarnings(t, fixture, i18n.English)

	if len(french) != 2 || len(english) != 2 {
		t.Fatalf("gotValue fr %+v en %+v, expectedValue two warnings", french, english)
	}
	for position := range french {
		if french[position].Code != english[position].Code || french[position].Code == "" {
			t.Errorf("warning %d: gotValue codes %q and %q, expectedValue the same non-empty code", position, french[position].Code, english[position].Code)
		}
	}
	if french[1].Code != string(i18n.KeyWarningSourcesChanged) || french[1].Text == english[1].Text || french[1].Text == french[1].Code {
		t.Fatalf("gotValue fr %+v en %+v, expectedValue the key as code and two distinct translated texts", french[1], english[1])
	}
}

func TestProposalWarnings_legacyStringRowIsShownAsIs(t *testing.T) {
	fixture := newDecisionFixture(t)
	proposalID := fixture.proposal(fixture.verifiedCandidate("aa"), store.ProposalFull)
	legacyMessage := i18n.Message{Key: i18n.KeyLegacyText, Params: map[string]string{"text": "old text"}}
	if err := fixture.store.AddProposalWarning(t.Context(), proposalID, legacyMessage); err != nil {
		t.Fatal(err)
	}

	for _, language := range []i18n.Language{i18n.French, i18n.English} {
		warnings := listedWarnings(t, fixture, language)
		if len(warnings) != 2 || warnings[1].Text != "old text" || warnings[1].Code != string(i18n.KeyLegacyText) {
			t.Fatalf("language %s: gotValue %+v, expectedValue the legacy text under the legacy code", language, warnings)
		}
	}
}

func TestProposalWarnings_secretsInParamsAreNeverRendered(t *testing.T) {
	fixture := newDecisionFixture(t)
	proposalID := fixture.proposal(fixture.verifiedCandidate("aa"), store.ProposalFull)
	if err := fixture.store.AddProposalWarning(t.Context(), proposalID, i18n.Message{Key: i18n.KeyLegacyText, Params: map[string]string{"text": "leak abc123secret"}}); err != nil {
		t.Fatal(err)
	}
	redact := func(text string) string { return strings.ReplaceAll(text, "abc123secret", "***") }
	service := New(Deps{Store: fixture.store, Translator: i18n.New(i18n.English, redact)})

	proposals, err := service.ListProposals(t.Context(), ProposalFilter{})
	if err != nil {
		t.Fatal(err)
	}

	for _, warning := range proposals[0].Warnings {
		if strings.Contains(warning.Text, "abc123secret") {
			t.Fatalf("gotValue %q, expectedValue the secret masked", warning.Text)
		}
	}
}

// textFieldNames sont les seuls champs JSON dont la valeur suit la langue.
var textFieldNames = []string{"text", "label", "message", "detail"}

func differencesBetween(path string, frenchTree, englishTree any) []string {
	var differences []string
	switch frenchValue := frenchTree.(type) {
	case map[string]any:
		englishValue, isMap := englishTree.(map[string]any)
		if !isMap || len(englishValue) != len(frenchValue) {
			return []string{path + ": different keys"}
		}
		for name, frenchChild := range frenchValue {
			if slices.Contains(textFieldNames, name) || strings.HasSuffix(name, "_labels") {
				continue
			}
			differences = append(differences, differencesBetween(path+"."+name, frenchChild, englishValue[name])...)
		}
	case []any:
		englishValue, isSlice := englishTree.([]any)
		if !isSlice || len(englishValue) != len(frenchValue) {
			return []string{path + ": different lengths"}
		}
		for position, frenchChild := range frenchValue {
			differences = append(differences, differencesBetween(fmt.Sprintf("%s[%d]", path, position), frenchChild, englishValue[position])...)
		}
	default:
		if frenchTree != englishTree {
			differences = append(differences, fmt.Sprintf("%s: gotValue en %v, expectedValue fr %v", path, englishTree, frenchTree))
		}
	}
	return differences
}

func TestProposalJSON_onlyTextFieldsDifferBetweenLanguages(t *testing.T) {
	fixture := newDecisionFixture(t)
	candidate := fixture.verifiedCandidate("aa")
	details := `{"percent":100,"excluded_files":[{"torrent_path":"sub.srt","reason":"annex_hash_failed"}]}`
	proposalID, _, err := fixture.store.UpsertProposal(t.Context(), store.Proposal{
		CandidateID: candidate.ID, GroupID: pipeline.GroupOf(candidate), Kind: store.ProposalFull, Details: json.RawMessage(details),
		Warnings: []i18n.Message{{Key: i18n.KeyWarningAnnexHashFailed, Params: map[string]string{"count": "2"}}},
	}, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.AddProposalWarning(t.Context(), proposalID, i18n.Message{Key: i18n.KeyLegacyText, Params: map[string]string{"text": "old"}}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.SaveCandidateHold(t.Context(), store.CandidateHold{
		CandidateID: candidate.ID, Reason: pipeline.ReasonMissingPiecesAboveCap, Kind: store.ProposalPartialAnnex,
	}, fixedNow); err != nil {
		t.Fatal(err)
	}
	trees := map[i18n.Language]any{}
	for _, language := range []i18n.Language{i18n.French, i18n.English} {
		service := New(Deps{Store: fixture.store, Translator: i18n.New(language, nil)})
		proposals, err := service.ListProposals(t.Context(), ProposalFilter{})
		if err != nil {
			t.Fatal(err)
		}
		status, err := service.Status(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(map[string]any{
			"proposals": proposals, "held": status.Held, "result": withReasonLabels(i18n.New(language, nil), pipeline.Result{Reasons: map[string]int{"no_exact_size": 1}}),
			"force":  ForceResult{ProposalID: proposalID, Warnings: warningViews(i18n.New(language, nil), []i18n.Message{{Key: i18n.KeyWarningSourcesChanged}})},
			"dedupe": DedupeProposal{ID: 1, Warnings: warningViews(i18n.New(language, nil), []i18n.Message{{Key: i18n.KeyWarningNoClientRecheck}}), Details: DedupeDetails{Reason: DedupeReasonIsolatedCopy}.labelled(i18n.New(language, nil))},
			"repair": RepairProposal{ID: 2, Warnings: warningViews(i18n.New(language, nil), []i18n.Message{{Key: i18n.KeyWarningRecheckFailed}}), Details: RepairDetails{Reason: RepairReasonNeedsClientRecheck}.labelled(i18n.New(language, nil))},
		})
		if err != nil {
			t.Fatal(err)
		}
		var decoded any
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		trees[language] = decoded
	}

	for _, difference := range differencesBetween("root", trees[i18n.French], trees[i18n.English]) {
		t.Error(difference)
	}
	if !strings.Contains(fmt.Sprint(trees[i18n.French]), "code:warning.annex_hash_failed") {
		t.Fatalf("gotValue %v, expectedValue the warning key under code", trees[i18n.French])
	}
}

func TestProposalWarnings_legacyMaintenanceCodeRendersTranslatedAndOnce(t *testing.T) {
	fixture := newDecisionFixture(t)
	proposalID := fixture.proposal(fixture.verifiedCandidate("aa"), store.ProposalFull)
	legacyCode := i18n.Message{Key: i18n.KeyLegacyText, Params: map[string]string{"text": WarningNoClientRecheck}}
	for _, message := range []i18n.Message{legacyCode, {Key: i18n.KeyWarningNoClientRecheck}} {
		if err := fixture.store.AddProposalWarning(t.Context(), proposalID, message); err != nil {
			t.Fatal(err)
		}
	}

	for _, language := range []i18n.Language{i18n.French, i18n.English} {
		warnings := listedWarnings(t, fixture, language)
		var codes []string
		for _, warning := range warnings {
			codes = append(codes, warning.Code)
		}
		expectedValue := []string{"warning.test", string(i18n.KeyWarningNoClientRecheck)}
		if !slices.Equal(codes, expectedValue) || strings.Contains(warnings[1].Text, WarningNoClientRecheck) {
			t.Fatalf("language %s: gotValue %+v, expectedValue codes %v with a translated text", language, warnings, expectedValue)
		}
	}
}
