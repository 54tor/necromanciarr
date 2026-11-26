package main

import (
	"bytes"
	"encoding/base64"
	"os"
	"strings"
	"testing"

	"necromanciarr/internal/i18n"
)

func TestRun_unknownSubcommandPrintsUsageAndExits2(t *testing.T) {
	var stderr bytes.Buffer

	if gotValue := run(nil, &bytes.Buffer{}, &stderr); gotValue != usageExitCode || !strings.Contains(stderr.String(), "usage") {
		t.Fatalf("gotValue %d, stderr %q", gotValue, stderr.String())
	}
}

func TestRun_configCheckPrintsRedactedConfigAndFailsOnError(t *testing.T) {
	t.Setenv("SECRET_FOR_TEST", "secret-value")
	configDir := writeDoctorConfig(t, `{"radarr": ["http://example.com:7878?apikey=${SECRET_FOR_TEST}"]}`)
	var stdout, stderr bytes.Buffer

	exitCode := run([]string{"config", "check", "--config-dir", configDir}, &stdout, &stderr)

	if exitCode != 0 || strings.Contains(stdout.String(), "secret-value") || !strings.Contains(stdout.String(), "***") {
		t.Fatalf("exit %d, stdout %q, stderr %q", exitCode, stdout.String(), stderr.String())
	}
	configDir = writeDoctorConfig(t, `{"bogus": 1}`)
	if exitCode = run([]string{"config", "check", "--config-dir", configDir}, &stdout, &stderr); exitCode == 0 || !strings.Contains(stderr.String(), "bogus") {
		t.Fatalf("exit %d, stderr %q", exitCode, stderr.String())
	}
}

func TestRun_configCheckWorksOnANonLoopbackListenWithoutToken(t *testing.T) {
	t.Setenv("NECROMANCIARR_LISTEN", "0.0.0.0:8787")
	t.Setenv("NECROMANCIARR_AUTH_TOKEN", "")
	var stdout, stderr bytes.Buffer

	exitCode := run([]string{"config", "check", "--config-dir", t.TempDir()}, &stdout, &stderr)

	if exitCode != successExitCode || !strings.Contains(stdout.String(), "0.0.0.0:8787") {
		t.Fatalf("gotValue exit %d stdout %q stderr %q, expectedValue the config printed without token", exitCode,
			stdout.String(), stderr.String())
	}
}

func TestRun_apiKeyPrintsAFreshTokenAndWritesNothing(t *testing.T) {
	workingDir := t.TempDir()
	t.Chdir(workingDir)
	t.Setenv("CONFIG_DIR", workingDir)
	var first, second, stderr bytes.Buffer

	firstExit := run([]string{"api-key"}, &first, &stderr)
	secondExit := run([]string{"api-key"}, &second, &bytes.Buffer{})

	firstToken := strings.TrimSpace(first.String())
	if firstExit != 0 || secondExit != 0 || len(firstToken) < 43 || firstToken == strings.TrimSpace(second.String()) {
		t.Fatalf("gotValue exits %d %d, tokens %q %q, expectedValue two distinct tokens", firstExit, secondExit, first.String(), second.String())
	}
	if strings.TrimSpace(stderr.String()) != i18n.New(i18n.French, nil).Text(i18n.KeyCLIAPIKeyHint, nil) || strings.Contains(stderr.String(), firstToken) {
		t.Fatalf("gotValue stderr %q, expectedValue only the hint", stderr.String())
	}
	if entries, err := os.ReadDir(workingDir); err != nil || len(entries) != 0 {
		t.Fatalf("gotValue %v %v, expectedValue nothing written", entries, err)
	}
}

func TestRun_extraArgumentsAndHelpPrintUsageAndExit2(t *testing.T) {
	for _, arguments := range [][]string{{"config", "check", "extra"}, {"api-key", "--help"}, {"api-key", "--config-dir", "x"}} {
		var stderr bytes.Buffer

		if exitCode := run(arguments, &bytes.Buffer{}, &stderr); exitCode != usageExitCode || !strings.Contains(stderr.String(), "usage") {
			t.Fatalf("%v: exit %d, stderr %q", arguments, exitCode, stderr.String())
		}
	}
}

func TestRun_configCheckWithAnUnknownFlagNamesItBeforeTheUsage(t *testing.T) {
	var stderr bytes.Buffer

	exitCode := run([]string{"config", "check", "--bogus", "--language", "en"}, &bytes.Buffer{}, &stderr)

	reasonAt, usageAt := strings.Index(stderr.String(), "-bogus"), strings.Index(stderr.String(), "usage: necromanciarr")
	if exitCode != usageExitCode || reasonAt < 0 || usageAt < 0 || reasonAt > usageAt {
		t.Fatalf("gotValue exit %d stderr %q, expectedValue the reason first, then the usage", exitCode, stderr.String())
	}
}

func TestRun_configCheckPrintsTheEffectiveLanguageAndItsSource(t *testing.T) {
	var stdout, stderr bytes.Buffer

	exitCode := run([]string{"config", "check", "--config-dir", t.TempDir(), "--language", "en"}, &stdout, &stderr)

	expectedValue := "Language: en (source: flag)"
	if exitCode != successExitCode || !strings.Contains(stderr.String(), expectedValue) ||
		!strings.Contains(stdout.String(), `"language": "en"`) {
		t.Fatalf("gotValue exit %d stdout %q stderr %q, expectedValue %q", exitCode, stdout.String(), stderr.String(), expectedValue)
	}
}

func TestAPIKey_secretGeneratesDecodable32Bytes(t *testing.T) {
	workingDir := t.TempDir()
	t.Chdir(workingDir)
	var first, second, stderr bytes.Buffer

	firstExit := run([]string{"api-key", "--secret"}, &first, &stderr)
	secondExit := run([]string{"api-key", "--secret"}, &second, &bytes.Buffer{})

	firstKey, secondKey := strings.TrimSuffix(first.String(), "\n"), strings.TrimSuffix(second.String(), "\n")
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(firstKey)
	if firstExit != 0 || secondExit != 0 || err != nil || len(decoded) != 32 || firstKey == secondKey {
		t.Fatalf("gotValue exits %d %d, keys %q %q (%v), expectedValue two distinct 32-byte base64url keys", firstExit, secondExit, firstKey, secondKey, err)
	}
	if strings.TrimSpace(stderr.String()) != i18n.New(i18n.French, nil).Text(i18n.KeyCLIAPIKeySecretHint, nil) || strings.Contains(stderr.String(), firstKey) {
		t.Fatalf("gotValue stderr %q, expectedValue only the hint", stderr.String())
	}
	if entries, err := os.ReadDir(workingDir); err != nil || len(entries) != 0 {
		t.Fatalf("gotValue %v %v, expectedValue nothing written", entries, err)
	}
	t.Setenv("NECROMANCIARR_SECRET_KEY", firstKey)
	configDir := writeDoctorConfig(t, `{"torznab": ["https://prowlarr.example.com/12/api?apikey=secret-indexer-a"]}`)
	if result := runCli(t, configDir, "config", "check"); result.exitCode != 0 {
		t.Fatalf("gotValue exit %d stderr %q, expectedValue the generated key accepted", result.exitCode, result.stderr)
	}
}

func TestRun_secretKeyRefusalsAreTranslated(t *testing.T) {
	configDir := writeDoctorConfig(t, `{"torznab": ["https://prowlarr.example.com/12/api?apikey=secret-indexer-a"]}`)
	scenarios := []struct {
		language, value, expected string
	}{
		{"fr", "", "torznab exige NECROMANCIARR_SECRET_KEY"},
		{"en", "", "torznab requires NECROMANCIARR_SECRET_KEY"},
		{"fr", "short-fake-value", "NECROMANCIARR_SECRET_KEY doit faire 32 octets"},
		{"en", "short-fake-value", "NECROMANCIARR_SECRET_KEY must be 32 bytes"},
	}
	for _, scenario := range scenarios {
		t.Setenv("NECROMANCIARR_SECRET_KEY", scenario.value)

		result := runCli(t, configDir, "config", "check", "--language", scenario.language)

		if result.exitCode == 0 || !strings.Contains(result.stderr, scenario.expected) || strings.Contains(result.stderr+result.stdout, "short-fake") {
			t.Fatalf("%s %q: gotValue exit %d stderr %q, expectedValue %q", scenario.language, scenario.value, result.exitCode, result.stderr, scenario.expected)
		}
	}
}
