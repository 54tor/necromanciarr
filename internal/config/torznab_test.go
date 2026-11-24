package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	fakeIndexerKey  = "secret-indexer-key"
	fakeProwlarrKey = "secret-prowlarr-key"
)

func loadTorznabConfig(t *testing.T, content string, environment map[string]string) (Config, error) {
	t.Helper()
	configDir := t.TempDir()
	writeFile(t, configDir, configFileName, content)
	return Load(nil, lookupFrom(environment), configDir)
}

func TestConfig_sourceOrderKeepsProviderFirst(t *testing.T) {
	indexers := `"torznab": ["https://prowlarr.example.com/12/api?apikey=k1", {"url": "https://prowlarr.example.com/7/api?apikey=k2", "name": "tracker-a"}]`
	scenarios := []struct {
		name          string
		sourceOrder   string
		expectedOrder []string
		expectedError string
	}{
		{name: "default is provider then torznab in list order", sourceOrder: ``, expectedOrder: []string{"provider", "torznab:12", "torznab:tracker-a"}},
		{name: "explicit order is kept", sourceOrder: `"sourceOrder": ["provider", "torznab:tracker-a", "torznab:12"],`, expectedOrder: []string{"provider", "torznab:tracker-a", "torznab:12"}},
		{name: "a subset is kept", sourceOrder: `"sourceOrder": ["provider", "torznab:12"],`, expectedOrder: []string{"provider", "torznab:12"}},
		{name: "provider after an indexer is refused, not reordered", sourceOrder: `"sourceOrder": ["torznab:12", "provider"],`, expectedError: "provider must be the first source"},
		{name: "provider missing is refused, not prepended", sourceOrder: `"sourceOrder": ["torznab:12"],`, expectedError: "provider must be the first source"},
		{name: "provider twice is refused", sourceOrder: `"sourceOrder": ["provider", "provider"],`, expectedError: "listed twice"},
		{name: "an indexer twice is refused", sourceOrder: `"sourceOrder": ["provider", "torznab:12", "torznab:12"],`, expectedError: "listed twice"},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			loadedConfig, err := loadTorznabConfig(t, "{"+scenario.sourceOrder+indexers+"}", nil)

			if scenario.expectedError != "" {
				if err == nil || !strings.Contains(err.Error(), scenario.expectedError) {
					t.Fatalf("gotValue %v, expectedValue an error containing %q", err, scenario.expectedError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(loadedConfig.SourceOrder, scenario.expectedOrder) {
				t.Fatalf("gotValue %v, expectedValue %v", loadedConfig.SourceOrder, scenario.expectedOrder)
			}
		})
	}
}

func TestConfig_sourceOrderDefaultsToProviderAloneWithoutIndexers(t *testing.T) {
	loadedConfig := mustLoad(t, nil, nil, t.TempDir())

	if !slices.Equal(loadedConfig.SourceOrder, []string{"provider"}) {
		t.Fatalf("gotValue %v, expectedValue [provider]", loadedConfig.SourceOrder)
	}
}

func TestConfig_sourceOrderRefusesUnknownSource(t *testing.T) {
	scenarios := map[string]string{
		"unknown indexer":  `"sourceOrder": ["provider", "torznab:nope"]`,
		"unknown kind":     `"sourceOrder": ["provider", "usenet:12"]`,
		"empty entry":      `"sourceOrder": ["provider", ""]`,
		"bare indexer id":  `"sourceOrder": ["provider", "12"]`,
		"uppercase prefix": `"sourceOrder": ["provider", "Torznab:12"]`,
	}
	for name, sourceOrder := range scenarios {
		t.Run(name, func(t *testing.T) {
			_, err := loadTorznabConfig(t, `{"torznab": ["https://prowlarr.example.com/12/api?apikey=k"], `+sourceOrder+`}`, nil)

			if err == nil || !strings.Contains(err.Error(), "sourceOrder[1]") {
				t.Fatalf("gotValue %v, expectedValue an error naming sourceOrder[1]", err)
			}
		})
	}
}

func TestConfig_torznabDefaultsNameFromPath(t *testing.T) {
	loadedConfig, err := loadTorznabConfig(t, `{"torznab": [
		"https://prowlarr.example.com:9696/12/api?apikey=k1",
		"https://jackett.example.com/api/v2.0/indexers/all/results/torznab/api?apikey=k2",
		"https://other.example.com/api/9?apikey=k3"
	]}`, nil)
	if err != nil {
		t.Fatal(err)
	}

	gotNames := []string{loadedConfig.Torznab[0].Name, loadedConfig.Torznab[1].Name, loadedConfig.Torznab[2].Name}

	expectedNames := []string{"12", "1", "9"}
	if !slices.Equal(gotNames, expectedNames) {
		t.Fatalf("gotValue %v, expectedValue %v", gotNames, expectedNames)
	}
}

func TestConfig_torznabRefusesDuplicateAndInvalidNames(t *testing.T) {
	scenarios := map[string]string{
		"same numeric id on two hosts": `["https://a.example.com/12/api?apikey=k", "https://b.example.com/12/api?apikey=k"]`,
		"explicit duplicate":           `[{"url": "https://a.example.com/1/api?apikey=k", "name": "x"}, {"url": "https://a.example.com/2/api?apikey=k", "name": "x"}]`,
		"uppercase":                    `[{"url": "https://a.example.com/1/api?apikey=k", "name": "Tracker"}]`,
		"underscore":                   `[{"url": "https://a.example.com/1/api?apikey=k", "name": "tracker_a"}]`,
		"colon":                        `[{"url": "https://a.example.com/1/api?apikey=k", "name": "torznab:a"}]`,
	}
	for name, indexers := range scenarios {
		t.Run(name, func(t *testing.T) {
			_, err := loadTorznabConfig(t, `{"torznab": `+indexers+`}`, nil)

			if err == nil || !strings.Contains(err.Error(), "name") {
				t.Fatalf("gotValue %v, expectedValue an error about the name", err)
			}
		})
	}
}

func TestConfig_torznabRefusesExtraQueryParam(t *testing.T) {
	scenarios := map[string]string{
		"extra parameter":   "https://prowlarr.example.com/12/api?apikey=secret-k&t=search",
		"userinfo":          "https://bob:secret-pass@prowlarr.example.com/12/api?apikey=secret-k",
		"fragment":          "https://prowlarr.example.com/12/api?apikey=secret-k#top",
		"other scheme":      "ftp://prowlarr.example.com/12/api?apikey=secret-k",
		"missing key":       "https://prowlarr.example.com/12/api",
		"raw at in the key": "https://prowlarr.example.com/12/api?apikey=secret@k",
		"no host":           "https:///12/api?apikey=secret-k",
	}
	for name, rawURL := range scenarios {
		t.Run(name, func(t *testing.T) {
			_, err := loadTorznabConfig(t, `{"torznab": [`+quoteJSON(rawURL)+`]}`, nil)

			if err == nil || !strings.Contains(err.Error(), "torznab[0]") {
				t.Fatalf("gotValue %v, expectedValue an error naming torznab[0]", err)
			}
			if strings.Contains(err.Error(), "secret-") || strings.Contains(err.Error(), "prowlarr.example.com") {
				t.Fatalf("gotValue %q, expectedValue an error without the URL", err)
			}
		})
	}
}

func quoteJSON(text string) string {
	encoded, _ := json.Marshal(text)
	return string(encoded)
}

func TestConfig_torznabRefusesUnknownObjectKeyAndWrongType(t *testing.T) {
	for name, entry := range map[string]string{
		"unknown key": `{"url": "https://a.example.com/1/api?apikey=k", "nom": "x"}`,
		"number":      `12`,
		"no url":      `{"name": "x"}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadTorznabConfig(t, `{"torznab": [`+entry+`]}`, nil)

			if err == nil || !strings.Contains(err.Error(), "torznab[0]") {
				t.Fatalf("gotValue %v, expectedValue an error naming torznab[0]", err)
			}
		})
	}
}

func TestConfig_torznabLimitsDefaults(t *testing.T) {
	loadedConfig, err := loadTorznabConfig(t, `{"torznab": [
		"https://prowlarr.example.com/12/api?apikey=k1",
		{"url": "https://prowlarr.example.com/7/api?apikey=k2", "tracker": "tracker-a", "categories": [2000, 5000],
		 "minInterval": "5s", "timeout": "10s", "maxQueriesPerRun": 5, "maxDownloadsPerRun": 2, "maxQueriesPerDay": 50, "maxDownloadsPerDay": 20}
	]}`, nil)
	if err != nil {
		t.Fatal(err)
	}

	defaulted, customised := loadedConfig.Torznab[0], loadedConfig.Torznab[1]

	gotDefaults := []any{
		defaulted.MinInterval, defaulted.MaxQueriesPerRun, defaulted.MaxDownloadsPerRun,
		defaulted.MaxQueriesPerDay, defaulted.MaxDownloadsPerDay, defaulted.Timeout,
	}
	expectedDefaults := []any{3 * time.Second, 30, 10, 200, 50, 30 * time.Second}
	if fmt.Sprint(gotDefaults) != fmt.Sprint(expectedDefaults) {
		t.Fatalf("gotValue %v, expectedValue %v", gotDefaults, expectedDefaults)
	}
	gotCustom := []any{
		customised.MinInterval, customised.MaxQueriesPerRun, customised.MaxDownloadsPerRun,
		customised.MaxQueriesPerDay, customised.MaxDownloadsPerDay, customised.Timeout, customised.Tracker, customised.Categories,
	}
	expectedCustom := []any{5 * time.Second, 5, 2, 50, 20, 10 * time.Second, "tracker-a", []int{2000, 5000}}
	if fmt.Sprint(gotCustom) != fmt.Sprint(expectedCustom) {
		t.Fatalf("gotValue %v, expectedValue %v", gotCustom, expectedCustom)
	}
}

func TestConfig_torznabRefusesNonPositiveLimits(t *testing.T) {
	for _, field := range []string{
		`"minInterval": "0s"`, `"minInterval": "999ms"`, `"minInterval": "-1s"`, `"minInterval": "soon"`, `"minInterval": "2h"`,
		`"timeout": "0s"`, `"timeout": "500ms"`, `"timeout": "6m"`,
		`"maxQueriesPerRun": 100001`, `"maxQueriesPerDay": 100001`, `"maxDownloadsPerRun": 100001`, `"maxDownloadsPerDay": 100001`,
		`"maxQueriesPerRun": 0`, `"maxDownloadsPerRun": -1`, `"maxQueriesPerDay": 0`, `"maxDownloadsPerDay": 0`,
		`"categories": [0]`, `"categories": [-5]`,
	} {
		t.Run(field, func(t *testing.T) {
			_, err := loadTorznabConfig(t, `{"torznab": [{"url": "https://a.example.com/1/api?apikey=k", `+field+`}]}`, nil)

			if err == nil || !strings.Contains(err.Error(), "torznab[0]") {
				t.Fatalf("gotValue %v, expectedValue an error naming torznab[0]", err)
			}
		})
	}
}

func TestConfig_torznabInterpolatesVariablesInTheURL(t *testing.T) {
	loadedConfig, err := loadTorznabConfig(t, `{"torznab": ["https://prowlarr.example.com/12/api?apikey=${PROWLARR_API_KEY}"],
		"prowlarr": "https://prowlarr.example.com:9696?apikey=${PROWLARR_API_KEY}"}`,
		map[string]string{"PROWLARR_API_KEY": fakeIndexerKey})
	if err != nil {
		t.Fatal(err)
	}

	if loadedConfig.Torznab[0].URL != "https://prowlarr.example.com/12/api?apikey="+fakeIndexerKey {
		t.Fatalf("gotValue %q, expectedValue the interpolated URL", "…")
	}
	if !slices.Contains(loadedConfig.KnownSecrets(), fakeIndexerKey) {
		t.Fatalf("gotValue %v, expectedValue the key among the known secrets", loadedConfig.KnownSecrets())
	}
	_, err = loadTorznabConfig(t, `{"torznab": ["https://prowlarr.example.com/12/api?apikey=${MISSING_KEY}"]}`, nil)
	if err == nil || !strings.Contains(err.Error(), "MISSING_KEY") {
		t.Fatalf("gotValue %v, expectedValue an error naming the undefined variable", err)
	}
}

func TestConfig_prowlarrIsValidatedLikeAnIndexerURL(t *testing.T) {
	for name, rawURL := range map[string]string{
		"extra parameter": "https://prowlarr.example.com:9696?apikey=secret-k&x=1",
		"userinfo":        "https://bob:secret-pass@prowlarr.example.com:9696?apikey=secret-k",
		"missing key":     "https://prowlarr.example.com:9696",
		"fragment":        "https://prowlarr.example.com:9696?apikey=secret-k#a",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadTorznabConfig(t, `{"prowlarr": `+quoteJSON(rawURL)+`}`, nil)

			if err == nil || !strings.Contains(err.Error(), "prowlarr") || strings.Contains(err.Error(), "secret-") {
				t.Fatalf("gotValue %v, expectedValue an error naming prowlarr without the URL", err)
			}
		})
	}
}

func TestConfig_torznabAndProwlarrKeysAreNeverRendered(t *testing.T) {
	loadedConfig, err := loadTorznabConfig(t, `{"torznab": [
		"https://prowlarr.example.com:9696/12/api?apikey=${INDEXER_KEY}",
		{"url": "https://prowlarr.example.com:9696/7/api?apikey=secret-a%26b%22c", "name": "tracker-a"}
	], "prowlarr": "https://prowlarr.example.com:9696?apikey=${PROWLARR_KEY}"}`,
		map[string]string{"INDEXER_KEY": fakeIndexerKey, "PROWLARR_KEY": fakeProwlarrKey})
	if err != nil {
		t.Fatal(err)
	}
	redactedJSON, err := loadedConfig.RedactedJSON()
	if err != nil {
		t.Fatal(err)
	}
	directJSON, err := json.Marshal(loadedConfig)
	if err != nil {
		t.Fatal(err)
	}
	elementJSON, err := json.Marshal(loadedConfig.Torznab)
	if err != nil {
		t.Fatal(err)
	}
	var logBuffer bytes.Buffer
	slog.New(slog.NewJSONHandler(&logBuffer, nil)).Info("config", "config", loadedConfig, "indexer", loadedConfig.Torznab[0])
	slog.New(slog.NewTextHandler(&logBuffer, nil)).Info("config", "indexer", loadedConfig.Torznab[1])
	renderings := []string{string(redactedJSON), string(directJSON), string(elementJSON), logBuffer.String(), loadedConfig.String()}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		renderings = append(renderings, fmt.Sprintf(verb, loadedConfig), fmt.Sprintf(verb, loadedConfig.Torznab[0]),
			fmt.Sprintf(verb, loadedConfig.Torznab), fmt.Sprintf(verb+"|%"+verb[1:], &loadedConfig.Torznab[1], loadedConfig.Torznab[1]))
	}

	for _, rendering := range renderings {
		assertNoSecret(t, rendering)
		if strings.Contains(rendering, "a%26b") || strings.Contains(rendering, `a&b"c`) || strings.Contains(rendering, `a&b`) {
			t.Errorf("leaked an escaped key: %s", rendering)
		}
	}
	if !strings.Contains(string(redactedJSON), "tracker-a") || !strings.Contains(string(redactedJSON), "prowlarr.example.com") {
		t.Fatalf("gotValue %s, expectedValue names and hosts kept", redactedJSON)
	}
}

func TestConfig_torznabAcceptsTheLimitBoundsAndNamesTheFloorInTheError(t *testing.T) {
	loadedConfig, err := loadTorznabConfig(t, `{"torznab": [{"url": "https://a.example.com/1/api?apikey=k", "minInterval": "1s", "timeout": "5m",
		"maxQueriesPerRun": 100000, "maxQueriesPerDay": 1}]}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if loadedConfig.Torznab[0].MinInterval != time.Second || loadedConfig.Torznab[0].Timeout != 5*time.Minute {
		t.Fatalf("gotValue %v", loadedConfig.Torznab[0])
	}

	_, err = loadTorznabConfig(t, `{"torznab": [{"url": "https://a.example.com/1/api?apikey=k", "minInterval": "500ms"}]}`, nil)

	if err == nil || !strings.Contains(err.Error(), "minInterval") || !strings.Contains(err.Error(), "1s") {
		t.Fatalf("gotValue %v, expectedValue an error naming minInterval and its 1s floor", err)
	}
}

func TestConfig_torznabTrackerKeepsTheOperatorSpellingTrimmed(t *testing.T) {
	loadedConfig, err := loadTorznabConfig(t, `{"torznab": [{"url": "https://a.example.com/1/api?apikey=k", "tracker": "  Tracker-A "}]}`, nil)
	if err != nil {
		t.Fatal(err)
	}

	gotValue := loadedConfig.Torznab[0].Tracker

	if gotValue != "Tracker-A" || !strings.EqualFold(gotValue, "tracker-a") {
		t.Fatalf("gotValue %q, expectedValue the trimmed spelling, compared case-insensitively by later lookups", gotValue)
	}
}
