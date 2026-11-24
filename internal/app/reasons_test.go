package app

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"necromanciarr/internal/i18n"
	"necromanciarr/internal/pipeline"
	"necromanciarr/internal/store"
)

var reasonConstantName = regexp.MustCompile(`^(Reason|RepairReason|DedupeReason|LinkReason|Exclusion)[A-Z]`)

func reasonCodesDeclaredIn(t *testing.T, directory string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(directory, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			spec, isValue := node.(*ast.ValueSpec)
			if !isValue || len(spec.Values) != len(spec.Names) {
				return true
			}
			for position, name := range spec.Names {
				literal, isLiteral := spec.Values[position].(*ast.BasicLit)
				if isLiteral && literal.Kind == token.STRING && reasonConstantName.MatchString(name.Name) {
					code, _ := strconv.Unquote(literal.Value)
					codes = append(codes, code)
				}
			}
			return true
		})
	}
	return codes
}

func TestReasonLabels_everyKnownReasonHasLabel(t *testing.T) {
	var codes []string
	for _, directory := range []string{"../pipeline", ".", "../dedupe", "../repair"} {
		codes = append(codes, reasonCodesDeclaredIn(t, directory)...)
	}
	if len(codes) < 40 {
		t.Fatalf("gotValue %d reason constants, expectedValue the pipeline, app, dedupe and repair ones", len(codes))
	}
	for _, language := range []i18n.Language{i18n.French, i18n.English} {
		translator := i18n.New(language, nil)
		for _, code := range codes {
			if gotValue := translator.ReasonLabel(code); gotValue == "" || gotValue == code {
				t.Errorf("language %s, reason %q: gotValue label %q, expectedValue a translated label", language, code, gotValue)
			}
		}
	}
}

func TestReasonLabel_unknownCodeIsRenderedAsItself(t *testing.T) {
	for _, language := range []i18n.Language{i18n.French, i18n.English} {
		if gotValue := i18n.New(language, nil).ReasonLabel("brand_new_code"); gotValue != "brand_new_code" {
			t.Fatalf("language %s: gotValue %q, expectedValue the code itself", language, gotValue)
		}
	}
}

func TestStageResult_carriesReasonLabelsAndKeepsReasonCodes(t *testing.T) {
	result := pipeline.Result{Reasons: map[string]int{pipeline.ReasonNoExactSize: 2, "brand_new_code": 1}}

	labelled := withReasonLabels(i18n.New(i18n.English, nil), result)

	expectedValue := map[string]string{pipeline.ReasonNoExactSize: i18n.New(i18n.English, nil).ReasonLabel(pipeline.ReasonNoExactSize), "brand_new_code": "brand_new_code"}
	for code, label := range expectedValue {
		if labelled.ReasonLabels[code] != label || labelled.Reasons[code] == 0 {
			t.Fatalf("gotValue %+v, expectedValue label %q for %q and unchanged counts", labelled, label, code)
		}
	}
}

func TestProposalJSON_reasonLabelsFollowLanguage(t *testing.T) {
	fixture := newDecisionFixture(t)
	candidate := fixture.verifiedCandidate("aa")
	details := `{"percent":100,"excluded_files":[{"torrent_path":"sub.srt","reason":"annex_hash_failed"}]}`
	if _, _, err := fixture.store.UpsertProposal(t.Context(), store.Proposal{
		CandidateID: candidate.ID, GroupID: pipeline.GroupOf(candidate), Kind: store.ProposalFull, Details: json.RawMessage(details),
	}, fixedNow); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.SaveCandidateHold(t.Context(), store.CandidateHold{
		CandidateID: candidate.ID, Reason: pipeline.ReasonMissingPiecesAboveCap, Kind: store.ProposalPartialAnnex,
	}, fixedNow); err != nil {
		t.Fatal(err)
	}
	labels := map[i18n.Language][]string{}
	for _, language := range []i18n.Language{i18n.French, i18n.English} {
		service := New(Deps{Store: fixture.store, Translator: i18n.New(language, nil)})
		proposals, err := service.ListProposals(t.Context(), ProposalFilter{})
		if err != nil || len(proposals) != 1 || len(proposals[0].Details.ExcludedFiles) != 1 {
			t.Fatalf("language %s: gotValue %+v %v, expectedValue one proposal with one excluded file", language, proposals, err)
		}
		excluded := proposals[0].Details.ExcludedFiles[0]
		status, err := service.Status(t.Context())
		if err != nil || len(status.Held) != 1 {
			t.Fatalf("language %s: gotValue %+v %v, expectedValue one held candidate", language, status.Held, err)
		}
		if excluded.Reason != pipeline.ExclusionAnnexHashFailed || status.Held[0].Reason != pipeline.ReasonMissingPiecesAboveCap {
			t.Fatalf("language %s: reason codes changed: %q, %q", language, excluded.Reason, status.Held[0].Reason)
		}
		labels[language] = []string{excluded.Label, status.Held[0].Label}
	}
	for position, frenchLabel := range labels[i18n.French] {
		englishLabel := labels[i18n.English][position]
		if frenchLabel == "" || englishLabel == "" || frenchLabel == englishLabel {
			t.Fatalf("gotValue fr %q en %q, expectedValue two distinct non-empty labels", frenchLabel, englishLabel)
		}
	}
}

// TestPipeline_everyCountedReasonIsAReasonIdentifier : un motif compté par une étape ne peut pas être un littéral, il
// échapperait au test des libellés.
func TestPipeline_everyCountedReasonIsAReasonIdentifier(t *testing.T) {
	paths, err := filepath.Glob("../pipeline/*.go")
	if err != nil {
		t.Fatal(err)
	}
	inspected := 0
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, isCall := node.(*ast.CallExpr)
			if !isCall {
				return true
			}
			selector, isSelector := call.Fun.(*ast.SelectorExpr)
			if !isSelector || selector.Sel.Name != "count" || len(call.Args) != 2 {
				return true
			}
			inspected++
			if literal, isLiteral := call.Args[0].(*ast.BasicLit); isLiteral {
				t.Errorf("%s: count(%s, …) uses a string literal, expectedValue a Reason constant", path, literal.Value)
			}
			return true
		})
	}
	if inspected < 20 {
		t.Fatalf("gotValue %d count calls, expectedValue the stages' ones", inspected)
	}
}
