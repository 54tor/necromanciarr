package arr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
			writer.Write([]byte(radarrPayload))
		case request.URL.Path == "/api/v3/series":
			writer.Write([]byte(sonarrSeriesPayload))
		case request.URL.Path == "/api/v3/episodefile" && request.URL.Query().Get("seriesId") == "7":
			writer.Write([]byte(sonarrEpisodeFilesPayload))
		case request.URL.Path == "/api/v3/episodefile":
			writer.Write([]byte("[]"))
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
