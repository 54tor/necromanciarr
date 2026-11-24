package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"necromanciarr/internal/app"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/store"
)

// textFieldNames sont les seuls champs JSON dont la valeur suit la langue ; tout autre champ (codes, états, noms) est stable.
var textFieldNames = []string{"message", "detail", "hint", "label", "text", "title", "description"}

func isTextField(name string) bool {
	return slices.Contains(textFieldNames, name) || strings.HasSuffix(name, "_labels")
}

func compareJSONTrees(t *testing.T, path string, frenchTree, englishTree any) {
	t.Helper()
	switch frenchValue := frenchTree.(type) {
	case map[string]any:
		englishValue, isMap := englishTree.(map[string]any)
		if !isMap || len(englishValue) != len(frenchValue) {
			t.Errorf("%s: gotValue en %v, expectedValue the same keys as fr %v", path, englishTree, frenchTree)
			return
		}
		for name, frenchChild := range frenchValue {
			englishChild, found := englishValue[name]
			if !found {
				t.Errorf("%s.%s: missing in en", path, name)
				continue
			}
			if isTextField(name) {
				continue
			}
			compareJSONTrees(t, path+"."+name, frenchChild, englishChild)
		}
	case []any:
		englishValue, isSlice := englishTree.([]any)
		if !isSlice || len(englishValue) != len(frenchValue) {
			t.Errorf("%s: gotValue en %v, expectedValue the same length as fr %v", path, englishTree, frenchTree)
			return
		}
		for position, frenchChild := range frenchValue {
			compareJSONTrees(t, fmt.Sprintf("%s[%d]", path, position), frenchChild, englishValue[position])
		}
	default:
		if frenchTree != englishTree {
			t.Errorf("%s: gotValue en %v, expectedValue fr %v (only text fields may differ)", path, englishTree, frenchTree)
		}
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
		{"proposals list", func() *fakeService { return &fakeService{proposals: proposals} }, []string{"proposals", "list"}},
		{"doctor", func() *fakeService { return &fakeService{} }, []string{"doctor"}},
		{"stage", func() *fakeService { return &fakeService{} }, []string{"scan"}},
		{"refused", func() *fakeService { return &fakeService{err: app.ErrConflictNeedsChoose} }, []string{"proposals", "accept", "999"}},
		{"stale", func() *fakeService { return &fakeService{err: fmt.Errorf("%w: generation 3", app.ErrStaleProposal)} }, []string{"proposals", "accept", "999"}},
		{"untyped failure", func() *fakeService { return &fakeService{err: fmt.Errorf("disk on fire")} }, []string{"proposals", "accept", "999"}},
		{"usage", func() *fakeService { return &fakeService{} }, []string{"proposals", "accept", "zero"}},
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

			compareJSONTrees(t, testCase.label, frenchTree, englishTree)
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
	if !strings.Contains(english.stdout, "reason no_exact_size: 1") || !strings.Contains(french.stdout, "motif no_exact_size : 1") {
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
