package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeCrossSeedJS(t *testing.T, content string) string {
	t.Helper()
	directory := t.TempDir()
	writeFile(t, directory, "config.js", content)
	return filepath.Join(directory, "config.js")
}

func configWithCrossSeed(t *testing.T, crossSeedPath, extraJSON string) string {
	t.Helper()
	configDir := t.TempDir()
	encodedPath, err := json.Marshal(crossSeedPath)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, configDir, configFileName, `{"crossSeedConfig": `+string(encodedPath)+extraJSON+`}`)
	return configDir
}

func TestConfig_priorityCliEnvJsonImportedDefaults(t *testing.T) {
	jsonFile := writeCrossSeedJS(t, `module.exports = { linkCategory: "from-json-path", linkType: "symlink" };`)
	envFile := writeCrossSeedJS(t, `module.exports = { linkCategory: "from-env-path" };`)
	cliFile := writeCrossSeedJS(t, `module.exports = { linkCategory: "from-cli-path" };`)
	configDir := configWithCrossSeed(t, jsonFile, "")
	environment := map[string]string{envCrossSeedConfig: envFile}
	pathCases := []struct {
		name             string
		args             []string
		environment      map[string]string
		expectedCategory string
	}{
		{"config.json path", nil, nil, "from-json-path"},
		{"env over config.json", nil, environment, "from-env-path"},
		{"cli over env", []string{"--cross-seed-config", cliFile}, environment, "from-cli-path"},
	}
	for _, testCase := range pathCases {
		t.Run(testCase.name, func(t *testing.T) {
			gotValue := mustLoad(t, testCase.args, testCase.environment, configDir).LinkCategory
			if gotValue != testCase.expectedCategory {
				t.Fatalf("gotValue %q, expectedValue %q", gotValue, testCase.expectedCategory)
			}
		})
	}
	linkTypeCases := []struct {
		name          string
		configDir     string
		expectedValue string
	}{
		{"default", t.TempDir(), defaultLinkType},
		{"imported over default", configDir, "symlink"},
		{"config.json over imported", configWithCrossSeed(t, jsonFile, `, "linkType": "reflink"`), "reflink"},
	}
	for _, testCase := range linkTypeCases {
		t.Run(testCase.name, func(t *testing.T) {
			if gotValue := mustLoad(t, nil, nil, testCase.configDir).LinkType; gotValue != testCase.expectedValue {
				t.Fatalf("gotValue %q, expectedValue %q", gotValue, testCase.expectedValue)
			}
		})
	}
}

func TestConfig_explicitJsonOverridesCrossSeedValues(t *testing.T) {
	crossSeedPath := writeCrossSeedJS(t, `module.exports = {
		radarr: ["http://imported.example.com:7878?apikey=imported"],
		linkCategory: "imported-category",
		skipRecheck: true,
	};`)
	configDir := configWithCrossSeed(t, crossSeedPath, `,
		"radarr": ["http://json.example.com:7878?apikey=json"],
		"linkCategory": "json-category",
		"skipRecheck": false`)

	loadedConfig := mustLoad(t, nil, nil, configDir)

	if len(loadedConfig.Radarr) != 1 || loadedConfig.Radarr[0].BaseURL != "http://json.example.com:7878" {
		t.Fatalf("gotValue %v, expectedValue the config.json radarr", loadedConfig.Radarr)
	}
	if loadedConfig.LinkCategory != "json-category" || loadedConfig.SkipRecheck {
		t.Fatalf("gotValue %q %v, expectedValue json-category false", loadedConfig.LinkCategory, loadedConfig.SkipRecheck)
	}
	if loadedConfig.Sources.Radarr != sourceConfigFile {
		t.Fatalf("gotValue %q, expectedValue %q", loadedConfig.Sources.Radarr, sourceConfigFile)
	}
}

func TestConfig_listsAreReplacedNotMerged(t *testing.T) {
	crossSeedPath := writeCrossSeedJS(t, `module.exports = {
		sonarr: ["http://first.example.com:8989?apikey=a", "http://second.example.com:8989?apikey=b"],
		linkDirs: ["/home/u/imported-links"],
	};`)
	configDir := configWithCrossSeed(t, crossSeedPath, `,
		"sonarr": ["http://json.example.com:8989?apikey=c"],
		"linkDirs": []`)

	loadedConfig := mustLoad(t, nil, nil, configDir)

	if len(loadedConfig.Sonarr) != 1 || loadedConfig.Sonarr[0].BaseURL != "http://json.example.com:8989" {
		t.Fatalf("gotValue %v, expectedValue only the config.json sonarr", loadedConfig.Sonarr)
	}
	if !slices.Equal(loadedConfig.LinkDirs, []string{"/home/u/imported-links"}) {
		t.Fatalf("an empty list must not hide the imported one: gotValue %v", loadedConfig.LinkDirs)
	}
	expectedSources := Sources{Radarr: sourceNone, Sonarr: sourceConfigFile, TorrentClients: sourceNone, LinkDirs: sourceCrossSeed}
	if loadedConfig.Sources != expectedSources {
		t.Fatalf("gotValue %+v, expectedValue %+v", loadedConfig.Sources, expectedSources)
	}
}

func TestConfig_worksWithoutCrossSeedConfig(t *testing.T) {
	loadedConfig := mustLoad(t, nil, nil, t.TempDir())

	if loadedConfig.CrossSeedImport.Configured || loadedConfig.CrossSeed != nil || len(loadedConfig.CrossSeedImport.ImportedKeys) != 0 {
		t.Fatalf("gotValue %+v, expectedValue nothing imported", loadedConfig.CrossSeedImport)
	}
	redactedJSON, err := loadedConfig.RedactedJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(redactedJSON), `"configured": false`) {
		t.Fatalf("config check must say cross-seed is not configured: %s", redactedJSON)
	}
}

func TestConfig_explicitMissingCrossSeedFileIsAnError(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "config.js")
	configDir := configWithCrossSeed(t, missingPath, "")

	_, fileErr := Load(nil, lookupFrom(nil), configDir)
	_, envErr := Load(nil, lookupFrom(map[string]string{envCrossSeedConfig: missingPath}), t.TempDir())

	for _, err := range []error{fileErr, envErr} {
		if err == nil || !strings.Contains(err.Error(), "cross-seed") {
			t.Fatalf("gotValue %v, expectedValue a cross-seed error", err)
		}
	}
}

func TestConfig_crossSeedDaemonFromExplicitUrlBeatsImportedHostPort(t *testing.T) {
	crossSeedPath := writeCrossSeedJS(t, `module.exports = { host: "localhost", port: 2468, apiKey: "imported-key" };`)
	daemonCases := []struct {
		name            string
		extraJSON       string
		expectedBaseURL string
		expectedAPIKey  string
	}{
		{"imported host and port", "", "http://localhost:2468", "imported-key"},
		{
			"explicit url", `, "crossSeed": "http://cross-seed.example.com:2468?apikey=${CROSS_SEED_API_KEY}"`,
			"http://cross-seed.example.com:2468", "explicit-key",
		},
	}
	for _, testCase := range daemonCases {
		t.Run(testCase.name, func(t *testing.T) {
			configDir := configWithCrossSeed(t, crossSeedPath, testCase.extraJSON)

			daemon := mustLoad(t, nil, map[string]string{"CROSS_SEED_API_KEY": "explicit-key"}, configDir).CrossSeed

			if daemon == nil || daemon.BaseURL != testCase.expectedBaseURL || daemon.APIKey != testCase.expectedAPIKey {
				t.Fatalf("gotValue %+v, expectedValue %s", daemon, testCase.expectedBaseURL)
			}
		})
	}
}

func TestConfig_crossSeedDaemonDefaultsToLocalhostWhenHostUnset(t *testing.T) {
	crossSeedPath := writeCrossSeedJS(t, `module.exports = { host: "0.0.0.0", port: 2470 };`)

	daemon := mustLoad(t, nil, nil, configWithCrossSeed(t, crossSeedPath, "")).CrossSeed

	if daemon == nil || daemon.BaseURL != "http://localhost:2470" {
		t.Fatalf("gotValue %+v, expectedValue http://localhost:2470", daemon)
	}
}

func TestConfig_invalidImportedServiceNamesCrossSeedSource(t *testing.T) {
	crossSeedPath := writeCrossSeedJS(t, `module.exports = { torrentClients: ["qbittorrent:ftp://alice:secret-qbit@example.com:8112"] };`)

	_, err := Load(nil, lookupFrom(nil), configWithCrossSeed(t, crossSeedPath, ""))
	_, overriddenErr := Load(nil, lookupFrom(nil), configWithCrossSeed(t, crossSeedPath,
		`, "torrentClients": ["qbittorrent:http://example.com:8080"]`))

	if err == nil || !strings.Contains(err.Error(), "cross-seed") || !strings.Contains(err.Error(), "torrentClients[0]") {
		t.Fatalf("gotValue %v, expectedValue an error naming cross-seed torrentClients[0]", err)
	}
	assertNoSecret(t, err.Error())
	if overriddenErr != nil {
		t.Fatalf("a config.json list replaces the imported one entirely: %v", overriddenErr)
	}
}

func TestConfig_redactsImportedCredentials(t *testing.T) {
	crossSeedPath := writeCrossSeedJS(t, `module.exports = {
		radarr: ["http://example.com:7878/?apikey=" + process.env.RADARR_KEY],
		torrentClients: ["qbittorrent:http://alice:secret-qbit@example.com:8080"],
		port: 2468,
		apiKey: "secret-cross-seed",
	};`)
	loadedConfig := mustLoad(t, nil, map[string]string{"RADARR_KEY": "secret-radarr"}, configWithCrossSeed(t, crossSeedPath, ""))

	for _, rendering := range renderingsOf(t, loadedConfig) {
		assertNoSecret(t, rendering)
	}
	if loadedConfig.CrossSeed == nil || loadedConfig.CrossSeed.APIKey != "secret-cross-seed" {
		t.Fatal("the daemon key must stay usable in memory")
	}
}

func TestConfig_redactsKeyEmbeddedInProxyPath(t *testing.T) {
	configDir := t.TempDir()
	writeFile(t, configDir, configFileName, `{"torrentClients": ["qbittorrent:http://example.com:7476/proxy/secret-proxy-key"]}`)
	loadedConfig := mustLoad(t, nil, nil, configDir)

	if loadedConfig.TorrentClients[0].BaseURL != "http://example.com:7476/proxy/secret-proxy-key" {
		t.Fatalf("the proxy key must stay usable in memory: %s", loadedConfig.TorrentClients[0].BaseURL)
	}
	renderings := renderingsOf(t, loadedConfig)
	renderings = append(renderings, loadedConfig.TorrentClients[0].String(), fmt.Sprintf("%#v", loadedConfig.TorrentClients[0]))
	for _, rendering := range renderings {
		assertNoSecret(t, rendering)
		if !strings.Contains(rendering, "/proxy/") {
			t.Errorf("the proxy path stays readable: %s", rendering)
		}
	}
}

func renderingsOf(t *testing.T, loadedConfig Config) []string {
	t.Helper()
	redactedJSON, err := loadedConfig.RedactedJSON()
	if err != nil {
		t.Fatal(err)
	}
	var logBuffer bytes.Buffer
	slog.New(slog.NewJSONHandler(&logBuffer, nil)).Info("config", "config", loadedConfig)
	renderings := []string{string(redactedJSON), logBuffer.String()}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		renderings = append(renderings, fmt.Sprintf(verb, loadedConfig))
	}
	return renderings
}

func TestConfigExample_loadsWithCrossSeedOptions(t *testing.T) {
	skipOnWindows(t)
	configDir := t.TempDir()
	exampleContent, err := os.ReadFile(filepath.Join("..", "..", "config.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, configDir, configFileName, string(exampleContent))
	crossSeedPath, err := filepath.Abs(filepath.Join("..", "crossseed", "testdata", "config.example.js"))
	if err != nil {
		t.Fatal(err)
	}
	environment := map[string]string{
		"RADARR_API_KEY": "r", "SONARR_API_KEY": "s", "QBIT_PASSWORD": "q", "RCLONE_RC_PASS": "p",
		envCrossSeedConfig: crossSeedPath, "CROSS_SEED_API_KEY": "k",
	}

	loadedConfig := mustLoad(t, nil, environment, configDir)

	expectedSources := Sources{Radarr: sourceConfigFile, Sonarr: sourceConfigFile, TorrentClients: sourceConfigFile, LinkDirs: sourceCrossSeed}
	if loadedConfig.Sources != expectedSources || !loadedConfig.CrossSeedImport.Configured {
		t.Fatalf("gotValue %+v %+v, expectedValue %+v", loadedConfig.Sources, loadedConfig.CrossSeedImport, expectedSources)
	}
	if loadedConfig.LinkType != "hardlink" || loadedConfig.LinkCategory != "cross-seed-link" || loadedConfig.CrossSeed == nil {
		t.Fatalf("imported values missing: %v", loadedConfig)
	}
	if !slices.Contains(loadedConfig.CrossSeedImport.IgnoredKeys, "torznab") {
		t.Fatalf("ignored keys must be listed: %v", loadedConfig.CrossSeedImport.IgnoredKeys)
	}
}

func TestConfig_ambiguousImportedPasswordsNeverLeak(t *testing.T) {
	rawClients := []string{
		"qbittorrent:http://alice:secret-pa/ss@example.com:8080",
		"qbittorrent:http://alice:secret-12?ss@example.com:8080",
		"qbittorrent:http://alice:secret-pa#ss@example.com:8080",
		"qbittorrent:http://alice:12/secret-ss@example.com:8080",
		"qbittorrent:http://alice:secret-p%2Fss@example.com:8080",
		"qbittorrent:http://alice:secret-pass@[2001:db8::1]:8080",
		"qbittorrent:readonly:http://example.com:7476/proxy/secret-proxy-key",
	}
	for _, rawClient := range rawClients {
		t.Run(rawClient, func(t *testing.T) {
			encodedClient, err := json.Marshal(rawClient)
			if err != nil {
				t.Fatal(err)
			}
			crossSeedPath := writeCrossSeedJS(t, `module.exports = { torrentClients: [`+string(encodedClient)+`] };`)

			loadedConfig, err := Load(nil, lookupFrom(nil), configWithCrossSeed(t, crossSeedPath, ""))
			if err != nil {
				assertNoSecret(t, err.Error())
				return
			}
			for _, rendering := range renderingsOf(t, loadedConfig) {
				assertNoSecret(t, rendering)
			}
		})
	}
}

func TestConfig_unsupportedImportedClientsBecomeAWarning(t *testing.T) {
	crossSeedPath := writeCrossSeedJS(t, `module.exports = { torrentClients: [
		"deluge:http://alice:secret-deluge@example.com:8112",
		"qbittorrent:http://example.com:8080",
		"secret-garbage",
		"transmission:http://alice:secret-transmission@example.com:9091",
	] };`)
	expectedWarning := []UnsupportedClient{{Index: 0, Type: "deluge"}, {Index: 2, Type: "unknown"}, {Index: 3, Type: "transmission"}}

	loadedConfig := mustLoad(t, nil, nil, configWithCrossSeed(t, crossSeedPath, ""))

	if len(loadedConfig.TorrentClients) != 1 || loadedConfig.TorrentClients[0].BaseURL != "http://example.com:8080" {
		t.Fatalf("gotValue %v, expectedValue only the qbittorrent client", loadedConfig.TorrentClients)
	}
	if !slices.Equal(loadedConfig.CrossSeedImport.UnsupportedTorrentClients, expectedWarning) {
		t.Fatalf("gotValue %v, expectedValue %v", loadedConfig.CrossSeedImport.UnsupportedTorrentClients, expectedWarning)
	}
	for _, rendering := range renderingsOf(t, loadedConfig) {
		assertNoSecret(t, rendering)
	}
}

func TestConfig_onlyUnsupportedImportedClientsLoadWithAnEmptyList(t *testing.T) {
	crossSeedPath := writeCrossSeedJS(t, `module.exports = { torrentClients: ["rtorrent:http://example.com:8000/RPC2"] };`)

	loadedConfig := mustLoad(t, nil, nil, configWithCrossSeed(t, crossSeedPath, ""))

	if len(loadedConfig.TorrentClients) != 0 || loadedConfig.Sources.TorrentClients != sourceNone {
		t.Fatalf("gotValue %v %q, expectedValue no client", loadedConfig.TorrentClients, loadedConfig.Sources.TorrentClients)
	}
	if len(loadedConfig.CrossSeedImport.UnsupportedTorrentClients) != 1 {
		t.Fatalf("gotValue %v, expectedValue one warning", loadedConfig.CrossSeedImport.UnsupportedTorrentClients)
	}
}

func TestConfig_explicitCrossSeedUrlRejectsUserinfo(t *testing.T) {
	configDir := t.TempDir()
	writeFile(t, configDir, configFileName, `{"crossSeed": "http://alice:secret-pass@example.com:2468?apikey=secret-key"}`)

	_, err := Load(nil, lookupFrom(nil), configDir)

	if err == nil || !strings.Contains(err.Error(), "userinfo") {
		t.Fatalf("gotValue %v, expectedValue a userinfo error", err)
	}
	assertNoSecret(t, err.Error())
}

func TestConfig_relativeCrossSeedConfigPaths(t *testing.T) {
	configDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(configDir, "cross-seed"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(configDir, "cross-seed"), "config.js", `module.exports = { linkCategory: "from-config-dir" };`)
	writeFile(t, configDir, configFileName, `{"crossSeedConfig": "cross-seed/config.js"}`)
	workingDir := t.TempDir()
	writeFile(t, workingDir, "config.js", `module.exports = { linkCategory: "from-working-dir" };`)
	t.Chdir(workingDir)

	fromFile := mustLoad(t, nil, nil, configDir).LinkCategory
	fromEnv := mustLoad(t, nil, map[string]string{envCrossSeedConfig: "config.js"}, configDir).LinkCategory

	if fromFile != "from-config-dir" || fromEnv != "from-working-dir" {
		t.Fatalf("gotValue %q %q, expectedValue from-config-dir from-working-dir", fromFile, fromEnv)
	}
}

func TestConfig_crossSeedModePriorityAndValidation(t *testing.T) {
	configDir := t.TempDir()
	if got := mustLoad(t, nil, nil, configDir).CrossSeedMode; got != "auto" {
		t.Fatalf("default: gotValue %q", got)
	}
	writeFile(t, configDir, "config.json", `{"crossSeedMode":"native"}`)
	if got := mustLoad(t, nil, nil, configDir).CrossSeedMode; got != "native" {
		t.Fatalf("json: gotValue %q", got)
	}
	if got := mustLoad(t, nil, map[string]string{"CROSS_SEED_MODE": "delegate"}, configDir).CrossSeedMode; got != "delegate" {
		t.Fatalf("env: gotValue %q", got)
	}
	if got := mustLoad(t, []string{"--cross-seed-mode", "off"}, map[string]string{"CROSS_SEED_MODE": "delegate"}, configDir).CrossSeedMode; got != "off" {
		t.Fatalf("cli: gotValue %q", got)
	}
	if got := mustLoad(t, []string{"--cross-seed-mode", "OFF"}, nil, configDir); !strings.Contains(string(mustJSON(t, got)), `"crossSeedMode":"off"`) {
		t.Fatal("crossSeedMode not shown in config check output")
	}
	_, err := Load([]string{"--cross-seed-mode", "turbo-extra-long-garbage-value-here"}, lookupFrom(nil), configDir)
	if err == nil || !strings.Contains(err.Error(), "crossSeedMode") || !strings.Contains(err.Error(), "delegate") ||
		strings.Contains(err.Error(), "garbage-value-here") {
		t.Fatalf("unexpected error %v", err)
	}
}

func mustJSON(t *testing.T, loadedConfig Config) []byte {
	t.Helper()
	encoded, err := loadedConfig.RedactedJSON()
	if err != nil {
		t.Fatal(err)
	}
	return bytes.ReplaceAll(encoded, []byte(`": "`), []byte(`":"`))
}
