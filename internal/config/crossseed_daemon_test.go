package config

import (
	"errors"
	"strings"
	"testing"
)

const importedDaemonScript = `module.exports = { host: "localhost", port: 2468, apiKey: "imported-key" };`

func TestCrossSeedDaemon_resolutionKeepsEachImportedPartIndependent(t *testing.T) {
	crossSeedPath := writeCrossSeedJS(t, importedDaemonScript)
	cases := []struct {
		name                    string
		storedJSON              string
		valeurAttendueURL       string
		valeurAttendueURLOrigin string
	}{
		{"nothing typed", `{}`, "http://localhost:2468", originCrossSeed},
		{"url only keeps the imported key", `{"crossSeedUrl": "http://cross-seed:2468"}`, "http://cross-seed:2468", originOverride},
		{"url with a base path", `{"crossSeedUrl": "https://cs.example.com/base"}`, "https://cs.example.com/base", originOverride},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			loadedConfig := mustLoadWithCrossSeed(t, crossSeedPath, testCase.storedJSON)

			valeurObtenue := loadedConfig.CrossSeed
			if valeurObtenue == nil || valeurObtenue.BaseURL != testCase.valeurAttendueURL || valeurObtenue.APIKey != "imported-key" {
				t.Fatalf("valeurObtenue %+v, valeurAttendue %s with the imported key", valeurObtenue, testCase.valeurAttendueURL)
			}
			if got := originOf(loadedConfig, settingCrossSeedURL, "").Origin; got != testCase.valeurAttendueURLOrigin {
				t.Fatalf("crossSeedUrl valeurObtenue %q, valeurAttendue %q", got, testCase.valeurAttendueURLOrigin)
			}
			if got := originOf(loadedConfig, settingCrossSeedAPIKey, "").Origin; got != originCrossSeed {
				t.Fatalf("crossSeedApiKey valeurObtenue %q, valeurAttendue %q", got, originCrossSeed)
			}
		})
	}
}

func TestCrossSeedDaemon_keyOnlyKeepsImportedHostAndPort(t *testing.T) {
	crossSeedPath := writeCrossSeedJS(t, `module.exports = { host: "0.0.0.0", port: 2470, apiKey: "imported-key" };`)
	reader := sealedSettingsReader(t, map[string]string{"crossSeedApiKey": "typed-key"})
	environment := map[string]string{envCrossSeedConfig: crossSeedPath, envSecretKey: testSecretKey}

	loadedConfig, err := LoadWith(nil, lookupFrom(environment), t.TempDir(), openFake(reader))
	if err != nil {
		t.Fatal(err)
	}

	valeurObtenue := loadedConfig.CrossSeed
	if valeurObtenue == nil || valeurObtenue.BaseURL != "http://localhost:2470" || valeurObtenue.APIKey != "typed-key" {
		t.Fatalf("valeurObtenue %+v, valeurAttendue http://localhost:2470 with the typed key", valeurObtenue)
	}
	if got := originOf(loadedConfig, settingCrossSeedAPIKey, "").Origin; got != originOverride {
		t.Fatalf("valeurObtenue %q, valeurAttendue %q", got, originOverride)
	}
	if got := originOf(loadedConfig, settingCrossSeedURL, "").Origin; got != originCrossSeed {
		t.Fatalf("valeurObtenue %q, valeurAttendue %q", got, originCrossSeed)
	}
}

func TestCrossSeedDaemon_bothTypedOverrideTheImport(t *testing.T) {
	crossSeedPath := writeCrossSeedJS(t, importedDaemonScript)
	reader := sealedSettingsReader(t, map[string]string{"crossSeedApiKey": "typed-key"})
	reader.settings["crossSeedUrl"] = mustMarshal(t, "http://cross-seed:2468")
	environment := map[string]string{envCrossSeedConfig: crossSeedPath, envSecretKey: testSecretKey}

	loadedConfig, err := LoadWith(nil, lookupFrom(environment), t.TempDir(), openFake(reader))
	if err != nil {
		t.Fatal(err)
	}

	valeurObtenue := loadedConfig.CrossSeed
	if valeurObtenue == nil || valeurObtenue.BaseURL != "http://cross-seed:2468" || valeurObtenue.APIKey != "typed-key" {
		t.Fatalf("valeurObtenue %+v, valeurAttendue the typed url and key", valeurObtenue)
	}
}

func TestCrossSeedDaemon_urlAloneWithoutImportHasNoKey(t *testing.T) {
	loadedConfig := mustLoadStored(t, nil, nil, `{"crossSeedUrl": "http://cross-seed:2468"}`)

	valeurObtenue := loadedConfig.CrossSeed
	if valeurObtenue == nil || valeurObtenue.BaseURL != "http://cross-seed:2468" || valeurObtenue.APIKey != "" {
		t.Fatalf("valeurObtenue %+v, valeurAttendue the url without key", valeurObtenue)
	}
}

func TestCrossSeedDaemon_nothingKnownMeansNoDaemon(t *testing.T) {
	loadedConfig := mustLoadStored(t, nil, nil, `{}`)

	if loadedConfig.CrossSeed != nil {
		t.Fatalf("valeurObtenue %+v, valeurAttendue nil", loadedConfig.CrossSeed)
	}
	for _, key := range []string{settingCrossSeedURL, settingCrossSeedAPIKey} {
		if got := originOf(loadedConfig, key, "").Origin; got != originDefault {
			t.Fatalf("%s valeurObtenue %q, valeurAttendue %q", key, got, originDefault)
		}
	}
}

func TestCrossSeedDaemon_keyWithoutAnyUrlIsRefusedAtLoad(t *testing.T) {
	reader := sealedSettingsReader(t, map[string]string{"crossSeedApiKey": "secret-daemon-key"})

	_, err := LoadWith(nil, lookupFrom(map[string]string{envSecretKey: testSecretKey}), t.TempDir(), openFake(reader))

	if err == nil || !strings.Contains(err.Error(), "crossSeedApiKey") || !strings.Contains(err.Error(), "crossSeedUrl") {
		t.Fatalf("valeurObtenue %v, valeurAttendue a message naming crossSeedApiKey and crossSeedUrl", err)
	}
	assertNoSecret(t, err.Error())
}

func TestCrossSeedDaemon_urlWithApikeyQueryIsRefusedWithAPointerToTheKeySetting(t *testing.T) {
	_, err := loadStored(t, nil, nil, `{"crossSeedUrl": "http://cross-seed:2468?apikey=secret-key"}`)

	if err == nil || !strings.Contains(err.Error(), "crossSeedApiKey") {
		t.Fatalf("valeurObtenue %v, valeurAttendue a message naming crossSeedApiKey", err)
	}
	assertNoSecret(t, err.Error())
}

func TestCrossSeedDaemon_urlRefusesUserinfoQueryFragmentAndOtherSchemes(t *testing.T) {
	for _, rawURL := range []string{
		"http://alice:secret-pass@cross-seed:2468", "http://cross-seed:2468?x=1", "http://cross-seed:2468#frag", "ftp://cross-seed",
	} {
		_, err := loadStored(t, nil, nil, `{"crossSeedUrl": "`+rawURL+`"}`)

		if err == nil {
			t.Fatalf("%s: valeurObtenue nil, valeurAttendue a refusal", rawURL)
		}
		assertNoSecret(t, err.Error())
	}
}

func TestCrossSeedDaemon_oldStoredCrossSeedRowIsRefusedNamingTheEarlierFormat(t *testing.T) {
	_, err := loadStored(t, nil, nil, `{"crossSeed": "http://cross-seed:2468?apikey=secret-key"}`)

	if !errors.Is(err, ErrInvalidStoredConfig) || !strings.Contains(err.Error(), "config unset crossSeed") ||
		!strings.Contains(err.Error(), "crossSeedUrl") || !strings.Contains(err.Error(), "crossSeedApiKey") {
		t.Fatalf("valeurObtenue %v, valeurAttendue the earlier-format refusal naming unset and the two settings", err)
	}
	assertNoSecret(t, err.Error())
}

func TestCrossSeedDaemon_apiKeyIsSealedAndNeverRendered(t *testing.T) {
	reader := sealedSettingsReader(t, map[string]string{"crossSeedApiKey": "secret-daemon-key"})
	reader.settings["crossSeedUrl"] = mustMarshal(t, "http://cross-seed:2468")

	loadedConfig, err := LoadWith(nil, lookupFrom(map[string]string{envSecretKey: testSecretKey}), t.TempDir(), openFake(reader))
	if err != nil {
		t.Fatal(err)
	}

	if !IsSecretSetting(settingCrossSeedAPIKey) || IsSecretSetting(settingCrossSeedURL) {
		t.Fatal("valeurAttendue crossSeedApiKey sealed and crossSeedUrl clear")
	}
	if loadedConfig.CrossSeed == nil || loadedConfig.CrossSeed.APIKey != "secret-daemon-key" {
		t.Fatalf("valeurObtenue %+v, valeurAttendue the opened key", loadedConfig.CrossSeed)
	}
	for _, rendering := range renderingsOf(t, loadedConfig) {
		assertNoSecret(t, rendering)
	}
}
