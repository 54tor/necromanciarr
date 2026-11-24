package doctor

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"necromanciarr/internal/config"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/prowlarr"
	"necromanciarr/internal/sources"
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
	if deps.NewCapsReader == nil && checkID != "torznab:12" {
		deps.NewCapsReader = func(config.TorznabIndexer) (CapsReader, error) { return fakeCaps{caps: modesOf("movie")}, nil }
	}
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
	settings := sourceSettings("http://example.com:9696/12/api?apikey=fake-key-value", "http://example.com:9696/13/api?apikey=fake-key-value", "http://example.com:9696/14/api?apikey=fake-key-value")
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
	settings := sourceSettings("http://example.com:9696/12/api?apikey=fake-key-value", "http://other.example.com/api?apikey=fake-key-value")
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
		// Les clés factices commencent par « secret- » ; l'identifiant du contrôle secret-key n'en est pas une.
		if strings.Contains(strings.ReplaceAll(rendered, `"`+IDSecretKey+`"`, ""), "secret-") {
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

func TestDoctor_prowlarrOnlyEvaluatesFeedsServedByItsOwnHost(t *testing.T) {
	scenarios := []struct {
		name           string
		prowlarrURL    string
		indexerURL     string
		expectedStatus Status
	}{
		{"foreign host with a numeric segment is ignored", "http://example.com:9696?apikey=x-key", "http://jackett.example.com:9696/99/api?apikey=x-key", OK},
		{"same host other port is ignored", "http://example.com:9696?apikey=x-key", "http://example.com:9797/99/api?apikey=x-key", OK},
		{"other scheme is ignored", "http://example.com:9696?apikey=x-key", "https://example.com:9696/99/api?apikey=x-key", OK},
		{"same host and missing id is warned", "http://example.com:9696?apikey=x-key", "http://example.com:9696/99/api?apikey=x-key", Warn},
		{"host case and trailing dot do not matter", "http://EXAMPLE.com:9696?apikey=x-key", "http://example.com.:9696/99/api?apikey=x-key", Warn},
		{"default port is explicit", "https://example.com?apikey=x-key", "https://example.com:443/99/api?apikey=x-key", Warn},
		{"docker service name with an underscore", "http://my_prowlarr:9696?apikey=x-key", "http://MY_PROWLARR:9696/99/api?apikey=x-key", Warn},
		{"under the url base", "http://example.com:9696/prowlarr?apikey=x-key", "http://example.com:9696/prowlarr/99/api?apikey=x-key", Warn},
		{"outside the url base is ignored", "http://example.com:9696/prowlarr?apikey=x-key", "http://example.com:9696/other/99/api?apikey=x-key", OK},
		{"sibling prefix is not under the url base", "http://example.com:9696/prowlarr?apikey=x-key", "http://example.com:9696/prowlarr2/99/api?apikey=x-key", OK},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			settings := sourceSettings(scenario.indexerURL)
			settings.Prowlarr = scenario.prowlarrURL
			deps := Deps{NewIndexerLister: func(string) (IndexerLister, error) {
				return fakeLister{indexers: []prowlarr.Indexer{{ID: 12, Enabled: true}}}, nil
			}}

			outcome := runCheck(t, settings, deps, "prowlarr")

			if outcome.Result.Status != scenario.expectedStatus {
				t.Fatalf("gotValue %s (%+v), expectedValue %s", outcome.Result.Status, outcome.Result, scenario.expectedStatus)
			}
		})
	}
}

func TestDoctor_torznabWarnsForAnIndexerOmittedFromSourceOrder(t *testing.T) {
	deps := capsDeps(fakeCaps{caps: modesOf("movie")}, nil)
	scenarios := []struct {
		name           string
		sourceOrder    []string
		expectedStatus Status
		expectedCode   string
	}{
		{"listed", []string{"provider", "torznab:12"}, OK, CodeTorznabOK},
		{"omitted", []string{"provider"}, Warn, CodeTorznabNotInSourceOrder},
		{"order not loaded by Load", nil, OK, CodeTorznabOK},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			settings := sourceSettings("http://example.com/12/api?apikey=fake-key-value")
			settings.SourceOrder = scenario.sourceOrder

			outcome := runCheck(t, settings, deps, "torznab:12")

			if outcome.Result.Status != scenario.expectedStatus || outcome.Result.Code != scenario.expectedCode {
				t.Fatalf("gotValue %+v, expectedValue %s with %s", outcome.Result, scenario.expectedStatus, scenario.expectedCode)
			}
			if scenario.expectedStatus == Warn && !strings.Contains(hintOf(outcome.Result), "torznab:12") {
				t.Fatalf("gotValue hint %q, expectedValue it to name torznab:12", hintOf(outcome.Result))
			}
		})
	}
}

func TestDoctor_torznabCachesTheCapsOutcomeForFiveMinutes(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				if status != http.StatusOK {
					writer.WriteHeader(status)
					return
				}
				content, _ := os.ReadFile(filepath.Join("..", "torznab", "testdata", "caps.xml"))
				writer.Header().Set("Content-Type", "application/xml")
				_, _ = writer.Write(content)
			}))
			defer server.Close()
			now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
			cache := NewCapsCache(DefaultCapsCacheTTL, func() time.Time { return now })
			settings := sourceSettings(server.URL + "/12/api?apikey=" + fakeIndexerKey)
			run := func(runSettings config.Config) Outcome {
				return runCheck(t, runSettings, Deps{CapsCache: cache}, "torznab:12")
			}

			first := run(settings)
			now = now.Add(4 * time.Minute)
			second := run(settings)
			gotAfterSecondRun := requests.Load()
			now = now.Add(2 * time.Minute)
			run(settings)
			gotAfterExpiry := requests.Load()
			changed := settings
			changed.Torznab = []config.TorznabIndexer{{URL: server.URL + "/12/api?apikey=" + fakeIndexerKey + "x", Name: "12"}}
			run(changed)

			if gotAfterSecondRun != 1 || gotAfterExpiry != 2 || requests.Load() != 3 {
				t.Fatalf("gotValue requests %d, %d, %d, expectedValue 1 (cached), 2 (expired), 3 (config changed)", gotAfterSecondRun, gotAfterExpiry, requests.Load())
			}
			if first.Result.Code != second.Result.Code {
				t.Fatalf("gotValue %s then %s, expectedValue the same outcome", first.Result.Code, second.Result.Code)
			}
		})
	}
}

// Clés de test à faible entropie, jamais de vraies clés.
var (
	doctorTestKey  = bytes.Repeat([]byte{0x01}, sources.KeySize)
	doctorOtherKey = bytes.Repeat([]byte{0x02}, sources.KeySize)
)

const sealedInfohash = "0123456789abcdef0123456789abcdef01234567"

type fakeSealedProbe struct {
	sealed []SealedTorrent
	err    error
}

func (probe fakeSealedProbe) SealedTorrents(context.Context, int) ([]SealedTorrent, error) {
	return probe.sealed, probe.err
}

func sealedFor(t *testing.T, key []byte, candidateID int64) SealedTorrent {
	t.Helper()
	sealer, err := sources.NewSealer(key)
	if err != nil {
		t.Fatal(err)
	}
	binding := sources.Binding{Infohash: sealedInfohash, Tracker: "tracker-a", Source: "torznab:12"}
	blob, err := sealer.Seal([]byte("d8:announce22:https://example.com/fakee"), binding)
	if err != nil {
		t.Fatal(err)
	}
	return SealedTorrent{CandidateID: candidateID, Binding: binding, Blob: blob}
}

func secretSettings(key []byte) config.Config {
	settings := sourceSettings("http://example.com/12/api?apikey=" + fakeIndexerKey)
	settings.SecretKey = key
	return settings
}

func TestDoctor_secretKeyReportsUnreadableBlob(t *testing.T) {
	probe := fakeSealedProbe{sealed: []SealedTorrent{sealedFor(t, doctorTestKey, 3), sealedFor(t, doctorOtherKey, 7)}}

	outcome := runCheck(t, secretSettings(doctorTestKey), Deps{SealedProbe: probe}, IDSecretKey)

	if outcome.Result.Status != Fail || outcome.Result.Code != CodeSealedTorrentUnreadable {
		t.Fatalf("gotValue %v %s, expectedValue fail %s", outcome.Result.Status, outcome.Result.Code, CodeSealedTorrentUnreadable)
	}
	report := Report{Checks: []Outcome{outcome}}
	french, english := renderedJSON(t, report, frenchTranslator), renderedJSON(t, report, englishTranslator)
	if !strings.Contains(french, "relancer Verify sur ce candidat") || !strings.Contains(english, "run Verify again on this candidate") {
		t.Fatalf("gotValue %s / %s, expectedValue the Verify hint in both languages", french, english)
	}
	if !strings.Contains(english, "7") || strings.Contains(english, "candidates 3") {
		t.Fatalf("gotValue %s, expectedValue only candidate 7 named", english)
	}
	for _, leaked := range []string{base64.RawURLEncoding.EncodeToString(doctorTestKey), "example.com/fake"} {
		if strings.Contains(french+english, leaked) {
			t.Fatalf("gotValue %q in %s", leaked, french+english)
		}
	}
}

func TestDoctor_secretKeyCodes(t *testing.T) {
	readable := fakeSealedProbe{sealed: []SealedTorrent{sealedFor(t, doctorTestKey, 3)}}
	scenarios := []struct {
		name           string
		settings       config.Config
		deps           Deps
		expectedStatus Status
		expectedCode   string
	}{
		{"key and readable blobs", secretSettings(doctorTestKey), Deps{SealedProbe: readable}, OK, CodeSecretKeyOK},
		{"key without probe", secretSettings(doctorTestKey), Deps{}, OK, CodeSecretKeyOK},
		{"torznab without key", secretSettings(nil), Deps{}, Fail, CodeSecretKeyMissing},
		{"wrong length", secretSettings(doctorTestKey[:16]), Deps{SealedProbe: readable}, Fail, CodeSecretKeyWrongLength},
		{"probe failure", secretSettings(doctorTestKey), Deps{SealedProbe: fakeSealedProbe{err: errors.New("database locked")}}, Warn, CodeSealedTorrentsUnchecked},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			outcome := runCheck(t, scenario.settings, scenario.deps, IDSecretKey)

			if outcome.Result.Status != scenario.expectedStatus || outcome.Result.Code != scenario.expectedCode {
				t.Fatalf("gotValue %v %s, expectedValue %v %s", outcome.Result.Status, outcome.Result.Code, scenario.expectedStatus, scenario.expectedCode)
			}
		})
	}
	if containsID(checkIDs(Build(config.Config{ListenAddr: "127.0.0.1:1"}, Deps{})), IDSecretKey) {
		t.Fatal("gotValue a secret-key check without torznab nor key, expectedValue none")
	}
}

func TestDoctor_secretKeyLoadFailureNamesItsCode(t *testing.T) {
	scenarios := map[error]string{
		config.ErrSecretKeyRequired:            CodeSecretKeyMissing,
		config.InvalidSecretKeyError{}:         CodeSecretKeyWrongLength,
		errors.New("readBytesPerSecond wrong"): "",
	}
	for loadErr, expectedCode := range scenarios {
		check, found := SecretKeyFailure(loadErr)
		if expectedCode == "" {
			if found {
				t.Fatalf("%v: gotValue a secret-key check, expectedValue none", loadErr)
			}
			continue
		}
		if !found || check.ID != IDSecretKey {
			t.Fatalf("%v: gotValue %v %q, expectedValue the secret-key check", loadErr, found, check.ID)
		}
		if gotCode := Run(context.Background(), []Check{check}).Checks[0].Result.Code; gotCode != expectedCode {
			t.Fatalf("%v: gotValue %s, expectedValue %s", loadErr, gotCode, expectedCode)
		}
	}
}
