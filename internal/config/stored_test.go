package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"necromanciarr/internal/store"
)

// fakeStoredReader tient en mémoire les réglages et les surcharges de liste d'une base.
type fakeStoredReader struct {
	settings  map[string][]byte
	overrides []store.ConfigOverride
}

func (fake fakeStoredReader) Settings(context.Context) (map[string][]byte, error) {
	return fake.settings, nil
}

func (fake fakeStoredReader) ConfigOverrides(context.Context) ([]store.ConfigOverride, error) {
	return fake.overrides, nil
}

func openFake(reader fakeStoredReader) func(string) (StoredReader, io.Closer, error) {
	return func(string) (StoredReader, io.Closer, error) { return reader, nil, nil }
}

// storedListNames sont les clés d'un document de test lues comme des listes à éléments ; les autres sont des réglages.
var storedListNames = []string{"radarr", "sonarr", "torrentClients", "linkDirs", "trackers", "torznab", "fsProviders"}

// storedFromJSON range un document à la forme de l'ancien config.json dans une base factice : chaque entrée de liste
// devient un ajout (chaîne brute décomposée par ElementFromRaw, URL Torznab seule, section trackers éclatée par clé,
// annonce seule mise en tableau, indexeur sans name nommé indexer-<rang>). Les ajouts sont relus dans l'ordre de leur item_id, comme ceux de la base.
func storedFromJSON(t *testing.T, content string) fakeStoredReader {
	t.Helper()
	var document map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &document); err != nil {
		t.Fatal(err)
	}
	reader := fakeStoredReader{settings: map[string][]byte{}}
	for key, rawValue := range document {
		if !slices.Contains(storedListNames, key) {
			reader.settings[key] = rawValue
			continue
		}
		reader.overrides = append(reader.overrides, addedOverrides(t, key, testElements(t, key, rawValue))...)
	}
	return reader
}

func testElements(t *testing.T, list string, rawValue json.RawMessage) []Element {
	t.Helper()
	if list == listTrackers {
		return trackerElements(t, rawValue)
	}
	var rawEntries []json.RawMessage
	if err := json.Unmarshal(rawValue, &rawEntries); err != nil {
		t.Fatal(err)
	}
	elements := make([]Element, 0, len(rawEntries))
	for position, rawEntry := range rawEntries {
		elements = append(elements, testElement(t, list, rawEntry))
		nameFixtureIndexer(list, elements[position], position)
	}
	return elements
}

// nameFixtureIndexer nomme un indexeur de fixture qui n'a pas de name : la base l'exige (testé à part), les tests du
// seul analyseur n'ont pas à le répéter.
func nameFixtureIndexer(list string, element Element, position int) {
	if _, hasName := element["name"]; list == listTorznab && !hasName {
		element["name"] = fmt.Sprintf("indexer-%d", position)
	}
}

func testElement(t *testing.T, list string, rawEntry json.RawMessage) Element {
	t.Helper()
	var text string
	if json.Unmarshal(rawEntry, &text) != nil {
		var element Element
		if err := json.Unmarshal(rawEntry, &element); err != nil {
			t.Fatal(err)
		}
		return element
	}
	if list == listTorznab {
		return Element{fieldURL: text}
	}
	element, err := ElementFromRaw(list, text)
	if err != nil {
		t.Fatal(err)
	}
	return element
}

func trackerElements(t *testing.T, rawSection json.RawMessage) []Element {
	t.Helper()
	var section map[string]any
	if err := json.Unmarshal(rawSection, &section); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(section))
	for key := range section {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	elements := make([]Element, 0, len(keys))
	for _, key := range keys {
		element := Element{fieldKey: key}
		switch entry := section[key].(type) {
		case map[string]any:
			for field, value := range entry {
				element[field] = value
			}
		case string:
			element["announce"] = []any{entry}
		default:
			element["announce"] = entry
		}
		elements = append(elements, element)
	}
	return elements
}

func addedOverrides(t *testing.T, list string, elements []Element) []store.ConfigOverride {
	t.Helper()
	overrides := make([]store.ConfigOverride, 0, len(elements))
	for position, itemID := range AssignIDs(list, elements) {
		overrides = append(overrides, store.ConfigOverride{List: list, ItemID: itemID, Value: mustMarshal(t, elements[position])})
	}
	return overrides
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func lookupFrom(variables map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		variableValue, found := variables[name]
		return variableValue, found
	}
}

func loadStored(t *testing.T, args []string, environment map[string]string, content string) (Config, error) {
	t.Helper()
	return LoadWith(args, lookupFrom(environment), t.TempDir(), openFake(storedFromJSON(t, content)))
}

func mustLoadStored(t *testing.T, args []string, environment map[string]string, content string) Config {
	t.Helper()
	loadedConfig, err := loadStored(t, args, environment, content)
	if err != nil {
		t.Fatal(err)
	}
	return loadedConfig
}

func crossSeedEnvironment(t *testing.T, content string) map[string]string {
	t.Helper()
	return map[string]string{envCrossSeedConfig: writeCrossSeedJS(t, content)}
}

const twoImportedRadarr = `module.exports = { radarr: [
	"http://first.example.com:7878?apikey=first-key",
	"http://second.example.com:7878?apikey=second-key",
] };`

func originOf(loadedConfig Config, key, itemID string) ValueOrigin {
	for _, origin := range loadedConfig.Origins {
		if origin.Key == key && origin.ItemID == itemID {
			return origin
		}
	}
	return ValueOrigin{}
}

// elementOrigins donne, dans l'ordre, l'origine de chaque élément effectif d'une liste.
func elementOrigins(loadedConfig Config, list string) []string {
	var origins []string
	for _, origin := range loadedConfig.Origins {
		if origin.Key == list && origin.ItemID != "" {
			origins = append(origins, origin.Origin)
		}
	}
	return origins
}

func TestLoadWith_emptyDatabaseUsesCrossSeedAndDefaults(t *testing.T) {
	environment := crossSeedEnvironment(t, `module.exports = { linkCategory: "imported-category" };`)
	valeurAttendue := []string{"imported-category", defaultListenAddr, defaultLinkType}

	loadedConfig, err := LoadWith(nil, lookupFrom(environment), t.TempDir(), openFake(fakeStoredReader{}))
	if err != nil {
		t.Fatal(err)
	}

	valeurObtenue := []string{loadedConfig.LinkCategory, loadedConfig.ListenAddr, loadedConfig.LinkType}
	if !slices.Equal(valeurObtenue, valeurAttendue) || len(loadedConfig.OrphanOverrides) != 0 {
		t.Fatalf("valeurObtenue %v (orphans %v), valeurAttendue %v", valeurObtenue, loadedConfig.OrphanOverrides, valeurAttendue)
	}
}

func TestLoadWith_withoutDatabaseOpenerUsesDefaults(t *testing.T) {
	valeurAttendue := defaultListenAddr

	loadedConfig, err := LoadWith(nil, lookupFrom(nil), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}

	if valeurObtenue := loadedConfig.ListenAddr; valeurObtenue != valeurAttendue {
		t.Fatalf("valeurObtenue %q, valeurAttendue %q", valeurObtenue, valeurAttendue)
	}
}

func TestLoadWith_opensTheDatabaseOfTheConfigDirectory(t *testing.T) {
	configDir := t.TempDir()
	valeurAttendue := filepath.Join(configDir, defaultDBPath)
	var valeurObtenue string
	opener := func(dbPath string) (StoredReader, io.Closer, error) {
		valeurObtenue = dbPath
		return fakeStoredReader{}, nil, nil
	}

	if _, err := LoadWith(nil, lookupFrom(nil), configDir, opener); err != nil {
		t.Fatal(err)
	}

	if valeurObtenue != valeurAttendue {
		t.Fatalf("valeurObtenue %q, valeurAttendue %q", valeurObtenue, valeurAttendue)
	}
}

func TestLoadWith_overrideReplacesOneRadarrInstanceAndKeepsTheOther(t *testing.T) {
	environment := crossSeedEnvironment(t, twoImportedRadarr)
	replacement := Element{"url": "http://replaced.example.com:7878", "apiKey": "replaced-key"}
	reader := fakeStoredReader{overrides: []store.ConfigOverride{
		{List: "radarr", ItemID: "http://second.example.com:7878", Value: mustMarshal(t, replacement)},
	}}
	valeurAttendue := []string{"http://first.example.com:7878", "http://replaced.example.com:7878"}

	loadedConfig, err := LoadWith(nil, lookupFrom(environment), t.TempDir(), openFake(reader))
	if err != nil {
		t.Fatal(err)
	}

	var valeurObtenue []string
	for _, service := range loadedConfig.Radarr {
		valeurObtenue = append(valeurObtenue, service.BaseURL)
	}
	if !slices.Equal(valeurObtenue, valeurAttendue) || loadedConfig.Radarr[1].APIKey != "replaced-key" {
		t.Fatalf("valeurObtenue %v, valeurAttendue %v with the replaced key", valeurObtenue, valeurAttendue)
	}
}

func TestLoadWith_deletedOverrideRemovesTheUpstreamElement(t *testing.T) {
	environment := crossSeedEnvironment(t, twoImportedRadarr)
	reader := fakeStoredReader{overrides: []store.ConfigOverride{
		{List: "radarr", ItemID: "http://first.example.com:7878", Deleted: true},
	}}
	valeurAttendue := "http://second.example.com:7878"

	loadedConfig, err := LoadWith(nil, lookupFrom(environment), t.TempDir(), openFake(reader))
	if err != nil {
		t.Fatal(err)
	}

	if len(loadedConfig.Radarr) != 1 || loadedConfig.Radarr[0].BaseURL != valeurAttendue {
		t.Fatalf("valeurObtenue %v, valeurAttendue only %s", loadedConfig.Radarr, valeurAttendue)
	}
}

func TestLoadWith_addedTrackerComesFromTheDatabaseOnly(t *testing.T) {
	added := Element{"key": "tracker-a", "announce": []string{"https://tracker-a.example.com/announce/" + fakePasskeyA}}
	reader := fakeStoredReader{overrides: []store.ConfigOverride{{List: "trackers", ItemID: "tracker-a", Value: mustMarshal(t, added)}}}
	valeurAttendue := ValueOrigin{Key: "trackers", ItemID: "tracker-a", Origin: "added"}

	loadedConfig, err := LoadWith(nil, lookupFrom(nil), t.TempDir(), openFake(reader))
	if err != nil {
		t.Fatal(err)
	}

	valeurObtenue := originOf(loadedConfig, "trackers", "tracker-a")
	if len(loadedConfig.Trackers) != 1 || loadedConfig.Trackers[0].Key != "tracker-a" || valeurObtenue != valeurAttendue {
		t.Fatalf("valeurObtenue %v (origin %+v), valeurAttendue one tracker with origin %+v", loadedConfig.Trackers, valeurObtenue, valeurAttendue)
	}
}

func TestLoadWith_environmentBeatsStoredSetting(t *testing.T) {
	environment := map[string]string{envListen: "127.0.0.1:2222"}
	valeurAttendue := ValueOrigin{Key: "listen", Origin: "env"}

	loadedConfig := mustLoadStored(t, nil, environment, `{"listen": "127.0.0.1:1111"}`)

	valeurObtenue := originOf(loadedConfig, "listen", "")
	if loadedConfig.ListenAddr != "127.0.0.1:2222" || valeurObtenue != valeurAttendue {
		t.Fatalf("valeurObtenue %q %+v, valeurAttendue 127.0.0.1:2222 %+v", loadedConfig.ListenAddr, valeurObtenue, valeurAttendue)
	}
}

func sealedRadarrReader(t *testing.T, key []byte) fakeStoredReader {
	t.Helper()
	sealer, err := newFieldSealer(key)
	if err != nil {
		t.Fatal(err)
	}
	element := map[string]any{"url": "http://sealed.example.com:7878", "apiKey": "sealed-radarr-key"}
	if err := sealer.SealElement("radarr", "http://sealed.example.com:7878", element); err != nil {
		t.Fatal(err)
	}
	return fakeStoredReader{overrides: []store.ConfigOverride{
		{List: "radarr", ItemID: "http://sealed.example.com:7878", Value: mustMarshal(t, element)},
	}}
}

func TestLoadWith_sealedSecretIsOpenedWithTheKey(t *testing.T) {
	environment := map[string]string{envSecretKey: testSecretKey}
	valeurAttendue := "sealed-radarr-key"

	loadedConfig, err := LoadWith(nil, lookupFrom(environment), t.TempDir(), openFake(sealedRadarrReader(t, testSecretKeyBytes)))
	if err != nil {
		t.Fatal(err)
	}

	if len(loadedConfig.Radarr) != 1 || loadedConfig.Radarr[0].APIKey != valeurAttendue {
		t.Fatalf("valeurObtenue %v, valeurAttendue the opened key", loadedConfig.Radarr)
	}
}

func TestLoadWith_sealedSecretWithoutKeyFails(t *testing.T) {
	_, err := LoadWith(nil, lookupFrom(nil), t.TempDir(), openFake(sealedRadarrReader(t, testSecretKeyBytes)))

	if !errors.Is(err, ErrSealedSecretUnreadable) || !strings.Contains(err.Error(), envSecretKey) {
		t.Fatalf("valeurObtenue %v, valeurAttendue ErrSealedSecretUnreadable", err)
	}
}

func TestLoadWith_sealedSecretWithWrongKeyFails(t *testing.T) {
	wrongKey := bytes.Repeat([]byte{0x02}, SecretKeySize)

	_, err := LoadWith(nil, lookupFrom(map[string]string{envSecretKey: testSecretKey}), t.TempDir(), openFake(sealedRadarrReader(t, wrongKey)))

	if !errors.Is(err, ErrSealedSecretUnreadable) {
		t.Fatalf("valeurObtenue %v, valeurAttendue ErrSealedSecretUnreadable", err)
	}
}

func TestLoadWith_sealedValueOutsideASecretFieldFails(t *testing.T) {
	element := Element{"path": sealedPrefix + "AAAA"}
	reader := fakeStoredReader{overrides: []store.ConfigOverride{{List: "linkDirs", ItemID: "x", Value: mustMarshal(t, element)}}}

	_, err := LoadWith(nil, lookupFrom(nil), t.TempDir(), openFake(reader))

	if !errors.Is(err, ErrSealedSecretUnreadable) {
		t.Fatalf("valeurObtenue %v, valeurAttendue ErrSealedSecretUnreadable", err)
	}
}

func TestLoadWith_sealedProwlarrSettingIsOpenedWithTheKey(t *testing.T) {
	sealer, err := newFieldSealer(testSecretKeyBytes)
	if err != nil {
		t.Fatal(err)
	}
	valeurAttendue := "https://prowlarr.example.com?apikey=" + fakeProwlarrKey
	sealed, err := sealer.SealSetting("prowlarr", valeurAttendue)
	if err != nil {
		t.Fatal(err)
	}
	reader := fakeStoredReader{settings: map[string][]byte{"prowlarr": mustMarshal(t, sealed)}}

	loadedConfig, err := LoadWith(nil, lookupFrom(map[string]string{envSecretKey: testSecretKey}), t.TempDir(), openFake(reader))
	_, withoutKeyErr := LoadWith(nil, lookupFrom(nil), t.TempDir(), openFake(reader))

	if err != nil || loadedConfig.Prowlarr != valeurAttendue || !errors.Is(withoutKeyErr, ErrSealedSecretUnreadable) {
		t.Fatalf("valeurObtenue %v (without key %v), valeurAttendue the opened url", err, withoutKeyErr)
	}
}

func TestLoadWith_unknownSettingKeyNamesTheKey(t *testing.T) {
	_, err := loadStored(t, nil, nil, `{"listn": "127.0.0.1:1"}`)

	if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), "listn") {
		t.Fatalf("valeurObtenue %v, valeurAttendue a usage error naming listn", err)
	}
}

func TestLoadWith_invalidSettingValueNamesTheKey(t *testing.T) {
	for _, content := range []string{`{"listen": 12}`, `{"dedupe": {"kindz": []}}`} {
		_, err := loadStored(t, nil, nil, content)

		if !errors.Is(err, ErrUsage) || (!strings.Contains(err.Error(), "listen") && !strings.Contains(err.Error(), "dedupe")) {
			t.Fatalf("%s: valeurObtenue %v, valeurAttendue a usage error naming the key", content, err)
		}
	}
}

func TestLoadWith_orphanOverrideIsReportedAndLoadSucceeds(t *testing.T) {
	environment := crossSeedEnvironment(t, twoImportedRadarr)
	replacement := Element{"url": "http://gone.example.com:7878", "apiKey": "gone-key"}
	reader := fakeStoredReader{overrides: []store.ConfigOverride{
		{List: "radarr", ItemID: "http://gone.example.com:7878", Value: mustMarshal(t, replacement), UpstreamFingerprint: "f"},
		{List: "radarr", ItemID: "http://first.example.com:7878", Value: []byte("{not json")},
		{List: "unknownList", ItemID: "x", Value: []byte("{}")},
	}}
	valeurAttendue := []OrphanOverride{
		{List: "radarr", ItemID: "http://first.example.com:7878"},
		{List: "radarr", ItemID: "http://gone.example.com:7878"},
		{List: "unknownList", ItemID: "x"},
	}

	loadedConfig, err := LoadWith(nil, lookupFrom(environment), t.TempDir(), openFake(reader))
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(loadedConfig.OrphanOverrides, valeurAttendue) || len(loadedConfig.Radarr) != 2 {
		t.Fatalf("valeurObtenue %v (radarr %v), valeurAttendue %v and both upstream radarr", loadedConfig.OrphanOverrides, loadedConfig.Radarr, valeurAttendue)
	}
}

func TestLoadWith_originsListEachValueSource(t *testing.T) {
	environment := crossSeedEnvironment(t, twoImportedRadarr+`module.exports.linkCategory = "imported";`)
	environment[envSeedTags] = "from-env"
	replacement := Element{"url": "http://second.example.com:7878", "apiKey": "other-key"}
	reader := fakeStoredReader{
		settings: map[string][]byte{"libraryApi": []byte(`"https://library.example.com"`)},
		overrides: []store.ConfigOverride{
			{List: "radarr", ItemID: "http://second.example.com:7878", Value: mustMarshal(t, replacement), UpstreamFingerprint: "stale"},
			{List: "linkDirs", ItemID: "/home/u/links", Value: mustMarshal(t, Element{"path": absPath("home", "u", "links")})},
		},
	}
	valeurAttendue := []ValueOrigin{
		{Key: "listen", Origin: "flag"},
		{Key: "libraryApi", Origin: "override"},
		{Key: "linkCategory", Origin: "crossseed"},
		{Key: "seedTags", Origin: "env"},
		{Key: "language", Origin: "default"},
		{Key: "radarr", ItemID: "http://first.example.com:7878", Origin: "crossseed"},
		{Key: "radarr", ItemID: "http://second.example.com:7878", Origin: "override", Drift: true},
		{Key: "linkDirs", ItemID: "/home/u/links", Origin: "added"},
	}

	loadedConfig, err := LoadWith([]string{"--listen", "127.0.0.1:3333"}, lookupFrom(environment), t.TempDir(), openFake(reader))
	if err != nil {
		t.Fatal(err)
	}

	for _, expected := range valeurAttendue {
		if valeurObtenue := originOf(loadedConfig, expected.Key, expected.ItemID); valeurObtenue != expected {
			t.Errorf("valeurObtenue %+v, valeurAttendue %+v", valeurObtenue, expected)
		}
	}
}

func TestLoadWith_crossSeedConfigPathOnlyFromFlagOrEnvironment(t *testing.T) {
	storedPath := writeCrossSeedJS(t, `module.exports = { linkCategory: "from-stored-path" };`)
	flagPath := writeCrossSeedJS(t, `module.exports = { linkCategory: "from-flag-path" };`)
	reader := fakeStoredReader{settings: map[string][]byte{"crossSeedConfig": mustMarshal(t, storedPath)}}

	_, storedErr := LoadWith(nil, lookupFrom(nil), t.TempDir(), openFake(reader))
	loadedConfig, err := LoadWith([]string{"--cross-seed-config", flagPath}, lookupFrom(nil), t.TempDir(), openFake(fakeStoredReader{}))

	if !errors.Is(storedErr, ErrUsage) || !strings.Contains(storedErr.Error(), "crossSeedConfig") {
		t.Fatalf("valeurObtenue %v, valeurAttendue crossSeedConfig refused as a stored setting", storedErr)
	}
	if err != nil || loadedConfig.LinkCategory != "from-flag-path" {
		t.Fatalf("valeurObtenue %q (%v), valeurAttendue from-flag-path", loadedConfig.LinkCategory, err)
	}
}

func sealedSettingsReader(t *testing.T, plainByKey map[string]string) fakeStoredReader {
	t.Helper()
	sealer, err := newFieldSealer(testSecretKeyBytes)
	if err != nil {
		t.Fatal(err)
	}
	reader := fakeStoredReader{settings: map[string][]byte{}}
	for key, plain := range plainByKey {
		sealed, err := sealer.SealSetting(key, plain)
		if err != nil {
			t.Fatal(err)
		}
		reader.settings[key] = mustMarshal(t, sealed)
	}
	return reader
}

const (
	sealedCrossSeedURL  = "http://cross-seed.example.com:2468?apikey=secret-daemon-key"
	sealedLibraryAPIURL = "https://alice:secret-library@example.com/api?token=secret-token"
)

func TestLoadWith_sealedCrossSeedAndLibraryAPIAreOpenedWithTheKey(t *testing.T) {
	reader := sealedSettingsReader(t, map[string]string{"crossSeed": sealedCrossSeedURL, "libraryApi": sealedLibraryAPIURL})
	valeurAttendue := []string{"http://cross-seed.example.com:2468", "secret-daemon-key", sealedLibraryAPIURL}

	loadedConfig, err := LoadWith(nil, lookupFrom(map[string]string{envSecretKey: testSecretKey}), t.TempDir(), openFake(reader))
	if err != nil {
		t.Fatal(err)
	}

	if loadedConfig.CrossSeed == nil {
		t.Fatal("valeurObtenue no cross-seed daemon, valeurAttendue the opened url")
	}
	valeurObtenue := []string{loadedConfig.CrossSeed.BaseURL, loadedConfig.CrossSeed.APIKey, loadedConfig.LibraryAPI}
	if !slices.Equal(valeurObtenue, valeurAttendue) {
		t.Fatalf("valeurObtenue %v, valeurAttendue %v", valeurObtenue, valeurAttendue)
	}
	for _, rendering := range renderingsOf(t, loadedConfig) {
		assertNoSecret(t, rendering)
	}
}

func TestLoadWith_sealedCrossSeedOrLibraryAPIWithoutKeyFailsWithoutTheURL(t *testing.T) {
	for key, plain := range map[string]string{"crossSeed": sealedCrossSeedURL, "libraryApi": sealedLibraryAPIURL} {
		reader := sealedSettingsReader(t, map[string]string{key: plain})

		_, err := LoadWith(nil, lookupFrom(nil), t.TempDir(), openFake(reader))

		if !errors.Is(err, ErrSealedSecretUnreadable) || strings.Contains(err.Error(), "example.com") {
			t.Fatalf("%s: valeurObtenue %v, valeurAttendue ErrSealedSecretUnreadable without the url", key, err)
		}
		assertNoSecret(t, err.Error())
	}
}

func TestLoadWith_clearCrossSeedSettingIsStillAccepted(t *testing.T) {
	valeurAttendue := "secret-daemon-key"

	loadedConfig := mustLoadStored(t, nil, nil, `{"crossSeed": "`+sealedCrossSeedURL+`"}`)

	if loadedConfig.CrossSeed == nil || loadedConfig.CrossSeed.APIKey != valeurAttendue {
		t.Fatalf("valeurObtenue %+v, valeurAttendue the daemon read from the clear value", loadedConfig.CrossSeed)
	}
}

func TestLoadWith_storedTorznabWithoutNameIsUsageErrorNamingTheItem(t *testing.T) {
	element := Element{"url": "https://prowlarr.example.com/12/api?apikey=" + fakeIndexerKey}
	reader := fakeStoredReader{overrides: []store.ConfigOverride{
		{List: "torznab", ItemID: "https://prowlarr.example.com", Value: mustMarshal(t, element)},
	}}

	_, err := LoadWith(nil, lookupFrom(map[string]string{envSecretKey: testSecretKey}), t.TempDir(), openFake(reader))

	if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), "torznab") ||
		!strings.Contains(err.Error(), "https://prowlarr.example.com") || !strings.Contains(err.Error(), "name") {
		t.Fatalf("valeurObtenue %v, valeurAttendue a usage error naming torznab, the item and name", err)
	}
	assertNoSecret(t, err.Error())
}

func TestLoadWith_invalidStoredContentIsAnInvalidStoredConfigUsageError(t *testing.T) {
	testCases := map[string]fakeStoredReader{
		"unknown setting key":   {settings: map[string][]byte{"bogus": []byte("1")}},
		"unreadable value":      {settings: map[string][]byte{"linkCategory": []byte("12")}},
		"sealed secret, no key": sealedRadarrReader(t, testSecretKeyBytes),
	}
	for name, reader := range testCases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadWith(nil, lookupFrom(nil), t.TempDir(), openFake(reader))

			estInvalideEnBase, estUsage := errors.Is(err, ErrInvalidStoredConfig), errors.Is(err, ErrUsage)
			if !estInvalideEnBase || !estUsage {
				t.Fatalf("valeurObtenue %v (stockée %v, usage %v), valeurAttendue les deux", err, estInvalideEnBase, estUsage)
			}
		})
	}
}

func TestLoadWith_commandLineUsageErrorIsNotAnInvalidStoredConfig(t *testing.T) {
	_, err := LoadWith([]string{"--unknown-flag"}, lookupFrom(nil), t.TempDir(), openFake(fakeStoredReader{}))

	if !errors.Is(err, ErrUsage) || errors.Is(err, ErrInvalidStoredConfig) {
		t.Fatalf("valeurObtenue %v, valeurAttendue ErrUsage seul", err)
	}
}
