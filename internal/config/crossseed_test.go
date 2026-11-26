package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"necromanciarr/internal/store"
)

func writeCrossSeedJS(t *testing.T, content string) string {
	t.Helper()
	directory := t.TempDir()
	writeFile(t, directory, "config.js", content)
	return filepath.Join(directory, "config.js")
}

// loadWithCrossSeed charge la configuration avec le config.js donné par CROSS_SEED_CONFIG et la base storedJSON.
func loadWithCrossSeed(t *testing.T, crossSeedPath, storedJSON string) (Config, error) {
	t.Helper()
	return loadStored(t, nil, map[string]string{envCrossSeedConfig: crossSeedPath}, storedJSON)
}

func mustLoadWithCrossSeed(t *testing.T, crossSeedPath, storedJSON string) Config {
	t.Helper()
	loadedConfig, err := loadWithCrossSeed(t, crossSeedPath, storedJSON)
	if err != nil {
		t.Fatal(err)
	}
	return loadedConfig
}

func TestConfig_priorityCliEnvStoredImportedDefaults(t *testing.T) {
	envFile := writeCrossSeedJS(t, `module.exports = { linkCategory: "from-env-path", linkType: "symlink" };`)
	cliFile := writeCrossSeedJS(t, `module.exports = { linkCategory: "from-cli-path" };`)
	environment := map[string]string{envCrossSeedConfig: envFile}
	pathCases := []struct {
		name             string
		args             []string
		expectedCategory string
	}{
		{"env path", nil, "from-env-path"},
		{"cli over env", []string{"--cross-seed-config", cliFile}, "from-cli-path"},
	}
	for _, testCase := range pathCases {
		t.Run(testCase.name, func(t *testing.T) {
			gotValue := mustLoad(t, testCase.args, environment, t.TempDir()).LinkCategory
			if gotValue != testCase.expectedCategory {
				t.Fatalf("gotValue %q, expectedValue %q", gotValue, testCase.expectedCategory)
			}
		})
	}
	linkTypeCases := []struct {
		name          string
		load          func() Config
		expectedValue string
	}{
		{"default", func() Config { return mustLoad(t, nil, nil, t.TempDir()) }, defaultLinkType},
		{"imported over default", func() Config { return mustLoadWithCrossSeed(t, envFile, `{}`) }, "symlink"},
		{"stored over imported", func() Config { return mustLoadWithCrossSeed(t, envFile, `{"linkType": "reflink"}`) }, "reflink"},
	}
	for _, testCase := range linkTypeCases {
		t.Run(testCase.name, func(t *testing.T) {
			if gotValue := testCase.load().LinkType; gotValue != testCase.expectedValue {
				t.Fatalf("gotValue %q, expectedValue %q", gotValue, testCase.expectedValue)
			}
		})
	}
}

func TestConfig_storedValuesOverrideCrossSeedValues(t *testing.T) {
	crossSeedPath := writeCrossSeedJS(t, `module.exports = {
		radarr: ["http://imported.example.com:7878?apikey=imported"],
		linkCategory: "imported-category",
		skipRecheck: true,
	};`)
	reader := storedFromJSON(t, `{"linkCategory": "json-category", "skipRecheck": false}`)
	replacement := Element{"url": "http://json.example.com:7878", "apiKey": "json"}
	reader.overrides = []store.ConfigOverride{{List: "radarr", ItemID: "http://imported.example.com:7878", Value: mustMarshal(t, replacement)}}

	loadedConfig, err := LoadWith(nil, lookupFrom(map[string]string{envCrossSeedConfig: crossSeedPath}), t.TempDir(), openFake(reader))
	if err != nil {
		t.Fatal(err)
	}

	if len(loadedConfig.Radarr) != 1 || loadedConfig.Radarr[0].BaseURL != "http://json.example.com:7878" {
		t.Fatalf("gotValue %v, expectedValue the stored radarr", loadedConfig.Radarr)
	}
	if loadedConfig.LinkCategory != "json-category" || loadedConfig.SkipRecheck {
		t.Fatalf("gotValue %q %v, expectedValue json-category false", loadedConfig.LinkCategory, loadedConfig.SkipRecheck)
	}
	if valeurObtenue := elementOrigins(loadedConfig, listRadarr); !slices.Equal(valeurObtenue, []string{originOverride}) {
		t.Fatalf("valeurObtenue %v, valeurAttendue [%s]", valeurObtenue, originOverride)
	}
}

func TestConfig_listsAreMergedPerElement(t *testing.T) {
	importedLinks := absPath("home", "u", "imported-links")
	crossSeedPath := writeCrossSeedJS(t, `module.exports = {
		sonarr: ["http://first.example.com:8989?apikey=a", "http://second.example.com:8989?apikey=b"],
		linkDirs: ["`+jsonText(importedLinks)+`"],
	};`)
	reader := storedFromJSON(t, `{"sonarr": ["http://json.example.com:8989?apikey=c"]}`)
	reader.overrides = append(reader.overrides,
		store.ConfigOverride{List: "sonarr", ItemID: "http://first.example.com:8989", Deleted: true},
		store.ConfigOverride{List: "sonarr", ItemID: "http://second.example.com:8989", Deleted: true})

	loadedConfig, err := LoadWith(nil, lookupFrom(map[string]string{envCrossSeedConfig: crossSeedPath}), t.TempDir(), openFake(reader))
	if err != nil {
		t.Fatal(err)
	}

	if len(loadedConfig.Sonarr) != 1 || loadedConfig.Sonarr[0].BaseURL != "http://json.example.com:8989" {
		t.Fatalf("gotValue %v, expectedValue only the stored sonarr", loadedConfig.Sonarr)
	}
	if !slices.Equal(loadedConfig.LinkDirs, []string{importedLinks}) {
		t.Fatalf("an untouched list keeps the imported elements: gotValue %v", loadedConfig.LinkDirs)
	}
	valeurAttendue := map[string][]string{
		listRadarr: nil, listSonarr: {originAdded}, listTorrentClients: nil, listLinkDirs: {originCrossSeed},
	}
	for list, originesAttendues := range valeurAttendue {
		if valeurObtenue := elementOrigins(loadedConfig, list); !slices.Equal(valeurObtenue, originesAttendues) {
			t.Fatalf("%s: valeurObtenue %v, valeurAttendue %v", list, valeurObtenue, originesAttendues)
		}
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

	_, flagErr := Load([]string{"--cross-seed-config", missingPath}, lookupFrom(nil), t.TempDir())
	_, envErr := Load(nil, lookupFrom(map[string]string{envCrossSeedConfig: missingPath}), t.TempDir())

	for _, err := range []error{flagErr, envErr} {
		if err == nil || !strings.Contains(err.Error(), "cross-seed") {
			t.Fatalf("gotValue %v, expectedValue a cross-seed error", err)
		}
	}
}

func TestConfig_crossSeedDaemonFromExplicitUrlBeatsImportedHostPort(t *testing.T) {
	crossSeedPath := writeCrossSeedJS(t, `module.exports = { host: "localhost", port: 2468, apiKey: "imported-key" };`)
	daemonCases := []struct {
		name            string
		storedJSON      string
		expectedBaseURL string
		expectedAPIKey  string
	}{
		{"imported host and port", `{}`, "http://localhost:2468", "imported-key"},
		{
			"explicit url", `{"crossSeed": "http://cross-seed.example.com:2468?apikey=explicit-key"}`,
			"http://cross-seed.example.com:2468", "explicit-key",
		},
	}
	for _, testCase := range daemonCases {
		t.Run(testCase.name, func(t *testing.T) {
			daemon := mustLoadWithCrossSeed(t, crossSeedPath, testCase.storedJSON).CrossSeed

			if daemon == nil || daemon.BaseURL != testCase.expectedBaseURL || daemon.APIKey != testCase.expectedAPIKey {
				t.Fatalf("gotValue %+v, expectedValue %s", daemon, testCase.expectedBaseURL)
			}
		})
	}
}

func TestConfig_crossSeedDaemonDefaultsToLocalhostWhenHostUnset(t *testing.T) {
	crossSeedPath := writeCrossSeedJS(t, `module.exports = { host: "0.0.0.0", port: 2470 };`)

	daemon := mustLoadWithCrossSeed(t, crossSeedPath, `{}`).CrossSeed

	if daemon == nil || daemon.BaseURL != "http://localhost:2470" {
		t.Fatalf("gotValue %+v, expectedValue http://localhost:2470", daemon)
	}
}

func TestConfig_invalidImportedServiceNamesCrossSeedSource(t *testing.T) {
	crossSeedPath := writeCrossSeedJS(t, `module.exports = { torrentClients: ["qbittorrent:ftp://alice:secret-qbit@example.com:8112"] };`)

	_, err := loadWithCrossSeed(t, crossSeedPath, `{}`)

	if err == nil || !strings.Contains(err.Error(), "cross-seed") || !strings.Contains(err.Error(), "torrentClients[0]") {
		t.Fatalf("gotValue %v, expectedValue an error naming cross-seed torrentClients[0]", err)
	}
	assertNoSecret(t, err.Error())
}

func TestConfig_redactsImportedCredentials(t *testing.T) {
	crossSeedPath := writeCrossSeedJS(t, `module.exports = {
		radarr: ["http://example.com:7878/?apikey=" + process.env.RADARR_KEY],
		torrentClients: ["qbittorrent:http://alice:secret-qbit@example.com:8080"],
		port: 2468,
		apiKey: "secret-cross-seed",
	};`)
	environment := map[string]string{"RADARR_KEY": "secret-radarr", envCrossSeedConfig: crossSeedPath}
	loadedConfig := mustLoad(t, nil, environment, t.TempDir())

	for _, rendering := range renderingsOf(t, loadedConfig) {
		assertNoSecret(t, rendering)
	}
	if loadedConfig.CrossSeed == nil || loadedConfig.CrossSeed.APIKey != "secret-cross-seed" {
		t.Fatal("the daemon key must stay usable in memory")
	}
}

func TestConfig_redactsKeyEmbeddedInProxyPath(t *testing.T) {
	loadedConfig := mustLoadStored(t, nil, nil, `{"torrentClients": ["qbittorrent:http://example.com:7476/proxy/secret-proxy-key"]}`)

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

func TestCrossSeedExample_loadsItsOptions(t *testing.T) {
	skipOnWindows(t)
	crossSeedPath, err := filepath.Abs(filepath.Join("..", "crossseed", "testdata", "config.example.js"))
	if err != nil {
		t.Fatal(err)
	}
	environment := map[string]string{
		"RADARR_API_KEY": "r", "SONARR_API_KEY": "s", "QBIT_PASSWORD": "q",
		envCrossSeedConfig: crossSeedPath, "CROSS_SEED_API_KEY": "k",
	}

	loadedConfig := mustLoad(t, nil, environment, t.TempDir())

	for _, list := range upstreamLists {
		valeurObtenue := elementOrigins(loadedConfig, list)
		toutVientDeCrossSeed := len(valeurObtenue) > 0 && !slices.ContainsFunc(valeurObtenue, func(origin string) bool { return origin != originCrossSeed })
		if !toutVientDeCrossSeed {
			t.Fatalf("%s: valeurObtenue %v, valeurAttendue des éléments tous crossseed", list, valeurObtenue)
		}
	}
	if !loadedConfig.CrossSeedImport.Configured {
		t.Fatalf("valeurObtenue %+v, valeurAttendue un import configuré", loadedConfig.CrossSeedImport)
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

			loadedConfig, err := loadWithCrossSeed(t, crossSeedPath, `{}`)
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

	loadedConfig := mustLoadWithCrossSeed(t, crossSeedPath, `{}`)

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

	loadedConfig := mustLoadWithCrossSeed(t, crossSeedPath, `{}`)

	if len(loadedConfig.TorrentClients) != 0 || len(elementOrigins(loadedConfig, listTorrentClients)) != 0 {
		t.Fatalf("gotValue %v %v, expectedValue no client", loadedConfig.TorrentClients, elementOrigins(loadedConfig, listTorrentClients))
	}
	if len(loadedConfig.CrossSeedImport.UnsupportedTorrentClients) != 1 {
		t.Fatalf("gotValue %v, expectedValue one warning", loadedConfig.CrossSeedImport.UnsupportedTorrentClients)
	}
}

func TestConfig_explicitCrossSeedUrlRejectsUserinfo(t *testing.T) {
	_, err := loadStored(t, nil, nil, `{"crossSeed": "http://alice:secret-pass@example.com:2468?apikey=secret-key"}`)

	if err == nil || !strings.Contains(err.Error(), "userinfo") {
		t.Fatalf("gotValue %v, expectedValue a userinfo error", err)
	}
	assertNoSecret(t, err.Error())
}

func TestConfig_relativeCrossSeedConfigPathsStartFromTheWorkingDirectory(t *testing.T) {
	workingDir := t.TempDir()
	writeFile(t, workingDir, "config.js", `module.exports = { linkCategory: "from-working-dir" };`)
	t.Chdir(workingDir)

	fromEnv := mustLoad(t, nil, map[string]string{envCrossSeedConfig: "config.js"}, t.TempDir()).LinkCategory
	fromFlag := mustLoad(t, []string{"--cross-seed-config", "config.js"}, nil, t.TempDir()).LinkCategory

	if fromEnv != "from-working-dir" || fromFlag != "from-working-dir" {
		t.Fatalf("gotValue %q %q, expectedValue from-working-dir twice", fromEnv, fromFlag)
	}
}

func TestConfig_crossSeedModePriorityAndValidation(t *testing.T) {
	const stored = `{"crossSeedMode":"native"}`
	if got := mustLoad(t, nil, nil, t.TempDir()).CrossSeedMode; got != "auto" {
		t.Fatalf("default: gotValue %q", got)
	}
	if got := mustLoadStored(t, nil, nil, stored).CrossSeedMode; got != "native" {
		t.Fatalf("stored: gotValue %q", got)
	}
	if got := mustLoadStored(t, nil, map[string]string{"CROSS_SEED_MODE": "delegate"}, stored).CrossSeedMode; got != "delegate" {
		t.Fatalf("env: gotValue %q", got)
	}
	if got := mustLoadStored(t, []string{"--cross-seed-mode", "off"}, map[string]string{"CROSS_SEED_MODE": "delegate"}, stored).CrossSeedMode; got != "off" {
		t.Fatalf("cli: gotValue %q", got)
	}
	if got := mustLoadStored(t, []string{"--cross-seed-mode", "OFF"}, nil, stored); !strings.Contains(string(mustJSON(t, got)), `"crossSeedMode":"off"`) {
		t.Fatal("crossSeedMode not shown in config check output")
	}
	_, err := loadStored(t, []string{"--cross-seed-mode", "turbo-extra-long-garbage-value-here"}, nil, stored)
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
