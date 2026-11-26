package main

import (
	"encoding/json"
	"strings"
	"testing"

	"necromanciarr/internal/app"
)

const (
	testRadarrURL    = "http://example.com:7878"
	testRadarrAPIKey = "dummy-radarr-key-0000"
	sealedMark       = "sealed:v1:"
)

// useEnvironment remplace l'environnement lu par loadConfig : celui de la machine ne doit pas changer le test.
func useEnvironment(t *testing.T, variables map[string]string) {
	t.Helper()
	previousLookup := lookupEnv
	lookupEnv = func(name string) (string, bool) {
		value, found := variables[name]
		return value, found
	}
	t.Cleanup(func() { lookupEnv = previousLookup })
}

// useStdin fait lire content aux options --value-stdin et --element-stdin, le temps du test.
func useStdin(t *testing.T, content string) {
	t.Helper()
	previousStdin := stdin
	stdin = strings.NewReader(content)
	t.Cleanup(func() { stdin = previousStdin })
}

func radarrElement() string {
	return `{"url": "` + testRadarrURL + `", "apiKey": "` + testRadarrAPIKey + `"}`
}

func TestConfigCommand_setThenGetRoundTrips(t *testing.T) {
	useEnvironment(t, nil)
	configDir := writeDoctorConfig(t, `{}`)
	valeurAttendue := "category-a"

	saved := runCli(t, configDir, "config", "set", "linkCategory", `"`+valeurAttendue+`"`)
	read := runCli(t, configDir, "config", "get", "linkCategory", "--json")

	var setting app.ConfigValue
	if err := json.Unmarshal([]byte(read.stdout), &setting); err != nil || saved.exitCode != successExitCode {
		t.Fatalf("set exit %d (%s), get %q (%v)", saved.exitCode, saved.stderr, read.stdout, err)
	}
	if valeurObtenue := setting.Value; valeurObtenue != valeurAttendue {
		t.Fatalf("valeurObtenue %v, valeurAttendue %q", valeurObtenue, valeurAttendue)
	}
}

func TestConfigCommand_neverPrintsASealedSecret(t *testing.T) {
	useEnvironment(t, map[string]string{"NECROMANCIARR_SECRET_KEY": fakeSecretKey})
	configDir := writeDoctorConfig(t, `{}`)
	useStdin(t, radarrElement())

	saved := runCli(t, configDir, "config", "item", "set", "radarr", testRadarrURL, "--element-stdin")
	whole := runCli(t, configDir, "config", "get", "--json")
	list := runCli(t, configDir, "config", "get", "radarr")

	if saved.exitCode != successExitCode || whole.exitCode != successExitCode || list.exitCode != successExitCode {
		t.Fatalf("exit codes %d %d %d, stderr %q %q", saved.exitCode, whole.exitCode, list.exitCode, saved.stderr, whole.stderr)
	}
	for _, printed := range []string{saved.stdout, saved.stderr, whole.stdout, whole.stderr, list.stdout, list.stderr} {
		if strings.Contains(printed, testRadarrAPIKey) || strings.Contains(printed, sealedMark) {
			t.Fatalf("valeurObtenue %q, valeurAttendue ni le secret ni sa forme scellée", printed)
		}
	}
	var view app.ConfigView
	if err := json.Unmarshal([]byte(whole.stdout), &view); err != nil || len(view.Lists["radarr"]) != 1 {
		t.Fatalf("valeurObtenue %q (%v), valeurAttendue un élément radarr", whole.stdout, err)
	}
	apiKey, isPresent := view.Lists["radarr"][0].Element["apiKey"]
	if !isPresent || apiKey != nil {
		t.Fatalf("valeurObtenue apiKey %v (présent %v), valeurAttendue un champ présent et null", apiKey, isPresent)
	}
}

func TestConfigCommand_secretWithoutKeyIsRefusedInTheLanguageWithoutTheSecret(t *testing.T) {
	useEnvironment(t, nil)
	configDir := writeDoctorConfig(t, `{}`)
	valeurAttendue := "NECROMANCIARR_SECRET_KEY is required to store a secret: nothing was written"

	refused := runCli(t, configDir, "config", "item", "set", "radarr", testRadarrURL, radarrElement())

	if refused.exitCode != failureExitCode || !strings.Contains(refused.stderr, valeurAttendue) {
		t.Fatalf("valeurObtenue exit %d stderr %q, valeurAttendue exit 1 et %q", refused.exitCode, refused.stderr, valeurAttendue)
	}
	if strings.Contains(refused.stderr+refused.stdout, testRadarrAPIKey) {
		t.Fatalf("valeurObtenue %q, valeurAttendue aucun secret", refused.stderr)
	}
}

func TestConfigCommand_valueStdinReachesTheService(t *testing.T) {
	fake := &fakeService{}
	useFakeService(t, fake)
	useStdin(t, `{"url": "http://example.com:9696?apikey=dummy"}`+"\n")
	valeurAttendue := `{"url": "http://example.com:9696?apikey=dummy"}`

	result := runCli(t, writeDoctorConfig(t, `{}`), "config", "set", "prowlarr", "--value-stdin")

	if valeurObtenue := strings.TrimSpace(string(fake.lastSettingValue)); result.exitCode != successExitCode || valeurObtenue != valeurAttendue {
		t.Fatalf("valeurObtenue %q (exit %d, %s), valeurAttendue %q", valeurObtenue, result.exitCode, result.stderr, valeurAttendue)
	}
}

func TestConfigCommand_elementStdinReachesTheService(t *testing.T) {
	fake := &fakeService{}
	useFakeService(t, fake)
	useStdin(t, radarrElement())

	result := runCli(t, writeDoctorConfig(t, `{}`), "config", "item", "set", "radarr", testRadarrURL, "--element-stdin")

	if valeurObtenue := fake.lastElement["url"]; result.exitCode != successExitCode || valeurObtenue != testRadarrURL {
		t.Fatalf("valeurObtenue %v (exit %d, %s), valeurAttendue %q", valeurObtenue, result.exitCode, result.stderr, testRadarrURL)
	}
}

func TestConfigCommand_usageErrorsExitWithCodeTwoWithoutCallingTheService(t *testing.T) {
	scenarios := []struct {
		name      string
		arguments []string
	}{
		{"value missing", []string{"config", "set", "linkCategory"}},
		{"key missing", []string{"config", "set"}},
		{"value both as argument and stdin", []string{"config", "set", "linkCategory", `"a"`, "--value-stdin"}},
		{"value not JSON", []string{"config", "set", "linkCategory", "not-json"}},
		{"element not an object", []string{"config", "item", "set", "radarr", testRadarrURL, `["a"]`}},
		{"element missing", []string{"config", "item", "set", "radarr", testRadarrURL}},
		{"unknown flag", []string{"config", "unset", "linkCategory", "--bogus"}},
		{"unknown key filter", []string{"config", "get", "notASetting"}},
	}
	for _, scenario := range scenarios {
		fake := &fakeService{}
		useFakeService(t, fake)
		useStdin(t, `"b"`)

		result := runCli(t, writeDoctorConfig(t, `{}`), scenario.arguments...)

		isMutationCalled := fake.called("SetSetting") || fake.called("PutItem") || fake.called("UnsetSetting")
		if result.exitCode != usageExitCode || isMutationCalled {
			t.Errorf("%s: valeurObtenue exit %d, calls %v, stderr %q ; valeurAttendue exit 2 sans écriture", scenario.name, result.exitCode, fake.calls, result.stderr)
		}
	}
}

func TestConfigCommand_helpWarnsAboutSecretsInArguments(t *testing.T) {
	valeurAttendue := "--value-stdin"

	result := runCli(t, t.TempDir(), "config", "nothing-here")

	if !strings.Contains(result.stderr, valeurAttendue) || !strings.Contains(result.stderr, "--element-stdin") {
		t.Fatalf("valeurObtenue %q, valeurAttendue un usage qui cite --value-stdin et --element-stdin", result.stderr)
	}
}
