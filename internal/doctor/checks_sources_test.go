package doctor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"necromanciarr/internal/config"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/prowlarr"
	"necromanciarr/internal/torznab"
)

const (
	fakeIndexerKey  = "secret-indexer-key"
	fakeProwlarrKey = "secret-prowlarr-key"
)

type fakeCaps struct {
	caps torznab.Caps
	err  error
}

func (reader fakeCaps) Caps(context.Context) (torznab.Caps, error) { return reader.caps, reader.err }

type fakeLister struct {
	indexers []prowlarr.Indexer
	err      error
}

func (lister fakeLister) Indexers(context.Context) ([]prowlarr.Indexer, error) {
	return lister.indexers, lister.err
}

func sourceSettings(indexerURLs ...string) config.Config {
	settings := config.Config{ListenAddr: "127.0.0.1:1"}
	for position, indexerURL := range indexerURLs {
		settings.Torznab = append(settings.Torznab, config.TorznabIndexer{URL: indexerURL, Name: fmt.Sprint(12 + position)})
	}
	return settings
}

func capsDeps(reader CapsReader, err error) Deps {
	return Deps{NewCapsReader: func(config.TorznabIndexer) (CapsReader, error) { return reader, err }}
}

func modesOf(modes ...string) torznab.Caps {
	caps := torznab.Caps{Modes: map[string][]string{}}
	for _, mode := range modes {
		caps.Modes[mode] = []string{"q"}
	}
	return caps
}

func runCheck(t *testing.T, settings config.Config, deps Deps, checkID string) Outcome {
	t.Helper()
	return resultOf(t, Run(context.Background(), Build(settings, deps)), checkID)
}

func TestDoctor_torznabChecksCapsOnlyWithoutSearchOrDownload(t *testing.T) {
	var mutex sync.Mutex
	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mutex.Lock()
		queries = append(queries, request.URL.Path+"?"+request.URL.Query().Get("t"))
		mutex.Unlock()
		content, err := os.ReadFile(filepath.Join("..", "torznab", "testdata", "caps.xml"))
		if err != nil {
			t.Error(err)
		}
		writer.Header().Set("Content-Type", "application/xml")
		_, _ = writer.Write(content)
	}))
	defer server.Close()
	settings := sourceSettings(server.URL + "/12/api?apikey=" + fakeIndexerKey)

	outcome := runCheck(t, settings, Deps{}, "torznab:12")

	if outcome.Result.Status != OK || outcome.Result.Code != CodeTorznabOK {
		t.Fatalf("gotValue %+v, expectedValue ok with %s", outcome.Result, CodeTorznabOK)
	}
	expectedQueries := "[/12/api?caps]"
	if fmt.Sprint(queries) != expectedQueries {
		t.Fatalf("gotValue %v, expectedValue %s: caps only, no search and no download", queries, expectedQueries)
	}
	if detail := detailOf(outcome.Result); !strings.Contains(detail, "movie") || strings.Contains(detail, fakeIndexerKey) {
		t.Fatalf("gotValue %q, expectedValue the modes without the key", detail)
	}
}

func TestDoctor_torznabMapsClientErrorsToCodes(t *testing.T) {
	scenarios := []struct {
		name         string
		err          error
		expectedCode string
	}{
		{"key refused", fmt.Errorf("%w (HTTP 401)", torznab.ErrAccessDenied), CodeTorznabAccessDenied},
		{"quota", &torznab.UnavailableError{RetryAfter: time.Minute}, CodeTorznabUnreachable},
		{"unavailable", fmt.Errorf("%w: request failed", torznab.ErrUnavailable), CodeTorznabUnreachable},
		{"deadline", fmt.Errorf("%w: %w", torznab.ErrUnavailable, context.DeadlineExceeded), CodeTorznabUnreachable},
		{"hostile", fmt.Errorf("%w: doctype", torznab.ErrHostileResponse), CodeTorznabHostileResponse},
		{"bad response", fmt.Errorf("%w (HTTP 404)", torznab.ErrBadResponse), CodeTorznabBadResponse},
		{"unknown error", errors.New("boom"), CodeTorznabUnreachable},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			outcome := runCheck(t, sourceSettings("http://example.com/12/api?apikey=fake-key-value"), capsDeps(fakeCaps{err: scenario.err}, nil), "torznab:12")

			if outcome.Result.Status != Fail || outcome.Result.Code != scenario.expectedCode {
				t.Fatalf("gotValue %+v, expectedValue fail with %s", outcome.Result, scenario.expectedCode)
			}
			if hintOf(outcome.Result) == "" {
				t.Fatalf("gotValue no hint, expectedValue a hint for %s", scenario.expectedCode)
			}
		})
	}
}

func TestDoctor_torznabWarnsWithoutMovieOrTvsearch(t *testing.T) {
	scenarios := []struct {
		name           string
		caps           torznab.Caps
		expectedStatus Status
		expectedCode   string
	}{
		{"search only", modesOf("search"), Warn, CodeTorznabNoMediaMode},
		{"nothing", modesOf(), Warn, CodeTorznabNoMediaMode},
		{"movie", modesOf("movie"), OK, CodeTorznabOK},
		{"tvsearch", modesOf("search", "tvsearch"), OK, CodeTorznabOK},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			outcome := runCheck(t, sourceSettings("http://example.com/12/api?apikey=fake-key-value"), capsDeps(fakeCaps{caps: scenario.caps}, nil), "torznab:12")

			if outcome.Result.Status != scenario.expectedStatus || outcome.Result.Code != scenario.expectedCode {
				t.Fatalf("gotValue %+v, expectedValue %s with %s", outcome.Result, scenario.expectedStatus, scenario.expectedCode)
			}
		})
	}
}

func TestDoctor_torznabHasOneCheckPerIndexerAndNoneWithoutIndexers(t *testing.T) {
	deps := capsDeps(fakeCaps{caps: modesOf("movie")}, nil)

	withIndexers := Build(sourceSettings("http://example.com/12/api?apikey=fake-key-value", "http://example.com/13/api?apikey=fake-key-value"), deps)
	without := Build(sourceSettings(), deps)

	gotIDs := checkIDs(withIndexers)
	if !containsID(gotIDs, "torznab:12") || !containsID(gotIDs, "torznab:13") || containsID(gotIDs, "prowlarr") {
		t.Fatalf("gotValue %v, expectedValue torznab:12 and torznab:13 without prowlarr", gotIDs)
	}
	if containsID(checkIDs(without), "torznab:12") || containsID(checkIDs(without), "prowlarr") {
		t.Fatalf("gotValue %v, expectedValue no source check", checkIDs(without))
	}
}

func checkIDs(checks []Check) []string {
	identifiers := make([]string, 0, len(checks))
	for _, check := range checks {
		identifiers = append(identifiers, check.ID)
	}
	return identifiers
}

func containsID(identifiers []string, wanted string) bool {
	for _, identifier := range identifiers {
		if identifier == wanted {
			return true
		}
	}
	return false
}

func TestDoctor_prowlarrWarnsForMissingOrDisabledIndexers(t *testing.T) {
	settings := sourceSettings("http://example.com/12/api?apikey=fake-key-value", "http://example.com/13/api?apikey=fake-key-value", "http://example.com/14/api?apikey=fake-key-value")
	settings.Prowlarr = "http://example.com:9696?apikey=" + fakeProwlarrKey
	lister := fakeLister{indexers: []prowlarr.Indexer{{ID: 12, Name: "Tracker A", Enabled: true}, {ID: 13, Name: "Tracker B", Enabled: false}}}
	deps := Deps{NewIndexerLister: func(string) (IndexerLister, error) { return lister, nil }}

	outcome := runCheck(t, settings, deps, "prowlarr")

	if outcome.Result.Status != Warn || outcome.Result.Code != CodeProwlarrIndexerMissingOrDisabled {
		t.Fatalf("gotValue %+v, expectedValue warn with %s", outcome.Result, CodeProwlarrIndexerMissingOrDisabled)
	}
	detail := detailOf(outcome.Result)
	for _, expectedPart := range []string{"Tracker B", "14", "disabled", "missing"} {
		if !strings.Contains(detail, expectedPart) {
			t.Errorf("gotValue %q, expectedValue it to mention %q", detail, expectedPart)
		}
	}
	if strings.Contains(detail, "Tracker A") || !strings.Contains(hintOf(outcome.Result), "Redirect") {
		t.Fatalf("gotValue detail %q hint %q, expectedValue only the faulty indexers and the Redirect advice", detail, hintOf(outcome.Result))
	}
}

func TestDoctor_prowlarrIsOKWhenEveryConfiguredIndexerIsEnabled(t *testing.T) {
	settings := sourceSettings("http://example.com/12/api?apikey=fake-key-value", "http://other.example.com/api?apikey=fake-key-value")
	settings.Prowlarr = "http://example.com:9696?apikey=" + fakeProwlarrKey
	lister := fakeLister{indexers: []prowlarr.Indexer{{ID: 12, Enabled: true}, {ID: 40, Enabled: false}}}
	deps := Deps{NewIndexerLister: func(string) (IndexerLister, error) { return lister, nil }}

	outcome := runCheck(t, settings, deps, "prowlarr")

	if outcome.Result.Status != OK || outcome.Result.Code != CodeProwlarrOK {
		t.Fatalf("gotValue %+v, expectedValue ok with %s (an indexer without numeric id is not matched)", outcome.Result, CodeProwlarrOK)
	}
}

func TestDoctor_prowlarrMapsErrorsToCodes(t *testing.T) {
	scenarios := []struct {
		name         string
		err          error
		expectedCode string
	}{
		{"key refused", fmt.Errorf("%w (HTTP 401)", prowlarr.ErrAccessDenied), CodeProwlarrAccessDenied},
		{"unavailable", fmt.Errorf("%w: request failed", prowlarr.ErrUnavailable), CodeProwlarrUnreachable},
		{"bad response", fmt.Errorf("%w: not JSON", prowlarr.ErrBadResponse), CodeProwlarrBadResponse},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			settings := sourceSettings()
			settings.Prowlarr = "http://example.com:9696?apikey=" + fakeProwlarrKey
			deps := Deps{NewIndexerLister: func(string) (IndexerLister, error) { return fakeLister{err: scenario.err}, nil }}

			outcome := runCheck(t, settings, deps, "prowlarr")

			if outcome.Result.Status != Fail || outcome.Result.Code != scenario.expectedCode {
				t.Fatalf("gotValue %+v, expectedValue fail with %s", outcome.Result, scenario.expectedCode)
			}
		})
	}
}

func TestDoctor_prowlarrListsThroughTheNativeAPIWithTheKeyInAHeader(t *testing.T) {
	var gotHeader string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotHeader = request.Header.Get("X-Api-Key")
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `[{"id":12,"name":"A","enable":true}]`)
	}))
	defer server.Close()
	settings := sourceSettings("http://example.com/12/api?apikey=fake-key-value")
	settings.Prowlarr = server.URL + "?apikey=" + fakeProwlarrKey

	outcome := runCheck(t, settings, Deps{}, "prowlarr")

	if outcome.Result.Code != CodeProwlarrOK || gotHeader != fakeProwlarrKey {
		t.Fatalf("gotValue %+v with header %q, expectedValue prowlarr_ok and the key header", outcome.Result, gotHeader)
	}
}

func TestDoctor_redactsTorznabAndSFTPSecrets(t *testing.T) {
	settings := sourceSettings("http://example.com/12/api?apikey=" + fakeIndexerKey)
	settings.Prowlarr = "http://example.com:9696?apikey=" + fakeProwlarrKey
	leaking := fmt.Errorf("%w: GET http://example.com/12/api?apikey=%s failed, said %s and %s", torznab.ErrUnavailable, fakeIndexerKey, fakeIndexerKey, fakeProwlarrKey)
	deps := Deps{
		NewCapsReader:    func(config.TorznabIndexer) (CapsReader, error) { return fakeCaps{err: leaking}, nil },
		NewIndexerLister: func(string) (IndexerLister, error) { return fakeLister{err: leaking}, nil },
	}

	report := Run(context.Background(), Build(settings, deps))

	for _, rendered := range []string{renderedJSON(t, report, frenchTranslator), renderedJSON(t, report, englishTranslator)} {
		if strings.Contains(rendered, "secret-") {
			t.Fatalf("leaked a secret: %s", rendered)
		}
	}
}

func renderedJSON(t *testing.T, report Report, translator i18n.Translator) string {
	t.Helper()
	raw, err := MarshalJSONReport(report, translator)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
