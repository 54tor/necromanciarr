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
)

const testToken = "test-token-value"

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

func TestNew_rejectsEmptyBaseURL(t *testing.T) {
	if _, err := New("", testToken); err == nil {
		t.Fatal("expected an error for an empty base URL")
	}
}

func TestSearch_followsCursorPagination(t *testing.T) {
	var requestCount atomic.Int32
	client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
		requestCount.Add(1)
		if gotAuth := request.Header.Get("Authorization"); gotAuth != "Bearer "+testToken {
			t.Errorf("gotAuth = %q", gotAuth)
		}
		if gotQuery := request.URL.Query().Get("q"); gotQuery != "tmdb:12345" {
			t.Errorf("gotQuery = %q", gotQuery)
		}
		if request.URL.Query().Get("page") == "2" {
			fmt.Fprint(writer, `{"member":[{"totalSize":20,"files":[{"path":"b.mkv","size":20}],"torrents":[{"name":"trk","infoHash":"bbbb"}]}],"view":{}}`)
			return
		}
		fmt.Fprint(writer, `{"member":[{"totalSize":10,"files":[{"path":"a.mkv","size":10}],"torrents":[{"name":"trk","infoHash":"aaaa"}]}],"view":{"next":"/api/v1/releases?q=tmdb%3A12345&limit=100&page=2"}}`)
	})
	gotReleases, err := client.Search(context.Background(), Query{TMDBID: 12345})
	if err != nil {
		t.Fatal(err)
	}
	if len(gotReleases) != 2 || gotReleases[0].InfoHash != "aaaa" || gotReleases[1].Files[0].Path != "b.mkv" || gotReleases[1].TotalSize != 20 {
		t.Fatalf("gotReleases = %+v", gotReleases)
	}
	if requestCount.Load() != 2 {
		t.Fatalf("requests = %d", requestCount.Load())
	}
}

func TestSearch_stopsAtPageCap(t *testing.T) {
	var requestCount atomic.Int32
	client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
		requestCount.Add(1)
		fmt.Fprint(writer, `{"member":[],"view":{"next":"/api/v1/releases?page=99"}}`)
	})
	if _, err := client.Search(context.Background(), Query{Text: "film", MaxPages: 3}); err != nil {
		t.Fatal(err)
	}
	if requestCount.Load() != 3 {
		t.Fatalf("requests = %d, expected 3", requestCount.Load())
	}
}

func TestSearch_rejectsForeignPaginationHost(t *testing.T) {
	client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
		fmt.Fprint(writer, `{"member":[],"view":{"next":"https://other.example.com/steal"}}`)
	})
	if _, err := client.Search(context.Background(), Query{Text: "film"}); !errors.Is(err, ErrBadResponse) {
		t.Fatalf("gotErr = %v", err)
	}
}

func TestNfos_decodesAndDeduplicates(t *testing.T) {
	client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/torrents/trk/aaaa/nfos" {
			t.Errorf("path = %q", request.URL.Path)
		}
		fmt.Fprintf(writer, `{"nfos":{"trk":[{"nfo":%q,"partial":false},{"nfo":%q,"partial":true}]},"releaseNfo":%q}`,
			encode("first"), encode("second"), encode("first"))
	})
	gotNfos, err := client.Nfos(context.Background(), "trk", "aaaa")
	if err != nil {
		t.Fatal(err)
	}
	if len(gotNfos) != 2 || string(gotNfos[0].Data) != "first" || gotNfos[1].Label != "api:trk#2(partial)" {
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
		if _, err := client.Nfos(context.Background(), "trk", "aaaa"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("gotErr = %v", err)
		}
	}
	if requestCount.Load() != 1 {
		t.Fatalf("requests = %d, expected 1", requestCount.Load())
	}
	currentTime = currentTime.Add(11 * time.Minute)
	client.Nfos(context.Background(), "trk", "aaaa")
	if requestCount.Load() != 2 {
		t.Fatalf("requests after TTL = %d, expected 2", requestCount.Load())
	}
}

func TestNfos_cachesSuccessForOneHour(t *testing.T) {
	var requestCount atomic.Int32
	client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
		requestCount.Add(1)
		fmt.Fprintf(writer, `{"releaseNfo":%q}`, encode("x"))
	})
	currentTime := time.Now()
	client.cache.now = func() time.Time { return currentTime }
	client.Nfos(context.Background(), "trk", "aaaa")
	currentTime = currentTime.Add(59 * time.Minute)
	client.Nfos(context.Background(), "trk", "aaaa")
	currentTime = currentTime.Add(2 * time.Minute)
	client.Nfos(context.Background(), "trk", "aaaa")
	if requestCount.Load() != 2 {
		t.Fatalf("requests = %d, expected 2", requestCount.Load())
	}
}

func TestErrors_mapStatusToTypedErrors(t *testing.T) {
	cases := map[int]error{408: ErrUnavailable, 429: ErrUnavailable, 401: ErrAccessDenied, 403: ErrAccessDenied, 404: ErrNotFound, 500: ErrUnavailable, 503: ErrUnavailable}
	for status, expectedErr := range cases {
		client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) { writer.WriteHeader(status) })
		_, gotErr := client.Torrent(context.Background(), "trk", "aaaa")
		if !errors.Is(gotErr, expectedErr) {
			t.Errorf("status %d: gotErr = %v, expected %v", status, gotErr, expectedErr)
		}
		if gotErr != nil && strings.Contains(gotErr.Error(), testToken) {
			t.Errorf("status %d: error leaks the token", status)
		}
	}
}

func TestErrors_unreachableAndBadJSON(t *testing.T) {
	client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) { fmt.Fprint(writer, "not json") })
	if _, err := client.Nfos(context.Background(), "trk", "aaaa"); !errors.Is(err, ErrBadResponse) {
		t.Fatalf("gotErr = %v", err)
	}
	unreachable, _ := New("http://127.0.0.1:1", testToken)
	_, gotErr := unreachable.Nfos(context.Background(), "trk", "aaaa")
	if !errors.Is(gotErr, ErrUnavailable) {
		t.Fatalf("gotErr = %v", gotErr)
	}
}

func TestTorrent_requestsNoAnnounceAndChecksBencode(t *testing.T) {
	client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/torrents/trk/aaaa/download" || request.URL.Query().Get("noAnnounce") != "1" {
			t.Errorf("url = %s", request.URL)
		}
		fmt.Fprint(writer, "d4:infod4:name1:aee")
	})
	gotBytes, err := client.Torrent(context.Background(), "trk", "aaaa")
	if err != nil || !bytes.HasPrefix(gotBytes, []byte("d")) {
		t.Fatalf("gotBytes = %q, err = %v", gotBytes, err)
	}
	htmlClient := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) { fmt.Fprint(writer, "<html>") })
	if _, err := htmlClient.Torrent(context.Background(), "trk", "aaaa"); !errors.Is(err, ErrBadResponse) {
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
		fmt.Fprint(writer, "d")
	})
	client.MaxInFlight = 2
	done := make(chan struct{})
	for index := 0; index < 6; index++ {
		go func() {
			client.Torrent(context.Background(), "trk", "aaaa")
			done <- struct{}{}
		}()
	}
	for index := 0; index < 6; index++ {
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

func TestClient_neverLogsToken(t *testing.T) {
	var logBuffer bytes.Buffer
	log.SetOutput(&logBuffer)
	t.Cleanup(func() { log.SetOutput(nil) })
	client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) { writer.WriteHeader(403) })
	_, searchErr := client.Search(context.Background(), Query{Text: "film"})
	_, nfoErr := client.Nfos(context.Background(), "trk", "aaaa")
	for _, output := range []string{logBuffer.String(), fmt.Sprint(searchErr), fmt.Sprint(nfoErr)} {
		if strings.Contains(output, testToken) {
			t.Fatalf("token leaked in %q", output)
		}
	}
}

func TestNfos_ordersSlugsDeterministically(t *testing.T) {
	client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
		fmt.Fprintf(writer, `{"nfos":{"zeta":[{"nfo":%q}],"alpha":[{"nfo":%q}],"mid":[{"nfo":%q}]}}`,
			encode("same"), encode("same"), encode("other"))
	})
	for attempt := 0; attempt < 20; attempt++ {
		client.cache = newCache()
		gotNfos, err := client.Nfos(context.Background(), "trk", "aaaa")
		if err != nil {
			t.Fatal(err)
		}
		if len(gotNfos) != 2 || gotNfos[0].Label != "api:alpha" || gotNfos[1].Label != "api:mid" {
			t.Fatalf("attempt %d: gotNfos = %+v", attempt, gotNfos)
		}
	}
}
