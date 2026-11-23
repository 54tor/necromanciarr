package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
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
	cases := map[string]string{
		"torrent client type": `{"torrentClients": ["transmission:http://alice:secret-pass@example.com"]}`,
		"torrent client url":  `{"torrentClients": ["qbittorrent:ftp://alice:secret-pass@example.com"]}`,
		"radarr scheme":       `{"radarr": ["ftp://example.com?apikey=secret-key"]}`,
		"radarr extra param":  `{"radarr": ["http://example.com?apikey=secret-key&token=secret-other"]}`,
		"library api":         `{"libraryApi": "ftp://alice:secret-pass@example.com"}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			configDir := t.TempDir()
			writeFile(t, configDir, configFileName, content)

			_, err := Load(nil, lookupFrom(nil), configDir)

			if err == nil || strings.Contains(err.Error(), "secret-") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestLoad_rejectsTrailingDataAfterJsonObject(t *testing.T) {
	for _, content := range []string{`{} garbage`, `{} {}`} {
		configDir := t.TempDir()
		writeFile(t, configDir, configFileName, content)

		if _, err := Load(nil, lookupFrom(nil), configDir); err == nil {
			t.Fatalf("expected an error for %q", content)
		}
	}
}

func TestLoad_rejectsMissingExplicitConfigDirWithoutCreatingIt(t *testing.T) {
	missingDir := filepath.Join(t.TempDir(), "typo")

	_, flagErr := Load([]string{"--config-dir", missingDir}, lookupFrom(nil), t.TempDir())
	_, envErr := Load(nil, lookupFrom(map[string]string{envConfigDir: missingDir}), t.TempDir())
	_, keyErr := ResolveConfigDir([]string{"--config-dir", missingDir}, lookupFrom(nil), t.TempDir())

	for _, err := range []error{flagErr, envErr, keyErr} {
		if err == nil || !strings.Contains(err.Error(), missingDir) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if _, err := os.Stat(missingDir); err == nil {
		t.Fatal("explicit dir must not be created")
	}
}

func TestLoad_missingConfigFileInExistingDirIsEmptyConfig(t *testing.T) {
	mustLoad(t, []string{"--config-dir", t.TempDir()}, nil, t.TempDir())
}

func TestLoad_rejectsZeroReadRateFlag(t *testing.T) {
	_, err := Load([]string{"--read-bytes-per-second", "0"}, lookupFrom(nil), t.TempDir())

	if err == nil || !strings.Contains(err.Error(), "must be positive") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoad_extraPositionalArgumentIsUsageError(t *testing.T) {
	_, err := Load([]string{"extra"}, lookupFrom(nil), t.TempDir())

	if !errors.Is(err, ErrUsage) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConfig_redactsUserinfoInRadarrAndLibraryApi(t *testing.T) {
	configDir := t.TempDir()
	writeFile(t, configDir, configFileName, `{
  "libraryApi": "https://bob:secret-library@example.com/api?token=secret-token&page=2",
  "radarr": ["http://alice:secret-proxy@example.com:7878/radarr?apikey=secret-radarr"]
}`)
	loadedConfig := mustLoad(t, nil, nil, configDir)

	if loadedConfig.Radarr[0].BaseURL != "http://example.com:7878/radarr" || loadedConfig.Radarr[0].User != "alice" {
		t.Fatalf("unexpected service %+v", loadedConfig.Radarr[0])
	}
	redactedJSON, err := loadedConfig.RedactedJSON()
	if err != nil {
		t.Fatal(err)
	}
	directJSON, err := json.Marshal(loadedConfig)
	if err != nil {
		t.Fatal(err)
	}
	var logBuffer bytes.Buffer
	slog.New(slog.NewJSONHandler(&logBuffer, nil)).Info("config", "config", loadedConfig, "service", loadedConfig.Radarr[0])
	renderings := []string{string(redactedJSON), string(directJSON), logBuffer.String(), loadedConfig.String(), loadedConfig.Radarr[0].String()}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		renderings = append(renderings, fmt.Sprintf(verb, loadedConfig), fmt.Sprintf(verb, loadedConfig.Radarr[0]))
	}
	for _, rendering := range renderings {
		assertNoSecret(t, rendering)
	}
}

func TestEnsureAPIKey_rejectsEmptyKeyFile(t *testing.T) {
	configDir := t.TempDir()
	writeFile(t, configDir, apiKeyFileName, " \n\t\n")

	if _, err := EnsureAPIKey(configDir); err == nil {
		t.Fatal("expected an error for an empty key file")
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
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode gotValue %v, expectedValue 0600", info.Mode().Perm())
	}
	entries, _ := os.ReadDir(configDir)
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}

func TestLoad_dedupeDefaultsWhenSectionAbsent(t *testing.T) {
	loadedConfig := mustLoad(t, nil, nil, t.TempDir())

	gotValue := loadedConfig.Dedupe
	if len(gotValue.TorrentRoots) != 0 || !slices.Equal(gotValue.Kinds, []string{"symlink"}) ||
		!gotValue.RelativeSymlinks || gotValue.MinFileAge != 10*time.Minute || !gotValue.RecheckAfter {
		t.Fatalf("unexpected dedupe defaults %+v", gotValue)
	}
}

func TestLoad_dedupeReadsEveryKey(t *testing.T) {
	configDir := t.TempDir()
	writeFile(t, configDir, configFileName, `{"dedupe": {"torrentRoots": ["${TORRENTS}"], "kinds": ["hardlink", "symlink"],
		"relativeSymlinks": false, "minFileAge": "1h30m", "recheckAfter": false}}`)
	environment := map[string]string{"TORRENTS": "/home/u/torrents"}

	gotValue := mustLoad(t, nil, environment, configDir).Dedupe

	expectedValue := Dedupe{
		TorrentRoots: []string{"/home/u/torrents"}, Kinds: []string{"hardlink", "symlink"},
		RelativeSymlinks: false, MinFileAge: 90 * time.Minute, RecheckAfter: false,
	}
	if !reflect.DeepEqual(gotValue, expectedValue) {
		t.Fatalf("gotValue %+v, expectedValue %+v", gotValue, expectedValue)
	}
}

func TestLoad_dedupeRejectsUnknownKindNamingTheKey(t *testing.T) {
	configDir := t.TempDir()
	writeFile(t, configDir, configFileName, `{"dedupe": {"kinds": ["symlink", "junction"]}}`)

	_, err := Load(nil, lookupFrom(nil), configDir)

	if err == nil || !strings.Contains(err.Error(), "dedupe.kinds[1]") {
		t.Fatalf("expected an error naming dedupe.kinds[1], got %v", err)
	}
}

func TestLoad_dedupeRejectsInvalidMinFileAge(t *testing.T) {
	for _, rawAge := range []string{"soon", "-1m"} {
		configDir := t.TempDir()
		writeFile(t, configDir, configFileName, `{"dedupe": {"minFileAge": "`+rawAge+`"}}`)

		_, err := Load(nil, lookupFrom(nil), configDir)

		if err == nil || !strings.Contains(err.Error(), "dedupe.minFileAge") {
			t.Fatalf("expected an error naming dedupe.minFileAge for %q, got %v", rawAge, err)
		}
	}
}

func TestExampleConfig_declaresDedupeSection(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "config.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rawFile fileConfig
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&rawFile); err != nil {
		t.Fatal(err)
	}
	if len(rawFile.Dedupe.Kinds) == 0 || rawFile.Dedupe.MinFileAge == "" {
		t.Fatalf("example dedupe section incomplete: %+v", rawFile.Dedupe)
	}
}

func TestConfig_rejectsDedupeRootsOverlappingLibraryRoots(t *testing.T) {
	cases := []struct {
		name            string
		libraryRoot     string
		torrentRoot     string
		expectedOverlap bool
	}{
		{"equal", "/volumes/mega/1080p", "/volumes/mega/1080p/", true},
		{"torrent root is an ancestor", "/volumes/mega/1080p", "/volumes/mega", true},
		{"torrent root is a descendant", "/volumes/mega", "/volumes/mega/qBittorrent", true},
		{"siblings", "/volumes/mega/1080p", "/volumes/mega/qBittorrent", false},
		{"shared prefix is not a parent", "/data/tv", "/data/tvshows", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			configDir := t.TempDir()
			writeFile(t, configDir, configFileName, fmt.Sprintf(`{"libraryRoots": [%q], "dedupe": {"torrentRoots": [%q]}}`,
				testCase.libraryRoot, testCase.torrentRoot))

			_, err := Load(nil, lookupFrom(nil), configDir)

			gotOverlap := err != nil
			if gotOverlap != testCase.expectedOverlap {
				t.Fatalf("overlap error %v, expected overlap %v", err, testCase.expectedOverlap)
			}
			if gotOverlap && !strings.Contains(err.Error(), "dedupe.torrentRoots[0]") {
				t.Fatalf("error does not name the key: %v", err)
			}
		})
	}
}
