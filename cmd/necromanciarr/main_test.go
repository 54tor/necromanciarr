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
