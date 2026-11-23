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

func TestRun_apiKeyIsStable(t *testing.T) {
	arguments := []string{"api-key", "--config-dir", t.TempDir()}
	var first, second bytes.Buffer

	run(arguments, &first, &bytes.Buffer{})
	run(arguments, &second, &bytes.Buffer{})

	if first.Len() < 64 || first.String() != second.String() {
		t.Fatalf("first %q second %q", first.String(), second.String())
	}
}

func TestRun_apiKeyDoesNotNeedValidConfig(t *testing.T) {
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"listen": "0.0.0.0:1", "radarr": ["http://example.com?apikey=${UNDEFINED_RADARR_KEY_FOR_TEST}"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer

	if exitCode := run([]string{"api-key", "--config-dir", configDir}, &stdout, &stderr); exitCode != 0 || stdout.Len() < 64 {
		t.Fatalf("exit %d, stderr %q", exitCode, stderr.String())
	}
}

func TestRun_apiKeyWithMissingExplicitDirFailsWithoutCreatingIt(t *testing.T) {
	missingDir := filepath.Join(t.TempDir(), "typo")
	var stderr bytes.Buffer

	exitCode := run([]string{"api-key", "--config-dir", missingDir}, &bytes.Buffer{}, &stderr)

	if _, statErr := os.Stat(missingDir); exitCode != 1 || statErr == nil {
		t.Fatalf("exit %d, stat err %v", exitCode, statErr)
	}
}

func TestRun_extraArgumentsAndHelpPrintUsageAndExit2(t *testing.T) {
	for _, arguments := range [][]string{{"config", "check", "extra"}, {"api-key", "--help"}} {
		var stderr bytes.Buffer

		if exitCode := run(arguments, &bytes.Buffer{}, &stderr); exitCode != usageExitCode || !strings.Contains(stderr.String(), "usage") {
			t.Fatalf("%v: exit %d, stderr %q", arguments, exitCode, stderr.String())
		}
	}
}
