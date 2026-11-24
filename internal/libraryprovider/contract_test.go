package libraryprovider

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
)

const (
	contractPath   = "../../docs/contract/openapi.yaml"
	contractPrefix = "/api/necromanciarr"
)

var contractInfohash = strings.Repeat("ab", 20)

// contractChecker valide chaque échange (requête et réponse) du fournisseur factice contre le contrat.
type contractChecker struct {
	testing  *testing.T
	router   routers.Router
	mutex    sync.Mutex
	exchange int
}

func newContractChecker(t *testing.T, serverURL string) *contractChecker {
	t.Helper()
	document, err := openapi3.NewLoader().LoadFromFile(contractPath)
	if err != nil {
		t.Fatalf("contract does not load: %v", err)
	}
	document.Servers = openapi3.Servers{{URL: serverURL + contractPrefix}}
	if err := document.Validate(context.Background()); err != nil {
		t.Fatalf("contract is invalid: %v", err)
	}
	router, err := legacy.NewRouter(document)
	if err != nil {
		t.Fatal(err)
	}
	return &contractChecker{testing: t, router: router}
}

func bearerOnly(_ context.Context, input *openapi3filter.AuthenticationInput) error {
	if input.RequestValidationInput.Request.Header.Get("Authorization") != "Bearer "+testToken {
		return errors.New("missing or wrong bearer token")
	}
	return nil
}

// declaredQueryParameters refuse tout paramètre de requête absent du contrat.
func declaredQueryParameters(route *routers.Route, query url.Values) error {
	declared := map[string]bool{}
	for _, parameter := range route.Operation.Parameters {
		declared[parameter.Value.Name] = true
	}
	for name := range query {
		if !declared[name] {
			return fmt.Errorf("query parameter %q is not in the contract", name)
		}
	}
	return nil
}

func (checker *contractChecker) check(received *http.Request, recorded *httptest.ResponseRecorder) error {
	// Côté serveur l'URL est relative : le routeur du contrat a besoin de l'hôte.
	request := received.Clone(received.Context())
	if request.URL.Host == "" {
		request.URL.Scheme, request.URL.Host = "http", received.Host
	}
	route, pathParams, err := checker.router.FindRoute(request)
	if err != nil {
		return fmt.Errorf("route not in the contract: %w", err)
	}
	if err := declaredQueryParameters(route, request.URL.Query()); err != nil {
		return err
	}
	requestInput := &openapi3filter.RequestValidationInput{
		Request: request, PathParams: pathParams, Route: route,
		Options: &openapi3filter.Options{AuthenticationFunc: bearerOnly},
	}
	if err := openapi3filter.ValidateRequest(context.Background(), requestInput); err != nil {
		return fmt.Errorf("request violates the contract: %w", err)
	}
	responseInput := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: requestInput, Status: recorded.Code, Header: recorded.Header(),
		Options: &openapi3filter.Options{IncludeResponseStatus: true},
	}
	responseInput.SetBodyBytes(recorded.Body.Bytes())
	if err := openapi3filter.ValidateResponse(context.Background(), responseInput); err != nil {
		return fmt.Errorf("response violates the contract: %w", err)
	}
	return nil
}

// wrap enregistre tout écart au contrat comme échec du test.
func (checker *contractChecker) wrap(handler http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		recorded := httptest.NewRecorder()
		handler(recorded, request)
		if err := checker.check(request, recorded); err != nil {
			checker.testing.Errorf("%s %s: %v", request.Method, request.URL, err)
		}
		checker.mutex.Lock()
		checker.exchange++
		checker.mutex.Unlock()
		for name, values := range recorded.Header() {
			writer.Header()[name] = values
		}
		writer.WriteHeader(recorded.Code)
		_, _ = writer.Write(recorded.Body.Bytes())
	})
}

func contractProvider(writer http.ResponseWriter, request *http.Request) {
	path := strings.TrimPrefix(request.URL.Path, contractPrefix)
	switch path {
	case "/v1/releases":
		writer.Header().Set("Content-Type", "application/json")
		next := "null"
		if request.URL.Query().Get("cursor") == "" {
			next = `"opaque-cursor"`
		}
		_, _ = fmt.Fprintf(writer, `{"items":[{"infohash":%q,"name":"Film","totalSize":10,"tmdb":1,"files":[{"path":"a.mkv","size":10}]}],"next":%s}`, contractInfohash, next)
	case "/v1/releases/" + contractInfohash:
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(writer, `{"infohash":%q,"tracker":"trk","totalSize":10,"files":[{"path":"a.mkv","size":10}]}`, contractInfohash)
	case "/v1/releases/" + contractInfohash + "/torrent":
		writer.Header().Set("Content-Type", "application/x-bittorrent")
		_, _ = writer.Write([]byte("d4:infod4:name1:aee"))
	case "/v1/releases/" + contractInfohash + "/nfos":
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(writer, `{"items":[{"label":"src","partial":false,"data":%q}]}`, base64.StdEncoding.EncodeToString([]byte("nfo")))
	default:
		writer.Header().Set("Content-Type", "application/problem+json")
		writer.WriteHeader(http.StatusNotFound)
		_, _ = writer.Write([]byte(`{"title":"Not found","status":404}`))
	}
}

func TestContract_responsesValidateAgainstOpenapi(t *testing.T) {
	var checker *contractChecker
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		checker.wrap(contractProvider).ServeHTTP(writer, request)
	}))
	t.Cleanup(server.Close)
	checker = newContractChecker(t, server.URL)
	client, err := New(server.URL+contractPrefix, testToken)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	sizes := make([]int64, 120)
	for index := range sizes {
		sizes[index] = int64(index + 1)
	}
	if _, err := client.Search(ctx, Query{TMDBID: 1, TVDBID: 2, IMDBID: "tt1", Text: "film", Tracker: "trk", Sizes: sizes}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if _, err := client.Release(ctx, contractInfohash, "trk"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := client.Torrent(ctx, contractInfohash, ""); err != nil {
		t.Fatalf("Torrent: %v", err)
	}
	if _, err := client.Nfos(ctx, contractInfohash, "trk"); err != nil {
		t.Fatalf("Nfos: %v", err)
	}
	unknown := strings.Repeat("cd", 20)
	if _, err := client.Torrent(ctx, unknown, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for an unknown release, gotErr = %v", err)
	}
	// 3 découpes de 120 tailles (50+50+20), chacune sur 2 pages, puis 4 autres appels.
	if expectedValue, gotValue := 6+4, checker.exchange; gotValue != expectedValue {
		t.Fatalf("expectedValue %d exchanges, gotValue %d", expectedValue, gotValue)
	}
}

func TestContract_rejectsRequestsOutsideTheContract(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	checker := newContractChecker(t, server.URL)
	rejected := map[string]string{
		"unknown query parameter": "/v1/releases?bogus=1",
		"unknown path":            "/v1/torrents/trk/aaaa",
		"too many sizes":          "/v1/releases?" + strings.Repeat("size=1&", 51),
		"invalid imdb":            "/v1/releases?imdb=abc",
	}
	for name, target := range rejected {
		request := httptest.NewRequest(http.MethodGet, server.URL+contractPrefix+target, nil)
		request.Header.Set("Authorization", "Bearer "+testToken)
		recorded := httptest.NewRecorder()
		recorded.Header().Set("Content-Type", "application/json")
		_, _ = recorded.WriteString(`{"items":[],"next":null}`)
		if err := checker.check(request, recorded); err == nil {
			t.Errorf("%s: expected a contract violation", name)
		}
	}
	badBody := httptest.NewRequest(http.MethodGet, server.URL+contractPrefix+"/v1/releases", nil)
	badBody.Header.Set("Authorization", "Bearer "+testToken)
	badRecorded := httptest.NewRecorder()
	badRecorded.Header().Set("Content-Type", "application/json")
	_, _ = badRecorded.WriteString(`{"items":[{"infohash":"zz","files":[]}],"next":null}`)
	if err := checker.check(badBody, badRecorded); err == nil {
		t.Error("expected a response schema violation")
	}
	valid := httptest.NewRequest(http.MethodGet, server.URL+contractPrefix+"/v1/releases?size=1&size=2", nil)
	valid.Header.Set("Authorization", "Bearer "+testToken)
	recorded := httptest.NewRecorder()
	recorded.Header().Set("Content-Type", "application/json")
	_, _ = recorded.WriteString(`{"items":[],"next":null}`)
	if err := checker.check(valid, recorded); err != nil {
		t.Fatalf("valid exchange rejected: %v", err)
	}
}
