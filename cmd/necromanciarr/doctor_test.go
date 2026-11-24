package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeDoctorConfig(t *testing.T, content string) string {
	t.Helper()
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return configDir
}

func libraryWithMedia(t *testing.T) string {
	t.Helper()
	libraryRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(libraryRoot, "film.mkv"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return libraryRoot
}

func TestCli_doctorExitCodes(t *testing.T) {
	healthyDir := writeDoctorConfig(t, `{"libraryRoots": [`+quote(libraryWithMedia(t))+`]}`)
	failingDir := writeDoctorConfig(t, `{"libraryRoots": ["/home/u/absent-library"]}`)
	testCases := []struct {
		name             string
		configDir        string
		expectedExitCode int
		expectedMark     string
	}{
		{"healthy", healthyDir, 0, "✓ Library roots"},
		{"failing", failingDir, 1, "✗ Library roots"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			gotValue := run([]string{"doctor", "--config-dir", testCase.configDir, "--check", "library-roots"}, &stdout, &stderr)

			if gotValue != testCase.expectedExitCode || !strings.Contains(stdout.String(), testCase.expectedMark) {
				t.Fatalf("exit %d, stdout %q, stderr %q", gotValue, stdout.String(), stderr.String())
			}
		})
	}
}

func quote(text string) string {
	encoded, _ := json.Marshal(text)
	return string(encoded)
}

func TestCli_unknownCheckIsUsageError(t *testing.T) {
	configDir := writeDoctorConfig(t, `{}`)
	var stdout, stderr bytes.Buffer

	gotValue := run([]string{"doctor", "--config-dir", configDir, "--check", "nope"}, &stdout, &stderr)

	if gotValue != usageExitCode || !strings.Contains(stderr.String(), "library-roots") || !strings.Contains(stderr.String(), `"nope"`) {
		t.Fatalf("exit %d, stderr %q", gotValue, stderr.String())
	}
}

func TestCli_jsonOutputIsStableAndRedacted(t *testing.T) {
	configDir := writeDoctorConfig(t, `{"radarr": ["http://127.0.0.1:1?apikey=ARRSECRETKEY"]}`)
	var stdout, stderr bytes.Buffer

	exitCode := run([]string{"doctor", "--json", "--config-dir", configDir, "--check", "arr:radarr:0", "--check=config"}, &stdout, &stderr)

	if exitCode != 1 || strings.Contains(stdout.String(), "ARRSECRETKEY") {
		t.Fatalf("exit %d, stdout %q", exitCode, stdout.String())
	}
	var decoded struct {
		Worst  string `json:"worst"`
		Checks []map[string]any
	}
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	expectedKeys := []string{"detail", "durationMs", "id", "status", "title"}
	if decoded.Worst != "fail" || len(decoded.Checks) != 2 {
		t.Fatalf("gotValue %+v", decoded)
	}
	for _, check := range decoded.Checks {
		for _, expectedKey := range expectedKeys {
			if _, present := check[expectedKey]; !present {
				t.Fatalf("key %q missing in %v", expectedKey, check)
			}
		}
	}
}

func TestCli_invalidConfigReportsFailedConfigCheck(t *testing.T) {
	configDir := writeDoctorConfig(t, `{"bogus": 1}`)
	var stdout, stderr bytes.Buffer

	exitCode := run([]string{"doctor", "--config-dir", configDir}, &stdout, &stderr)

	if exitCode != 1 || !strings.Contains(stdout.String(), "✗ Configuration") {
		t.Fatalf("exit %d, stdout %q", exitCode, stdout.String())
	}
}
