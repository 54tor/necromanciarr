package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const bootstrapTokenPrefix = "NECROMANCIARR_AUTH_TOKEN="

func runServeOnce(t *testing.T, configDir string) serveOutcome {
	t.Helper()
	harness := useServeHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outcome := startServe(ctx, []string{"--config-dir", configDir, "--language", "en"})
	select {
	case <-harness.addresses:
		cancel()
		return waitOutcome(t, outcome)
	case result := <-outcome:
		return result
	}
}

func tokenFromDotEnv(t *testing.T, configDir string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(configDir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(content), "\n") {
		if token, found := strings.CutPrefix(line, bootstrapTokenPrefix); found {
			return token
		}
	}
	t.Fatal("no auth token in .env")
	return ""
}

func TestServeBootstrap_firstLaunchPrintsTokenOnceOnStderrAndWritesEnv(t *testing.T) {
	configDir := t.TempDir()
	result := runServeOnce(t, configDir)

	storedToken := tokenFromDotEnv(t, configDir)
	valeurObtenue := strings.Count(result.stderr, storedToken)
	if valeurObtenue != 1 || strings.Contains(result.stdout, storedToken) {
		t.Fatalf("valeurObtenue %d on stderr (stdout leak: %v), valeurAttendue 1 and none on stdout\n%s", valeurObtenue, strings.Contains(result.stdout, storedToken), result.stderr)
	}
}

func TestServeBootstrap_secondLaunchPrintsNoToken(t *testing.T) {
	configDir := t.TempDir()
	runServeOnce(t, configDir)
	storedToken := tokenFromDotEnv(t, configDir)

	result := runServeOnce(t, configDir)

	if strings.Contains(result.stderr, storedToken) || strings.Contains(result.stdout, storedToken) {
		t.Fatalf("valeurObtenue a token in the output, valeurAttendue none on a second launch\n%s", result.stderr)
	}
}

func TestServeBootstrap_unwritableConfigDirFailsWithActionableMessage(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	configDir := t.TempDir()
	if err := os.Chmod(configDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(configDir, 0o700) })
	useServeHarness(t)

	var result serveOutcome
	result = <-startServe(context.Background(), []string{"--config-dir", configDir, "--language", "en"})

	if result.exitCode == successExitCode || !strings.Contains(result.stderr, configDir) || !strings.Contains(result.stderr, "NECROMANCIARR_AUTH_TOKEN") {
		t.Fatalf("valeurObtenue %+v, valeurAttendue a failure naming the folder and NECROMANCIARR_AUTH_TOKEN", result)
	}
}

func TestServeBootstrap_databaseWithoutKeyRefusesWithTranslatedMessageAndWritesNoEnv(t *testing.T) {
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "necromanciarr.db"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	useServeHarness(t)

	result := <-startServe(context.Background(), []string{"--config-dir", configDir, "--language", "en"})

	messageAttendu := "a database exists but NECROMANCIARR_SECRET_KEY is missing"
	if result.exitCode == successExitCode || !strings.Contains(result.stderr, messageAttendu) {
		t.Fatalf("valeurObtenue %+v, valeurAttendue a failure containing %q", result, messageAttendu)
	}
	if _, err := os.Stat(filepath.Join(configDir, ".env")); !os.IsNotExist(err) {
		t.Errorf(".env créé malgré le refus : %v", err)
	}
}
