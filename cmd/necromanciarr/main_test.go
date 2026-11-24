package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun_unknownSubcommandPrintsUsageAndExits2(t *testing.T) {
	var stderr bytes.Buffer

	if gotValue := run(nil, &bytes.Buffer{}, &stderr); gotValue != usageExitCode || !strings.Contains(stderr.String(), "usage") {
		t.Fatalf("gotValue %d, stderr %q", gotValue, stderr.String())
	}
}

func TestRun_configCheckPrintsRedactedConfigAndFailsOnError(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("SECRET_FOR_TEST", "secret-value")
	configPath := filepath.Join(configDir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"radarr": ["http://example.com:7878?apikey=${SECRET_FOR_TEST}"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer

	exitCode := run([]string{"config", "check", "--config-dir", configDir}, &stdout, &stderr)

	if exitCode != 0 || strings.Contains(stdout.String(), "secret-value") || !strings.Contains(stdout.String(), "***") {
		t.Fatalf("exit %d, stdout %q, stderr %q", exitCode, stdout.String(), stderr.String())
	}
	if err := os.WriteFile(configPath, []byte(`{"bogus": 1}`), 0o600); err != nil {
		t.Fatal(err)
	}
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
	if strings.TrimSpace(stderr.String()) != apiKeyHint || strings.Contains(stderr.String(), firstToken) {
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

	expectedValue := "Language: en (source: --language)"
	if exitCode != successExitCode || !strings.Contains(stderr.String(), expectedValue) ||
		!strings.Contains(stdout.String(), `"language": "en"`) {
		t.Fatalf("gotValue exit %d stdout %q stderr %q, expectedValue %q", exitCode, stdout.String(), stderr.String(), expectedValue)
	}
}
