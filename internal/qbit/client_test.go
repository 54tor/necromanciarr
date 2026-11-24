package qbit_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"necromanciarr/internal/config"
	"necromanciarr/internal/qbit"
	"necromanciarr/internal/repair"
	"necromanciarr/internal/torrentcheck"
)

const (
	testPollInterval = time.Millisecond
	testGuardTimeout = 2 * time.Second
	torrentRoot      = "/home/u/torrents"
	moviePath        = "/home/u/torrents/Movie/movie.mkv"
	proxyPrefix      = "/proxy/fake-proxy-key"
)

func newTestClient(t *testing.T, baseURL, user, password string, options ...qbit.Option) *qbit.Client {
	t.Helper()
	options = append(options, qbit.WithPollInterval(testPollInterval))
	client, err := qbit.New(baseURL, user, password, options...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

func movieTorrent(progressAfter float64) *fakeTorrent {
	return &fakeTorrent{
		Name: "Movie", SavePath: torrentRoot, ContentPath: torrentRoot + "/Movie", State: "uploading", Progress: 1,
		Size: 42, ProgressAfter: progressAfter, checkPollsTotal: 3,
		Files: []fakeFile{{Name: "Movie/movie.mkv", Size: 40, Progress: 1}, {Name: "Movie/movie.nfo", Size: 2, Progress: 1}},
	}
}

func TestAdd_worksThroughBasePathProxyWithoutCredentials(t *testing.T) {
	fake := newFakeQbit(webAPIVersion5)
	fake.prefix = proxyPrefix
	server := fake.start(t)
	client := newTestClient(t, server.URL+proxyPrefix, "", "")

	err := client.Add(context.Background(), []byte("d4:infoe"), "/home/u/torrents", qbit.AddOptions{})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if fake.loginCount() != 0 {
		t.Fatalf("expected no login without credentials, got %d", fake.loginCount())
	}
	if len(fake.addsSnapshot()) != 1 {
		t.Fatalf("expected one add through the prefix, got %d", len(fake.addsSnapshot()))
	}
}

func TestAdd_sendsSavePathAndTorrentMultipart(t *testing.T) {
	fake := newFakeQbit(webAPIVersion4)
	server := fake.start(t)
	client := newTestClient(t, server.URL, "", "")
	expectedTorrent := []byte("d4:infod4:name5:Moviee")

	if err := client.Add(context.Background(), expectedTorrent, "/home/u/torrents", qbit.AddOptions{}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	gotAdd := fake.addsSnapshot()[0]
	if string(gotAdd.TorrentData) != string(expectedTorrent) {
		t.Fatalf("torrent part: expected %q, got %q", expectedTorrent, gotAdd.TorrentData)
	}
	expectedFields := map[string]string{
		"savepath": "/home/u/torrents", "autoTMM": "false", "contentLayout": "Original",
		"paused": "true", "stopped": "true", "skip_checking": "false",
	}
	for fieldName, expectedValue := range expectedFields {
		if gotValue := gotAdd.Fields[fieldName]; gotValue != expectedValue {
			t.Errorf("field %s: expected %q, got %q", fieldName, expectedValue, gotValue)
		}
	}
	if _, hasRootFolder := gotAdd.Fields["root_folder"]; hasRootFolder {
		t.Error("root_folder must not be sent: the layout is already on disk")
	}
}

func TestAdd_sendsCategoryTagsStartAndSkipChecking(t *testing.T) {
	fake := newFakeQbit(webAPIVersion4)
	server := fake.start(t)
	client := newTestClient(t, server.URL, "", "")
	options := qbit.AddOptions{Category: "necromanciarr", Tags: []string{"revived", "review"}, Start: true, SkipChecking: true}

	if err := client.Add(context.Background(), []byte("d4:infoe"), "/home/u/torrents", options); err != nil {
		t.Fatalf("Add: %v", err)
	}
	expectedFields := map[string]string{
		"category": "necromanciarr", "tags": "revived,review", "paused": "false", "stopped": "false", "skip_checking": "true",
	}
	for fieldName, expectedValue := range expectedFields {
		if gotValue := fake.addsSnapshot()[0].Fields[fieldName]; gotValue != expectedValue {
			t.Errorf("field %s: expected %q, got %q", fieldName, expectedValue, gotValue)
		}
	}
}

func TestAdd_neverStartsTheTorrentByDefault(t *testing.T) {
	for _, version := range []string{webAPIVersion4, webAPIVersion5, webAPIJSONAdd} {
		t.Run(version, func(t *testing.T) {
			fake := newFakeQbit(version)
			server := fake.start(t)
			client := newTestClient(t, server.URL, "", "")

			if err := client.Add(context.Background(), []byte("d4:infoe"), "/home/u/torrents", qbit.AddOptions{}); err != nil {
				t.Fatalf("Add: %v", err)
			}
			gotFields := fake.addsSnapshot()[0].Fields
			for _, fieldName := range []string{"paused", "stopped"} {
				if expectedValue := "true"; gotFields[fieldName] != expectedValue {
					t.Errorf("field %s: expected %q, got %q", fieldName, expectedValue, gotFields[fieldName])
				}
			}
		})
	}
}

func TestAdd_reportsRejectionAsBadResponse(t *testing.T) {
	for _, version := range []string{webAPIVersion4, webAPIJSONAdd} {
		t.Run(version, func(t *testing.T) {
			fake := newFakeQbit(version)
			fake.addRejected = true
			server := fake.start(t)
			client := newTestClient(t, server.URL, "", "")

			err := client.Add(context.Background(), []byte("d4:infoe"), "/home/u/torrents", qbit.AddOptions{})
			if !errors.Is(err, qbit.ErrBadResponse) {
				t.Fatalf("expected ErrBadResponse, got %v", err)
			}
		})
	}
}

func TestAdd_reauthenticatesOnceWhenSessionExpires(t *testing.T) {
	fake := newFakeQbit(webAPIVersion4)
	fake.requireAuth = true
	server := fake.start(t)
	client := newTestClient(t, server.URL, fakeUser, fakePassword)
	if _, err := client.Torrents(context.Background()); err != nil {
		t.Fatalf("Torrents: %v", err)
	}
	fake.update(func(locked *fakeQbit) { locked.sessionExpired = true })

	if err := client.Add(context.Background(), []byte("d4:infoe"), "/home/u/torrents", qbit.AddOptions{}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if expectedValue, gotValue := 2, fake.loginCount(); gotValue != expectedValue {
		t.Fatalf("expected %d logins, got %d", expectedValue, gotValue)
	}
	if expectedValue, gotValue := 1, len(fake.addsSnapshot()); gotValue != expectedValue {
		t.Fatalf("expected %d add, got %d", expectedValue, gotValue)
	}
}

func TestLogin_failsWithTypedErrorOnBadCredentials(t *testing.T) {
	fake := newFakeQbit(webAPIVersion4)
	fake.requireAuth = true
	fake.loginsRejected = true
	server := fake.start(t)
	client := newTestClient(t, server.URL, fakeUser, fakePassword)

	_, err := client.Torrents(context.Background())
	if !errors.Is(err, qbit.ErrAuthFailed) {
		t.Fatalf("expected ErrAuthFailed, got %v", err)
	}
	if fake.loginCount() != 1 {
		t.Fatalf("expected a single login attempt, got %d", fake.loginCount())
	}
}

func TestRequests_carryRefererAndOriginOfBaseURL(t *testing.T) {
	fake := newFakeQbit(webAPIVersion4)
	fake.prefix = proxyPrefix
	server := fake.start(t)
	client := newTestClient(t, server.URL+proxyPrefix, "", "")

	if _, err := client.Torrents(context.Background()); err != nil {
		t.Fatalf("Torrents: %v", err)
	}
	if expectedReferer := server.URL + proxyPrefix; fake.headers().referers[0] != expectedReferer {
		t.Errorf("Referer: expected %q, got %q", expectedReferer, fake.headers().referers[0])
	}
	if fake.headers().origins[0] != server.URL {
		t.Errorf("Origin: expected %q, got %q", server.URL, fake.headers().origins[0])
	}
}

func TestPauseAndResume_useVerbsOfTheWebAPIVersion(t *testing.T) {
	cases := []struct {
		version         string
		expectedActions []string
	}{
		{webAPIVersion4, []string{"pause:" + hashMovie, "resume:" + hashMovie}},
		{webAPIVersion5, []string{"stop:" + hashMovie, "start:" + hashMovie}},
	}
	for _, testCase := range cases {
		t.Run(testCase.version, func(t *testing.T) {
			fake := newFakeQbit(testCase.version)
			fake.torrents[hashMovie] = movieTorrent(1)
			server := fake.start(t)
			client := newTestClient(t, server.URL, "", "")

			if err := client.Pause(context.Background(), hashMovie); err != nil {
				t.Fatalf("Pause: %v", err)
			}
			if err := client.Resume(context.Background(), hashMovie); err != nil {
				t.Fatalf("Resume: %v", err)
			}
			if gotActions := fake.snapshotActions(); !slices.Equal(gotActions, testCase.expectedActions) {
				t.Fatalf("expected %v, got %v", testCase.expectedActions, gotActions)
			}
		})
	}
}

func TestProgress_parsesFraction(t *testing.T) {
	fake := newFakeQbit(webAPIVersion4)
	torrent := movieTorrent(1)
	torrent.Progress = 0.625
	fake.torrents[hashMovie] = torrent
	server := fake.start(t)
	client := newTestClient(t, server.URL, "", "")

	gotValue, err := client.Progress(context.Background(), hashMovie)
	if err != nil {
		t.Fatalf("Progress: %v", err)
	}
	if expectedValue := 0.625; gotValue != expectedValue {
		t.Fatalf("expected %v, got %v", expectedValue, gotValue)
	}
	if _, err := client.Progress(context.Background(), hashPack); !errors.Is(err, qbit.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for an unknown hash, got %v", err)
	}
}

func TestTorrentsAndFiles_parseFields(t *testing.T) {
	fake := newFakeQbit(webAPIVersion4)
	torrent := movieTorrent(1)
	torrent.Category, torrent.Tags = "necromanciarr", "revived, review"
	fake.torrents[hashMovie] = torrent
	server := fake.start(t)
	client := newTestClient(t, server.URL, "", "")

	gotTorrents, err := client.Torrents(context.Background())
	if err != nil {
		t.Fatalf("Torrents: %v", err)
	}
	expectedTorrent := qbit.TorrentInfo{
		Hash: hashMovie, Name: "Movie", SavePath: torrentRoot, ContentPath: torrentRoot + "/Movie", Category: "necromanciarr",
		Tags: []string{"revived", "review"}, State: "uploading", Progress: 1, Size: 42,
	}
	if len(gotTorrents) != 1 || fmt.Sprint(gotTorrents[0]) != fmt.Sprint(expectedTorrent) {
		t.Fatalf("expected %+v, got %+v", expectedTorrent, gotTorrents)
	}
	gotFiles, err := client.Files(context.Background(), hashMovie)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	expectedFiles := []qbit.TorrentFile{{Name: "Movie/movie.mkv", Size: 40, Progress: 1}, {Name: "Movie/movie.nfo", Size: 2, Progress: 1}}
	if !slices.Equal(gotFiles, expectedFiles) {
		t.Fatalf("expected %+v, got %+v", expectedFiles, gotFiles)
	}
	if _, err := client.Files(context.Background(), hashPack); !errors.Is(err, qbit.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestExportTorrent_returnsRawBytesOrTypedErrors(t *testing.T) {
	fake := newFakeQbit(webAPIVersion4)
	fake.torrents[hashMovie] = movieTorrent(1)
	server := fake.start(t)
	client := newTestClient(t, server.URL, "", "")

	gotValue, err := client.ExportTorrent(context.Background(), hashMovie)
	if err != nil {
		t.Fatalf("ExportTorrent: %v", err)
	}
	if !strings.Contains(string(gotValue), hashMovie) {
		t.Fatalf("unexpected export %q", gotValue)
	}
	if _, err := client.ExportTorrent(context.Background(), hashPack); !errors.Is(err, qbit.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for an unknown hash, got %v", err)
	}
}

func TestExportTorrent_isUnsupportedBeforeQbittorrent45(t *testing.T) {
	fake := newFakeQbit(webAPIOld)
	fake.torrents[hashMovie] = movieTorrent(1)
	server := fake.start(t)
	client := newTestClient(t, server.URL, "", "")

	if _, err := client.ExportTorrent(context.Background(), hashMovie); !errors.Is(err, qbit.ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
	fakeNoMethod := newFakeQbit(webAPIVersion4)
	fakeNoMethod.forcedStatus = http.StatusMethodNotAllowed
	serverNoMethod := fakeNoMethod.start(t)
	clientNoMethod := newTestClient(t, serverNoMethod.URL, "", "")
	if _, err := clientNoMethod.ExportTorrent(context.Background(), hashMovie); !errors.Is(err, qbit.ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported on 405, got %v", err)
	}
}

func TestRecheck_rejectsHashesThatAreNotASingleInfoHash(t *testing.T) {
	fake := newFakeQbit(webAPIVersion4)
	server := fake.start(t)
	client := newTestClient(t, server.URL, "", "")

	for _, invalidHash := range []string{"all", hashMovie + "|" + hashPack, "", "zz" + hashMovie[2:]} {
		if err := client.Recheck(context.Background(), invalidHash); !errors.Is(err, qbit.ErrInvalidHash) {
			t.Errorf("hash %q: expected ErrInvalidHash, got %v", invalidHash, err)
		}
	}
	if gotActions := fake.snapshotActions(); len(gotActions) != 0 {
		t.Fatalf("no request expected, got %v", gotActions)
	}
}

func TestRecheckAndGuard_pausesThenResumesWhenCompleteAfterChecking(t *testing.T) {
	for _, version := range []string{webAPIVersion4, webAPIVersion5} {
		t.Run(version, func(t *testing.T) {
			fake := newFakeQbit(version)
			fake.torrents[hashMovie] = movieTorrent(1)
			server := fake.start(t)
			client := newTestClient(t, server.URL, "", "")

			gotResult, err := client.RecheckAndGuard(context.Background(), hashMovie, testGuardTimeout)
			if err != nil {
				t.Fatalf("RecheckAndGuard: %v", err)
			}
			expectedResult := qbit.RecheckResult{Progress: 1, Complete: true, Resumed: true}
			if gotResult != expectedResult {
				t.Fatalf("expected %+v, got %+v", expectedResult, gotResult)
			}
			gotActions := fake.snapshotActions()
			if len(gotActions) != 3 || !strings.HasPrefix(gotActions[1], "recheck:") {
				t.Fatalf("expected pause, recheck, resume; got %v", gotActions)
			}
			if gotState, _ := fake.torrentState(hashMovie); gotState != "uploading" {
				t.Fatalf("expected uploading after resume, got %s", gotState)
			}
		})
	}
}

func TestRecheckAndGuard_staysPausedWhenProgressBelowOne(t *testing.T) {
	fake := newFakeQbit(webAPIVersion5)
	fake.torrents[hashMovie] = movieTorrent(0.75)
	server := fake.start(t)
	client := newTestClient(t, server.URL, "", "")

	gotResult, err := client.RecheckAndGuard(context.Background(), hashMovie, testGuardTimeout)
	if err != nil {
		t.Fatalf("RecheckAndGuard: %v", err)
	}
	expectedResult := qbit.RecheckResult{Progress: 0.75, Complete: false, Resumed: false}
	if gotResult != expectedResult {
		t.Fatalf("expected %+v, got %+v", expectedResult, gotResult)
	}
	if gotState, _ := fake.torrentState(hashMovie); gotState != "stoppedDL" {
		t.Fatalf("expected the torrent to stay stopped, got %s", gotState)
	}
	for _, gotAction := range fake.snapshotActions() {
		if strings.HasPrefix(gotAction, "start:") || strings.HasPrefix(gotAction, "resume:") {
			t.Fatalf("an incomplete torrent must never be resumed: %v", fake.snapshotActions())
		}
	}
}

func TestRecheckAndGuard_leavesTorrentPausedOnTimeout(t *testing.T) {
	fake := newFakeQbit(webAPIVersion4)
	torrent := movieTorrent(1)
	torrent.checkPollsTotal = 1 << 30
	fake.torrents[hashMovie] = torrent
	server := fake.start(t)
	client := newTestClient(t, server.URL, "", "")

	gotResult, err := client.RecheckAndGuard(context.Background(), hashMovie, 20*time.Millisecond)
	if !errors.Is(err, qbit.ErrRecheckTimeout) {
		t.Fatalf("expected ErrRecheckTimeout, got %v", err)
	}
	if gotResult.Resumed || gotResult.Complete {
		t.Fatalf("expected neither complete nor resumed, got %+v", gotResult)
	}
}

func TestRecheckPath_returnsTypedErrorAndStaysPausedWhenIncomplete(t *testing.T) {
	fake := newFakeQbit(webAPIVersion4)
	fake.torrents[hashMovie] = movieTorrent(0.5)
	pack := movieTorrent(1)
	pack.Name, pack.ContentPath = "Pack", torrentRoot+"/Pack"
	pack.Files = []fakeFile{{Name: "Pack/other.mkv", Size: 40, Progress: 1}}
	fake.torrents[hashPack] = pack
	server := fake.start(t)
	client := newTestClient(t, server.URL, "", "")

	err := client.RecheckPath(context.Background(), moviePath)
	if !errors.Is(err, qbit.ErrIncompleteAfterRecheck) || !strings.Contains(err.Error(), hashMovie) {
		t.Fatalf("expected ErrIncompleteAfterRecheck naming %s, got %v", hashMovie, err)
	}
	if gotState, _ := fake.torrentState(hashMovie); gotState != "pausedDL" {
		t.Fatalf("expected pausedDL, got %s", gotState)
	}
	if gotState, _ := fake.torrentState(hashPack); gotState != "uploading" {
		t.Fatalf("a torrent not covering the path must be left alone, got %s", gotState)
	}
}

func TestRecheckPath_honoursCallerTimeoutAndStaysPaused(t *testing.T) {
	fake := newFakeQbit(webAPIVersion4)
	torrent := movieTorrent(1)
	torrent.checkPollsTotal = 1 << 30
	fake.torrents[hashMovie] = torrent
	server := fake.start(t)
	client := newTestClient(t, server.URL, "", "", qbit.WithRecheckTimeout(20*time.Millisecond))

	if err := client.RecheckPath(context.Background(), moviePath); !errors.Is(err, qbit.ErrRecheckTimeout) {
		t.Fatalf("expected ErrRecheckTimeout, got %v", err)
	}
	if gotState, _ := fake.torrentState(hashMovie); gotState != "pausedUP" {
		t.Fatalf("expected pausedUP, got %s", gotState)
	}
}

func TestRecheckPath_resumesCompleteTorrentCoveringPath(t *testing.T) {
	fake := newFakeQbit(webAPIVersion5)
	fake.torrents[hashMovie] = movieTorrent(1)
	server := fake.start(t)
	client := newTestClient(t, server.URL, "", "")

	if err := client.RecheckPath(context.Background(), moviePath); err != nil {
		t.Fatalf("RecheckPath: %v", err)
	}
	if gotState, _ := fake.torrentState(hashMovie); gotState != "uploading" {
		t.Fatalf("expected uploading, got %s", gotState)
	}
}

func TestRecheckPath_returnsNotFoundWhenNoTorrentCoversPath(t *testing.T) {
	fake := newFakeQbit(webAPIVersion4)
	fake.torrents[hashMovie] = movieTorrent(1)
	server := fake.start(t)
	client := newTestClient(t, server.URL, "", "")

	err := client.RecheckPath(context.Background(), "/home/u/torrents/Other/other.mkv")
	if !errors.Is(err, qbit.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if gotActions := fake.snapshotActions(); len(gotActions) != 0 {
		t.Fatalf("no action expected, got %v", gotActions)
	}
}

func TestReadOnlyClient_refusesEveryMutatingMethod(t *testing.T) {
	fake := newFakeQbit(webAPIVersion4)
	fake.torrents[hashMovie] = movieTorrent(1)
	server := fake.start(t)
	client, err := qbit.NewFromConfig(config.TorrentClient{Type: "qbittorrent", ReadOnly: true, BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	ctx := context.Background()
	mutations := map[string]func() error{
		"Add":     func() error { return client.Add(ctx, []byte("d4:infoe"), torrentRoot, qbit.AddOptions{}) },
		"Recheck": func() error { return client.Recheck(ctx, hashMovie) },
		"Pause":   func() error { return client.Pause(ctx, hashMovie) },
		"Resume":  func() error { return client.Resume(ctx, hashMovie) },
		"RecheckAndGuard": func() error {
			_, guardErr := client.RecheckAndGuard(ctx, hashMovie, testGuardTimeout)
			return guardErr
		},
		"RecheckPath": func() error { return client.RecheckPath(ctx, moviePath) },
	}
	for methodName, mutation := range mutations {
		if err := mutation(); !errors.Is(err, qbit.ErrReadOnly) {
			t.Errorf("%s: expected ErrReadOnly, got %v", methodName, err)
		}
	}
	if gotActions := fake.snapshotActions(); len(gotActions) != 0 {
		t.Fatalf("no mutating request expected, got %v", gotActions)
	}
	if _, err := client.Torrents(ctx); err != nil {
		t.Fatalf("reads stay allowed: %v", err)
	}
}

func TestNewFromConfig_rejectsUnknownType(t *testing.T) {
	_, err := qbit.NewFromConfig(config.TorrentClient{Type: "transmission", BaseURL: "http://example.com:9091"})
	if err == nil {
		t.Fatal("expected an error for a non qbittorrent type")
	}
}

func TestNew_rejectsBaseURLCarryingCredentialsOrQuery(t *testing.T) {
	for _, invalidURL := range []string{"http://alice:" + fakePassword + "@example.com", "http://example.com?x=1", "ftp://example.com", "example.com"} {
		_, err := qbit.New(invalidURL, "", "")
		if err == nil {
			t.Errorf("expected an error for an invalid base url")
			continue
		}
		if strings.Contains(err.Error(), fakePassword) {
			t.Errorf("error leaks the password: %v", err)
		}
	}
}

func TestClient_refusesRedirectToAnotherHost(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		t.Error("the redirect target must never be contacted")
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(elsewhere.Close)
	redirecting := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, elsewhere.URL+request.URL.Path, http.StatusFound)
	}))
	t.Cleanup(redirecting.Close)
	client := newTestClient(t, redirecting.URL, fakeUser, fakePassword)

	if _, err := client.Torrents(context.Background()); !errors.Is(err, qbit.ErrBadResponse) {
		t.Fatalf("expected ErrBadResponse on a cross-host redirect, got %v", err)
	}
}

func TestErrorsAndString_neverContainSecrets(t *testing.T) {
	const bodySecret = "body-secret-marker"
	fake := newFakeQbit(webAPIVersion4)
	fake.requireAuth = true
	fake.prefix = proxyPrefix
	server := fake.start(t)
	client := newTestClient(t, server.URL+proxyPrefix, fakeUser, fakePassword)
	if _, err := client.Torrents(context.Background()); err != nil {
		t.Fatalf("Torrents: %v", err)
	}
	fake.update(func(locked *fakeQbit) {
		locked.forcedStatus, locked.forcedBody = http.StatusInternalServerError, bodySecret+" "+fakePassword
	})
	_, serverErr := client.Torrents(context.Background())
	if !errors.Is(serverErr, qbit.ErrBadResponse) {
		t.Fatalf("expected ErrBadResponse, got %v", serverErr)
	}
	fake.update(func(locked *fakeQbit) { locked.forcedStatus, locked.loginsRejected = 0, true })
	_, loginErr := newTestClient(t, server.URL+proxyPrefix, fakeUser, fakePassword).Torrents(context.Background())
	if !errors.Is(loginErr, qbit.ErrAuthFailed) {
		t.Fatalf("expected ErrAuthFailed, got %v", loginErr)
	}
	unreachable := httptest.NewServer(http.NotFoundHandler())
	unreachable.Close()
	_, transportErr := newTestClient(t, unreachable.URL+proxyPrefix, fakeUser, fakePassword).Torrents(context.Background())
	if !errors.Is(transportErr, qbit.ErrBadResponse) {
		t.Fatalf("expected ErrBadResponse on a transport error, got %v", transportErr)
	}
	rendered := []string{
		serverErr.Error(), loginErr.Error(), transportErr.Error(), client.String(),
		fmt.Sprintf("%v %+v %#v %s", client, client, client, client),
	}
	for _, gotText := range rendered {
		for _, secret := range []string{fakePassword, fakeSessionID, bodySecret, proxyPrefix, "fake-proxy-key"} {
			if strings.Contains(gotText, secret) {
				t.Errorf("secret %q leaked in %q", secret, gotText)
			}
		}
	}
}

func TestRecheckAndGuard_pausesAgainWhenClientRestartsIncompleteTorrent(t *testing.T) {
	fake := newFakeQbit(webAPIVersion4)
	torrent := movieTorrent(0.5)
	torrent.StateAfter = "downloading"
	fake.torrents[hashMovie] = torrent
	server := fake.start(t)
	client := newTestClient(t, server.URL, "", "")

	gotResult, err := client.RecheckAndGuard(context.Background(), hashMovie, testGuardTimeout)
	if err != nil {
		t.Fatalf("RecheckAndGuard: %v", err)
	}
	if gotResult.Complete || gotResult.Resumed {
		t.Fatalf("expected an incomplete, not resumed result, got %+v", gotResult)
	}
	gotActions := fake.snapshotActions()
	if expectedLast := "pause:" + hashMovie; gotActions[len(gotActions)-1] != expectedLast {
		t.Fatalf("expected a final %s, got %v", expectedLast, gotActions)
	}
	if gotState, _ := fake.torrentState(hashMovie); gotState != "pausedDL" {
		t.Fatalf("expected pausedDL, got %s", gotState)
	}
}

func TestRecheckAndGuard_timesOutWhenCheckingNeverStarts(t *testing.T) {
	fake := newFakeQbit(webAPIVersion5)
	torrent := movieTorrent(1)
	torrent.NoCheckingState = true
	fake.torrents[hashMovie] = torrent
	server := fake.start(t)
	client := newTestClient(t, server.URL, "", "", qbit.WithCheckStartTimeout(20*time.Millisecond))

	started := time.Now()
	gotResult, err := client.RecheckAndGuard(context.Background(), hashMovie, time.Hour)
	if !errors.Is(err, qbit.ErrRecheckTimeout) {
		t.Fatalf("expected ErrRecheckTimeout, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > testGuardTimeout {
		t.Fatalf("the start sub-deadline was not applied (%s)", elapsed)
	}
	if gotResult.Resumed {
		t.Fatal("a torrent whose check never started must not be resumed")
	}
	if gotState, _ := fake.torrentState(hashMovie); gotState != "stoppedUP" {
		t.Fatalf("expected stoppedUP, got %s", gotState)
	}
}

// twinTorrents renvoie deux torrents (cross-seed) qui partagent le même fichier sur disque.
func twinTorrents(fake *fakeQbit, secondProgressAfter float64) {
	fake.torrents[hashMovie] = movieTorrent(1)
	twin := movieTorrent(secondProgressAfter)
	twin.Files = []fakeFile{{Name: "Movie/movie.mkv", Size: 40, Progress: 1}}
	fake.torrents[hashPack] = twin
}

func TestRecheckPath_pausesEveryCoveringTorrentBeforeAnyRecheck(t *testing.T) {
	fake := newFakeQbit(webAPIVersion4)
	twinTorrents(fake, 0.5)
	server := fake.start(t)
	client := newTestClient(t, server.URL, "", "")

	err := client.RecheckPath(context.Background(), moviePath)
	if !errors.Is(err, qbit.ErrIncompleteAfterRecheck) || !strings.Contains(err.Error(), hashPack) {
		t.Fatalf("expected ErrIncompleteAfterRecheck naming %s, got %v", hashPack, err)
	}
	gotActions := fake.snapshotActions()
	expectedFirst := []string{"pause:" + hashMovie, "pause:" + hashPack}
	if len(gotActions) < 2 || !slices.Equal(gotActions[:2], expectedFirst) {
		t.Fatalf("expected %v first, got %v", expectedFirst, gotActions)
	}
	if gotState, _ := fake.torrentState(hashMovie); gotState != "uploading" {
		t.Fatalf("the complete twin should be resumed, got %s", gotState)
	}
	if gotState, _ := fake.torrentState(hashPack); gotState != "pausedDL" {
		t.Fatalf("the incomplete twin should stay paused, got %s", gotState)
	}
}

func TestRecheckPath_leavesEveryTorrentPausedWhenARecheckFails(t *testing.T) {
	fake := newFakeQbit(webAPIVersion4)
	twinTorrents(fake, 1)
	fake.torrents[hashPack].RecheckFails = true
	server := fake.start(t)
	client := newTestClient(t, server.URL, "", "")

	if err := client.RecheckPath(context.Background(), moviePath); !errors.Is(err, qbit.ErrBadResponse) {
		t.Fatalf("expected ErrBadResponse, got %v", err)
	}
	for _, hash := range []string{hashMovie, hashPack} {
		if gotState, _ := fake.torrentState(hash); gotState != "pausedUP" {
			t.Errorf("%s: expected pausedUP, got %s", hash, gotState)
		}
	}
	for _, gotAction := range fake.snapshotActions() {
		if strings.HasPrefix(gotAction, "resume:") {
			t.Fatalf("nothing may be resumed after an error: %v", fake.snapshotActions())
		}
	}
}

func TestRecheckPath_skipsTorrentRemovedBetweenListAndFiles(t *testing.T) {
	fake := newFakeQbit(webAPIVersion4)
	twinTorrents(fake, 1)
	fake.torrents[hashPack].FilesMissing = true
	server := fake.start(t)
	client := newTestClient(t, server.URL, "", "")

	if err := client.RecheckPath(context.Background(), moviePath); err != nil {
		t.Fatalf("RecheckPath: %v", err)
	}
	for _, gotAction := range fake.snapshotActions() {
		if strings.HasSuffix(gotAction, hashPack) {
			t.Fatalf("the vanished torrent must be skipped: %v", fake.snapshotActions())
		}
	}
}

// buildTorrent fabrique un .torrent v1 à deux fichiers sous le dossier racine Movie.
func buildTorrent(t *testing.T) []byte {
	t.Helper()
	info := metainfo.Info{
		Name: "Movie", PieceLength: 16384, Pieces: make([]byte, 20),
		Files: []metainfo.FileInfo{{Length: 40, Path: []string{"movie.mkv"}}, {Length: 2, Path: []string{"movie.nfo"}}},
	}
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatalf("marshal info: %v", err)
	}
	raw, err := bencode.Marshal(metainfo.MetaInfo{InfoBytes: infoBytes})
	if err != nil {
		t.Fatalf("marshal metainfo: %v", err)
	}
	return raw
}

func TestTorrentForPath_returnsParsedTorrentAndFileIndex(t *testing.T) {
	raw := buildTorrent(t)
	parsed, err := torrentcheck.Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	fake := newFakeQbit(webAPIVersion4)
	torrent := movieTorrent(1)
	torrent.Files = []fakeFile{{Name: "Movie/movie.mkv", Size: 40}, {Name: "Movie/movie.nfo", Size: 2}}
	torrent.Export = raw
	fake.torrents[parsed.InfoHash] = torrent
	server := fake.start(t)
	client := newTestClient(t, server.URL, "", "")
	var source repair.TorrentSource = client

	gotRef, found, err := source.TorrentForPath(context.Background(), "/home/u/torrents/Movie/movie.nfo")
	if err != nil || !found {
		t.Fatalf("TorrentForPath: found=%t err=%v", found, err)
	}
	if expectedValue := 1; gotRef.FileIndex != expectedValue || gotRef.Torrent.InfoHash != parsed.InfoHash {
		t.Fatalf("expected file %d of %s, got %d of %s", expectedValue, parsed.InfoHash, gotRef.FileIndex, gotRef.Torrent.InfoHash)
	}
	_, found, err = source.TorrentForPath(context.Background(), "/home/u/torrents/Other/other.mkv")
	if err != nil || found {
		t.Fatalf("expected not found without error, got found=%t err=%v", found, err)
	}
}

func TestRecheckPath_resumesOnlyTorrentsThatWereRunningBefore(t *testing.T) {
	for _, pausedState := range []string{"pausedUP", "stoppedUP", "pausedDL"} {
		t.Run(pausedState, func(t *testing.T) {
			fake := newFakeQbit(webAPIVersion4)
			twinTorrents(fake, 1)
			fake.torrents[hashPack].State = pausedState
			server := fake.start(t)
			client := newTestClient(t, server.URL, "", "")

			if err := client.RecheckPath(context.Background(), moviePath); err != nil {
				t.Fatalf("RecheckPath: %v", err)
			}
			if gotState, _ := fake.torrentState(hashMovie); gotState != "uploading" {
				t.Fatalf("the running torrent should be resumed, got %s", gotState)
			}
			if slices.Contains(fake.snapshotActions(), "resume:"+hashPack) {
				t.Fatalf("the torrent the user left paused was resumed: %v", fake.snapshotActions())
			}
			if gotState, _ := fake.torrentState(hashPack); gotState != "pausedUP" {
				t.Fatalf("expectedValue the paused torrent left paused, gotValue %s", gotState)
			}
		})
	}
}

// echoingServer répond 500 en recopiant le corps reçu, comme un serveur ou un proxy bavard.
func echoingServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		writer.Header().Set("Content-Type", "text/plain")
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write(body)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestAdd_neverLogsTorrentBytes(t *testing.T) {
	const passkey = "fake-passkey-qbit"
	const announceHost = "tracker-q.example.com"
	torrent, err := bencode.Marshal(map[string]any{
		"announce": "https://" + announceHost + "/" + passkey + "/announce",
		"info":     map[string]any{"name": "Movie", "length": 1, "piece length": 16, "pieces": strings.Repeat("a", 20)},
	})
	if err != nil {
		t.Fatal(err)
	}
	var logged strings.Builder
	previousOutput, previousSlog := log.Writer(), slog.Default()
	log.SetOutput(&logged)
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() {
		log.SetOutput(previousOutput)
		slog.SetDefault(previousSlog)
	})
	client := newTestClient(t, echoingServer(t).URL, "", "")

	addErr := client.Add(context.Background(), torrent, "/home/u/torrents", qbit.AddOptions{})

	if !errors.Is(addErr, qbit.ErrBadResponse) {
		t.Fatalf("gotValue %v, expectedValue ErrBadResponse", addErr)
	}
	for _, leaked := range []string{passkey, announceHost, "announce", "pieces"} {
		if strings.Contains(addErr.Error(), leaked) || strings.Contains(fmt.Sprintf("%+v", addErr), leaked) || strings.Contains(logged.String(), leaked) {
			t.Fatalf("gotValue %q in the error %q or the logs %q, expectedValue no byte of the torrent", leaked, addErr, logged.String())
		}
	}
}

func TestNew_givesTheLibraryASilentLogger(t *testing.T) {
	client := newTestClient(t, "http://127.0.0.1:8080", "", "")

	if gotValue := qbit.LibraryLogWriter(client); gotValue != io.Discard {
		t.Fatalf("gotValue %T, expectedValue io.Discard: the library logs URLs and errors", gotValue)
	}
}

func TestAdd_transportFailureNeverQuotesTheBaseURLSecret(t *testing.T) {
	const secretSegment = "/proxy/fake-proxy-key-transport"
	unreachable := httptest.NewServer(http.NotFoundHandler())
	unreachable.Close()
	client := newTestClient(t, unreachable.URL+secretSegment, "", "")

	addErr := client.Add(context.Background(), []byte("d4:infoe"), "/home/u/torrents", qbit.AddOptions{})

	if !errors.Is(addErr, qbit.ErrBadResponse) {
		t.Fatalf("gotValue %v, expectedValue ErrBadResponse", addErr)
	}
	for _, leaked := range []string{"fake-proxy-key-transport", unreachable.URL} {
		if strings.Contains(addErr.Error(), leaked) || strings.Contains(fmt.Sprintf("%+v", addErr), leaked) {
			t.Fatalf("gotValue %q in %q, expectedValue no base URL nor secret segment", leaked, addErr)
		}
	}
}
