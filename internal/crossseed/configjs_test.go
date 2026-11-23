package crossseed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const exampleConfigPath = "testdata/config.example.js"

func lookupFrom(variables map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, found := variables[name]
		return value, found
	}
}

func writeConfigJS(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.js")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustLoadConfigJS(t *testing.T, path string, environment map[string]string) Imported {
	t.Helper()
	imported, err := LoadConfigJS(context.Background(), path, lookupFrom(environment))
	if err != nil {
		t.Fatal(err)
	}
	return imported
}

func exampleEnvironment() map[string]string {
	return map[string]string{
		"RADARR_API_KEY": "secret-radarr", "SONARR_API_KEY": "secret-sonarr",
		"QBIT_PASSWORD": "secret-qbit", "CROSS_SEED_API_KEY": "secret-cross-seed",
	}
}

func TestLoadConfigJS_readsArrClientsAndLinkSettings(t *testing.T) {
	expectedValue := Imported{
		Radarr:         []string{"http://example.com:7878/?apikey=secret-radarr"},
		Sonarr:         []string{"http://example.com:8989/?apikey=secret-sonarr"},
		TorrentClients: []string{"qbittorrent:http://alice:secret-qbit@example.com:8080"},
		LinkDirs:       []string{"/home/u/data/cross-seed-links"},
		LinkType:       "hardlink",
		SkipRecheck:    true,
		LinkCategory:   "cross-seed-link",
		Port:           2468,
		APIKey:         "secret-cross-seed",
	}

	gotValue := mustLoadConfigJS(t, exampleConfigPath, exampleEnvironment())

	gotComparable := gotValue
	gotComparable.IgnoredKeys, gotComparable.ImportedKeys = nil, nil
	if fmt.Sprint(plainImported(gotComparable)) != fmt.Sprint(plainImported(expectedValue)) {
		t.Fatalf("gotValue %+v, expectedValue %+v", plainImported(gotComparable), plainImported(expectedValue))
	}
	expectedImportedKeys := []string{"apiKey", "linkCategory", "linkDirs", "linkType", "port", "radarr", "skipRecheck", "sonarr", "torrentClients"}
	if !slices.Equal(gotValue.ImportedKeys, expectedImportedKeys) {
		t.Fatalf("gotValue %v, expectedValue %v", gotValue.ImportedKeys, expectedImportedKeys)
	}
}

func TestLoadConfigJS_resolvesProcessEnv(t *testing.T) {
	path := writeConfigJS(t, `module.exports = {
		apiKey: process.env.PRESENT,
		linkCategory: typeof process.env.ABSENT,
		host: String(Object.keys(process.env).length),
	};`)
	environment := map[string]string{"PRESENT": "from-env"}

	gotValue := mustLoadConfigJS(t, path, environment)

	if gotValue.APIKey != "from-env" {
		t.Fatalf("gotValue %q, expectedValue %q", gotValue.APIKey, "from-env")
	}
	if gotValue.LinkCategory != "undefined" {
		t.Fatalf("unset variable: gotValue %q, expectedValue %q", gotValue.LinkCategory, "undefined")
	}
	if gotValue.Host != "0" {
		t.Fatalf("environment must not be enumerable: gotValue %q", gotValue.Host)
	}
}

func TestLoadConfigJS_cannotReadFilesOrUseRequire(t *testing.T) {
	path := writeConfigJS(t, `module.exports = {
		linkCategory: [typeof require, typeof fs, typeof setTimeout, typeof setInterval, typeof eval,
			typeof Function, typeof process.argv, typeof process.cwd, typeof process.exit, typeof process.platform,
			typeof console, typeof module.require].join(","),
	};`)
	expectedValue := strings.TrimSuffix(strings.Repeat("undefined,", 12), ",")

	gotValue := mustLoadConfigJS(t, path, nil)

	if gotValue.LinkCategory != expectedValue {
		t.Fatalf("gotValue %q, expectedValue %q", gotValue.LinkCategory, expectedValue)
	}
}

func TestLoadConfigJS_cannotRequireOrTouchTheFilesystem(t *testing.T) {
	markerPath := filepath.Join(t.TempDir(), "marker")
	scripts := map[string]string{
		"require":            `require("fs").writeFileSync(` + quoteJS(markerPath) + `, "x"); module.exports = {};`,
		"function generator": `(function(){}).constructor("return 1")(); module.exports = {};`,
		"async generator":    `(async function(){}).constructor("return 1"); module.exports = {};`,
		"import":             `import(` + quoteJS(markerPath) + `); module.exports = {};`,
	}
	for name, script := range scripts {
		t.Run(name, func(t *testing.T) {
			path := writeConfigJS(t, script)

			_, err := LoadConfigJS(context.Background(), path, lookupFrom(nil))

			if err == nil {
				t.Fatal("expected an error")
			}
			if _, statErr := os.Stat(markerPath); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("the script touched the filesystem: %v", statErr)
			}
		})
	}
}

func quoteJS(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func TestLoadConfigJS_stopsInfiniteLoop(t *testing.T) {
	path := writeConfigJS(t, `for (;;) {}`)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := LoadConfigJS(ctx, path, lookupFrom(nil))

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("gotValue %v, expectedValue %v", err, context.DeadlineExceeded)
	}
}

func TestLoadConfigJS_infiniteLoopIsInterrupted(t *testing.T) {
	path := writeConfigJS(t, `module.exports = { get radarr() { while (true) {} } };`)
	startedAt := time.Now()

	_, err := LoadConfigJS(context.Background(), path, lookupFrom(nil))

	if !errors.Is(err, ErrEvaluationTimeout) {
		t.Fatalf("gotValue %v, expectedValue %v", err, ErrEvaluationTimeout)
	}
	if elapsed := time.Since(startedAt); elapsed > evaluationTimeout+time.Second {
		t.Fatalf("interruption took %v", elapsed)
	}
}

func TestLoadConfigJS_deepRecursionIsRefused(t *testing.T) {
	path := writeConfigJS(t, `function dive(depth) { return dive(depth + 1); } module.exports = { port: dive(0) };`)

	if _, err := LoadConfigJS(context.Background(), path, lookupFrom(nil)); err == nil {
		t.Fatal("expected a stack limit error")
	}
}

func TestLoadConfigJS_oversizedFileRejected(t *testing.T) {
	path := writeConfigJS(t, "module.exports = {};\n//"+strings.Repeat("x", maxSourceBytes))

	_, err := LoadConfigJS(context.Background(), path, lookupFrom(nil))

	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("gotValue %v, expectedValue a size error", err)
	}
}

func TestLoadConfigJS_missingFileIsAnError(t *testing.T) {
	_, err := LoadConfigJS(context.Background(), filepath.Join(t.TempDir(), "absent.js"), lookupFrom(nil))

	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("gotValue %v, expectedValue %v", err, os.ErrNotExist)
	}
}

func TestLoadConfigJS_moduleExportsMustBePlainObject(t *testing.T) {
	scripts := []string{
		`module.exports = ["radarr"];`,
		`module.exports = "radarr";`,
		`module.exports = function () {};`,
		`module.exports = new Map();`,
		`module.exports = null;`,
		`exports = { port: 1 };`,
	}
	for _, script := range scripts {
		t.Run(script, func(t *testing.T) {
			path := writeConfigJS(t, script)
			imported, err := LoadConfigJS(context.Background(), path, lookupFrom(nil))
			// exports réaffecté n'atteint pas module.exports, comme sous Node : rien n'est importé.
			if script == `exports = { port: 1 };` {
				if err != nil || imported.Port != 0 {
					t.Fatalf("gotValue %+v %v, expectedValue nothing imported", imported, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "plain object") {
				t.Fatalf("gotValue %v, expectedValue a plain object error", err)
			}
		})
	}
}

func TestLoadConfigJS_exportsAliasIsRead(t *testing.T) {
	path := writeConfigJS(t, `exports.port = 2468;`)

	if gotValue := mustLoadConfigJS(t, path, nil).Port; gotValue != 2468 {
		t.Fatalf("gotValue %d, expectedValue 2468", gotValue)
	}
}

func TestLoadConfigJS_wrongTypeNamesTheKey(t *testing.T) {
	cases := map[string]string{
		"radarr":         `radarr: "http://example.com:7878?apikey=secret-radarr"`,
		"torrentClients": `torrentClients: ["qbittorrent:http://example.com", 3]`,
		"skipRecheck":    `skipRecheck: "secret-yes"`,
		"port":           `port: 24.5`,
		"apiKey":         `apiKey: 12345`,
		"linkDirs":       `linkDirs: "secret-dir"`,
	}
	for key, entry := range cases {
		t.Run(key, func(t *testing.T) {
			path := writeConfigJS(t, "module.exports = {"+entry+"};")

			_, err := LoadConfigJS(context.Background(), path, lookupFrom(nil))

			if err == nil || !strings.Contains(err.Error(), `"`+key+`"`) {
				t.Fatalf("gotValue %v, expectedValue an error naming %q", err, key)
			}
			if strings.Contains(err.Error(), "secret-") || strings.Contains(err.Error(), "12345") {
				t.Fatalf("error echoes a value: %v", err)
			}
		})
	}
}

func TestLoadConfigJS_namesBadKey(t *testing.T) {
	path := writeConfigJS(t, `module.exports = { linkType: "secret-copy" };`)

	_, err := LoadConfigJS(context.Background(), path, lookupFrom(nil))

	if err == nil || !strings.Contains(err.Error(), `"linkType"`) || strings.Contains(err.Error(), "secret-") {
		t.Fatalf("gotValue %v, expectedValue an error naming linkType without its value", err)
	}
}

func TestLoadConfigJS_scriptErrorsNeverEchoValues(t *testing.T) {
	scripts := []string{
		`throw new Error(process.env.TOKEN);`,
		`module.exports = { apiKey: "secret-literal" ;`,
	}
	for _, script := range scripts {
		path := writeConfigJS(t, script)

		_, err := LoadConfigJS(context.Background(), path, lookupFrom(map[string]string{"TOKEN": "secret-token"}))

		if err == nil || strings.Contains(err.Error(), "secret-") {
			t.Fatalf("gotValue %v, expectedValue an error without values", err)
		}
	}
}

func TestLoadConfigJS_ignoresSearchRulesAndReportsTheirNames(t *testing.T) {
	expectedValue := []string{
		"action", "autoResumeMaxDownload", "blockList", "dataDirs", "delay", "duplicateCategories", "excludeOlder",
		"excludeRecentSearch", "flatLinking", "fuzzySizeThreshold", "host", "ignoreNonRelevantFilesToResume",
		"includeNonVideos", "includeSingleEpisodes", "matchMode", "maxDataDepth", "notificationWebhookUrls", "outputDir",
		"rssCadence", "searchCadence", "seasonFromEpisodes", "torznab", "useClientTorrents",
	}

	imported := mustLoadConfigJS(t, exampleConfigPath, exampleEnvironment())

	// host vaut undefined dans l'exemple : JSON.stringify l'omet, il n'est ni importé ni ignoré.
	gotValue := imported.IgnoredKeys
	expectedValue = slices.DeleteFunc(expectedValue, func(key string) bool { return key == "host" })
	if !slices.Equal(gotValue, expectedValue) {
		t.Fatalf("gotValue %v, expectedValue %v", gotValue, expectedValue)
	}
	if strings.Contains(strings.Join(gotValue, ","), "indexer.example.com") {
		t.Fatal("ignored keys must carry names only")
	}
}

func TestLoadConfigJS_acceptsCommentsTrailingCommasAndEnvReferences(t *testing.T) {
	path := writeConfigJS(t, `// commentaire de ligne
/* commentaire
   de bloc */
module.exports = {
	linkDirs: [
		"C:\\cross-seed\\links", // chemin Windows aux barres doublées
		process.env.LINK_ROOT,
	],
	host: process.env.MISSING || "localhost",
};
`)
	expectedValue := []string{`C:\cross-seed\links`, "/home/u/links"}

	gotValue := mustLoadConfigJS(t, path, map[string]string{"LINK_ROOT": "/home/u/links"})

	if !slices.Equal(gotValue.LinkDirs, expectedValue) || gotValue.Host != "localhost" {
		t.Fatalf("gotValue %+v, expectedValue %v and localhost", plainImported(gotValue), expectedValue)
	}
}

func TestLoadConfigJS_nullAndUndefinedKeysAreNotImported(t *testing.T) {
	path := writeConfigJS(t, `module.exports = { apiKey: null, host: undefined, port: 2468 };`)

	gotValue := mustLoadConfigJS(t, path, nil)

	if !slices.Equal(gotValue.ImportedKeys, []string{"port"}) {
		t.Fatalf("gotValue %v, expectedValue [port]", gotValue.ImportedKeys)
	}
}

func TestLoadConfigJS_quiProxyClientAccepted(t *testing.T) {
	path := writeConfigJS(t, `module.exports = {
		torrentClients: ["qbittorrent:readonly:http://example.com:7476/proxy/secret-proxy-key"],
	};`)
	expectedValue := []string{"qbittorrent:readonly:http://example.com:7476/proxy/secret-proxy-key"}

	gotValue := mustLoadConfigJS(t, path, nil)

	if !slices.Equal(gotValue.TorrentClients, expectedValue) {
		t.Fatalf("gotValue %v, expectedValue %v", gotValue.TorrentClients, expectedValue)
	}
	assertNoSecret(t, gotValue.String())
}

func TestImported_redactsCredentialsEverywhere(t *testing.T) {
	imported := mustLoadConfigJS(t, exampleConfigPath, exampleEnvironment())
	imported.TorrentClients = append(imported.TorrentClients, "qbittorrent:http://example.com:7476/proxy/secret-proxy-key")

	directJSON, err := json.Marshal(imported)
	if err != nil {
		t.Fatal(err)
	}
	var logBuffer bytes.Buffer
	slog.New(slog.NewJSONHandler(&logBuffer, nil)).Info("imported", "imported", imported)
	renderings := []string{string(directJSON), logBuffer.String()}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		renderings = append(renderings, fmt.Sprintf(verb, imported))
	}
	for _, rendering := range renderings {
		assertNoSecret(t, rendering)
		if !strings.Contains(rendering, "example.com") {
			t.Errorf("redaction must keep the host: %s", rendering)
		}
	}
}

func TestImported_redactionFailsClosedOnAmbiguousUserinfo(t *testing.T) {
	cases := []struct {
		raw          string
		expectedHost string
	}{
		{"qbittorrent:http://alice:secret-pa/ss@example.com:8080", redactedMark},
		{"qbittorrent:http://alice:secret-pa?ss@example.com:8080", redactedMark},
		{"qbittorrent:http://alice:secret-pa#ss@example.com:8080", redactedMark},
		{"qbittorrent:http://alice:secret-p@ss@example.com:8080", "example.com:8080"},
		{"qbittorrent:readonly:http://alice:secret-p:ss@example.com:8080", "example.com:8080"},
		{"qbittorrent:http://alice:secret-p%2Fss@example.com:8080", "example.com:8080"},
		{"qbittorrent:http://alice:secret-pass@[2001:db8::1]:8080", "[2001:db8::1]:8080"},
		{"qbittorrent:http://example.com:7476/proxy/secret-proxy-key", "example.com:7476"},
		{"qbittorrent:http://example.com:7476/proxy/secret-ke@y", redactedMark},
		{"http://example.com:7878/?apikey=secret-radarr", "example.com:7878"},
		{"http://secret-only-userinfo@", redactedMark},
		{"secret-no-scheme", ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.raw, func(t *testing.T) {
			imported := Imported{TorrentClients: []string{testCase.raw}, Radarr: []string{testCase.raw}}
			directJSON, err := json.Marshal(imported)
			if err != nil {
				t.Fatal(err)
			}
			renderings := []string{
				string(directJSON), imported.String(), fmt.Sprintf("%#v", imported),
				slog.AnyValue(imported).Resolve().String(),
			}
			for _, rendering := range renderings {
				assertNoSecret(t, rendering)
			}
			if gotValue := redactService(testCase.raw); testCase.expectedHost != "" && !strings.HasSuffix(gotValue, "://"+testCase.expectedHost) {
				t.Fatalf("gotValue %q, expectedValue host %q", gotValue, testCase.expectedHost)
			}
		})
	}
}

func assertNoSecret(t *testing.T, rendered string) {
	t.Helper()
	if strings.Contains(rendered, "secret-") {
		t.Errorf("leaked a secret: %s", rendered)
	}
}
