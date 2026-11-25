package arr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

const radarrPayload = `[
 {"id":1,"title":"Film","year":1999,"imdbId":"tt0133093","tmdbId":603,"hasFile":true,"movieFile":{"id":9,"path":"/movies/Film (1999)/Film.mkv"}},
 {"id":2,"title":"Wanted","tmdbId":604,"hasFile":false}
]`

const sonarrSeriesPayload = `[{"id":7,"title":"Show","year":2008,"imdbId":"tt0903747","tvdbId":81189},{"id":8,"title":"Empty","year":2010,"tvdbId":5}]`

const sonarrEpisodeFilesPayload = `[
 {"id":1,"seriesId":7,"path":"/tv/Show/S01E01.mkv"},
 {"id":2,"seriesId":7,"path":"/tv/Show/S02E03E04.mkv"},
 {"id":3,"seriesId":7,"path":"/tv/Show/Specials/S00E05.mkv"}
]`

const sonarrEpisodesPayload = `[
 {"seriesId":7,"episodeFileId":1,"seasonNumber":1,"episodeNumber":1},
 {"seriesId":7,"episodeFileId":2,"seasonNumber":2,"episodeNumber":4},
 {"seriesId":7,"episodeFileId":2,"seasonNumber":2,"episodeNumber":3},
 {"seriesId":7,"episodeFileId":3,"seasonNumber":0,"episodeNumber":5},
 {"seriesId":7,"episodeFileId":0,"seasonNumber":1,"episodeNumber":2}
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
		case request.URL.Path == "/api/v3/episode" && request.URL.Query().Get("seriesId") == "7":
			_, _ = writer.Write([]byte(sonarrEpisodesPayload))
		case request.URL.Path == "/api/v3/episodefile" || request.URL.Path == "/api/v3/episode":
			_, _ = writer.Write([]byte("[]"))
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestItems_parsesRadarrTitleYearAndIMDB(t *testing.T) {
	server := newArrServer(t)

	gotRadarr, err := New("radarr", server.URL, "secret-key").Items(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	expectedRadarr := Item{Path: "/movies/Film (1999)/Film.mkv", TMDBID: 603, IMDBID: "tt0133093", Title: "Film", Year: 1999}
	if len(gotRadarr) != 1 || gotRadarr[0] != expectedRadarr {
		t.Fatalf("expectedValue [%+v], gotValue %+v", expectedRadarr, gotRadarr)
	}
}

func TestItems_parsesSonarrSeasonAndEpisode(t *testing.T) {
	server := newArrServer(t)

	gotSonarr, err := New("sonarr", server.URL, "secret-key").Items(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	show := Item{TVDBID: 81189, IMDBID: "tt0903747", Title: "Show", Year: 2008}
	expectedSonarr := []Item{
		{Path: "/tv/Show/S01E01.mkv", Season: 1, Episode: 1},
		{Path: "/tv/Show/S02E03E04.mkv", Season: 2, Episode: 3},
		{Path: "/tv/Show/Specials/S00E05.mkv", Season: 0, Episode: 5},
	}
	for index := range expectedSonarr {
		expectedSonarr[index].TVDBID, expectedSonarr[index].IMDBID = show.TVDBID, show.IMDBID
		expectedSonarr[index].Title, expectedSonarr[index].Year = show.Title, show.Year
	}
	if !slices.Equal(gotSonarr, expectedSonarr) {
		t.Fatalf("expectedValue %+v, gotValue %+v", expectedSonarr, gotSonarr)
	}
}

func TestItems_sonarrRequestsPerSeriesStayConstant(t *testing.T) {
	var requestPaths []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestPaths = append(requestPaths, request.URL.Path)
		switch request.URL.Path {
		case "/api/v3/series":
			_, _ = writer.Write([]byte(`[{"id":1,"title":"A","tvdbId":1},{"id":2,"title":"B","tvdbId":2}]`))
		case "/api/v3/episodefile":
			_, _ = writer.Write([]byte(manyEpisodeFiles(request.URL.Query().Get("seriesId"), 40)))
		case "/api/v3/episode":
			_, _ = writer.Write([]byte(manyEpisodes(request.URL.Query().Get("seriesId"), 40)))
		}
	}))
	t.Cleanup(server.Close)

	items, err := New("sonarr", server.URL, "k").Items(context.Background())

	if err != nil || len(items) != 80 {
		t.Fatalf("gotValue %d items (err %v), expectedValue 80", len(items), err)
	}
	expectedRequests := 1 + 2*2
	if len(requestPaths) != expectedRequests {
		t.Fatalf("expectedValue %d requests, gotValue %d: %v", expectedRequests, len(requestPaths), requestPaths)
	}
}

func manyEpisodeFiles(seriesID string, count int) string {
	var entries []string
	for number := 1; number <= count; number++ {
		entries = append(entries, fmt.Sprintf(`{"id":%d,"path":"/tv/%s/E%d.mkv"}`, number, seriesID, number))
	}
	return "[" + strings.Join(entries, ",") + "]"
}

func manyEpisodes(_ string, count int) string {
	var entries []string
	for number := 1; number <= count; number++ {
		entries = append(entries, fmt.Sprintf(`{"episodeFileId":%d,"seasonNumber":1,"episodeNumber":%d}`, number, number))
	}
	return "[" + strings.Join(entries, ",") + "]"
}

func TestItems_sanitisesHostileTitles(t *testing.T) {
	longTitle := strings.Repeat("é", 1000)
	payload := `[{"title":"A\u0000B\u001b[31m\u009bC\u202eD\u200bE\u007f","year":1,"tmdbId":1,"movieFile":{"path":"/m/a.mkv"}},` +
		`{"title":"` + longTitle + `","tmdbId":2,"movieFile":{"path":"/m/b.mkv"}}]`
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(payload))
	}))
	t.Cleanup(server.Close)

	items, err := New("radarr", server.URL, "k").Items(context.Background())
	if err != nil || len(items) != 2 {
		t.Fatalf("gotValue %+v (err %v)", items, err)
	}

	if items[0].Title != "AB[31mCDE" {
		t.Fatalf("expectedValue a title without control or bidi characters, gotValue %q", items[0].Title)
	}
	if got := utf8.RuneCountInString(items[1].Title); got != 256 {
		t.Fatalf("expectedValue 256 runes, gotValue %d", got)
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

func TestItems_refusesAResponseBodyOverTheCap(t *testing.T) {
	server := newArrServer(t)
	client := New("radarr", server.URL, "secret-key")
	client.maxBodyBytes = int64(len(radarrPayload) / 2)

	_, err := client.Items(context.Background())

	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("gotValue %v, expectedValue ErrResponseTooLarge", err)
	}
}
