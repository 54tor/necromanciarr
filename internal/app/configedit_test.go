package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"necromanciarr/internal/config"
	"necromanciarr/internal/store"
)

const (
	configEditSecretKey   = "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE"
	configEditRadarrURL   = "http://radarr.example.com:7878"
	configEditRadarrKey   = "fake-radarr-key-0000"
	configEditOtherKey    = "fake-radarr-key-1111"
	configEditDatabaseRel = "necromanciarr.db"
)

// configEditFixture est un Service d'édition sur une vraie base et le vrai chargement, sans réseau.
type configEditFixture struct {
	service Service
	store   *store.Store
	load    StoredConfigLoader
}

func withSecretKey(environment map[string]string) map[string]string {
	environment["NECROMANCIARR_SECRET_KEY"] = configEditSecretKey
	return environment
}

func newConfigEditFixture(t *testing.T, environment map[string]string, crossSeedJS string) configEditFixture {
	t.Helper()
	configDir := t.TempDir()
	if crossSeedJS != "" {
		crossSeedPath := filepath.Join(configDir, "config.js")
		if err := os.WriteFile(crossSeedPath, []byte(crossSeedJS), 0o600); err != nil {
			t.Fatal(err)
		}
		environment["CROSS_SEED_CONFIG"] = crossSeedPath
	}
	openedStore, err := store.Open(filepath.Join(configDir, configEditDatabaseRel))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = openedStore.Close() })
	lookup := func(name string) (string, bool) {
		value, found := environment[name]
		return value, found
	}
	load := func(reader config.StoredReader) (config.Config, error) {
		return config.LoadWith(nil, lookup, configDir, func(string) (config.StoredReader, io.Closer, error) {
			return reader, nil, nil
		})
	}
	startup, err := load(openedStore)
	if err != nil {
		t.Fatal(err)
	}
	service := New(Deps{Config: startup, ConfigStore: openedStore, LoadStoredConfig: load})
	return configEditFixture{service: service, store: openedStore, load: load}
}

func (fixture configEditFixture) reload(t *testing.T) config.Config {
	t.Helper()
	loaded, err := fixture.load(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func (fixture configEditFixture) overrides(t *testing.T) []store.ConfigOverride {
	t.Helper()
	overrides, err := fixture.store.ConfigOverrides(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return overrides
}

// putSealedRow écrit une ligne scellée sous itemID sans passer par la validation de l'édition.
func (fixture configEditFixture) putSealedRow(t *testing.T, list, itemID string, element map[string]any) {
	t.Helper()
	secretKey, err := base64.RawURLEncoding.DecodeString(configEditSecretKey)
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := config.NewSecretSealer(secretKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := sealer.SealElement(list, itemID, element); err != nil {
		t.Fatal(err)
	}
	value, err := json.Marshal(element)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.PutConfigOverride(context.Background(), store.ConfigOverride{List: list, ItemID: itemID, Value: value}); err != nil {
		t.Fatal(err)
	}
}

func radarrElement(apiKey any) map[string]any {
	return map[string]any{"url": configEditRadarrURL, "apiKey": apiKey}
}

func upstreamRadarrJS() string {
	return `module.exports = { radarr: ["` + configEditRadarrURL + `?apikey=` + configEditRadarrKey + `"] };`
}

func TestPutItem_storesSecretSealedAndNeverReturnsIt(t *testing.T) {
	fixture := newConfigEditFixture(t, withSecretKey(map[string]string{}), "")
	ctx := context.Background()

	if err := fixture.service.PutItem(ctx, "radarr", "", radarrElement(configEditRadarrKey)); err != nil {
		t.Fatal(err)
	}

	overrides := fixture.overrides(t)
	if len(overrides) != 1 || strings.Contains(string(overrides[0].Value), configEditRadarrKey) ||
		!strings.Contains(string(overrides[0].Value), "sealed:v1:") {
		t.Fatalf("valeurObtenue %d rows %+v, valeurAttendue one sealed row", len(overrides), overrides)
	}
	valeurObtenue := fixture.reload(t).Radarr[0].APIKey
	if valeurObtenue != configEditRadarrKey {
		t.Fatalf("valeurObtenue %q, valeurAttendue the stored key read back by the loader", valeurObtenue)
	}
	view, err := fixture.service.ConfigView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	encodedView, _ := json.Marshal(view)
	if strings.Contains(string(encodedView), configEditRadarrKey) || strings.Contains(string(encodedView), "sealed:v1:") {
		t.Fatalf("valeurObtenue %s, valeurAttendue a view without any secret", encodedView)
	}
	item := view.Lists["radarr"][0]
	if value, present := item.Element["apiKey"]; !present || value != nil || item.Origin != string(config.OriginAdded) {
		t.Fatalf("valeurObtenue %+v, valeurAttendue an added item whose apiKey is present and hidden", item)
	}
}

func TestPutItem_withoutSecretKeyRejectsASecret(t *testing.T) {
	fixture := newConfigEditFixture(t, map[string]string{}, "")

	err := fixture.service.PutItem(context.Background(), "radarr", "", radarrElement(configEditRadarrKey))

	if !errors.Is(err, config.ErrSecretKeyMissing) || len(fixture.overrides(t)) != 0 {
		t.Fatalf("valeurObtenue %v and %d rows, valeurAttendue ErrSecretKeyMissing and nothing written", err, len(fixture.overrides(t)))
	}
}

func TestPutItem_keepsTheStoredSecretWhenTheFieldIsOmitted(t *testing.T) {
	fixture := newConfigEditFixture(t, withSecretKey(map[string]string{}), "")
	ctx := context.Background()
	if err := fixture.service.PutItem(ctx, "radarr", "", radarrElement(configEditRadarrKey)); err != nil {
		t.Fatal(err)
	}
	itemID := fixture.overrides(t)[0].ItemID

	for _, element := range []map[string]any{{"url": configEditRadarrURL}, radarrElement(nil)} {
		if err := fixture.service.PutItem(ctx, "radarr", itemID, element); err != nil {
			t.Fatal(err)
		}

		valeurAttendue, valeurObtenue := configEditRadarrKey, fixture.reload(t).Radarr[0].APIKey
		if valeurObtenue != valeurAttendue {
			t.Fatalf("element %v: valeurObtenue %q, valeurAttendue the stored key kept", element, valeurObtenue)
		}
	}
}

func TestPutItem_refusesToCarryASecretToANewDestination(t *testing.T) {
	fixture := newConfigEditFixture(t, withSecretKey(map[string]string{}), "")
	ctx := context.Background()
	if err := fixture.service.PutItem(ctx, "radarr", "", radarrElement(configEditRadarrKey)); err != nil {
		t.Fatal(err)
	}
	itemID := fixture.overrides(t)[0].ItemID
	storedBefore := string(fixture.overrides(t)[0].Value)

	err := fixture.service.PutItem(ctx, "radarr", itemID, map[string]any{"url": "http://other.example.com:7878"})

	if !errors.Is(err, config.ErrUsage) || string(fixture.overrides(t)[0].Value) != storedBefore {
		t.Fatalf("valeurObtenue %v, valeurAttendue a usage error and the stored item unchanged", err)
	}
}

func TestPutItem_invalidElementWritesNothing(t *testing.T) {
	fixture := newConfigEditFixture(t, withSecretKey(map[string]string{}), "")
	cases := []struct {
		list      string
		element   map[string]any
		fieldName string
	}{
		{"torznab", map[string]any{"url": "https://indexer.example.com/api?apikey=fake"}, "name"},
		{"radarr", map[string]any{"url": "${RADARR_URL}", "apiKey": "fake"}, "radarr.url"},
		{"radarr", map[string]any{"url": "ftp://radarr.example.com", "apiKey": "fake"}, "radarr"},
		{"unknownList", map[string]any{"path": "/srv/links"}, "unknownList"},
	}
	for _, testCase := range cases {
		err := fixture.service.PutItem(context.Background(), testCase.list, "", testCase.element)

		if !errors.Is(err, config.ErrUsage) || !strings.Contains(err.Error(), testCase.fieldName) {
			t.Fatalf("%s %v: valeurObtenue %v, valeurAttendue a usage error naming %q", testCase.list, testCase.element, err, testCase.fieldName)
		}
		if rowCount := len(fixture.overrides(t)); rowCount != 0 {
			t.Fatalf("%s: valeurObtenue %d rows, valeurAttendue nothing written", testCase.list, rowCount)
		}
	}
}

func TestPutItem_refusesAVariableReferenceInEveryList(t *testing.T) {
	const secretReference = "${NECROMANCIARR_SECRET_KEY}"
	fixture := newConfigEditFixture(t, withSecretKey(map[string]string{}), "")
	cases := []struct {
		list      string
		element   map[string]any
		fieldName string
	}{
		{"fsProviders", map[string]any{
			"type": "sftp", "name": "seedbox", "address": "seedbox.example.com:22", "user": "media",
			"password": secretReference, "knownHosts": "/home/u/.ssh/known_hosts",
		}, "fsProviders.password"},
		{"torznab", map[string]any{"name": "indexer", "url": "https://indexer.example.com/api?apikey=" + secretReference}, "torznab.url"},
		{"trackers", map[string]any{"key": "tracker-a", "announce": []any{"https://tracker-a.example.com/" + secretReference}}, "trackers.announce"},
		{"sonarr", map[string]any{"url": "http://sonarr.example.com:8989", "apiKey": secretReference}, "sonarr.apiKey"},
		{"torrentClients", map[string]any{"type": "qbittorrent", "url": "http://qbit.example.com:8080", "password": secretReference}, "torrentClients.password"},
		{"linkDirs", map[string]any{"path": "/srv/" + secretReference}, "linkDirs.path"},
	}
	for _, testCase := range cases {
		err := fixture.service.PutItem(context.Background(), testCase.list, "", testCase.element)

		if !errors.Is(err, config.ErrUsage) || !strings.Contains(err.Error(), testCase.fieldName) {
			t.Fatalf("%s: valeurObtenue %v, valeurAttendue a usage error naming %q", testCase.list, err, testCase.fieldName)
		}
		if strings.Contains(err.Error(), configEditSecretKey) || len(fixture.overrides(t)) != 0 {
			t.Fatalf("%s: valeurObtenue %v, valeurAttendue no key in the error and nothing written", testCase.list, err)
		}
	}
}

func TestDeleteItem_onUpstreamElementWritesADeletedMarker(t *testing.T) {
	fixture := newConfigEditFixture(t, withSecretKey(map[string]string{}), upstreamRadarrJS())

	if err := fixture.service.DeleteItem(context.Background(), "radarr", configEditRadarrURL); err != nil {
		t.Fatal(err)
	}

	overrides := fixture.overrides(t)
	if len(overrides) != 1 || !overrides[0].Deleted || overrides[0].UpstreamFingerprint == "" {
		t.Fatalf("valeurObtenue %+v, valeurAttendue one deleted marker with the upstream fingerprint", overrides)
	}
	if radarrCount := len(fixture.reload(t).Radarr); radarrCount != 0 {
		t.Fatalf("valeurObtenue %d radarr, valeurAttendue the upstream element hidden", radarrCount)
	}
}

func TestDeleteItem_onAddedElementRemovesTheRow(t *testing.T) {
	fixture := newConfigEditFixture(t, withSecretKey(map[string]string{}), "")
	ctx := context.Background()
	if err := fixture.service.PutItem(ctx, "linkDirs", "", map[string]any{"path": "/srv/links"}); err != nil {
		t.Fatal(err)
	}

	if err := fixture.service.DeleteItem(ctx, "linkDirs", "/srv/links"); err != nil {
		t.Fatal(err)
	}

	if rowCount := len(fixture.overrides(t)); rowCount != 0 {
		t.Fatalf("valeurObtenue %d rows, valeurAttendue the added row removed", rowCount)
	}
}

func TestUnsetItem_restoresTheUpstreamValue(t *testing.T) {
	fixture := newConfigEditFixture(t, withSecretKey(map[string]string{}), upstreamRadarrJS())
	ctx := context.Background()
	if err := fixture.service.PutItem(ctx, "radarr", configEditRadarrURL, radarrElement(configEditOtherKey)); err != nil {
		t.Fatal(err)
	}
	if overrides := fixture.overrides(t); overrides[0].UpstreamFingerprint == "" || fixture.reload(t).Radarr[0].APIKey != configEditOtherKey {
		t.Fatalf("valeurObtenue %+v, valeurAttendue an override with the upstream fingerprint", overrides)
	}

	if err := fixture.service.UnsetItem(ctx, "radarr", configEditRadarrURL); err != nil {
		t.Fatal(err)
	}

	valeurAttendue, valeurObtenue := configEditRadarrKey, fixture.reload(t).Radarr[0].APIKey
	if valeurObtenue != valeurAttendue || len(fixture.overrides(t)) != 0 {
		t.Fatalf("valeurObtenue %q, valeurAttendue the cross-seed key back and no row", valeurObtenue)
	}
}

func TestPutItem_omittedSecretOfAnUpstreamElementKeepsTheUpstreamValue(t *testing.T) {
	fixture := newConfigEditFixture(t, withSecretKey(map[string]string{}), upstreamRadarrJS())

	if err := fixture.service.PutItem(context.Background(), "radarr", configEditRadarrURL, map[string]any{"url": configEditRadarrURL}); err != nil {
		t.Fatal(err)
	}

	valeurAttendue, valeurObtenue := configEditRadarrKey, fixture.reload(t).Radarr[0].APIKey
	if valeurObtenue != valeurAttendue || strings.Contains(string(fixture.overrides(t)[0].Value), configEditRadarrKey) {
		t.Fatalf("valeurObtenue %q, valeurAttendue the upstream key kept, sealed", valeurObtenue)
	}
}

func TestSetSetting_rejectsAnUnknownKey(t *testing.T) {
	fixture := newConfigEditFixture(t, withSecretKey(map[string]string{}), "")

	err := fixture.service.SetSetting(context.Background(), "unknownSetting", json.RawMessage(`"x"`))

	settings, _ := fixture.store.Settings(context.Background())
	if !errors.Is(err, config.ErrUsage) || len(settings) != 0 {
		t.Fatalf("valeurObtenue %v and %d settings, valeurAttendue a usage error and nothing written", err, len(settings))
	}
}

func TestSetSetting_rejectsAVariableReference(t *testing.T) {
	fixture := newConfigEditFixture(t, withSecretKey(map[string]string{}), "")

	err := fixture.service.SetSetting(context.Background(), "linkCategory", json.RawMessage(`"${CATEGORY}"`))

	if !errors.Is(err, config.ErrUsage) || !strings.Contains(err.Error(), "linkCategory") {
		t.Fatalf("valeurObtenue %v, valeurAttendue a usage error naming linkCategory", err)
	}
}

func TestSetSetting_sealsASecretSettingAndHidesIt(t *testing.T) {
	const prowlarrSecret = "fake-prowlarr-key-0000"
	fixture := newConfigEditFixture(t, withSecretKey(map[string]string{}), "")
	ctx := context.Background()

	err := fixture.service.SetSetting(ctx, "prowlarr", json.RawMessage(`"https://prowlarr.example.com:9696?apikey=`+prowlarrSecret+`"`))
	if err != nil {
		t.Fatal(err)
	}

	settings, _ := fixture.store.Settings(ctx)
	view, _ := fixture.service.ConfigView(ctx)
	prowlarr := view.Settings["prowlarr"]
	if strings.Contains(string(settings["prowlarr"]), prowlarrSecret) || prowlarr.Value != nil || !prowlarr.Secret || !prowlarr.Set {
		t.Fatalf("valeurObtenue %q and %+v, valeurAttendue a sealed row and a hidden, set value", settings["prowlarr"], prowlarr)
	}
	if !strings.Contains(fixture.reload(t).Prowlarr, prowlarrSecret) {
		t.Fatal("valeurObtenue a prowlarr url without the key, valeurAttendue the stored key read back")
	}
}

func TestConfigView_marksEnvironmentLockedSettings(t *testing.T) {
	fixture := newConfigEditFixture(t, withSecretKey(map[string]string{"NECROMANCIARR_LISTEN": "127.0.0.1:9000"}), "")
	ctx := context.Background()
	before, err := fixture.service.ConfigView(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if err := fixture.service.SetSetting(ctx, "listen", json.RawMessage(`"127.0.0.1:9001"`)); err != nil {
		t.Fatal(err)
	}

	after, err := fixture.service.ConfigView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	listen := after.Settings["listen"]
	if !listen.Locked || listen.Origin != "env" || listen.Value != "127.0.0.1:9000" || after.Settings["linkType"].Locked {
		t.Fatalf("valeurObtenue %+v, valeurAttendue listen locked by the environment", listen)
	}
	if before.RestartRequired || !after.RestartRequired {
		t.Fatalf("valeurObtenue restart %v then %v, valeurAttendue false then true", before.RestartRequired, after.RestartRequired)
	}
}

func TestPutItem_trackerWithASingleAnnounceStringLoadsBackSealed(t *testing.T) {
	fixture := newConfigEditFixture(t, withSecretKey(map[string]string{}), "")
	announce := "https://tracker.example.com/announce/fake-passkey-0000"
	valeurAttendue := []string{announce}

	err := fixture.service.PutItem(context.Background(), "trackers", "tracker-a",
		map[string]any{"key": "tracker-a", "announce": announce})
	if err != nil {
		t.Fatal(err)
	}

	overrides := fixture.overrides(t)
	if len(overrides) != 1 || strings.Contains(string(overrides[0].Value), announce) {
		t.Fatalf("valeurObtenue %+v, valeurAttendue one row without the clear announce", overrides)
	}
	trackers := fixture.reload(t).Trackers
	if len(trackers) != 1 || !slices.Equal(trackers[0].Announce, valeurAttendue) {
		t.Fatalf("valeurObtenue %+v, valeurAttendue one tracker announcing %q", trackers, valeurAttendue)
	}
}

func TestPutItem_refusedCandidateStaysAUsageErrorAfterTheStoredClass(t *testing.T) {
	fixture := newConfigEditFixture(t, withSecretKey(map[string]string{}), "")
	valeurAttendue := ErrorCodeUsage

	err := fixture.service.PutItem(context.Background(), "torznab", "", map[string]any{"url": "http://example.com/api"})
	valeurObtenue, _ := Classify(err)

	if valeurObtenue != valeurAttendue || len(fixture.overrides(t)) != 0 {
		t.Fatalf("valeurObtenue %q (%v), valeurAttendue %q et rien d'écrit", valeurObtenue, err, valeurAttendue)
	}
}

func TestUnsetItem_removesTheRowOfAnUnknownList(t *testing.T) {
	fixture := newConfigEditFixture(t, map[string]string{}, "")
	ctx := context.Background()
	orphan := store.ConfigOverride{List: "retiredList", ItemID: "http://example.com", Value: []byte(`{}`)}
	if err := fixture.store.PutConfigOverride(ctx, orphan); err != nil {
		t.Fatal(err)
	}
	valeurAttendue := 0

	err := fixture.service.UnsetItem(ctx, orphan.List, orphan.ItemID)
	valeurObtenue := len(fixture.overrides(t))

	if err != nil || valeurObtenue != valeurAttendue {
		t.Fatalf("valeurObtenue %d rows (%v), valeurAttendue %d", valeurObtenue, err, valeurAttendue)
	}
}

func TestUnsetItem_refusesAnUnknownListWithoutRow(t *testing.T) {
	fixture := newConfigEditFixture(t, map[string]string{}, "")

	err := fixture.service.UnsetItem(context.Background(), "retiredList", "http://example.com")

	if !errors.Is(err, ErrUnknownConfigKey) {
		t.Fatalf("valeurObtenue %v, valeurAttendue ErrUnknownConfigKey", err)
	}
}

func TestPutItem_refusesAnItemIDThatIsNotAnEndpoint(t *testing.T) {
	fixture := newConfigEditFixture(t, withSecretKey(map[string]string{}), "")
	refusedIDs := []string{
		configEditRadarrURL + "/api/dummy-proxy-key-0000",
		"http://alice:dummy-pass-0000@radarr.example.com:7878",
		configEditRadarrURL + "?apikey=dummy-key-0000",
	}
	for _, refusedID := range refusedIDs {
		err := fixture.service.PutItem(context.Background(), "radarr", refusedID, radarrElement(configEditRadarrKey))

		if !errors.Is(err, config.ErrUsage) || strings.Contains(err.Error(), "dummy") {
			t.Errorf("valeurObtenue %v, valeurAttendue un refus d'usage sans l'identifiant", err)
		}
	}
	if valeurObtenue := len(fixture.overrides(t)); valeurObtenue != 0 {
		t.Fatalf("valeurObtenue %d rows, valeurAttendue 0", valeurObtenue)
	}
}

func TestPutItem_acceptsAnEndpointItemIDWithItsCollisionSuffix(t *testing.T) {
	fixture := newConfigEditFixture(t, withSecretKey(map[string]string{}), "")
	ctx := context.Background()
	valeurAttendue := 2

	for _, acceptedID := range []string{configEditRadarrURL, configEditRadarrURL + "#2"} {
		if err := fixture.service.PutItem(ctx, "radarr", acceptedID, radarrElement(configEditRadarrKey)); err != nil {
			t.Fatalf("%s: %v", acceptedID, err)
		}
	}

	if valeurObtenue := len(fixture.overrides(t)); valeurObtenue != valeurAttendue {
		t.Fatalf("valeurObtenue %d rows, valeurAttendue %d", valeurObtenue, valeurAttendue)
	}
}

func TestPutItem_editsALegacyStoredItemID(t *testing.T) {
	fixture := newConfigEditFixture(t, withSecretKey(map[string]string{}), "")
	ctx := context.Background()
	legacyID := configEditRadarrURL + "/legacy"
	fixture.putSealedRow(t, "radarr", legacyID, radarrElement(configEditRadarrKey))

	err := fixture.service.PutItem(ctx, "radarr", legacyID, radarrElement(nil))
	if err != nil {
		t.Fatalf("valeurObtenue %v, valeurAttendue l'édition d'une ligne existante acceptée", err)
	}
}
