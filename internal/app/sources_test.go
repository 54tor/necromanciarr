package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"necromanciarr/internal/config"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/sources"
	"necromanciarr/internal/store"
	"necromanciarr/internal/torznab"
)

// Valeurs de test à faible entropie (gitleaks).
const (
	hostileAPIKey  = "fake-hostile-key-0000"
	hostilePasskey = "fake-passkey-zzzz-0000"
)

// fakeIndexerServer répond comme un indexeur Torznab ; status (200 par défaut) force le statut des réponses et
// requests compte les requêtes reçues.
type fakeIndexerServer struct {
	server   *httptest.Server
	status   atomic.Int32
	requests atomic.Int32
}

func newFakeIndexerServer(t *testing.T) *fakeIndexerServer {
	t.Helper()
	fake := &fakeIndexerServer{}
	fake.status.Store(http.StatusOK)
	fake.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fake.requests.Add(1)
		writer.Header().Set("Content-Type", "application/xml")
		if status := int(fake.status.Load()); status != http.StatusOK {
			writer.WriteHeader(status)
			_, _ = writer.Write([]byte("error for apikey=" + request.URL.Query().Get("apikey")))
			return
		}
		if request.URL.Query().Get("t") == "caps" {
			_, _ = writer.Write([]byte(`<caps><searching><search available="yes" supportedParams="q"/></searching></caps>`))
			return
		}
		_, _ = writer.Write([]byte(`<?xml version="1.0"?><rss><channel></channel></rss>`))
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func fakeIndexerEntry(name, baseURL string) config.TorznabIndexer {
	entry := torznabEntry(name)
	entry.URL, entry.MinInterval, entry.Timeout = baseURL+"/api?apikey="+hostileAPIKey, time.Millisecond, 5*time.Second
	return entry
}

// sourcesService construit le Service de production (buildDeps) sur la configuration donnée.
func sourcesService(t *testing.T, settings config.Config) (Service, *store.Store) {
	t.Helper()
	openedStore := openTestStore(t)
	deps, _, err := buildDeps(context.Background(), settings, openedStore)
	if err != nil {
		t.Fatal(err)
	}
	return New(deps), openedStore
}

func recordUsage(t *testing.T, openedStore *store.Store, indexer, kind string, count int) {
	t.Helper()
	for range count {
		if err := openedStore.RecordIndexerUsage(context.Background(), indexer, kind, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSources_listReportsUsageAndCaps(t *testing.T) {
	settings := sourcesSettings(t)
	first, second, unused := torznabEntry("a"), torznabEntry("b"), torznabEntry("c")
	first.Tracker = "tracker-a"
	second.MaxQueriesPerDay = 1
	settings.Torznab = []config.TorznabIndexer{first, second, unused}
	settings.SourceOrder = []string{"provider", "torznab:b", "torznab:a"}
	service, openedStore := sourcesService(t, settings)
	recordUsage(t, openedStore, "torznab:a", sources.KindQuery, 3)
	recordUsage(t, openedStore, "torznab:a", sources.KindDownload, 1)
	recordUsage(t, openedStore, "torznab:b", sources.KindQuery, 1)

	gotValue, err := service.ListSources(t.Context())

	expectedValue := []SourceRow{
		{Order: 1, Source: "provider", Type: SourceTypeProvider, State: SourceStateOK},
		{
			Order: 2, Source: "torznab:b", Type: SourceTypeTorznab, State: SourceStateExhausted,
			Queries: &SourceUsage{Used: 1, Cap: 1}, Downloads: &SourceUsage{Used: 0, Cap: 50},
		},
		{
			Order: 3, Source: "torznab:a", Type: SourceTypeTorznab, Tracker: "tracker-a", State: SourceStateOK,
			Queries: &SourceUsage{Used: 3, Cap: 200}, Downloads: &SourceUsage{Used: 1, Cap: 50},
		},
		{
			Order: 0, Source: "torznab:c", Type: SourceTypeTorznab, State: SourceStateDisabled,
			Queries: &SourceUsage{Used: 0, Cap: 200}, Downloads: &SourceUsage{Used: 0, Cap: 50},
		},
	}
	gotJSON, _ := json.Marshal(gotValue)
	expectedJSON, _ := json.Marshal(expectedValue)
	if err != nil || !bytes.Equal(gotJSON, expectedJSON) {
		t.Fatalf("valeurObtenue %s (%v), valeurAttendue %s", gotJSON, err, expectedJSON)
	}
}

func TestSources_providerOnlyListsTheProviderWithoutUsage(t *testing.T) {
	service, _ := sourcesService(t, sourcesSettings(t))

	gotValue, err := service.ListSources(t.Context())

	isExpected := err == nil && len(gotValue) == 1 && gotValue[0].Source == "provider" && gotValue[0].State == SourceStateOK &&
		gotValue[0].Queries == nil && gotValue[0].Downloads == nil
	if !isExpected {
		t.Fatalf("valeurObtenue %+v %v, valeurAttendue the provider row alone, usage n/a", gotValue, err)
	}
	encoded, _ := json.Marshal(gotValue[0])
	if !strings.Contains(string(encoded), `"queries":null`) || !strings.Contains(string(encoded), `"last_error":""`) {
		t.Fatalf("valeurObtenue %s, valeurAttendue stable keys with null usage", encoded)
	}
}

func TestSources_withoutLibraryAPITheProviderIsDisabled(t *testing.T) {
	settings := sourcesSettings(t)
	settings.LibraryAPI = ""
	service, _ := sourcesService(t, settings)

	gotValue, err := service.ListSources(t.Context())

	if err != nil || len(gotValue) != 1 || gotValue[0].State != SourceStateDisabled {
		t.Fatalf("valeurObtenue %+v %v, valeurAttendue one disabled provider row", gotValue, err)
	}
}

func TestSources_testCountsAgainstQuota(t *testing.T) {
	indexer := newFakeIndexerServer(t)
	settings := sourcesSettings(t)
	settings.Torznab = []config.TorznabIndexer{fakeIndexerEntry("a", indexer.server.URL)}
	service, openedStore := sourcesService(t, settings)

	passed, passErr := service.TestSource(t.Context(), "a")
	indexer.status.Store(http.StatusForbidden)
	failed, failErr := service.TestSource(t.Context(), "torznab:a")
	listed, listErr := service.ListSources(t.Context())

	used, countErr := openedStore.CountIndexerUsage(t.Context(), "torznab:a", sources.KindQuery, time.Now().Add(-store.IndexerUsageWindow))
	expectedPassed := SourceTestResult{Source: "torznab:a", Result: SourceTestOK}
	expectedFailed := SourceTestResult{Source: "torznab:a", Result: SourceTestFailed, Error: "indexer_access_denied"}
	isExpected := passErr == nil && failErr == nil && listErr == nil && countErr == nil && passed == expectedPassed && failed == expectedFailed &&
		used == 3 && indexer.requests.Load() == 3 && len(listed) == 2 && listed[1].LastError == "indexer_access_denied" &&
		listed[1].Queries.Used == 3
	if !isExpected {
		t.Fatalf("valeurObtenue %+v %v / %+v %v, %d rows %d requests, list %+v %v, valeurAttendue ok then denied, 3 rows (caps, search, search: caps cached by the client)",
			passed, passErr, failed, failErr, used, indexer.requests.Load(), listed, listErr)
	}
}

func TestSources_testIsRefusedAtTheDailyCapWithoutARequest(t *testing.T) {
	indexer := newFakeIndexerServer(t)
	settings := sourcesSettings(t)
	entry := fakeIndexerEntry("a", indexer.server.URL)
	entry.MaxQueriesPerDay = 1
	settings.Torznab = []config.TorznabIndexer{entry}
	service, openedStore := sourcesService(t, settings)
	recordUsage(t, openedStore, "torznab:a", sources.KindQuery, 1)

	gotValue, err := service.TestSource(t.Context(), "torznab:a")

	expectedValue := SourceTestResult{Source: "torznab:a", Result: SourceTestFailed, Error: "indexer_budget_exhausted"}
	if err != nil || gotValue != expectedValue || indexer.requests.Load() != 0 {
		t.Fatalf("valeurObtenue %+v %v, %d requests, valeurAttendue %+v without any request", gotValue, err, indexer.requests.Load(), expectedValue)
	}
}

func TestSources_testRefusesTheProviderAndAnUnknownName(t *testing.T) {
	service, _ := sourcesService(t, sourcesSettings(t))

	_, providerErr := service.TestSource(t.Context(), "provider")
	_, unknownErr := service.TestSource(t.Context(), "torznab:nope")

	providerCode, _ := Classify(providerErr)
	unknownCode, _ := Classify(unknownErr)
	isExpected := errors.Is(providerErr, ErrSourceNotTestable) && providerCode == ErrorCodeRefused &&
		errors.Is(unknownErr, ErrSourceNotFound) && unknownCode == ErrorCodeNotFound &&
		MessageOf(unknownErr).Key == i18n.KeyErrorSourceNotFound && MessageOf(providerErr).Key == i18n.KeyErrorSourceNotTestable
	if !isExpected {
		t.Fatalf("valeurObtenue %v (%s) / %v (%s), valeurAttendue refused provider, not_found unknown", providerErr, providerCode, unknownErr, unknownCode)
	}
}

func TestSources_listNeverShowsKeys(t *testing.T) {
	indexer := newFakeIndexerServer(t)
	settings := sourcesSettings(t)
	entry := fakeIndexerEntry("a", indexer.server.URL)
	entry.Tracker = "https://tracker-a.example.com/" + hostilePasskey + "/announce"
	settings.Torznab = []config.TorznabIndexer{entry}
	settings.SecretKey = bytes.Repeat([]byte("k"), 32)
	service, _ := sourcesService(t, settings)
	indexer.status.Store(http.StatusInternalServerError)

	tested, testErr := service.TestSource(t.Context(), "torznab:a")
	listed, listErr := service.ListSources(t.Context())

	encoded, _ := json.Marshal(struct {
		Tested SourceTestResult
		Listed []SourceRow
	}{tested, listed})
	hostHost := strings.TrimPrefix(indexer.server.URL, "http://")
	for _, leaked := range []string{hostileAPIKey, hostilePasskey, strings.Repeat("k", 32), "apikey", hostHost, "/api", "/announce"} {
		if strings.Contains(string(encoded), leaked) {
			t.Fatalf("valeurObtenue %s, valeurAttendue no %q", encoded, leaked)
		}
	}
	if testErr != nil || listErr != nil || tested.Error != "indexer_unavailable" || listed[1].LastError != "indexer_unavailable" {
		t.Fatalf("valeurObtenue %s (%v, %v), valeurAttendue the stable code indexer_unavailable", encoded, testErr, listErr)
	}
}

var (
	errAccessDeniedForTest = fmt.Errorf("%w: status 403", torznab.ErrAccessDenied)
	errHostileForTest      = fmt.Errorf("%w: doctype", torznab.ErrHostileResponse)
)

func TestSourceErrorCode_mapsEachIndexerErrorToAStableReason(t *testing.T) {
	cases := map[string]error{
		"indexer_budget_exhausted": sources.ErrBudgetExhausted,
		"indexer_access_denied":    errAccessDeniedForTest,
		"indexer_hostile_response": errHostileForTest,
		"indexer_unavailable":      errors.New("dial tcp: something with apikey=" + hostileAPIKey),
	}
	for expectedValue, err := range cases {
		if gotValue := sourceErrorCode(err); gotValue != expectedValue {
			t.Fatalf("valeurObtenue %q for %v, valeurAttendue %q", gotValue, err, expectedValue)
		}
	}
	if gotValue := sourceErrorCode(nil); gotValue != "" {
		t.Fatalf("valeurObtenue %q, valeurAttendue empty for no error", gotValue)
	}
}

func TestSources_secondConcurrentTestOfTheSameIndexerIsRefused(t *testing.T) {
	release := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		<-release
		writer.Header().Set("Content-Type", "application/xml")
		if request.URL.Query().Get("t") == "caps" {
			_, _ = writer.Write([]byte(`<caps><searching><search available="yes" supportedParams="q"/></searching></caps>`))
			return
		}
		_, _ = writer.Write([]byte(`<?xml version="1.0"?><rss><channel></channel></rss>`))
	}))
	t.Cleanup(server.Close)
	settings := sourcesSettings(t)
	settings.Torznab = []config.TorznabIndexer{fakeIndexerEntry("a", server.URL)}
	service, openedStore := sourcesService(t, settings)
	firstDone := make(chan error, 1)
	go func() {
		_, err := service.TestSource(context.Background(), "torznab:a")
		firstDone <- err
	}()
	for requests.Load() == 0 {
		time.Sleep(time.Millisecond)
	}

	_, secondErr := service.TestSource(t.Context(), "a")
	close(release)
	firstErr := <-firstDone
	_, thirdErr := service.TestSource(t.Context(), "torznab:a")

	used, countErr := openedStore.CountIndexerUsage(t.Context(), "torznab:a", sources.KindQuery, time.Now().Add(-store.IndexerUsageWindow))
	secondCode, _ := Classify(secondErr)
	isExpected := errors.Is(secondErr, ErrSourceTestRunning) && secondCode == ErrorCodeRefused && firstErr == nil && thirdErr == nil &&
		countErr == nil && used == 3 && MessageOf(secondErr).Key == i18n.KeyErrorSourceTestRunning
	if !isExpected {
		t.Fatalf("valeurObtenue second %v (%s), first %v, third %v, %d rows (%v), valeurAttendue the second refused, 3 rows (caps, search, search)",
			secondErr, secondCode, firstErr, thirdErr, used, countErr)
	}
}

func TestSources_listReportsTheCapsCache(t *testing.T) {
	indexer := newFakeIndexerServer(t)
	settings := sourcesSettings(t)
	settings.Torznab = []config.TorznabIndexer{fakeIndexerEntry("a", indexer.server.URL)}
	service, _ := sourcesService(t, settings)

	before, beforeErr := service.ListSources(t.Context())
	_, testErr := service.TestSource(t.Context(), "a")
	after, afterErr := service.ListSources(t.Context())

	isExpected := beforeErr == nil && testErr == nil && afterErr == nil && !before[0].CapsCached && !before[1].CapsCached && after[1].CapsCached
	if !isExpected {
		t.Fatalf("valeurObtenue %+v then %+v, valeurAttendue caps_cached true after a test only", before, after)
	}
	encoded, _ := json.Marshal(after[1])
	if !strings.Contains(string(encoded), `"caps_cached":true`) {
		t.Fatalf("valeurObtenue %s, valeurAttendue the caps_cached key", encoded)
	}
}
