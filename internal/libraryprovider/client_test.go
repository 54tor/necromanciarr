package libraryprovider

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"necromanciarr/internal/config"
)

const testToken = "test-token-value"

var (
	hashA = strings.Repeat("a", 40)
	hashB = strings.Repeat("b", 40)
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := New(server.URL+"/api/v1", testToken)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func encode(text string) string { return base64.StdEncoding.EncodeToString([]byte(text)) }

func releaseJSON(infohash string) string {
	return fmt.Sprintf(`{"infohash":%q,"name":"n","totalSize":10,"files":[{"path":"a.mkv","size":10}]}`, infohash)
}

func TestNew_rejectsEmptyTokenAndBaseUrl(t *testing.T) {
	for name, arguments := range map[string][2]string{
		"empty base":  {"", testToken},
		"empty token": {"https://example.com", ""},
		"no scheme":   {"example.com/api", testToken},
		"fragment":    {"https://example.com/#x", testToken},
	} {
		if _, err := New(arguments[0], arguments[1]); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestNewFromConfig_usesLibraryApiAndProviderToken(t *testing.T) {
	client, err := NewFromConfig(config.Config{LibraryAPI: "https://example.com/api", LibraryProviderToken: testToken})
	if err != nil || client.token != testToken || client.base.Host != "example.com" {
		t.Fatalf("gotClient = %+v, gotErr = %v", client, err)
	}
}

func TestSearch_sendsRepeatedSizeParameterAndOptionalTracker(t *testing.T) {
	var gotQueries []string
	client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
		if gotAuth := request.Header.Get("Authorization"); gotAuth != "Bearer "+testToken {
			t.Errorf("gotAuth = %q", gotAuth)
		}
		if request.URL.Path != "/api/v1/v1/releases" {
			t.Errorf("gotPath = %q", request.URL.Path)
		}
		gotQueries = append(gotQueries, request.URL.RawQuery)
		_, _ = fmt.Fprint(writer, `{"items":[],"next":null}`)
	})
	if _, err := client.Search(context.Background(), Query{TMDBID: 7, Sizes: []int64{5, 6}}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Search(context.Background(), Query{IMDBID: "tt9", Text: "x y", Tracker: "trk"}); err != nil {
		t.Fatal(err)
	}
	expectedValues := []string{"limit=100&size=5&size=6&tmdb=7", "imdb=tt9&limit=100&q=x+y&tracker=trk"}
	if fmt.Sprint(gotQueries) != fmt.Sprint(expectedValues) {
		t.Fatalf("gotValue %v, expectedValue %v", gotQueries, expectedValues)
	}
}

func TestSearch_followsCursorAsOpaqueParameter(t *testing.T) {
	client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("cursor") == "c 2/+" {
			_, _ = fmt.Fprintf(writer, `{"items":[%s],"next":null}`, releaseJSON(hashB))
			return
		}
		_, _ = fmt.Fprintf(writer, `{"items":[%s],"next":"c 2/+"}`, releaseJSON(hashA))
	})
	gotReleases, err := client.Search(context.Background(), Query{TMDBID: 1})
	if err != nil || len(gotReleases) != 2 || gotReleases[0].Infohash != hashA || gotReleases[1].Infohash != hashB {
		t.Fatalf("gotReleases = %+v, gotErr = %v", gotReleases, err)
	}
}

func TestSearch_neverFollowsNextAsUrl(t *testing.T) {
	var foreignHits atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { foreignHits.Add(1) }))
	t.Cleanup(foreign.Close)
	client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("cursor") != foreign.URL+"/steal" {
			_, _ = fmt.Fprintf(writer, `{"items":[],"next":%q}`, foreign.URL+"/steal")
			return
		}
		_, _ = fmt.Fprint(writer, `{"items":[],"next":null}`)
	})
	if _, err := client.Search(context.Background(), Query{Text: "film"}); err != nil {
		t.Fatal(err)
	}
	if foreignHits.Load() != 0 {
		t.Fatalf("foreign host contacted %d times", foreignHits.Load())
	}
}

func TestSearch_stopsAtMaxPages(t *testing.T) {
	var requestCount atomic.Int32
	client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
		requestCount.Add(1)
		_, _ = fmt.Fprint(writer, `{"items":[],"next":"more"}`)
	})
	if _, err := client.Search(context.Background(), Query{Text: "film", MaxPages: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Search(context.Background(), Query{Text: "film"}); err != nil {
		t.Fatal(err)
	}
	if expectedValue, gotValue := int32(3+defaultMaxPages), requestCount.Load(); gotValue != expectedValue {
		t.Fatalf("expectedValue %d requests, gotValue %d", expectedValue, gotValue)
	}
}

func TestSearch_chunksMoreThan50SizesAndDeduplicates(t *testing.T) {
	var sizeCounts []int
	client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
		sizeCounts = append(sizeCounts, len(request.URL.Query()["size"]))
		_, _ = fmt.Fprintf(writer, `{"items":[%s],"next":null}`, releaseJSON(hashA))
	})
	sizes := make([]int64, 101)
	gotReleases, err := client.Search(context.Background(), Query{Sizes: sizes})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(sizeCounts) != "[50 50 1]" || len(gotReleases) != 1 {
		t.Fatalf("gotSizeCounts = %v, gotReleases = %d", sizeCounts, len(gotReleases))
	}
}

func TestSearch_rejectsInvalidInfohashInResponse(t *testing.T) {
	client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
		_, _ = fmt.Fprint(writer, `{"items":[{"infohash":"zz","totalSize":1,"files":[]}],"next":null}`)
	})
	if _, err := client.Search(context.Background(), Query{Text: "film"}); !errors.Is(err, ErrBadResponse) {
		t.Fatalf("gotErr = %v", err)
	}
}

func TestRelease_returnsFullFileList(t *testing.T) {
	client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/v1/releases/"+hashA || request.URL.Query().Get("tracker") != "trk" {
			t.Errorf("gotURL = %s", request.URL)
		}
		_, _ = fmt.Fprint(writer, `{"infohash":"`+strings.ToUpper(hashA)+`","tracker":"trk","totalSize":30,"tmdb":4,"files":[{"path":"a/b.mkv","size":10},{"path":"c.nfo","size":20}]}`)
	})
	gotRelease, err := client.Release(context.Background(), hashA, "trk")
	if err != nil || gotRelease.Infohash != hashA || len(gotRelease.Files) != 2 || gotRelease.TMDBID != 4 {
		t.Fatalf("gotRelease = %+v, gotErr = %v", gotRelease, err)
	}
}

func TestTorrentAndNfos_omitTrackerWhenEmpty(t *testing.T) {
	client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.RawQuery != "" {
			t.Errorf("gotQuery = %q on %s", request.URL.RawQuery, request.URL.Path)
		}
		if strings.HasSuffix(request.URL.Path, "/nfos") {
			_, _ = fmt.Fprint(writer, `{"items":[]}`)
			return
		}
		_, _ = fmt.Fprint(writer, "d4:infod4:name1:aee")
	})
	if _, err := client.Torrent(context.Background(), hashA, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Nfos(context.Background(), hashA, ""); err != nil {
		t.Fatal(err)
	}
}

func TestNfos_doesNotRequireTracker(t *testing.T) {
	client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Has("tracker") {
			t.Errorf("gotQuery = %q", request.URL.RawQuery)
		}
		_, _ = fmt.Fprint(writer, `{"items":[{"label":"a","data":"`+encode("x")+`"}]}`)
	})
	gotNfos, err := client.Nfos(context.Background(), hashA, "")
	if err != nil || len(gotNfos) != 1 {
		t.Fatalf("gotNfos = %+v, gotErr = %v", gotNfos, err)
	}
}

func TestNfos_decodesStripsWhitespaceAndDeduplicates(t *testing.T) {
	client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
		spaced := encode("first")[:4] + "\r\n " + encode("first")[4:]
		_, _ = fmt.Fprintf(writer, `{"items":[{"label":"canon","data":%q},{"label":"dup","data":%q},{"label":"part","partial":true,"data":%q}]}`,
			spaced, encode("first"), encode("second"))
	})
	gotNfos, err := client.Nfos(context.Background(), hashA, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(gotNfos) != 2 || gotNfos[0].Label != "canon" || string(gotNfos[0].Data) != "first" || !gotNfos[1].Partial || string(gotNfos[1].Data) != "second" {
		t.Fatalf("gotNfos = %+v", gotNfos)
	}
}

func TestNfos_cachesNotFoundForTenMinutes(t *testing.T) {
	var requestCount atomic.Int32
	client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
		requestCount.Add(1)
		http.NotFound(writer, request)
	})
	currentTime := time.Now()
	client.cache.now = func() time.Time { return currentTime }
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := client.Nfos(context.Background(), hashA, "trk"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("gotErr = %v", err)
		}
	}
	if requestCount.Load() != 1 {
		t.Fatalf("requests = %d, expected 1", requestCount.Load())
	}
	currentTime = currentTime.Add(11 * time.Minute)
	_, _ = client.Nfos(context.Background(), hashA, "trk")
	if requestCount.Load() != 2 {
		t.Fatalf("requests after TTL = %d, expected 2", requestCount.Load())
	}
}

func TestNfos_cachesSuccessForOneHourPerTracker(t *testing.T) {
	var requestCount atomic.Int32
	client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
		requestCount.Add(1)
		_, _ = fmt.Fprint(writer, `{"items":[]}`)
	})
	currentTime := time.Now()
	client.cache.now = func() time.Time { return currentTime }
	_, _ = client.Nfos(context.Background(), hashA, "trk")
	_, _ = client.Nfos(context.Background(), hashA, "other")
	currentTime = currentTime.Add(59 * time.Minute)
	_, _ = client.Nfos(context.Background(), hashA, "trk")
	currentTime = currentTime.Add(2 * time.Minute)
	_, _ = client.Nfos(context.Background(), hashA, "trk")
	if requestCount.Load() != 3 {
		t.Fatalf("requests = %d, expected 3", requestCount.Load())
	}
}

func TestTorrent_rejectsInvalidInfohashBeforeRequest(t *testing.T) {
	var requestCount atomic.Int32
	client := newTestClient(t, func(http.ResponseWriter, *http.Request) { requestCount.Add(1) })
	for _, infohash := range []string{"", "aaaa", strings.Repeat("g", 40), hashA + "/../x"} {
		if _, err := client.Torrent(context.Background(), infohash, ""); err == nil {
			t.Errorf("infohash %q accepted", infohash)
		}
		if _, err := client.Nfos(context.Background(), infohash, ""); err == nil {
			t.Errorf("infohash %q accepted by Nfos", infohash)
		}
		if _, err := client.Release(context.Background(), infohash, ""); err == nil {
			t.Errorf("infohash %q accepted by Release", infohash)
		}
	}
	if requestCount.Load() != 0 {
		t.Fatalf("requests = %d", requestCount.Load())
	}
}

func TestTorrent_checksBencodeAndIsNotCached(t *testing.T) {
	var requestCount atomic.Int32
	client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
		requestCount.Add(1)
		_, _ = fmt.Fprint(writer, "d4:infod4:name1:aee")
	})
	for attempt := 0; attempt < 2; attempt++ {
		if gotBytes, err := client.Torrent(context.Background(), hashA, ""); err != nil || !bytes.HasPrefix(gotBytes, []byte("d")) {
			t.Fatalf("gotBytes = %q, err = %v", gotBytes, err)
		}
	}
	if requestCount.Load() != 2 {
		t.Fatalf("requests = %d, expected 2", requestCount.Load())
	}
	htmlClient := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) { _, _ = fmt.Fprint(writer, "<html>") })
	if _, err := htmlClient.Torrent(context.Background(), hashA, ""); !errors.Is(err, ErrBadResponse) {
		t.Fatalf("gotErr = %v", err)
	}
}

func TestTorrent_rejectsOverflowInsteadOfTruncating(t *testing.T) {
	client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("d"))
		chunk := make([]byte, 1<<20)
		for written := 0; written <= maxTorrentBytes; written += len(chunk) {
			_, _ = writer.Write(chunk)
		}
	})
	if _, err := client.Torrent(context.Background(), hashA, ""); !errors.Is(err, ErrBadResponse) {
		t.Fatalf("gotErr = %v", err)
	}
}

func TestErrors_mapStatusToTypedErrors(t *testing.T) {
	cases := map[int]error{408: ErrUnavailable, 429: ErrUnavailable, 401: ErrAccessDenied, 403: ErrAccessDenied, 404: ErrNotFound, 500: ErrUnavailable, 503: ErrUnavailable, 400: ErrBadResponse}
	for status, expectedErr := range cases {
		client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) { writer.WriteHeader(status) })
		_, gotErr := client.Torrent(context.Background(), hashA, "")
		if !errors.Is(gotErr, expectedErr) {
			t.Errorf("status %d: gotErr = %v, expected %v", status, gotErr, expectedErr)
		}
	}
}

func TestErrors_unreachableAndBadJSON(t *testing.T) {
	client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) { _, _ = fmt.Fprint(writer, "not json") })
	if _, err := client.Nfos(context.Background(), hashA, ""); !errors.Is(err, ErrBadResponse) {
		t.Fatalf("gotErr = %v", err)
	}
	unreachable, _ := New("http://127.0.0.1:1", testToken)
	if _, gotErr := unreachable.Nfos(context.Background(), hashA, ""); !errors.Is(gotErr, ErrUnavailable) {
		t.Fatalf("gotErr = %v", gotErr)
	}
}

func TestErrors_readProblemJsonDetailWithoutToken(t *testing.T) {
	client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/problem+json")
		writer.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprintf(writer, `{"title":"Quota","detail":"plan expired, token %s refused","status":403}`, testToken)
	})
	_, gotErr := client.Torrent(context.Background(), hashA, "")
	if !errors.Is(gotErr, ErrAccessDenied) || !strings.Contains(gotErr.Error(), "plan expired") || strings.Contains(gotErr.Error(), testToken) {
		t.Fatalf("gotErr = %v", gotErr)
	}
	if strings.Contains(gotErr.Error(), "127.0.0.1") || strings.Contains(gotErr.Error(), "/v1/") {
		t.Fatalf("error leaks the URL: %v", gotErr)
	}
}

func TestErrors_problemJsonDetailIsSanitizedAndCapped(t *testing.T) {
	client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintf(writer, `{"title":"T","detail":"line1\nline2\u001b[31m%s"}`, strings.Repeat("x", 500))
	})
	_, gotErr := client.Nfos(context.Background(), hashA, "")
	message := strings.TrimPrefix(gotErr.Error(), ErrNotFound.Error()+": ")
	if !errors.Is(gotErr, ErrNotFound) || len([]rune(message)) != maxDetailRunes || strings.ContainsAny(message, "\n\x1b") {
		t.Fatalf("gotMessage = %q (%d runes)", message, len([]rune(message)))
	}
}

func TestRetryAfter_isExposedOnRateLimit(t *testing.T) {
	client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Retry-After", "42")
		writer.WriteHeader(http.StatusTooManyRequests)
	})
	_, gotErr := client.Torrent(context.Background(), hashA, "")
	var rateLimited *RateLimitedError
	if !errors.Is(gotErr, ErrUnavailable) || !errors.As(gotErr, &rateLimited) || rateLimited.RetryAfter != 42*time.Second {
		t.Fatalf("gotErr = %v", gotErr)
	}
}

func TestRateLimited_exposesRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cases := map[string]time.Duration{
		"30":     30 * time.Second,
		"999999": time.Hour,
		"-5":     0,
		"soon":   0,
		"":       0,
		now.Add(90 * time.Second).Format(http.TimeFormat): 90 * time.Second,
		now.Add(-time.Minute).Format(http.TimeFormat):     0,
		now.Add(48 * time.Hour).Format(http.TimeFormat):   time.Hour,
	}
	for header, expectedValue := range cases {
		if gotValue := parseRetryAfter(header, now); gotValue != expectedValue {
			t.Errorf("header %q: expectedValue %v, gotValue %v", header, expectedValue, gotValue)
		}
	}
}

func TestClient_defaultTransportIgnoresProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:9")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9")
	client, err := New("http://example.com", testToken)
	if err != nil {
		t.Fatal(err)
	}
	transport, isTransport := client.httpClient.Transport.(*http.Transport)
	if !isTransport || transport.Proxy != nil {
		t.Fatalf("transport = %#v", client.httpClient.Transport)
	}
}

func TestClient_refusesRedirects(t *testing.T) {
	client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, "/elsewhere", http.StatusFound)
	})
	if _, err := client.Torrent(context.Background(), hashA, ""); !errors.Is(err, ErrBadResponse) {
		t.Fatalf("gotErr = %v", err)
	}
}

func TestClient_limitsInFlightRequests(t *testing.T) {
	var inFlight, peak atomic.Int32
	client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
		current := inFlight.Add(1)
		for {
			recorded := peak.Load()
			if current <= recorded || peak.CompareAndSwap(recorded, current) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		inFlight.Add(-1)
		_, _ = fmt.Fprint(writer, "d")
	})
	client.MaxInFlight = 2
	done := make(chan struct{})
	for range 6 {
		go func() {
			_, _ = client.Torrent(context.Background(), hashA, "")
			done <- struct{}{}
		}()
	}
	for range 6 {
		<-done
	}
	if peak.Load() > 2 {
		t.Fatalf("peak = %d, expected <= 2", peak.Load())
	}
}

func TestCache_evictsOldestWhenFull(t *testing.T) {
	store := newCache()
	currentTime := time.Now()
	store.now = func() time.Time { currentTime = currentTime.Add(time.Second); return currentTime }
	for index := 0; index <= maxCacheSize; index++ {
		store.put(fmt.Sprint(index), index, nil)
	}
	if _, found := store.get("0"); found {
		t.Fatal("oldest entry should have been evicted")
	}
	if _, found := store.get(fmt.Sprint(maxCacheSize)); !found || len(store.entries) > maxCacheSize {
		t.Fatalf("size = %d", len(store.entries))
	}
}

func TestClient_neverLogsOrEchoesToken(t *testing.T) {
	var logBuffer bytes.Buffer
	log.SetOutput(&logBuffer)
	t.Cleanup(func() { log.SetOutput(nil) })
	client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) { writer.WriteHeader(403) })
	_, searchErr := client.Search(context.Background(), Query{Text: "film"})
	_, nfoErr := client.Nfos(context.Background(), hashA, "")
	_, torrentErr := client.Torrent(context.Background(), hashA, "")
	for _, output := range []string{logBuffer.String(), fmt.Sprint(searchErr), fmt.Sprint(nfoErr), fmt.Sprint(torrentErr)} {
		if strings.Contains(output, testToken) {
			t.Fatalf("token leaked in %q", output)
		}
	}
}
