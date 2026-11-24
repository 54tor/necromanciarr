package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"necromanciarr/internal/crossseed"
	"necromanciarr/internal/dedupe"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/linker"
	"necromanciarr/internal/qbit"
	"necromanciarr/internal/repair"
	"necromanciarr/internal/store"
)

var bothLanguages = []i18n.Language{i18n.French, i18n.English}

func TestLinks_reasonIsAStableCodeWithATranslatedLabel(t *testing.T) {
	expectedCodes := map[repair.Condition]string{
		repair.ConditionLost: LinkReasonMissing, repair.ConditionDiverged: LinkReasonInodeDiverged,
		repair.ConditionReplaced: LinkReasonReplaced, repair.ConditionResized: LinkReasonResized,
		repair.ConditionRenamedTarget: LinkReasonTargetRenamed, repair.ConditionUnverified: LinkReasonNotCompared,
		repair.ConditionBroken: LinkReasonTargetMissing, repair.ConditionUnreadable: LinkReasonInspectionFailed,
	}
	for condition, expectedValue := range expectedCodes {
		inspection := repair.Inspection{Condition: condition}
		if condition == repair.ConditionUnreadable {
			inspection.Err = errors.New("stat /srv/media/x: permission denied")
		}
		item := linkItem(store.Link{LinkPath: "/srv/links/x"}, inspection)
		item = item.labelled(i18n.New(i18n.English, nil))
		if item.Reason != expectedValue || strings.ContainsAny(item.Reason, " /:") {
			t.Fatalf("condition %v: gotValue reason %q, expectedValue the code %q", condition, item.Reason, expectedValue)
		}
		if item.Label == "" || item.Label == item.Reason || strings.Contains(item.Label, "/srv/") {
			t.Fatalf("condition %v: gotValue label %q, expectedValue a translated label without the raw error", condition, item.Label)
		}
	}
}

func TestLinks_everyReasonCodeHasLabelsInBothLanguages(t *testing.T) {
	for _, code := range []string{
		LinkReasonMissing, LinkReasonTargetMissing, LinkReasonInodeDiverged, LinkReasonTargetRenamed, LinkReasonNotCompared,
		LinkReasonReplaced, LinkReasonResized, LinkReasonTargetUnreadable, LinkReasonInspectionFailed,
	} {
		french, english := i18n.New(i18n.French, nil).ReasonLabel(code), i18n.New(i18n.English, nil).ReasonLabel(code)
		if french == code || english == code || french == english {
			t.Fatalf("code %q: gotValue %q and %q, expectedValue two distinct labels", code, french, english)
		}
	}
}

func TestDedupeReason_isAStableCodeNotTheErrorText(t *testing.T) {
	crossDevice := errors.Join(fmt.Errorf("hardlink: %w", linker.ErrCrossDevice), fmt.Errorf("reflink: %w", linker.ErrUnsupported))
	cases := map[string]struct {
		cause         error
		expectedValue string
	}{
		"hash unavailable": {fmt.Errorf("wrapped: %w", dedupe.ErrHashUnavailable), DedupeReasonHashUnavailable},
		"no kind":          {linker.ErrNoKind, DedupeReasonNoKind},
		"cross device":     {crossDevice, DedupeReasonCrossDevice},
		"unsupported":      {fmt.Errorf("symlink: %w", linker.ErrUnsupported), DedupeReasonKindUnsupported},
		"probe failure":    {errors.New("probe /srv/torrents: boom"), DedupeReasonProbeFailed},
	}
	for name, testCase := range cases {
		details := dedupeDetailsOf(dedupe.Replacement{Reason: testCase.cause})
		if details.Reason != testCase.expectedValue {
			t.Errorf("%s: gotValue %q, expectedValue %q", name, details.Reason, testCase.expectedValue)
		}
		for _, language := range bothLanguages {
			label := details.labelled(i18n.New(language, nil)).Label
			if label == "" || label == details.Reason || strings.Contains(label, "/srv/") {
				t.Errorf("%s (%s): gotValue label %q, expectedValue a translated label", name, language, label)
			}
		}
	}
}

func TestDedupeDetails_warningsAreStoredAsKeysAndLegacySentencesStayReadable(t *testing.T) {
	details := dedupeDetailsOf(dedupe.Replacement{Warnings: []i18n.Message{{Key: i18n.KeyWarningDedupeSampledVerify}}})
	if expectedValue := []string{string(i18n.KeyWarningDedupeSampledVerify)}; !slices.Equal(details.Warnings, expectedValue) {
		t.Fatalf("gotValue %v, expectedValue %v", details.Warnings, expectedValue)
	}
	messages := messagesOfCodes([]string{string(i18n.KeyWarningDedupeSampledVerify), "inode non fiable", WarningNoClientRecheck})
	expectedKeys := []i18n.Key{i18n.KeyWarningDedupeSampledVerify, i18n.KeyLegacyText, i18n.KeyWarningNoClientRecheck}
	for position, expectedKey := range expectedKeys {
		if messages[position].Key != expectedKey {
			t.Fatalf("message %d: gotValue %q, expectedValue %q", position, messages[position].Key, expectedKey)
		}
	}
}

func TestMaintenanceWarnings_carryAStableCode(t *testing.T) {
	cases := map[string]struct {
		recordErr, recheckErr error
		expectedValue         []i18n.Key
	}{
		"paused after recheck": {nil, qbit.ErrIncompleteAfterRecheck, []i18n.Key{i18n.KeyWarningPausedIncompleteAfterRecheck}},
		"recheck failed":       {errors.New("boom"), errors.New("boom"), []i18n.Key{i18n.KeyWarningLinkNotRecorded, i18n.KeyWarningRecheckFailed}},
		"nothing":              {nil, nil, nil},
	}
	for name, testCase := range cases {
		warnings := outcomeWarnings(testCase.recordErr, testCase.recheckErr)
		var gotValue []i18n.Key
		for _, warning := range warnings {
			gotValue = append(gotValue, warning.Key)
		}
		if !slices.Equal(gotValue, testCase.expectedValue) {
			t.Errorf("%s: gotValue %v, expectedValue %v", name, gotValue, testCase.expectedValue)
		}
	}
}

func TestRepair_warningsCarryStableCode(t *testing.T) {
	result := MaintenanceResult{}.withWarnings(i18n.New(i18n.English, nil), outcomeWarnings(nil, qbit.ErrIncompleteAfterRecheck))

	expectedValue := []Warning{{Code: string(i18n.KeyWarningPausedIncompleteAfterRecheck), Text: i18n.New(i18n.English, nil).Text(i18n.KeyWarningPausedIncompleteAfterRecheck, nil)}}
	if !slices.Equal(result.Warnings, expectedValue) {
		t.Fatalf("gotValue %+v, expectedValue %+v", result.Warnings, expectedValue)
	}
}

func TestStatus_crossSeedWarningsAreMessagesRenderedAtTheEdge(t *testing.T) {
	fixture := newDecisionFixture(t)
	resolution := CrossSeedResolution{
		Behavior: crossseed.Native,
		Warnings: []i18n.Message{{Key: i18n.KeyWarningCrossSeedFallbackNative}, {Key: i18n.KeyWarningMultipleClients}},
	}
	for _, language := range bothLanguages {
		service := New(Deps{Store: fixture.store, Translator: i18n.New(language, nil), ResolveCrossSeed: func(context.Context) CrossSeedResolution { return resolution }})

		status, err := service.Status(t.Context())

		expectedCodes := []string{string(i18n.KeyWarningCrossSeedFallbackNative), string(i18n.KeyWarningMultipleClients)}
		var gotCodes []string
		for _, warning := range status.Warnings {
			gotCodes = append(gotCodes, warning.Code)
			if warning.Text == "" || warning.Text == warning.Code {
				t.Fatalf("language %s: gotValue text %q, expectedValue a translated text", language, warning.Text)
			}
		}
		if err != nil || !slices.Equal(gotCodes, expectedCodes) {
			t.Fatalf("language %s: gotValue %v %v, expectedValue %v", language, gotCodes, err, expectedCodes)
		}
	}
}

func TestProposals_maintenanceDetailsAreLabelledAndLegacyRowsStillWork(t *testing.T) {
	fixture := newDecisionFixture(t)
	legacy := `{"torrent_path":"/t/a","reason":"dedupe: no cached fingerprint","warnings":["inode non fiable"],"actionable":false}`
	current := `{"torrent_path":"/t/b","reason":"cross_device","warnings":["warning.dedupe.sampled_verify"],"actionable":false}`
	for _, details := range []string{legacy, current} {
		if _, err := fixture.store.CreateProposal(t.Context(), store.Proposal{Kind: store.ProposalDedupe, Decision: store.ProposalPending, Details: json.RawMessage(details)}, fixedNow); err != nil {
			t.Fatal(err)
		}
	}
	labels := map[i18n.Language][]string{}
	for _, language := range bothLanguages {
		service := New(Deps{Store: fixture.store, Translator: i18n.New(language, nil)})
		proposals, err := service.ListProposals(t.Context(), ProposalFilter{})
		if err != nil || len(proposals) != 2 {
			t.Fatalf("language %s: gotValue %d proposals %v, expectedValue 2", language, len(proposals), err)
		}
		for _, proposal := range proposals {
			var decoded struct {
				Reason   string   `json:"reason"`
				Label    string   `json:"label"`
				Warnings []string `json:"warnings"`
			}
			if err := json.Unmarshal(proposal.Maintenance, &decoded); err != nil || decoded.Label == "" {
				t.Fatalf("language %s: gotValue %s %v, expectedValue a label next to the reason", language, proposal.Maintenance, err)
			}
			labels[language] = append(labels[language], decoded.Label)
		}
	}
	if slices.Equal(labels[i18n.French], labels[i18n.English]) {
		t.Fatalf("gotValue %v, expectedValue labels that follow the language", labels)
	}
}

func TestTexts_onlyTextFieldsDifferBetweenLanguages(t *testing.T) {
	fixture := newDecisionFixture(t)
	resolution := CrossSeedResolution{Warnings: []i18n.Message{{Key: i18n.KeyWarningCrossSeedDelegateUnreachable}}}
	if _, err := fixture.store.CreateProposal(t.Context(), store.Proposal{
		Kind: store.ProposalDedupe, Decision: store.ProposalPending,
		Details: json.RawMessage(`{"torrent_path":"/t/b","reason":"hash_unavailable","warnings":["warning.dedupe.sampled_verify"],"actionable":false}`),
	}, fixedNow); err != nil {
		t.Fatal(err)
	}
	trees := map[i18n.Language]any{}
	for _, language := range bothLanguages {
		translator := i18n.New(language, nil)
		service := New(Deps{Store: fixture.store, Translator: translator, ResolveCrossSeed: func(context.Context) CrossSeedResolution { return resolution }})
		status, err := service.Status(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		proposals, err := service.ListProposals(t.Context(), ProposalFilter{})
		if err != nil {
			t.Fatal(err)
		}
		link := linkItem(store.Link{LinkPath: "/l"}, repair.Inspection{Condition: repair.ConditionResized}).labelled(translator)
		encoded, err := json.Marshal(map[string]any{
			"statusWarnings": status.Warnings, "proposals": proposals, "link": link,
			"applied": MaintenanceResult{}.withWarnings(translator, outcomeWarnings(errors.New("x"), qbit.ErrIncompleteAfterRecheck)),
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
}

// TestLowerLayers_neverBuildOrHoldATranslator : sous app, les paquets produisent des clés et des paramètres, jamais du
// texte rendu.
func TestLowerLayers_neverBuildOrHoldATranslator(t *testing.T) {
	for _, directory := range []string{"../hashsource", "../dedupe", "../repair", "../crossseed", "../linker"} {
		paths, err := filepath.Glob(filepath.Join(directory, "*.go"))
		if err != nil || len(paths) == 0 {
			t.Fatalf("%s: gotValue %v %v, expectedValue source files", directory, paths, err)
		}
		for _, path := range paths {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(file, func(node ast.Node) bool {
				selector, isSelector := node.(*ast.SelectorExpr)
				if !isSelector {
					return true
				}
				qualifier, isName := selector.X.(*ast.Ident)
				buildsTranslator := isName && qualifier.Name == "i18n" && (selector.Sel.Name == "New" || selector.Sel.Name == "Translator")
				if buildsTranslator {
					t.Errorf("%s: i18n.%s used below the app layer", path, selector.Sel.Name)
				}
				return true
			})
		}
	}
}
