package config

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, directory, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustLoad(t *testing.T, args []string, environment map[string]string, configDir string) Config {
	t.Helper()
	loadedConfig, err := Load(args, lookupFrom(environment), configDir)
	if err != nil {
		t.Fatal(err)
	}
	return loadedConfig
}

func TestLoad_defaultsListenToLoopbackWithoutFile(t *testing.T) {
	expectedValue := "127.0.0.1:8787"

	loadedConfig := mustLoad(t, nil, nil, t.TempDir())

	if gotValue := loadedConfig.ListenAddr; gotValue != expectedValue {
		t.Fatalf("gotValue %q, expectedValue %q", gotValue, expectedValue)
	}
}

func TestLoad_rejectsNonLoopbackWithoutToken(t *testing.T) {
	_, err := Load([]string{"--listen", "0.0.0.0:8787"}, lookupFrom(nil), t.TempDir())

	if err == nil {
		t.Fatal("expected an error for a non-loopback address without token")
	}
}

func TestLoad_acceptsNonLoopbackWithTokenFromEnvironment(t *testing.T) {
	environment := map[string]string{envAuthToken: "token-for-test"}

	loadedConfig := mustLoad(t, []string{"--listen", "0.0.0.0:8787"}, environment, t.TempDir())

	if loadedConfig.AuthToken != "token-for-test" {
		t.Fatal("AuthToken not read from environment")
	}
}

func TestLoad_priorityCliOverEnvOverJsonOverDefault(t *testing.T) {
	configDir := t.TempDir()
	writeFile(t, configDir, configFileName, `{"libraryApi": "http://file.example.com", "dbPath": "/home/u/file.db"}`)
	environment := map[string]string{envLibraryAPI: "http://env.example.com"}
	cases := []struct {
		name        string
		args        []string
		environment map[string]string
		expectedAPI string
		expectedDB  string
	}{
		{"file only", nil, nil, "http://file.example.com", "/home/u/file.db"},
		{"env over file", nil, environment, "http://env.example.com", "/home/u/file.db"},
		{"cli over env", []string{"--library-api", "http://cli.example.com", "--db-path", "/home/u/cli.db"}, environment, "http://cli.example.com", "/home/u/cli.db"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			loadedConfig := mustLoad(t, testCase.args, testCase.environment, configDir)
			if loadedConfig.LibraryAPI != testCase.expectedAPI || loadedConfig.DBPath != testCase.expectedDB {
				t.Fatalf("gotValue %q %q, expectedValue %q %q", loadedConfig.LibraryAPI, loadedConfig.DBPath, testCase.expectedAPI, testCase.expectedDB)
			}
		})
	}
	if gotValue := mustLoad(t, nil, nil, t.TempDir()).DBPath; gotValue != defaultDBPath {
		t.Fatalf("default DBPath gotValue %q", gotValue)
	}
}

func TestLoad_configDirFlagAndEnvironmentChooseTheDirectory(t *testing.T) {
	flagDir, environmentDir := t.TempDir(), t.TempDir()
	writeFile(t, flagDir, configFileName, `{"dbPath": "/home/u/flag.db"}`)
	writeFile(t, environmentDir, configFileName, `{"dbPath": "/home/u/env.db"}`)
	environment := map[string]string{envConfigDir: environmentDir}

	if gotValue := mustLoad(t, []string{"--config-dir", flagDir}, environment, t.TempDir()).DBPath; gotValue != "/home/u/flag.db" {
		t.Fatalf("flag: gotValue %q", gotValue)
	}
	if gotValue := mustLoad(t, nil, environment, t.TempDir()).DBPath; gotValue != "/home/u/env.db" {
		t.Fatalf("env: gotValue %q", gotValue)
	}
}

func TestLoad_readsDotEnvFromConfigDirWithoutOverridingEnvironment(t *testing.T) {
	configDir := t.TempDir()
	writeFile(t, configDir, ".env", "# commentaire\nRADARR_API_KEY=from-dotenv\nexport QBIT_PASSWORD=\"dot-env-pass\"\nSONARR_API_KEY=dotenv-sonarr\n")
	writeFile(t, configDir, configFileName, `{
  "radarr": ["http://example.com:7878?apikey=${RADARR_API_KEY}"],
  "sonarr": ["http://example.com:8989?apikey=${SONARR_API_KEY}"],
  "torrentClients": ["qbittorrent:http://alice:${QBIT_PASSWORD}@example.com:8080"]
}`)
	environment := map[string]string{"SONARR_API_KEY": "from-real-env"}

	loadedConfig := mustLoad(t, nil, environment, configDir)

	if loadedConfig.Radarr[0].APIKey != "from-dotenv" {
		t.Errorf("radarr key gotValue %q", loadedConfig.Radarr[0].APIKey)
	}
	if loadedConfig.Sonarr[0].APIKey != "from-real-env" {
		t.Errorf("sonarr key gotValue %q: .env must not override the environment", loadedConfig.Sonarr[0].APIKey)
	}
	if loadedConfig.TorrentClients[0].Password != "dot-env-pass" {
		t.Errorf("qbit password gotValue %q", loadedConfig.TorrentClients[0].Password)
	}
}

func TestLoad_readsDotEnvFromWorkingDirectoryAfterConfigDir(t *testing.T) {
	configDir, workingDir := t.TempDir(), t.TempDir()
	writeFile(t, workingDir, ".env", "ONLY_CWD=cwd-value\nBOTH=from-cwd\n")
	writeFile(t, configDir, ".env", "BOTH=from-config-dir\n")
	writeFile(t, configDir, configFileName, `{"libraryRoots": ["${ONLY_CWD}", "${BOTH}"]}`)
	t.Chdir(workingDir)

	loadedConfig := mustLoad(t, nil, nil, configDir)

	if expectedValue := "cwd-value,from-config-dir"; strings.Join(loadedConfig.LibraryRoots, ",") != expectedValue {
		t.Fatalf("gotValue %v, expectedValue %q", loadedConfig.LibraryRoots, expectedValue)
	}
}

func TestLoad_undefinedVariableNamesItInTheError(t *testing.T) {
	configDir := t.TempDir()
	writeFile(t, configDir, configFileName, `{"radarr": ["http://example.com:7878?apikey=${NOPE_KEY}"]}`)

	_, err := Load(nil, lookupFrom(nil), configDir)

	if err == nil || !strings.Contains(err.Error(), "NOPE_KEY") || !strings.Contains(err.Error(), "radarr[0]") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoad_rejectsUnknownJsonKeyNamingIt(t *testing.T) {
	configDir := t.TempDir()
	writeFile(t, configDir, configFileName, `{"listn": "127.0.0.1:1"}`)

	_, err := Load(nil, lookupFrom(nil), configDir)

	if err == nil || !strings.Contains(err.Error(), "listn") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoad_errorsNeverContainServiceSecrets(t *testing.T) {
	configDir := t.TempDir()
	writeFile(t, configDir, configFileName, `{"torrentClients": ["transmission:http://alice:secret-pass@example.com"],
  "radarr": ["ftp://example.com?apikey=secret-key"]}`)

	_, err := Load(nil, lookupFrom(nil), configDir)

	if err == nil || strings.Contains(err.Error(), "secret-") {
		t.Fatalf("unexpected error: %v", err)
	}
	writeFile(t, configDir, configFileName, `{"radarr": ["ftp://example.com?apikey=secret-key"]}`)
	if _, err = Load(nil, lookupFrom(nil), configDir); err == nil || strings.Contains(err.Error(), "secret-") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoad_acceptsConfigExampleJson(t *testing.T) {
	configDir := t.TempDir()
	exampleContent, err := os.ReadFile(filepath.Join("..", "..", "config.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, configDir, configFileName, string(exampleContent))
	environment := map[string]string{"RADARR_API_KEY": "r", "SONARR_API_KEY": "s", "QBIT_PASSWORD": "q"}

	loadedConfig := mustLoad(t, nil, environment, configDir)

	if len(loadedConfig.Radarr) == 0 || len(loadedConfig.Sonarr) == 0 || len(loadedConfig.TorrentClients) == 0 {
		t.Fatalf("example config incomplete: %v", loadedConfig)
	}
}

func TestConfig_redactsUrlCredentials(t *testing.T) {
	configDir := t.TempDir()
	writeFile(t, configDir, configFileName, `{
  "radarr": ["http://example.com:7878?apikey=secret-radarr"],
  "torrentClients": ["qbittorrent:http://alice:secret-qbit@example.com:8080"]
}`)
	environment := map[string]string{envAuthToken: "secret-auth", envLibraryProviderToken: "secret-provider"}
	loadedConfig := mustLoad(t, nil, environment, configDir)

	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		assertNoSecret(t, fmt.Sprintf(verb, loadedConfig))
	}
	var logBuffer bytes.Buffer
	slog.New(slog.NewTextHandler(&logBuffer, nil)).Info("config", "config", loadedConfig)
	assertNoSecret(t, logBuffer.String())
	redactedJSON, err := loadedConfig.RedactedJSON()
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecret(t, string(redactedJSON))
	if !strings.Contains(string(redactedJSON), "***") {
		t.Fatalf("expected redaction mark in %s", redactedJSON)
	}
}

func TestEnsureAPIKey_isStableAcrossRunsAndPrivate(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "nested")

	firstKey, err := EnsureAPIKey(configDir)
	if err != nil {
		t.Fatal(err)
	}
	secondKey, err := EnsureAPIKey(configDir)
	if err != nil {
		t.Fatal(err)
	}

	if firstKey != secondKey || len(firstKey) != 64 {
		t.Fatalf("keys differ or wrong length: %q %q", firstKey, secondKey)
	}
	info, err := os.Stat(filepath.Join(configDir, apiKeyFileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode gotValue %v, expectedValue 0600", info.Mode().Perm())
	}
	entries, _ := os.ReadDir(configDir)
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}
