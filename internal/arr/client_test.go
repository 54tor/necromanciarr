package arr

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const radarrPayload = `[
 {"id":1,"title":"Film","tmdbId":603,"hasFile":true,"movieFile":{"id":9,"path":"/movies/Film (1999)/Film.mkv"}},
 {"id":2,"title":"Wanted","tmdbId":604,"hasFile":false}
]`

const sonarrSeriesPayload = `[{"id":7,"title":"Show","tvdbId":81189},{"id":8,"title":"Empty","tvdbId":5}]`

const sonarrEpisodeFilesPayload = `[
 {"id":1,"seriesId":7,"path":"/tv/Show/S01E01.mkv"},
 {"id":2,"seriesId":7,"path":"/tv/Show/S01E02.mkv"}
]`

func newArrServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Api-Key") != "secret-key" {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case request.URL.Path == "/api/v3/movie":
			_, _ = writer.Write([]byte(radarrPayload))
		case request.URL.Path == "/api/v3/series":
			_, _ = writer.Write([]byte(sonarrSeriesPayload))
		case request.URL.Path == "/api/v3/episodefile" && request.URL.Query().Get("seriesId") == "7":
			_, _ = writer.Write([]byte(sonarrEpisodeFilesPayload))
		case request.URL.Path == "/api/v3/episodefile":
			_, _ = writer.Write([]byte("[]"))
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestItems_parsesRadarrAndSonarrPayloads(t *testing.T) {
	server := newArrServer(t)

	gotRadarr, err := New("radarr", server.URL, "secret-key").Items(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expectedRadarr := Item{Path: "/movies/Film (1999)/Film.mkv", TMDBID: 603}
	if len(gotRadarr) != 1 || gotRadarr[0] != expectedRadarr {
		t.Fatalf("expectedValue [%+v], gotValue %+v", expectedRadarr, gotRadarr)
	}

	gotSonarr, err := New("sonarr", server.URL, "secret-key").Items(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expectedSonarr := []Item{
		{Path: "/tv/Show/S01E01.mkv", TVDBID: 81189},
		{Path: "/tv/Show/S01E02.mkv", TVDBID: 81189},
	}
	if len(gotSonarr) != 2 || gotSonarr[0] != expectedSonarr[0] || gotSonarr[1] != expectedSonarr[1] {
		t.Fatalf("expectedValue %+v, gotValue %+v", expectedSonarr, gotSonarr)
	}
}

func TestItems_rejectsUnknownKind(t *testing.T) {
	if _, err := New("lidarr", "http://example.com", "k").Items(context.Background()); err == nil {
		t.Fatal("expectedValue an error for unknown kind")
	}
}

func TestItems_errorNeverContainsAPIKey(t *testing.T) {
	server := newArrServer(t)

	_, err := New("radarr", server.URL, "wrong-secret").Items(context.Background())

	if err == nil {
		t.Fatal("expectedValue an error")
	}
	if strings.Contains(err.Error(), "wrong-secret") {
		t.Fatalf("api key leaked in error: %v", err)
	}
}

func newStatusServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v3/system/status" || request.Header.Get("X-Api-Key") != "secret-key" {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = writer.Write([]byte(`{"version":"5.0.0"}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestArrPing_okWithValidKey(t *testing.T) {
	server := newStatusServer(t)

	if err := New("radarr", server.URL, "secret-key").Ping(context.Background()); err != nil {
		t.Fatalf("gotValue %v, expectedValue nil", err)
	}
}

func TestArrPing_wrongKeyIsAccessDenied(t *testing.T) {
	server := newStatusServer(t)

	err := New("radarr", server.URL, "wrong-secret").Ping(context.Background())

	if !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("gotValue %v, expectedValue ErrAccessDenied", err)
	}
}

func TestArrPing_timeoutReturnsErrorWithoutKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := New("radarr", server.URL, "secret-key").Ping(ctx)

	if err == nil || strings.Contains(err.Error(), "secret-key") {
		t.Fatalf("gotValue %v, expectedValue an error without the key", err)
	}
}

func TestArrPing_keyNeverAppearsInErrors(t *testing.T) {
	err := New("radarr", "http://127.0.0.1:1", "secret-key").Ping(context.Background())

	if err == nil || strings.Contains(err.Error(), "secret-key") {
		t.Fatalf("gotValue %v, expectedValue an error without the key", err)
	}
}

func TestArrClient_sendsBasicCredentialsToTheConfiguredHost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		user, password, hasBasic := request.BasicAuth()
		if !hasBasic || user != "alice" || password != "fake-password" || request.Header.Get("X-Api-Key") != "secret-key" {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = writer.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)

	err := New("radarr", server.URL, "secret-key", WithBasicAuth("alice", "fake-password")).Ping(context.Background())
	if err != nil {
		t.Fatalf("gotValue %v, expectedValue nil", err)
	}
}

func TestArrClient_withoutUserSendsNoAuthorizationHeader(t *testing.T) {
	gotAuthorization := "unset"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotAuthorization = request.Header.Get("Authorization")
		_, _ = writer.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)

	if err := New("radarr", server.URL, "secret-key", WithBasicAuth("", "")).Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotAuthorization != "" {
		t.Fatalf("gotValue %q, expectedValue no Authorization header", gotAuthorization)
	}
}

func TestArrClient_refusesRedirectsAndNeverForwardsCredentials(t *testing.T) {
	otherHostReached := false
	otherHost := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		otherHostReached = true
		_, _ = writer.Write([]byte(`{}`))
	}))
	t.Cleanup(otherHost.Close)
	redirecting := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, otherHost.URL+request.URL.Path, http.StatusFound)
	}))
	t.Cleanup(redirecting.Close)

	err := New("radarr", redirecting.URL, "secret-key", WithBasicAuth("alice", "fake-password")).Ping(context.Background())

	if !errors.Is(err, ErrRedirectRefused) {
		t.Fatalf("gotValue %v, expectedValue ErrRedirectRefused", err)
	}
	if otherHostReached {
		t.Fatal("redirect followed: credentials sent to another host")
	}
	if strings.Contains(err.Error(), "secret-key") || strings.Contains(err.Error(), "fake-password") {
		t.Fatalf("secret leaked in error: %v", err)
	}
}
