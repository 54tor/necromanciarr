package torznab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// Valeur volontairement pauvre en entropie : un faux secret ne doit pas ressembler à un vrai (gitleaks).
const testAPIKey = "not-a-real-key"

type fakeClock struct {
	mutex sync.Mutex
	now   time.Time
}

func (clock *fakeClock) Now() time.Time {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	return clock.now
}

func (clock *fakeClock) advance(delay time.Duration) {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	clock.now = clock.now.Add(delay)
}

// fakeIndexer est un indexeur Torznab factice qui compte ses requêtes et garde la dernière requête reçue.
type fakeIndexer struct {
	server    *httptest.Server
	mutex     sync.Mutex
	requests  int
	lastQuery url.Values
}

func newFakeIndexer(t *testing.T, handler http.HandlerFunc) *fakeIndexer {
	t.Helper()
	indexer := &fakeIndexer{}
	indexer.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		indexer.mutex.Lock()
		indexer.requests++
		indexer.lastQuery = request.URL.Query()
		indexer.mutex.Unlock()
		handler(writer, request)
	}))
	t.Cleanup(indexer.server.Close)
	return indexer
}

func (indexer *fakeIndexer) requestCount() int {
	indexer.mutex.Lock()
	defer indexer.mutex.Unlock()
	return indexer.requests
}

func (indexer *fakeIndexer) query() url.Values {
	indexer.mutex.Lock()
	defer indexer.mutex.Unlock()
	return indexer.lastQuery
}

func xmlResponse(body string) http.HandlerFunc {
	return func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
		_, _ = writer.Write([]byte(body))
	}
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func endpointFor(baseURL string) Endpoint {
	return Endpoint{Name: "demo", URL: baseURL + "/api?apikey=" + testAPIKey, MinInterval: 3 * time.Second, Timeout: 5 * time.Second}
}

// newTestClient crée un client à horloge factice ; l'attente du limiteur avance l'horloge et note chaque délai.
func newTestClient(t *testing.T, baseURL string) (*Client, *fakeClock, *[]time.Duration) {
	t.Helper()
	clock := &fakeClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	client, err := New(endpointFor(baseURL), Deps{Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	waits := &[]time.Duration{}
	client.wait = func(_ context.Context, delay time.Duration) error {
		*waits = append(*waits, delay)
		clock.advance(delay)
		return nil
	}
	return client, clock, waits
}

func TestClient_waitsMinIntervalBetweenRequests(t *testing.T) {
	indexer := newFakeIndexer(t, xmlResponse(`<rss><channel></channel></rss>`))
	client, _, waits := newTestClient(t, indexer.server.URL)

	for range 2 {
		if _, err := client.Search(context.Background(), Query{Mode: "search", Text: "invented"}); err != nil {
			t.Fatal(err)
		}
	}

	expectedValue := []time.Duration{3 * time.Second}
	if gotValue := *waits; len(gotValue) != 1 || gotValue[0] != expectedValue[0] {
		t.Fatalf("expectedValue %v, gotValue %v", expectedValue, gotValue)
	}

	// Un autre indexeur a son propre limiteur : sa première requête n'attend pas.
	otherClient, _, otherWaits := newTestClient(t, indexer.server.URL)
	if _, err := otherClient.Search(context.Background(), Query{Mode: "search", Text: "invented"}); err != nil {
		t.Fatal(err)
	}
	if gotValue := *otherWaits; len(gotValue) != 0 {
		t.Fatalf("expectedValue no wait for another indexer, gotValue %v", gotValue)
	}
}

func TestClient_waitIsCancelledByContext(t *testing.T) {
	indexer := newFakeIndexer(t, xmlResponse(`<rss><channel></channel></rss>`))
	endpoint := endpointFor(indexer.server.URL)
	endpoint.MinInterval = time.Hour
	client, err := New(endpoint, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Search(context.Background(), Query{Mode: "search", Text: "invented"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, gotErr := client.Search(ctx, Query{Mode: "search", Text: "invented"})

	if !errors.Is(gotErr, ErrUnavailable) || !errors.Is(gotErr, context.DeadlineExceeded) {
		t.Fatalf("gotErr = %v", gotErr)
	}
	if gotValue := indexer.requestCount(); gotValue != 1 {
		t.Fatalf("expectedValue 1 request, gotValue %d", gotValue)
	}
}

func TestClient_requestTimeoutIsUnavailable(t *testing.T) {
	indexer := newFakeIndexer(t, func(_ http.ResponseWriter, request *http.Request) { <-request.Context().Done() })
	endpoint := endpointFor(indexer.server.URL)
	endpoint.Timeout = 50 * time.Millisecond
	client, err := New(endpoint, Deps{})
	if err != nil {
		t.Fatal(err)
	}

	_, gotErr := client.Search(context.Background(), Query{Mode: "search", Text: "invented"})

	if !errors.Is(gotErr, ErrUnavailable) || strings.Contains(gotErr.Error(), testAPIKey) {
		t.Fatalf("gotErr = %v", gotErr)
	}
}

func TestCaps_cachedFor24Hours(t *testing.T) {
	indexer := newFakeIndexer(t, xmlResponse(fixture(t, "caps.xml")))
	client, clock, _ := newTestClient(t, indexer.server.URL)

	gotCaps, err := client.Caps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotValue := indexer.query().Get("t"); gotValue != "caps" {
		t.Fatalf("expectedValue t=caps, gotValue %q", gotValue)
	}
	expectedModes := map[string]string{"search": "q", "tvsearch": "q,tvdbid,season,ep", "movie": "q,imdbid,tmdbid"}
	for mode, expectedValue := range expectedModes {
		if gotValue := strings.Join(gotCaps.Modes[mode], ","); gotValue != expectedValue {
			t.Errorf("mode %s: expectedValue %q, gotValue %q", mode, expectedValue, gotValue)
		}
	}
	if _, hasAudio := gotCaps.Modes["audio"]; hasAudio || len(gotCaps.Modes) != 3 {
		t.Errorf("unexpected modes %v", gotCaps.Modes)
	}
	if gotValue := fmt.Sprint(gotCaps.Categories); gotValue != "[2000 2040 5000 5040]" {
		t.Errorf("gotValue categories %s", gotValue)
	}

	clock.advance(24*time.Hour - time.Second)
	if _, err := client.Caps(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotValue := indexer.requestCount(); gotValue != 1 {
		t.Fatalf("expectedValue cached caps, gotValue %d requests", gotValue)
	}

	clock.advance(2 * time.Second)
	if _, err := client.Caps(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotValue := indexer.requestCount(); gotValue != 2 {
		t.Fatalf("expectedValue refreshed caps after 24 h, gotValue %d requests", gotValue)
	}
}

func TestCaps_defaultsSupportedParamsWhenAbsent(t *testing.T) {
	indexer := newFakeIndexer(t, xmlResponse(`<caps><searching><tv-search available="yes"/><movie-search available="yes"/></searching></caps>`))
	client, _, _ := newTestClient(t, indexer.server.URL)

	gotCaps, err := client.Caps(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if gotValue := strings.Join(gotCaps.Modes["tvsearch"], ","); gotValue != "q,rid,season,ep" {
		t.Errorf("gotValue tvsearch %q", gotValue)
	}
	if gotValue := strings.Join(gotCaps.Modes["movie"], ","); gotValue != "q,imdbid" {
		t.Errorf("gotValue movie %q", gotValue)
	}
}

func TestNew_refusesInvalidEndpoint(t *testing.T) {
	cases := map[string]string{
		"not http":        "ftp://indexer.example.com/api?apikey=" + testAPIKey,
		"no host":         "http:///api?apikey=" + testAPIKey,
		"fragment":        "http://indexer.example.com/api?apikey=" + testAPIKey + "#frag",
		"userinfo":        "http://user:" + testAPIKey + "@indexer.example.com/api",
		"extra parameter": "http://indexer.example.com/api?apikey=" + testAPIKey + "&passkey=" + testAPIKey,
		"unparsable":      "http://indexer.example.com/%zz?apikey=" + testAPIKey,
	}
	for name, rawURL := range cases {
		t.Run(name, func(t *testing.T) {
			_, gotErr := New(Endpoint{Name: "demo", URL: rawURL}, Deps{})
			if gotErr == nil || strings.Contains(gotErr.Error(), testAPIKey) {
				t.Fatalf("gotErr = %v", gotErr)
			}
		})
	}
}

func TestClient_textFormsNeverShowTheKey(t *testing.T) {
	indexer := newFakeIndexer(t, xmlResponse(fixture(t, "search-movie.xml")))
	client, _, _ := newTestClient(t, indexer.server.URL)
	endpoint := endpointFor(indexer.server.URL)
	results, err := client.Search(context.Background(), Query{Mode: "movie", IMDBID: "tt0000001"})
	if err != nil {
		t.Fatal(err)
	}

	var logged bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logged, nil))
	logger.Info("values", "client", client, "endpoint", endpoint, "item", results.Items[0], "results", results)
	endpointJSON, err := json.Marshal(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	resultsJSON, err := json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	renderings := []string{
		fmt.Sprintf("%v %+v %#v %s", client, client, client, client),
		fmt.Sprintf("%v %+v %#v %s", endpoint, endpoint, endpoint, endpoint),
		fmt.Sprintf("%v %+v %#v", results, results, results),
		fmt.Sprintf("%v %+v %#v", results.Items[0], results.Items[0], results.Items[0]),
		logged.String(), string(endpointJSON), string(resultsJSON),
	}
	for _, rendering := range renderings {
		for _, secret := range []string{testAPIKey, "fake-passkey-0000", "/dl/"} {
			if strings.Contains(rendering, secret) {
				t.Errorf("%q leaks in %s", secret, rendering)
			}
		}
	}
	if !strings.Contains(renderings[0], "demo") {
		t.Errorf("client rendering lost its name: %s", renderings[0])
	}
}

func TestCaps_concurrentCallsShareOneRequest(t *testing.T) {
	caps := fixture(t, "caps.xml")
	indexer := newFakeIndexer(t, func(writer http.ResponseWriter, request *http.Request) {
		time.Sleep(50 * time.Millisecond)
		xmlResponse(caps)(writer, request)
	})
	client, _, _ := newTestClient(t, indexer.server.URL)
	const callers = 8

	var group sync.WaitGroup
	for range callers {
		group.Go(func() {
			if _, err := client.Caps(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()

	if gotValue := indexer.requestCount(); gotValue != 1 {
		t.Fatalf("expectedValue 1 shared request, gotValue %d", gotValue)
	}
}

func TestCaps_cachedCapsAnswersOnlyFromAFreshCacheWithoutRequest(t *testing.T) {
	indexer := newFakeIndexer(t, xmlResponse(fixture(t, "caps.xml")))
	client, clock, _ := newTestClient(t, indexer.server.URL)
	if _, cached := client.CachedCaps(); cached {
		t.Fatal("expectedValue no cached caps before the first request")
	}
	if _, err := client.Caps(context.Background()); err != nil {
		t.Fatal(err)
	}

	gotCaps, cached := client.CachedCaps()

	if !cached || len(gotCaps.Modes) != 3 || indexer.requestCount() != 1 {
		t.Fatalf("gotValue cached %v modes %v requests %d, expectedValue the cached caps without request", cached, gotCaps.Modes, indexer.requestCount())
	}
	gotCaps.Modes["search"][0] = "mutated"
	if again, _ := client.CachedCaps(); again.Modes["search"][0] != "q" {
		t.Fatal("expectedValue the cache protected from the caller")
	}
	clock.advance(24 * time.Hour)
	if _, cached := client.CachedCaps(); cached {
		t.Fatal("expectedValue an expired cache reported as absent")
	}
}
