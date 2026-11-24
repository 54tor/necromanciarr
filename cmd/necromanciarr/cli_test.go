package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"necromanciarr/internal/app"
	"necromanciarr/internal/config"
	"necromanciarr/internal/doctor"
	"necromanciarr/internal/pipeline"
	"necromanciarr/internal/store"
)

// fakeService enregistre la méthode appelée et ses arguments ; err est renvoyée par toute action, proposals sert
// ListProposals (filtré comme le vrai Service), maintenanceWarnings est posé sur chaque application dedupe ou repair.
type fakeService struct {
	calls               []string
	err                 error
	proposals           []app.Proposal
	status              app.Status
	maintenanceWarnings []string
	applyErrors         map[int64]error
	lastApplyOptions    []app.DecisionOption
	dedupePlan          []app.DedupeProposal
	linkPage            app.LinkPage
}

func (fake *fakeService) record(method string, arguments ...any) {
	fake.calls = append(fake.calls, strings.TrimSpace(method+" "+fmt.Sprint(arguments...)))
}

func (fake *fakeService) called(method string) bool {
	return slices.ContainsFunc(fake.calls, func(call string) bool { return call == method || strings.HasPrefix(call, method+" ") })
}

func (fake *fakeService) RunStage(_ context.Context, stage pipeline.Stage) (pipeline.Result, error) {
	fake.record("RunStage", stage)
	return pipeline.Result{Stage: stage, Processed: 2, Reasons: map[string]int{"no_exact_size": 1}}, fake.err
}

func (fake *fakeService) Status(context.Context) (app.Status, error) {
	fake.record("Status")
	return fake.status, fake.err
}

func (fake *fakeService) ListProposals(_ context.Context, filter app.ProposalFilter) ([]app.Proposal, error) {
	fake.record("ListProposals")
	var kept []app.Proposal
	for _, proposal := range fake.proposals {
		keptByKind := filter.Kind == "" || proposal.Kind == filter.Kind
		keptByDecision := filter.Decision == "" || proposal.Decision == filter.Decision
		if keptByKind && keptByDecision {
			kept = append(kept, proposal)
		}
	}
	return kept, fake.err
}

func (fake *fakeService) Accept(_ context.Context, proposalID int64, _ ...app.DecisionOption) error {
	fake.record("Accept", proposalID)
	return fake.err
}

func (fake *fakeService) Reject(_ context.Context, proposalID int64) error {
	fake.record("Reject", proposalID)
	return fake.err
}

func (fake *fakeService) Choose(_ context.Context, proposalID int64) error {
	fake.record("Choose", proposalID)
	return fake.err
}

func (fake *fakeService) Force(_ context.Context, proposalID int64) (app.ForceResult, error) {
	fake.record("Force", proposalID)
	return app.ForceResult{ProposalID: proposalID, Forced: true}, fake.err
}

func (fake *fakeService) AcceptGroup(_ context.Context, groupID int64) ([]int64, error) {
	fake.record("AcceptGroup", groupID)
	return []int64{groupID}, fake.err
}

func (fake *fakeService) Doctor(_ context.Context, options app.DoctorOptions) (doctor.Report, error) {
	fake.record("Doctor", options.CheckIDs)
	return doctor.Report{Checks: []doctor.NamedResult{{ID: "config", Title: "Configuration"}}}, fake.err
}

func (fake *fakeService) DedupePlan(context.Context) ([]app.DedupeProposal, error) {
	fake.record("DedupePlan")
	if fake.dedupePlan != nil {
		return fake.dedupePlan, fake.err
	}
	return []app.DedupeProposal{{ID: 7, Decision: store.ProposalPending, Details: app.DedupeDetails{TorrentPath: "/home/u/torrents/a.mkv", Actionable: true, Kind: "symlink"}}}, fake.err
}

func (fake *fakeService) DedupeAccept(_ context.Context, proposalID int64, options ...app.DecisionOption) (app.MaintenanceResult, error) {
	fake.record("DedupeAccept", proposalID)
	return fake.applied(proposalID, options)
}

// applied rend le résultat d'une application : l'erreur propre à l'id si elle est donnée, sinon err.
func (fake *fakeService) applied(proposalID int64, options []app.DecisionOption) (app.MaintenanceResult, error) {
	fake.lastApplyOptions = options
	if err, failing := fake.applyErrors[proposalID]; failing {
		return app.MaintenanceResult{}, err
	}
	return app.MaintenanceResult{ProposalID: proposalID, Applied: true, Warnings: fake.maintenanceWarnings}, fake.err
}

func (fake *fakeService) RepairScan(_ context.Context, directory string) ([]app.RepairProposal, error) {
	fake.record("RepairScan", directory)
	return nil, fake.err
}

func (fake *fakeService) RepairAccept(_ context.Context, proposalID int64, options ...app.DecisionOption) (app.MaintenanceResult, error) {
	fake.record("RepairAccept", proposalID)
	return fake.applied(proposalID, options)
}

func (fake *fakeService) ListLinks(_ context.Context, filter app.LinkFilter) (app.LinkPage, error) {
	fake.record("ListLinks", filter)
	return fake.linkPage, fake.err
}

func (*fakeService) Actions() []app.Action { return app.Actions() }

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

// useFakeService remplace l'ouverture du Service (production et diagnostic) par fake, le temps du test.
func useFakeService(t *testing.T, fake *fakeService) {
	t.Helper()
	previousService, previousDoctor := openService, openDoctorService
	openService = func(context.Context, config.Config) (app.Service, io.Closer, error) { return fake, nopCloser{}, nil }
	openDoctorService = func(config.Config) app.Service { return fake }
	t.Cleanup(func() { openService, openDoctorService = previousService, previousDoctor })
}

type cliRun struct {
	exitCode       int
	stdout, stderr string
}

func runCli(t *testing.T, configDir string, args ...string) cliRun {
	t.Helper()
	var stdout, stderr bytes.Buffer
	exitCode := run(append(args, "--config-dir", configDir), &stdout, &stderr)
	return cliRun{exitCode: exitCode, stdout: stdout.String(), stderr: stderr.String()}
}

func registryMethods() map[string]string {
	methods := map[string]string{}
	for _, action := range app.Actions() {
		methods[action.Name] = action.Method
	}
	return methods
}

func TestCli_coversEveryRegisteredAction(t *testing.T) {
	configDir := writeDoctorConfig(t, `{}`)
	methods := registryMethods()
	for _, action := range app.Actions() {
		covered := false
		for _, spec := range commands {
			if !slices.Contains(spec.actions, action.Name) {
				continue
			}
			fake := &fakeService{proposals: []app.Proposal{{ID: 1, Kind: store.ProposalDedupe, Decision: store.ProposalAccepted}}}
			useFakeService(t, fake)

			result := runCli(t, configDir, append(append(slices.Clone(spec.path), spec.example...), "--json")...)

			if result.exitCode != successExitCode || !fake.called(methods[action.Name]) || !json.Valid([]byte(result.stdout)) {
				t.Fatalf("%s via %q: exit %d, calls %v, stdout %q, stderr %q", action.Name, spec.name(), result.exitCode, fake.calls, result.stdout, result.stderr)
			}
			covered = true
		}
		if !covered {
			t.Fatalf("gotValue no command for action %q, expectedValue at least one", action.Name)
		}
	}
}

func TestCli_noCommandWithoutAnAction(t *testing.T) {
	methods := registryMethods()
	serviceType := reflect.TypeFor[app.Service]()
	for _, spec := range commands {
		if len(spec.actions) == 0 {
			t.Fatalf("gotValue command %q without action, expectedValue every command bound to the registry", spec.name())
		}
		for _, actionName := range spec.actions {
			method, registered := methods[actionName]
			if _, found := serviceType.MethodByName(method); !registered || !found {
				t.Fatalf("command %q: gotValue action %q unknown, expectedValue a registered action", spec.name(), actionName)
			}
		}
	}
}

func TestCli_neverPrintsSecrets(t *testing.T) {
	const secret = "CLISECRETKEY"
	configDir := writeDoctorConfig(t, `{"radarr": ["http://example.com:7878?apikey=`+secret+`"]}`)
	leaky := errors.New("request to http://example.com/x?apikey=" + secret + " failed, key " + secret)
	fake := &fakeService{err: leaky, status: app.Status{Warnings: []string{"warning with " + secret}}}
	useFakeService(t, fake)

	for _, args := range [][]string{{"status"}, {"status", "--json"}, {"proposals", "accept", "1"}, {"proposals", "accept", "1", "--json"}, {"scan"}} {
		result := runCli(t, configDir, args...)

		if strings.Contains(result.stdout+result.stderr, secret) || result.exitCode != failureExitCode {
			t.Fatalf("%v: exit %d, stdout %q, stderr %q", args, result.exitCode, result.stdout, result.stderr)
		}
	}
	fake.err = nil
	if result := runCli(t, configDir, "status"); strings.Contains(result.stderr, secret) || !strings.Contains(result.stderr, "warning:") {
		t.Fatalf("warnings: stderr %q, expectedValue a masked warning line", result.stderr)
	}
}

func TestCli_exitCodesMapTypedErrors(t *testing.T) {
	configDir := writeDoctorConfig(t, `{}`)
	testCases := []struct {
		name             string
		err              error
		expectedExitCode int
		expectedCode     string
	}{
		{"stale", fmt.Errorf("%w: generation", app.ErrStaleProposal), staleExitCode, errorCodeStale},
		{"refused", app.ErrConflictNeedsChoose, refusedExitCode, errorCodeRefused},
		{"invalid transition", store.ErrInvalidTransition, refusedExitCode, errorCodeRefused},
		{"not found", store.ErrProposalNotFound, failureExitCode, errorCodeNotFound},
		{"failure", errors.New("disk on fire"), failureExitCode, errorCodeFailure},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			useFakeService(t, &fakeService{err: testCase.err})

			text := runCli(t, configDir, "proposals", "accept", "1")
			result := runCli(t, configDir, "proposals", "accept", "1", "--json")

			var decoded struct {
				Error struct{ Code, Message string } `json:"error"`
			}
			decodeErr := json.Unmarshal([]byte(result.stdout), &decoded)
			if text.exitCode != testCase.expectedExitCode || result.exitCode != testCase.expectedExitCode || decodeErr != nil || decoded.Error.Code != testCase.expectedCode {
				t.Fatalf("gotValue exit %d/%d %q %v, expectedValue %d %s", text.exitCode, result.exitCode, result.stdout, decodeErr, testCase.expectedExitCode, testCase.expectedCode)
			}
		})
	}
	useFakeService(t, &fakeService{err: app.ErrStaleProposal})
	if result := runCli(t, configDir, "proposals", "accept", "1"); !strings.Contains(result.stderr, app.StaleProposalMessage) {
		t.Fatalf("gotValue stderr %q, expectedValue the stale proposal message", result.stderr)
	}
	for _, args := range [][]string{{"proposals", "accept"}, {"proposals", "accept", "zero"}, {"proposals", "list", "--kind", "bogus"}, {"proposals", "accept", "1", "2"}} {
		if result := runCli(t, configDir, args...); result.exitCode != usageExitCode {
			t.Fatalf("%v: gotValue exit %d, expectedValue usage", args, result.exitCode)
		}
	}
}

func TestCli_forceRequiresYesAndWarnsAboutRewrittenFiles(t *testing.T) {
	configDir := writeDoctorConfig(t, `{}`)
	fake := &fakeService{}
	useFakeService(t, fake)

	refused := runCli(t, configDir, "proposals", "force", "3")

	if refused.exitCode != usageExitCode || fake.called("Force") || !strings.Contains(refused.stderr, "rewritten") {
		t.Fatalf("gotValue exit %d, calls %v, stderr %q, expectedValue a refusal with the warning", refused.exitCode, fake.calls, refused.stderr)
	}
	confirmed := runCli(t, configDir, "proposals", "force", "3", "--yes")
	if confirmed.exitCode != successExitCode || !fake.called("Force 3") || !strings.Contains(confirmed.stderr, "rewritten") {
		t.Fatalf("gotValue exit %d, calls %v, stderr %q, expectedValue Force called after the warning", confirmed.exitCode, fake.calls, confirmed.stderr)
	}
}

func TestCli_helpPrintsUsage(t *testing.T) {
	for _, args := range [][]string{{"help"}, {}, {"--help"}, {"proposals"}} {
		var stdout, stderr bytes.Buffer

		exitCode := run(args, &stdout, &stderr)

		if exitCode != usageExitCode || stderr.Len() == 0 {
			t.Fatalf("%v: gotValue exit %d stderr %q, expectedValue 2 with a message", args, exitCode, stderr.String())
		}
	}
	for _, line := range strings.Split(usageText, "\n") {
		if utf8.RuneCountInString(line) > tableWidth {
			t.Fatalf("usage line wider than %d columns: %q", tableWidth, line)
		}
	}
}

func TestCli_stageCommandsPrintTextAndJSON(t *testing.T) {
	configDir := writeDoctorConfig(t, `{}`)
	fake := &fakeService{}
	useFakeService(t, fake)

	text := runCli(t, configDir, "scan")
	jsonRun := runCli(t, configDir, "verify", "--json")

	var decoded map[string]any
	if err := json.Unmarshal([]byte(jsonRun.stdout), &decoded); err != nil || decoded["stage"] != "verify" || decoded["processed"] != 2.0 {
		t.Fatalf("gotValue %q %v, expectedValue the verify result with stable field names", jsonRun.stdout, err)
	}
	if text.exitCode != 0 || !strings.Contains(text.stdout, "scan: processed 2") || !strings.Contains(text.stdout, "reason no_exact_size: 1") {
		t.Fatalf("gotValue %+v, expectedValue a readable summary", text)
	}
	if strings.Contains(text.stdout+text.stderr+jsonRun.stdout, "\x1b[") {
		t.Fatal("gotValue ANSI escape codes, expectedValue plain output")
	}
}

func TestCli_proposalsListFitsIn100Columns(t *testing.T) {
	configDir := writeDoctorConfig(t, `{}`)
	longName := strings.Repeat("Very.Long.Release.Name.", 10) + "2160p"
	maintenance := json.RawMessage(`{"torrent_path": "/home/u/torrents/` + strings.Repeat("deep/", 30) + `movie.mkv"}`)
	fake := &fakeService{proposals: []app.Proposal{
		{ID: 12, Kind: store.ProposalPartialAnnex, Decision: store.ProposalApplied, Step: store.StepSeededPausedIncomplete, GroupID: 3, CandidateName: longName, Current: true},
		{ID: 13, Kind: store.ProposalDedupe, Decision: store.ProposalPending, Current: true, Maintenance: maintenance},
	}}
	useFakeService(t, fake)

	text := runCli(t, configDir, "proposals", "list", "--state", "applied", "--kind", "partial_annex")
	all := runCli(t, configDir, "proposals", "list")

	for _, line := range strings.Split(strings.TrimSpace(all.stdout), "\n") {
		if utf8.RuneCountInString(line) > tableWidth {
			t.Fatalf("gotValue a %d-column line %q, expectedValue at most %d", utf8.RuneCountInString(line), line, tableWidth)
		}
	}
	if !strings.Contains(text.stdout, "seeded_paused_incomplete") || strings.Contains(text.stdout, "dedupe") || !strings.Contains(all.stdout, "movie.mkv") {
		t.Fatalf("gotValue %q and %q, expectedValue the filtered table and the dedupe path", text.stdout, all.stdout)
	}
}

func TestCli_dedupeApplyAllAcceptedAppliesOnlyAcceptedProposals(t *testing.T) {
	configDir := writeDoctorConfig(t, `{}`)
	fake := &fakeService{proposals: []app.Proposal{
		{ID: 4, Kind: store.ProposalDedupe, Decision: store.ProposalAccepted},
		{ID: 5, Kind: store.ProposalDedupe, Decision: store.ProposalPending},
		{ID: 6, Kind: store.ProposalRepair, Decision: store.ProposalAccepted},
	}}
	useFakeService(t, fake)

	result := runCli(t, configDir, "dedupe", "apply", "--all-accepted")

	if result.exitCode != 0 || !fake.called("DedupeAccept 4") || fake.called("DedupeAccept 5") || fake.called("DedupeAccept 6") || fake.called("Accept") {
		t.Fatalf("gotValue exit %d calls %v, expectedValue only proposal 4 applied and nothing accepted", result.exitCode, fake.calls)
	}
	for _, args := range [][]string{{"dedupe", "apply"}, {"dedupe", "apply", "4", "--all-accepted"}} {
		if usage := runCli(t, configDir, args...); usage.exitCode != usageExitCode {
			t.Fatalf("%v: gotValue exit %d, expectedValue usage", args, usage.exitCode)
		}
	}
	single := &fakeService{}
	useFakeService(t, single)
	result = runCli(t, configDir, "repair", "apply", "9")
	if result.exitCode != 0 || !slices.Equal(single.calls, []string{"RepairAccept 9"}) || !slices.Contains(single.lastApplyOptions, app.AcceptPending) {
		t.Fatalf("gotValue exit %d calls %v options %v, expectedValue one RepairAccept that accepts a pending proposal itself", result.exitCode, single.calls, single.lastApplyOptions)
	}
}

func TestCli_warningsOnlyExitZeroWithAWarningLine(t *testing.T) {
	configDir := writeDoctorConfig(t, `{}`)
	useFakeService(t, &fakeService{maintenanceWarnings: []string{app.WarningPausedIncompleteAfterRecheck}})

	text := runCli(t, configDir, "dedupe", "apply", "8")
	jsonRun := runCli(t, configDir, "dedupe", "apply", "8", "--json")

	if text.exitCode != 0 || !strings.Contains(text.stderr, "warning: proposal 8: "+app.WarningPausedIncompleteAfterRecheck) {
		t.Fatalf("gotValue %+v, expectedValue exit 0 and a warning line", text)
	}
	if jsonRun.exitCode != 0 || !strings.Contains(jsonRun.stdout, `"warnings"`) || strings.Contains(jsonRun.stderr, "warning:") {
		t.Fatalf("gotValue %+v, expectedValue the warning inside the JSON only", jsonRun)
	}
}

func TestCli_dedupePlanAndRepairScanTextOutput(t *testing.T) {
	configDir := writeDoctorConfig(t, `{}`)
	fake := &fakeService{}
	useFakeService(t, fake)

	plan := runCli(t, configDir, "dedupe", "plan")
	scan := runCli(t, configDir, "repair", "scan", "--dir", "/home/u/torrents")

	if plan.exitCode != 0 || !strings.Contains(plan.stdout, "/home/u/torrents/a.mkv") || !strings.Contains(plan.stdout, "estimated read") {
		t.Fatalf("plan: gotValue %+v", plan)
	}
	if scan.exitCode != 0 || !fake.called("RepairScan /home/u/torrents") || !strings.Contains(scan.stdout, "no broken link") {
		t.Fatalf("scan: gotValue %+v calls %v", scan, fake.calls)
	}
}

func TestCli_neverPrintsSecretsWithJSONEscapedCharacters(t *testing.T) {
	const secret = `tok&en<x"y\zé`
	t.Setenv("LIBRARY_PROVIDER_TOKEN", secret)
	configDir := writeDoctorConfig(t, `{}`)
	escaped, err := json.Marshal(secret)
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{secret, strings.Trim(string(escaped), `"`), `tok&en<x\"y\\zé`}
	leakyText := "provider said " + secret
	fake := &fakeService{
		status:              app.Status{Warnings: []string{leakyText}},
		maintenanceWarnings: []string{leakyText},
	}
	useFakeService(t, fake)
	assertMasked := func(args []string, result cliRun) {
		t.Helper()
		for _, form := range forbidden {
			if strings.Contains(result.stdout+result.stderr, form) {
				t.Fatalf("%v: gotValue %q in stdout %q stderr %q, expectedValue every form masked", args, form, result.stdout, result.stderr)
			}
		}
	}

	for _, args := range [][]string{{"status"}, {"status", "--json"}, {"dedupe", "apply", "2"}, {"dedupe", "apply", "2", "--json"}} {
		assertMasked(args, runCli(t, configDir, args...))
	}
	fake.err = errors.New("request failed: " + secret)
	for _, args := range [][]string{{"status"}, {"status", "--json"}} {
		result := runCli(t, configDir, args...)
		assertMasked(args, result)
		if result.exitCode != failureExitCode {
			t.Fatalf("%v: gotValue exit %d, expectedValue failure", args, result.exitCode)
		}
	}
	if result := runCli(t, configDir, "status", "--json"); !json.Valid([]byte(result.stdout)) || !strings.Contains(result.stdout, `"error"`) {
		t.Fatalf("gotValue %q, expectedValue a valid error object", result.stdout)
	}
}

func TestCli_listOutputsAreAlwaysJSONArrays(t *testing.T) {
	configDir := writeDoctorConfig(t, `{}`)
	oneAccepted := []app.Proposal{
		{ID: 4, Kind: store.ProposalDedupe, Decision: store.ProposalAccepted},
		{ID: 6, Kind: store.ProposalRepair, Decision: store.ProposalAccepted},
	}
	testCases := []struct {
		name      string
		proposals []app.Proposal
		args      []string
	}{
		{"proposals list empty", nil, []string{"proposals", "list"}},
		{"dedupe plan empty", nil, []string{"dedupe", "plan"}},
		{"repair scan empty", nil, []string{"repair", "scan"}},
		{"dedupe apply all nothing", nil, []string{"dedupe", "apply", "--all-accepted"}},
		{"dedupe apply all one", oneAccepted, []string{"dedupe", "apply", "--all-accepted"}},
		{"repair apply all nothing", nil, []string{"repair", "apply", "--all-accepted"}},
		{"repair apply all one", oneAccepted, []string{"repair", "apply", "--all-accepted"}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			useFakeService(t, &fakeService{proposals: testCase.proposals, dedupePlan: []app.DedupeProposal{}})

			result := runCli(t, configDir, append(testCase.args, "--json")...)

			var decoded any
			if err := json.Unmarshal([]byte(result.stdout), &decoded); err != nil || result.exitCode != 0 {
				t.Fatalf("gotValue %q exit %d %v, expectedValue valid JSON", result.stdout, result.exitCode, err)
			}
			if _, isArray := decoded.([]any); !isArray {
				t.Fatalf("gotValue %T (%s), expectedValue a top-level array", decoded, result.stdout)
			}
		})
	}
}

func TestCli_applyAllAcceptedAppliesEveryProposalAndReportsTheWorstFailure(t *testing.T) {
	configDir := writeDoctorConfig(t, `{}`)
	accepted := []app.Proposal{
		{ID: 1, Kind: store.ProposalDedupe, Decision: store.ProposalAccepted},
		{ID: 2, Kind: store.ProposalDedupe, Decision: store.ProposalAccepted},
		{ID: 3, Kind: store.ProposalDedupe, Decision: store.ProposalAccepted},
	}
	testCases := []struct {
		name             string
		applyErrors      map[int64]error
		expectedExitCode int
	}{
		{"one failure", map[int64]error{1: errors.New("disk on fire")}, failureExitCode},
		{"stale wins", map[int64]error{1: errors.New("disk on fire"), 2: app.ErrStaleProposal}, staleExitCode},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			fake := &fakeService{proposals: accepted, applyErrors: testCase.applyErrors}
			useFakeService(t, fake)

			result := runCli(t, configDir, "dedupe", "apply", "--all-accepted", "--json")

			var items []struct {
				ProposalID int64 `json:"proposal_id"`
				Applied    bool  `json:"applied"`
				Error      *struct{ Code string }
			}
			if err := json.Unmarshal([]byte(result.stdout), &items); err != nil || len(items) != 3 || !fake.called("DedupeAccept 3") {
				t.Fatalf("gotValue %q %v calls %v, expectedValue all three applied independently", result.stdout, err, fake.calls)
			}
			if result.exitCode != testCase.expectedExitCode || items[0].Error == nil || !items[2].Applied {
				t.Fatalf("gotValue exit %d items %+v, expectedValue %d with the per-item errors", result.exitCode, items, testCase.expectedExitCode)
			}
		})
	}
	useFakeService(t, &fakeService{applyErrors: map[int64]error{5: fmt.Errorf("%w: changed", app.ErrStaleProposal)}})
	if result := runCli(t, configDir, "dedupe", "apply", "5"); result.exitCode != staleExitCode {
		t.Fatalf("single stale: gotValue exit %d, expectedValue %d", result.exitCode, staleExitCode)
	}
}

func TestCli_jsonStaysValidWithAssignmentsAndQuotesInMessages(t *testing.T) {
	t.Setenv("LIBRARY_PROVIDER_TOKEN", `x"y`)
	configDir := writeDoctorConfig(t, `{}`)
	tricky := `token=x"y password=a\b"c url http://example.com/?apikey=k"q`
	fake := &fakeService{status: app.Status{Warnings: []string{tricky}}, maintenanceWarnings: []string{tricky}}
	useFakeService(t, fake)
	runs := [][]string{{"status", "--json"}, {"dedupe", "apply", "2", "--json"}}

	fake.err = nil
	for _, args := range runs {
		if result := runCli(t, configDir, args...); !json.Valid([]byte(result.stdout)) || strings.Contains(result.stdout, `x\"y`) {
			t.Fatalf("%v: gotValue %q, expectedValue valid JSON without the secret", args, result.stdout)
		}
	}
	fake.err = errors.New(tricky)
	for _, args := range runs {
		if result := runCli(t, configDir, args...); !json.Valid([]byte(result.stdout)) || !strings.Contains(result.stdout, `"error"`) {
			t.Fatalf("%v: gotValue %q, expectedValue a valid error object", args, result.stdout)
		}
	}
}

func sampleLinkPage() app.LinkPage {
	longPath := "/home/u/links/" + strings.Repeat("Very.Long.Movie.Name.", 8) + "mkv"
	return app.LinkPage{
		Items: []app.LinkItem{
			{ID: 3, LinkPath: "/home/u/links/ok.mkv", TargetPath: "/home/u/library/ok.mkv", Kind: "symlink", State: app.LinkStateOK},
			{ID: 4, LinkPath: longPath, TargetPath: "/home/u/library/gone.mkv", Kind: "hardlink", State: app.LinkStateLost, Reason: "the link target is missing"},
		},
		Total: 9, Next: 12, Truncated: true,
		Counts: map[string]int{app.LinkStateOK: 1, app.LinkStateDiverged: 0, app.LinkStateLost: 8, app.LinkStateUnreadable: 0},
	}
}

func TestCli_linksListPrintsATableAndStableJSON(t *testing.T) {
	configDir := writeDoctorConfig(t, `{}`)
	fake := &fakeService{linkPage: sampleLinkPage()}
	useFakeService(t, fake)

	text := runCli(t, configDir, "links", "list", "--state", "lost", "--limit", "2", "--offset", "10")
	jsonRun := runCli(t, configDir, "links", "list", "--json")

	if text.exitCode != successExitCode || !fake.called("ListLinks {lost 2 10}") {
		t.Fatalf("gotValue %+v calls %v, expectedValue exit 0 and the filter passed through", text, fake.calls)
	}
	for _, expectedValue := range []string{"lost 8", "/home/u/links/ok.mkv", "the link target is missing", "showing 11-12 of 9", "--offset 12"} {
		if !strings.Contains(text.stdout, expectedValue) {
			t.Fatalf("gotValue %q, expectedValue %q in the text output", text.stdout, expectedValue)
		}
	}
	for _, line := range strings.Split(text.stdout, "\n") {
		if gotValue := utf8.RuneCountInString(line); gotValue > 100 {
			t.Fatalf("gotValue a %d-column line %q, expectedValue at most 100", gotValue, line)
		}
	}
	if !strings.Contains(text.stderr, "warning: the inspection time budget ran out") {
		t.Fatalf("gotValue stderr %q, expectedValue the truncation warning", text.stderr)
	}
	var gotValue map[string]any
	if err := json.Unmarshal([]byte(jsonRun.stdout), &gotValue); err != nil || jsonRun.exitCode != successExitCode {
		t.Fatalf("gotValue %+v, expectedValue a JSON page", jsonRun)
	}
	for _, field := range []string{"items", "total", "next", "truncated", "counts"} {
		if _, present := gotValue[field]; !present {
			t.Fatalf("gotValue %v, expectedValue the field %q", gotValue, field)
		}
	}
	firstItem := gotValue["items"].([]any)[0].(map[string]any)
	for _, field := range []string{"id", "link_path", "target_path", "kind", "state", "recorded_at"} {
		if _, present := firstItem[field]; !present {
			t.Fatalf("gotValue %v, expectedValue the item field %q", firstItem, field)
		}
	}
}

func TestCli_linksListRejectsABadCountAndKeepsAnEmptyListAnArray(t *testing.T) {
	configDir := writeDoctorConfig(t, `{}`)
	fake := &fakeService{}
	useFakeService(t, fake)

	badLimit := runCli(t, configDir, "links", "list", "--limit", "many", "--json")
	if badLimit.exitCode != usageExitCode || !strings.Contains(badLimit.stdout, `"usage"`) || fake.called("ListLinks") {
		t.Fatalf("gotValue %+v calls %v, expectedValue a usage error before any call", badLimit, fake.calls)
	}

	empty := runCli(t, configDir, "links", "list", "--json")

	if !strings.Contains(empty.stdout, `"items": []`) {
		t.Fatalf("gotValue %q, expectedValue an empty items array", empty.stdout)
	}
}
