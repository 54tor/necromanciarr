package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"necromanciarr/internal/app"
	"necromanciarr/internal/doctor"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/pipeline"
	"necromanciarr/internal/store"
)

// textFieldNames sont les seuls champs JSON dont la valeur suit la langue ; tout autre champ (codes, états, noms) est stable.
var textFieldNames = []string{"message", "detail", "hint", "label", "text", "title", "description"}

func isTextField(name string) bool {
	return slices.Contains(textFieldNames, name) || strings.HasSuffix(name, "_labels")
}

// compareJSONTrees liste les écarts entre fr et en : un champ de texte n'est exempté que si ses deux valeurs sont des
// chaînes, sinon on descend (un objet ou un tableau sous « detail » porte peut-être un code).
func compareJSONTrees(path string, frenchTree, englishTree any) []string {
	var differences []string
	switch frenchValue := frenchTree.(type) {
	case map[string]any:
		englishValue, isMap := englishTree.(map[string]any)
		if !isMap || len(englishValue) != len(frenchValue) {
			return []string{fmt.Sprintf("%s: gotValue en %v, expectedValue the same keys as fr %v", path, englishTree, frenchTree)}
		}
		for name, frenchChild := range frenchValue {
			englishChild, found := englishValue[name]
			if !found {
				differences = append(differences, path+"."+name+": missing in en")
				continue
			}
			_, frenchIsText := frenchChild.(string)
			_, englishIsText := englishChild.(string)
			if isTextField(name) && frenchIsText && englishIsText {
				continue
			}
			differences = append(differences, compareJSONTrees(path+"."+name, frenchChild, englishChild)...)
		}
	case []any:
		englishValue, isSlice := englishTree.([]any)
		if !isSlice || len(englishValue) != len(frenchValue) {
			return []string{fmt.Sprintf("%s: gotValue en %v, expectedValue the same length as fr %v", path, englishTree, frenchTree)}
		}
		for position, frenchChild := range frenchValue {
			differences = append(differences, compareJSONTrees(fmt.Sprintf("%s[%d]", path, position), frenchChild, englishValue[position])...)
		}
	default:
		if frenchTree != englishTree {
			differences = append(differences, fmt.Sprintf("%s: gotValue en %v, expectedValue fr %v (only text fields may differ)", path, englishTree, frenchTree))
		}
	}
	return differences
}

func TestCompareJSONTrees_recursesIntoNonStringTextFields(t *testing.T) {
	frenchTree := map[string]any{"detail": map[string]any{"code": "a"}, "message": "bonjour"}
	englishTree := map[string]any{"detail": map[string]any{"code": "b"}, "message": "hello"}

	gotValue := compareJSONTrees("root", frenchTree, englishTree)

	if len(gotValue) != 1 || !strings.Contains(gotValue[0], "root.detail.code") {
		t.Fatalf("gotValue %v, expectedValue only the code under detail to differ", gotValue)
	}
}

func jsonOf(t *testing.T, configDir, language string, fake *fakeService, args ...string) any {
	t.Helper()
	useFakeService(t, fake)
	result := runCli(t, configDir, append(slices.Clone(args), "--json", "--language", language)...)
	var decoded any
	if err := json.Unmarshal([]byte(result.stdout), &decoded); err != nil {
		t.Fatalf("%v (%s): stdout %q is not JSON", args, language, result.stdout)
	}
	return decoded
}

func TestJSONOutput_onlyTextFieldsDifferBetweenLanguages(t *testing.T) {
	configDir := writeDoctorConfig(t, `{}`)
	proposals := []app.Proposal{{ID: 1, Kind: store.ProposalFull, Decision: store.ProposalPending, Current: true, CandidateName: "Movie.2020"}}
	testCases := []struct {
		label string
		fake  func() *fakeService
		args  []string
	}{
		{"status", func() *fakeService { return &fakeService{status: app.Status{CrossSeedMode: "delegate"}} }, []string{"status"}},
		{"links list", func() *fakeService { return &fakeService{linkPage: sampleLinkPage()} }, []string{"links", "list"}},
		{"status with warnings", func() *fakeService {
			return &fakeService{status: app.Status{Warnings: []app.Warning{{Code: "warning.crossseed.fallback_native", Text: "x"}}}}
		}, []string{"status"}},
		{"dedupe apply with warnings", func() *fakeService {
			return &fakeService{maintenanceWarnings: []app.Warning{{Code: "warning.recheck_failed", Text: "y"}}}
		}, []string{"dedupe", "apply", "5"}},
		{"proposals list", func() *fakeService { return &fakeService{proposals: proposals} }, []string{"proposals", "list"}},
		{"doctor", func() *fakeService { return &fakeService{} }, []string{"doctor"}},
		{"sources list", func() *fakeService { return &fakeService{sourceRows: sampleSourceRows()} }, []string{"sources", "list"}},
		{"sources test failed", func() *fakeService { return &fakeService{sourceTest: &failedSourceTest} }, []string{"sources", "test", "a"}},
		{"volumes list", func() *fakeService { return &fakeService{volumeRows: sampleVolumeRows()} }, []string{"volumes", "list"}},
		{"volumes probe failed", func() *fakeService { return &fakeService{volumeProbe: &failedVolumeProbe} }, []string{"volumes", "probe", "seedbox"}},
		{"volumes commands", func() *fakeService { return &fakeService{} }, []string{"volumes", "commands", "seedbox"}},
		{"doctor with findings", func() *fakeService { return &fakeService{doctorReport: &doctorFindingsReport} }, []string{"doctor"}},
		{"stage", func() *fakeService { return &fakeService{} }, []string{"scan"}},
		{"refused", func() *fakeService { return &fakeService{err: app.ErrConflictNeedsChoose} }, []string{"proposals", "accept", "999"}},
		{"stale", func() *fakeService { return &fakeService{err: fmt.Errorf("%w: generation 3", app.ErrStaleProposal)} }, []string{"proposals", "accept", "999"}},
		{"untyped failure", func() *fakeService { return &fakeService{err: fmt.Errorf("disk on fire")} }, []string{"proposals", "accept", "999"}},
		{"usage", func() *fakeService { return &fakeService{} }, []string{"proposals", "accept", "zero"}},
		{"confirmation required", func() *fakeService { return &fakeService{} }, []string{"proposals", "force", "3"}},
		{"apply all accepted, one failing", func() *fakeService {
			return &fakeService{
				proposals:   []app.Proposal{{ID: 5, Kind: store.ProposalDedupe, Decision: store.ProposalAccepted}},
				applyErrors: map[int64]error{5: app.ErrNotAccepted},
			}
		}, []string{"dedupe", "apply", "--all-accepted"}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.label, func(t *testing.T) {
			frenchTree := jsonOf(t, configDir, "fr", testCase.fake(), testCase.args...)
			englishTree := jsonOf(t, configDir, "en", testCase.fake(), testCase.args...)

			for _, difference := range compareJSONTrees(testCase.label, frenchTree, englishTree) {
				t.Error(difference)
			}
		})
	}
}

func TestJSONOutput_errorMessageFollowsTheLanguageAndTheCodeDoesNot(t *testing.T) {
	configDir := writeDoctorConfig(t, `{}`)
	messageIn := func(language string) (code, message string) {
		useFakeService(t, &fakeService{err: app.ErrConflictNeedsChoose})
		result := runCli(t, configDir, "proposals", "accept", "1", "--json", "--language", language)
		var decoded struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		if err := json.Unmarshal([]byte(result.stdout), &decoded); err != nil {
			t.Fatalf("stdout %q is not JSON", result.stdout)
		}
		return decoded.Error.Code, decoded.Error.Message
	}

	frenchCode, frenchMessage := messageIn("fr")
	englishCode, englishMessage := messageIn("en")

	if frenchCode != "refused" || englishCode != "refused" {
		t.Fatalf("gotValue codes %q and %q, expectedValue refused in both languages", frenchCode, englishCode)
	}
	if !strings.Contains(frenchMessage, "litige") || !strings.Contains(englishMessage, "conflict") {
		t.Fatalf("gotValue fr %q en %q, expectedValue messages in their language", frenchMessage, englishMessage)
	}
}

func TestUsage_rendersInConfiguredLanguage(t *testing.T) {
	for language, expectedValue := range map[i18n.Language]string{i18n.French: "actions disponibles :", i18n.English: "commands:"} {
		gotValue := usageText(i18n.New(language, nil))

		if !strings.Contains(gotValue, expectedValue) {
			t.Errorf("%s: gotValue %q, expectedValue it to contain %q", language, gotValue, expectedValue)
		}
		for _, line := range strings.Split(gotValue, "\n") {
			if utf8.RuneCountInString(line) > tableWidth {
				t.Errorf("%s: usage line wider than %d columns: %q", language, tableWidth, line)
			}
		}
	}
}

func TestUsage_commandLineLanguageDecidesBeforeTheConfigurationLoads(t *testing.T) {
	configDir := writeDoctorConfig(t, `{}`)
	useFakeService(t, &fakeService{})

	english := runCli(t, configDir, "proposals", "accept", "--language", "en")
	french := runCli(t, configDir, "proposals", "accept", "--language", "fr")

	if english.exitCode != usageExitCode || !strings.Contains(english.stderr, "commands:") || !strings.Contains(french.stderr, "actions disponibles :") {
		t.Fatalf("gotValue en %q fr %q, expectedValue the usage in each language", english.stderr, french.stderr)
	}
}

func TestText_stageResultAndStatusFollowTheLanguage(t *testing.T) {
	configDir := writeDoctorConfig(t, `{}`)
	useFakeService(t, &fakeService{})

	english := runCli(t, configDir, "scan", "--language", "en")
	french := runCli(t, configDir, "scan", "--language", "fr")

	if !strings.Contains(english.stdout, "scan: processed 2") || !strings.Contains(french.stdout, "scan : 2 traités") {
		t.Fatalf("gotValue en %q fr %q, expectedValue the summary in each language", english.stdout, french.stdout)
	}
	if !strings.Contains(english.stdout, "reason no_exact_size (no torrent of exactly this size): 1") || !strings.Contains(french.stdout, "motif no_exact_size (aucun torrent de cette taille exacte) : 1") {
		t.Fatalf("gotValue en %q fr %q, expectedValue the reason line with the code unchanged", english.stdout, french.stdout)
	}
}

func TestText_controlCharactersInParamsNeverReachTheTerminal(t *testing.T) {
	configDir := writeDoctorConfig(t, `{}`)
	useFakeService(t, &fakeService{proposals: []app.Proposal{{ID: 1, Kind: store.ProposalFull, Current: true, CandidateName: "evil\x1b[31mred\x07"}}})

	result := runCli(t, configDir, "proposals", "list", "--language", "fr")

	if strings.ContainsAny(result.stdout, "\x1b\x07") || !strings.Contains(result.stdout, "evil") {
		t.Fatalf("gotValue %q, expectedValue the name without control characters", result.stdout)
	}
}

func TestConfigError_unknownLanguageEchoesAtMostTwentyCharacters(t *testing.T) {
	var result cliRun
	longValue := strings.Repeat("x", 60)

	result = runCli(t, writeDoctorConfig(t, `{}`), "status", "--language", longValue)

	if result.exitCode != usageExitCode || strings.Contains(result.stderr, longValue) || !strings.Contains(result.stderr, strings.Repeat("x", 20)) {
		t.Fatalf("gotValue exit %d stderr %q, expectedValue a usage error echoing 20 characters of the value", result.exitCode, result.stderr)
	}
}

func TestErrors_confirmationRequiredKeepsItsCodeAndExitCodeAndIsTranslated(t *testing.T) {
	configDir := writeDoctorConfig(t, `{}`)
	useFakeService(t, &fakeService{})

	french := runCli(t, configDir, "proposals", "force", "3", "--json", "--language", "fr")
	english := runCli(t, configDir, "proposals", "force", "3", "--language", "en")

	if french.exitCode != usageExitCode || !strings.Contains(french.stdout, `"code": "confirmation_required"`) ||
		!strings.Contains(french.stdout, "confirmation requise : relancer avec --yes") {
		t.Fatalf("gotValue %d %q, expectedValue the French message with the stable code", french.exitCode, french.stdout)
	}
	if !strings.Contains(english.stderr, "error: confirmation required: rerun with --yes") || strings.Contains(english.stderr, "internal error") {
		t.Fatalf("gotValue %q, expectedValue the English confirmation message", english.stderr)
	}
}

func TestErrors_untypedFailureIsAnInternalErrorWithoutAServerLogHint(t *testing.T) {
	configDir := writeDoctorConfig(t, `{}`)
	useFakeService(t, &fakeService{err: fmt.Errorf("disk on fire")})

	french := runCli(t, configDir, "proposals", "accept", "1", "--language", "fr")
	english := runCli(t, configDir, "proposals", "accept", "1", "--language", "en")

	if !strings.Contains(french.stderr, "erreur interne (disk on fire)") || !strings.Contains(english.stderr, "internal error (disk on fire)") {
		t.Fatalf("gotValue fr %q en %q, expectedValue the internal error with its detail", french.stderr, english.stderr)
	}
	if strings.Contains(french.stderr+english.stderr, "journal") || strings.Contains(french.stderr+english.stderr, "server log") {
		t.Fatalf("gotValue %q %q, expectedValue no server log hint on the CLI", french.stderr, english.stderr)
	}
}

func TestErrors_unknownLanguageUsesItsTranslatedMessage(t *testing.T) {
	configDir := writeDoctorConfig(t, `{}`)

	french := runCli(t, configDir, "status", "--language", "de")
	var stderr bytes.Buffer
	exitCode := run([]string{"config", "check", "--config-dir", configDir, "--language", "de"}, &bytes.Buffer{}, &stderr)

	if french.exitCode != usageExitCode || !strings.Contains(french.stderr, `langue inconnue « de » : valeurs admises fr, en`) {
		t.Fatalf("gotValue %d %q, expectedValue the translated message", french.exitCode, french.stderr)
	}
	if exitCode != usageExitCode || !strings.Contains(stderr.String(), "langue inconnue") {
		t.Fatalf("gotValue %d %q, expectedValue the translated message from config check", exitCode, stderr.String())
	}
}

func TestReportError_stripsControlCharactersFromTheEchoedValue(t *testing.T) {
	var stderr bytes.Buffer

	exitCode := run([]string{"config", "check", "--config-dir", t.TempDir(), "--language", "\x1b]0;pwned\x07x"}, &bytes.Buffer{}, &stderr)

	if exitCode != usageExitCode || strings.ContainsAny(stderr.String(), "\x1b\x07") {
		t.Fatalf("gotValue %d %q, expectedValue no control character", exitCode, stderr.String())
	}
}

func TestStageSummary_followsPluralRules(t *testing.T) {
	for _, testCase := range []struct {
		language      i18n.Language
		processed     int
		failed        int
		expectedLines []string
	}{
		{i18n.French, 1, 1, []string{"scan : 1 traité, 0 ignoré, 1 en échec", "1 élément en échec"}},
		{i18n.French, 2, 2, []string{"scan : 2 traités, 0 ignoré, 2 en échec", "2 éléments en échec"}},
		{i18n.English, 1, 1, []string{"scan: processed 1, skipped 0, failed 1", "1 item failed"}},
		{i18n.English, 0, 2, []string{"scan: processed 0, skipped 0, failed 2", "2 items failed"}},
	} {
		translator := i18n.New(testCase.language, nil)
		var summary strings.Builder
		writeStageResult(&summary, translator, pipeline.Result{Stage: "scan", Processed: testCase.processed, Failed: testCase.failed})
		warnings := stageWarnings(translator, pipeline.Result{Failed: testCase.failed})

		gotValue := summary.String() + strings.Join(warnings, "\n")
		for _, expectedValue := range testCase.expectedLines {
			if !strings.Contains(gotValue, expectedValue) {
				t.Errorf("%s: gotValue %q, expectedValue it to contain %q", testCase.language, gotValue, expectedValue)
			}
		}
	}
}

func TestApiKeyHint_followsTheLanguage(t *testing.T) {
	for language, expectedValue := range map[string]string{"fr": "à placer dans .env", "en": "put it in .env"} {
		t.Setenv("NECROMANCIARR_LANGUAGE", language)
		var stderr bytes.Buffer

		exitCode := run([]string{"api-key"}, &bytes.Buffer{}, &stderr)

		if exitCode != successExitCode || !strings.Contains(stderr.String(), expectedValue) {
			t.Fatalf("%s: gotValue %d %q, expectedValue %q", language, exitCode, stderr.String(), expectedValue)
		}
	}
}

var doctorFindingsReport = doctor.Report{Worst: doctor.Fail, Checks: []doctor.Outcome{
	{ID: "config", TitleKey: i18n.KeyDoctorTitleConfig, Result: doctor.Result{
		Status: doctor.Warn, Code: doctor.CodeWorkingDirDotEnv,
		Detail: i18n.Message{Key: i18n.KeyDoctorConfigDotEnv, Params: map[string]string{"dir": "/home/u/config"}},
		Hint:   i18n.Message{Key: i18n.KeyDoctorConfigDotEnvHint},
	}},
	{ID: "arr:radarr:0", TitleKey: i18n.KeyDoctorTitleArr, TitleParams: map[string]string{"kind": "radarr", "index": "0"}, Result: doctor.Result{
		Status: doctor.Fail, Code: doctor.CodeArrKeyRejected,
		Detail: i18n.Message{Key: i18n.KeyDoctorArrKeyRejected}, Hint: i18n.Message{Key: i18n.KeyDoctorArrKeyRejectedHint},
	}},
	{ID: "library-roots", TitleKey: i18n.KeyDoctorTitleLibraryRoots, Result: doctor.Result{
		Status: doctor.Warn, Code: doctor.CodeRootEmpty,
		Details: []i18n.Message{{Key: i18n.KeyDoctorRootNoMedia, Params: map[string]string{"root": "/srv/media", "entries": "3"}}},
	}},
}}

func TestDoctorConfigFailure_codeIsStableAndTextFollowsTheLanguage(t *testing.T) {
	configDir := writeDoctorConfig(t, `{"bogus": 1}`)

	frenchTree := jsonOf(t, configDir, "fr", &fakeService{}, "doctor")
	englishTree := jsonOf(t, configDir, "en", &fakeService{}, "doctor")

	for _, difference := range compareJSONTrees("doctor config failure", frenchTree, englishTree) {
		t.Error(difference)
	}
	code := englishTree.(map[string]any)["checks"].([]any)[0].(map[string]any)["code"]
	if code != "config_invalid" {
		t.Fatalf("gotValue code %v, expectedValue config_invalid", code)
	}
}

func TestDoctorConfigFailure_namesTheMissingSecretKey(t *testing.T) {
	t.Setenv("NECROMANCIARR_SECRET_KEY", "")
	configDir := writeDoctorConfig(t, `{"torznab": ["https://prowlarr.example.com/12/api?apikey=secret-indexer-a"]}`)

	checks := jsonOf(t, configDir, "en", &fakeService{}, "doctor").(map[string]any)["checks"].([]any)

	codes := []any{checks[0].(map[string]any)["code"], checks[len(checks)-1].(map[string]any)["code"]}
	if len(checks) != 2 || codes[0] != "config_invalid" || codes[1] != "secret_key_missing" {
		t.Fatalf("gotValue %v, expectedValue config_invalid then secret_key_missing", checks)
	}
}
