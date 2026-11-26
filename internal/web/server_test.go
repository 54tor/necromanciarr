package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"necromanciarr/internal/app"
	"necromanciarr/internal/config"
	"necromanciarr/internal/config/configtest"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/pipeline"
	"necromanciarr/internal/store"
)

type errorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Detail  string `json:"detail"`
	} `json:"error"`
}

func decodeError(t *testing.T, body string) errorEnvelope {
	t.Helper()
	var envelope errorEnvelope
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("error body is not JSON: %q", body)
	}
	return envelope
}

func expectError(t *testing.T, label string, response string, gotStatus, expectedStatus int, expectedCode string) {
	t.Helper()
	gotCode := decodeError(t, response).Error.Code
	if gotStatus != expectedStatus || gotCode != expectedCode {
		t.Fatalf("%s: gotValue %d %q, expectedValue %d %q (body %q)", label, gotStatus, gotCode, expectedStatus, expectedCode, response)
	}
}

func TestRun_refusesANonLoopbackListenWithoutToken(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	handler := newTestHandler(t, &fakeService{}, config.Config{ListenAddr: "0.0.0.0:8787"})

	err = Run(context.Background(), listener, handler)

	if err == nil || !strings.Contains(err.Error(), "NECROMANCIARR_AUTH_TOKEN") {
		t.Fatalf("gotValue %v, expectedValue a refusal naming the token variable", err)
	}
}

func TestAuth_requiredWhenNotLoopback(t *testing.T) {
	handler := newTestHandler(t, &fakeService{}, tokenSettings())

	anonymous := serve(handler, apiRequest(http.MethodGet, "/api/status", ""))
	expectError(t, "no token", readBody(t, anonymous), anonymous.Code, http.StatusUnauthorized, errorCodeUnauthorized)
	if anonymous.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("gotValue WWW-Authenticate %q, expectedValue Bearer", anonymous.Header().Get("WWW-Authenticate"))
	}
	authorized := apiRequest(http.MethodGet, "/api/status", "")
	authorized.Header.Set("Authorization", "Bearer "+testToken)
	if gotValue := serve(handler, authorized).Code; gotValue != http.StatusOK {
		t.Fatalf("gotValue %d, expectedValue 200 with the token", gotValue)
	}
	lowerCaseScheme := apiRequest(http.MethodGet, "/api/status", "")
	lowerCaseScheme.Header.Set("Authorization", "bearer "+testToken)
	if gotValue := serve(handler, lowerCaseScheme).Code; gotValue != http.StatusOK {
		t.Fatalf("gotValue %d, expectedValue 200 with a lower-case scheme (case-insensitive per HTTP)", gotValue)
	}
	loopbackHandler := newTestHandler(t, &fakeService{}, loopbackSettings())
	if gotValue := serve(loopbackHandler, apiRequest(http.MethodGet, "/api/status", "")).Code; gotValue != http.StatusOK {
		t.Fatalf("gotValue %d, expectedValue 200 on loopback without a configured token", gotValue)
	}
}

func TestAuth_rejectsWrongTokenInConstantTime(t *testing.T) {
	fake := &fakeService{}
	handler := newTestHandler(t, fake, tokenSettings())
	sameLengthToken := strings.Repeat("x", len(testToken))
	attempts := map[string]func(*http.Request){
		"same length, other content": func(request *http.Request) { request.Header.Set("Authorization", "Bearer "+sameLengthToken) },
		"other length":               func(request *http.Request) { request.Header.Set("Authorization", "Bearer "+testToken+"x") },
		"prefix of the token":        func(request *http.Request) { request.Header.Set("Authorization", "Bearer "+testToken[:4]) },
		"other scheme":               func(request *http.Request) { request.Header.Set("Authorization", "Basic "+testToken) },
		"token in the query":         func(request *http.Request) { request.URL.RawQuery = "token=" + testToken + "&api_key=" + testToken },
		"token in a cookie":          func(request *http.Request) { request.AddCookie(&http.Cookie{Name: "token", Value: testToken}) },
	}
	for label, prepare := range attempts {
		request := apiRequest(http.MethodGet, "/api/status", "")
		prepare(request)

		response := serve(handler, request)

		expectError(t, label, readBody(t, response), response.Code, http.StatusUnauthorized, errorCodeUnauthorized)
	}
	if fake.called("Status") {
		t.Fatal("gotValue a Status call, expectedValue none without a valid token")
	}
}

func sampleProposals() []app.Proposal {
	return []app.Proposal{
		{
			ID: 4, CandidateID: 40, CandidateName: "Film.2001.1080p", CandidateSize: 4096, Tracker: "tracker.example.com",
			GroupID: 9, Kind: store.ProposalPartialAnnex, Decision: store.ProposalPending, Generation: 2, Current: true,
			Warnings: []app.Warning{{Code: "warning.annex_pieces_not_proven", Text: "2 pieces cover a missing .nfo"}},
			Details: pipeline.ProposalDetails{
				Percent: 99.5, VerifiedPieces: 398, MissingPieces: 2, TotalPieces: 400,
				Candidates: []pipeline.GroupMember{{CandidateID: 40, Name: "Film.2001.1080p", ProposalID: 4, Percent: 99.5}},
			},
		},
		{
			ID: 5, CandidateID: 50, GroupID: 9, Kind: store.ProposalConflict, Decision: store.ProposalPending, Current: false,
			Warnings: []app.Warning{},
			Details: pipeline.ProposalDetails{Candidates: []pipeline.GroupMember{
				{CandidateID: 11, Name: "first", ProposalID: 21, Percent: 100},
				{CandidateID: 12, Name: "second", ProposalID: 22, Percent: 100},
			}},
		},
	}
}

func TestProposals_listIncludesPercentVerifiedAndMissingPieces(t *testing.T) {
	fake := &fakeService{proposals: sampleProposals()}
	handler := newTestHandler(t, fake, loopbackSettings())

	response := serve(handler, apiRequest(http.MethodGet, "/api/proposals?decision=pending&kind=partial_annex&group=9&current_only=true", ""))

	body := readBody(t, response)
	var gotValue []app.Proposal
	if err := json.Unmarshal([]byte(body), &gotValue); err != nil || response.Code != http.StatusOK {
		t.Fatalf("gotValue %d %q, expectedValue a JSON array", response.Code, body)
	}
	if expectedValue := sampleProposals(); !reflect.DeepEqual(gotValue, expectedValue) {
		t.Fatalf("gotValue %+v, expectedValue the Service DTO verbatim %+v", gotValue, expectedValue)
	}
	for _, field := range []string{`"percent"`, `"missing_pieces"`, `"kind"`, `"warnings"`, `"group_id"`, `"decision"`, `"candidates"`, `"current"`} {
		if !strings.Contains(body, field) {
			t.Fatalf("gotValue %q, expectedValue the field %s", body, field)
		}
	}
	expectedFilter := app.NewProposalFilter("pending", "partial_annex", 9, true, "")
	if fake.lastFilter != expectedFilter {
		t.Fatalf("gotValue filter %+v, expectedValue %+v", fake.lastFilter, expectedFilter)
	}
	badQuery := serve(handler, apiRequest(http.MethodGet, "/api/proposals?group=x", ""))
	expectError(t, "bad group", readBody(t, badQuery), badQuery.Code, http.StatusBadRequest, app.ErrorCodeUsage)
	unknownParameter := serve(handler, apiRequest(http.MethodGet, "/api/proposals?state=pending", ""))
	expectError(t, "unknown parameter", readBody(t, unknownParameter), unknownParameter.Code, http.StatusBadRequest, app.ErrorCodeUsage)
}

func TestProposals_emptyListIsAJSONArray(t *testing.T) {
	handler := newTestHandler(t, &fakeService{}, loopbackSettings())

	response := serve(handler, apiRequest(http.MethodGet, "/api/proposals", ""))

	if gotValue := strings.TrimSpace(readBody(t, response)); gotValue != "[]" {
		t.Fatalf("gotValue %q, expectedValue []", gotValue)
	}
}

func TestChoose_rejectsCandidateNotInProposal(t *testing.T) {
	fake := &fakeService{proposals: sampleProposals()}
	handler := newTestHandler(t, fake, loopbackSettings())

	stranger := serve(handler, apiRequest(http.MethodPost, "/api/proposals/5/choose", `{"candidate_id": 99}`))
	expectError(t, "stranger", readBody(t, stranger), stranger.Code, http.StatusUnprocessableEntity, app.ErrorCodeRefused)
	missing := serve(handler, apiRequest(http.MethodPost, "/api/proposals/5/choose", `{}`))
	expectError(t, "no candidate", readBody(t, missing), missing.Code, http.StatusBadRequest, app.ErrorCodeUsage)
	unknownProposal := serve(handler, apiRequest(http.MethodPost, "/api/proposals/77/choose", `{"candidate_id": 11}`))
	expectError(t, "unknown proposal", readBody(t, unknownProposal), unknownProposal.Code, http.StatusNotFound, app.ErrorCodeNotFound)
	if fake.called("Choose") {
		t.Fatal("gotValue a Choose call, expectedValue none for a refused candidate")
	}

	member := serve(handler, apiRequest(http.MethodPost, "/api/proposals/5/choose", `{"candidate_id": 12}`))

	if member.Code != http.StatusOK || !fake.called("Choose 22") {
		t.Fatalf("gotValue %d, calls %v, expectedValue Choose of the member's proposal 22", member.Code, fake.calls)
	}
}

func TestForce_onlyAllowedForPartialAnnexAlreadyLinkedAndSeededPaused(t *testing.T) {
	fake := &fakeService{forceErrors: map[int64]error{
		2: app.ErrForceNotAllowed,
		3: app.ErrNotForceable,
		4: fmt.Errorf("%w: generation 3", app.ErrStaleProposal),
	}}
	handler := newTestHandler(t, fake, loopbackSettings())

	unconfirmed := serve(handler, apiRequest(http.MethodPost, "/api/proposals/1/force", `{}`))
	expectError(t, "unconfirmed", readBody(t, unconfirmed), unconfirmed.Code, http.StatusBadRequest, errorCodeConfirmationRequired)
	if fake.called("Force") {
		t.Fatal("gotValue a Force call, expectedValue none without confirmation")
	}
	refusals := []struct {
		proposalID     int
		expectedStatus int
		expectedCode   string
	}{
		{2, http.StatusUnprocessableEntity, app.ErrorCodeRefused},
		{3, http.StatusUnprocessableEntity, app.ErrorCodeRefused},
		{4, http.StatusConflict, app.ErrorCodeStale},
	}
	for _, refusal := range refusals {
		target := "/api/proposals/" + strconv.Itoa(refusal.proposalID) + "/force"
		response := serve(handler, apiRequest(http.MethodPost, target, `{"confirm": true}`))
		expectError(t, target, readBody(t, response), response.Code, refusal.expectedStatus, refusal.expectedCode)
	}
	stale := serve(handler, apiRequest(http.MethodPost, "/api/proposals/4/force", `{"confirm": true}`))
	if gotMessage := decodeError(t, readBody(t, stale)).Error.Message; gotMessage != "stale proposal, re-verify" {
		t.Fatalf("gotValue %q, expectedValue %q", gotMessage, "stale proposal, re-verify")
	}

	forced := serve(handler, apiRequest(http.MethodPost, "/api/proposals/1/force", `{"confirm": true}`))

	var gotValue app.ForceResult
	if err := json.Unmarshal([]byte(readBody(t, forced)), &gotValue); err != nil || forced.Code != http.StatusOK || !gotValue.Forced {
		t.Fatalf("gotValue %d %+v, expectedValue a forced result", forced.Code, gotValue)
	}
}

func TestServer_proposalActionsCallTheService(t *testing.T) {
	fake := &fakeService{}
	handler := newTestHandler(t, fake, loopbackSettings())
	calls := []struct {
		target, body, expectedCall string
	}{
		{"/api/proposals/3/accept", `{"allow_partial_pack": true}`, fmt.Sprint("Accept 3 ", []app.DecisionOption{app.AllowPartialPack})},
		{"/api/proposals/3/reject", ``, "Reject 3"},
		{"/api/groups/8/accept", `{}`, "AcceptGroup 8"},
	}
	for _, call := range calls {
		response := serve(handler, apiRequest(http.MethodPost, call.target, call.body))
		if response.Code != http.StatusOK || !fake.called(call.expectedCall) {
			t.Fatalf("%s: gotValue %d, calls %v, expectedValue %q", call.target, response.Code, fake.calls, call.expectedCall)
		}
	}
	badID := serve(handler, apiRequest(http.MethodPost, "/api/proposals/-1/accept", `{}`))
	expectError(t, "bad id", readBody(t, badID), badID.Code, http.StatusBadRequest, app.ErrorCodeUsage)
	unknownField := serve(handler, apiRequest(http.MethodPost, "/api/proposals/3/accept", `{"force": true}`))
	expectError(t, "unknown field", readBody(t, unknownField), unknownField.Code, http.StatusBadRequest, app.ErrorCodeUsage)
}

func TestServer_doctorRunsSynchronouslyOnlyByPost(t *testing.T) {
	fake := &fakeService{}
	handler := newTestHandler(t, fake, loopbackSettings())

	all := serve(handler, apiRequest(http.MethodPost, "/api/doctor/run", `{"checks": ["config", "database"]}`))
	single := serve(handler, apiRequest(http.MethodPost, "/api/doctor/checks/config", `{}`))

	if all.Code != http.StatusOK || single.Code != http.StatusOK || !fake.called("Doctor [config database]") || !fake.called("Doctor [config]") {
		t.Fatalf("gotValue %d %d, calls %v, expectedValue both doctor calls", all.Code, single.Code, fake.calls)
	}
	for _, target := range []string{"/api/doctor", "/api/doctor/run", "/api/doctor/checks/config", "/api/doctor/config"} {
		if response := serve(handler, apiRequest(http.MethodGet, target, "")); response.Code == http.StatusOK {
			t.Fatalf("GET %s: gotValue 200, expectedValue the probing diagnostic refused without POST", target)
		}
	}
	fake.errors = map[string]error{"Doctor": fmt.Errorf("%w %q", app.ErrUnknownCheck, "nope")}
	unknown := serve(handler, apiRequest(http.MethodPost, "/api/doctor/checks/nope", `{}`))
	expectError(t, "unknown check", readBody(t, unknown), unknown.Code, http.StatusBadRequest, app.ErrorCodeUsage)
}

func TestServer_doctorReportCarriesCodeAndRenderedTitle(t *testing.T) {
	handler := newTestHandler(t, &fakeService{}, loopbackSettings())

	response := serve(handler, apiRequest(http.MethodPost, "/api/doctor/run", `{}`))

	var decoded struct {
		Checks []struct{ ID, Title, Status, Code, Detail string }
	}
	if err := json.Unmarshal([]byte(readBody(t, response)), &decoded); err != nil || len(decoded.Checks) != 1 {
		t.Fatalf("gotValue %v, %+v", err, decoded)
	}
	if check := decoded.Checks[0]; check.ID != "config" || check.Title != "Configuration" || check.Code != "ok" {
		t.Fatalf("gotValue %+v, expectedValue id config, title Configuration, code ok", check)
	}
}

func TestServer_unknownApiRouteAndStageAreJSONErrors(t *testing.T) {
	handler := newTestHandler(t, &fakeService{}, loopbackSettings())

	unknownRoute := serve(handler, apiRequest(http.MethodGet, "/api/nothing", ""))
	expectError(t, "unknown route", readBody(t, unknownRoute), unknownRoute.Code, http.StatusNotFound, app.ErrorCodeNotFound)
	unknownStage := serve(handler, apiRequest(http.MethodPost, "/api/stages/teleport/run", `{}`))
	expectError(t, "unknown stage", readBody(t, unknownStage), unknownStage.Code, http.StatusBadRequest, app.ErrorCodeUsage)
}

func TestServer_sendsSecurityHeaders(t *testing.T) {
	handler := newTestHandler(t, &fakeService{}, loopbackSettings())
	expectedHeaders := map[string]string{
		"X-Content-Type-Options":     "nosniff",
		"Referrer-Policy":            "no-referrer",
		"X-Frame-Options":            "DENY",
		"Cross-Origin-Opener-Policy": "same-origin",
		"Content-Security-Policy":    contentSecurityPolicy,
	}
	for _, target := range []string{"/", "/proposals", "/assets/app.js", "/api/status", "/healthz", "/api/nothing"} {
		response := serve(handler, apiRequest(http.MethodGet, target, ""))
		for name, expectedValue := range expectedHeaders {
			if gotValue := response.Header().Get(name); gotValue != expectedValue {
				t.Fatalf("%s: gotValue %s %q, expectedValue %q", target, name, gotValue, expectedValue)
			}
		}
		if response.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("%s: gotValue a CORS header, expectedValue none", target)
		}
	}
	for _, target := range []string{"/api/status", "/healthz"} {
		if gotValue := serve(handler, apiRequest(http.MethodGet, target, "")).Header().Get("Cache-Control"); gotValue != "no-store" {
			t.Fatalf("%s: gotValue Cache-Control %q, expectedValue no-store", target, gotValue)
		}
	}
	if gotValue := serve(handler, apiRequest(http.MethodGet, "/assets/app.js", "")).Header().Get("Cache-Control"); !strings.Contains(gotValue, "immutable") {
		t.Fatalf("gotValue Cache-Control %q, expectedValue immutable for a hashed asset", gotValue)
	}
}

func TestServer_rejectsUnknownHostHeader(t *testing.T) {
	handler := newTestHandler(t, &fakeService{}, loopbackSettings())
	for _, host := range []string{"evil.example.com", "evil.example.com:8787", "127.0.0.1:9999", ""} {
		request := apiRequest(http.MethodGet, "/api/status", "")
		request.Host = host

		response := serve(handler, request)

		expectError(t, "host "+host, readBody(t, response), response.Code, http.StatusForbidden, errorCodeForbidden)
	}
	for _, host := range []string{"127.0.0.1:8787", "localhost:8787", "LOCALHOST:8787", "[::1]:8787"} {
		request := apiRequest(http.MethodGet, "/api/status", "")
		request.Host = host
		if gotValue := serve(handler, request).Code; gotValue != http.StatusOK {
			t.Fatalf("host %q: gotValue %d, expectedValue 200", host, gotValue)
		}
	}
	networkHandler := newTestHandler(t, &fakeService{}, tokenSettings())
	proxied := apiRequest(http.MethodGet, "/api/status", "")
	proxied.Host = "necromanciarr.example.com"
	proxied.Header.Set("Authorization", "Bearer "+testToken)
	if gotValue := serve(networkHandler, proxied).Code; gotValue != http.StatusOK {
		t.Fatalf("gotValue %d, expectedValue 200: a token protects a non-loopback listener whatever its host name", gotValue)
	}
}

func TestServer_rejectsCrossSitePost(t *testing.T) {
	fake := &fakeService{}
	handler := newTestHandler(t, fake, loopbackSettings())
	attempts := []struct {
		label          string
		prepare        func(*http.Request)
		expectedStatus int
		expectedCode   string
	}{
		{"foreign origin", func(request *http.Request) { request.Header.Set("Origin", "http://evil.example.com") }, http.StatusForbidden, errorCodeForbidden},
		{"null origin", func(request *http.Request) { request.Header.Set("Origin", "null") }, http.StatusForbidden, errorCodeForbidden},
		{"no origin nor custom header", func(request *http.Request) { request.Header.Del("X-Requested-With") }, http.StatusForbidden, errorCodeForbidden},
		{"form content type", func(request *http.Request) { request.Header.Set("Content-Type", "text/plain") }, http.StatusUnsupportedMediaType, app.ErrorCodeUsage},
	}
	for _, attempt := range attempts {
		request := apiRequest(http.MethodPost, "/api/proposals/3/reject", `{}`)
		attempt.prepare(request)

		response := serve(handler, request)

		expectError(t, attempt.label, readBody(t, response), response.Code, attempt.expectedStatus, attempt.expectedCode)
	}
	if fake.called("Reject") {
		t.Fatal("gotValue a Reject call, expectedValue none for a cross-site request")
	}
	sameOrigin := apiRequest(http.MethodPost, "/api/proposals/3/reject", `{}`)
	sameOrigin.Header.Del("X-Requested-With")
	sameOrigin.Header.Set("Origin", "http://"+testHost)
	if gotValue := serve(handler, sameOrigin).Code; gotValue != http.StatusOK {
		t.Fatalf("gotValue %d, expectedValue 200 for a same-origin post", gotValue)
	}
	preflight := apiRequest(http.MethodOptions, "/api/proposals/3/reject", "")
	preflight.Header.Set("Origin", "http://evil.example.com")
	if serve(handler, preflight).Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("gotValue a CORS header on a preflight, expectedValue none")
	}
}

func TestServer_limitsBodySize(t *testing.T) {
	fake := &fakeService{}
	handler := newTestHandler(t, fake, loopbackSettings())
	oversized := `{"allow_partial_pack": true, "padding": "` + strings.Repeat("a", maxBodyBytes) + `"}`

	response := serve(handler, apiRequest(http.MethodPost, "/api/proposals/3/accept", oversized))

	expectError(t, "oversized", readBody(t, response), response.Code, http.StatusRequestEntityTooLarge, app.ErrorCodeUsage)
	if fake.called("Accept") {
		t.Fatal("gotValue an Accept call, expectedValue none for an oversized body")
	}
}

func TestServer_errorsNeverLeakSecretsOrPaths(t *testing.T) {
	settings := tokenSettings()
	settings.LibraryProviderToken = "provider-test-token"
	fake := &fakeService{
		status: app.Status{Warnings: []app.Warning{{Code: "warning.test", Text: "daemon refused provider-test-token at http://example.com/?apikey=provider-test-token"}}},
		errors: map[string]error{
			"Reject": errors.New("open /home/u/library/film.mkv: token=" + testToken + " goroutine 7 [running]"),
			"Accept": fmt.Errorf("%w: proposal 3 under /home/u/library", app.ErrConflictNeedsChoose),
		},
	}
	handler := newTestHandler(t, fake, settings)
	authorize := func(request *http.Request) *http.Request {
		request.Header.Set("Authorization", "Bearer "+testToken)
		return request
	}

	failure := serve(handler, authorize(apiRequest(http.MethodPost, "/api/proposals/3/reject", `{}`)))
	refusal := serve(handler, authorize(apiRequest(http.MethodPost, "/api/proposals/3/accept", `{}`)))
	status := serve(handler, authorize(apiRequest(http.MethodGet, "/api/status", "")))

	failureBody, refusalBody, statusBody := readBody(t, failure), readBody(t, refusal), readBody(t, status)
	expectError(t, "failure", failureBody, failure.Code, http.StatusInternalServerError, app.ErrorCodeFailure)
	expectError(t, "refusal", refusalBody, refusal.Code, http.StatusUnprocessableEntity, app.ErrorCodeRefused)
	if gotValue := decodeError(t, refusalBody).Error.Message; gotValue != "a conflict cannot be accepted, choose a torrent instead" {
		t.Fatalf("gotValue %q, expectedValue only the typed message", gotValue)
	}
	for _, body := range []string{failureBody, refusalBody, statusBody} {
		for _, leak := range []string{testToken, "provider-test-token", "/home/u", "goroutine"} {
			if strings.Contains(body, leak) {
				t.Fatalf("gotValue %q, expectedValue no %q", body, leak)
			}
		}
	}
}

func TestHealthz_returns200WhenDatabaseAnswersAnd503Otherwise(t *testing.T) {
	health := &fakeHealth{}
	handler := newTestHandlerWith(t, &fakeService{}, health, tokenSettings(), builtAssets())

	healthy := serve(handler, apiRequest(http.MethodGet, "/healthz", ""))
	if healthy.Code != http.StatusOK || readBody(t, healthy) != "ok" {
		t.Fatalf("gotValue %d %q, expectedValue 200 ok without authentication", healthy.Code, readBody(t, healthy))
	}
	if health.deadline.IsZero() {
		t.Fatal("gotValue no deadline, expectedValue a bounded ping")
	}
	health.err = errDatabaseDown

	unhealthy := serve(handler, apiRequest(http.MethodGet, "/healthz", ""))

	if unhealthy.Code != http.StatusServiceUnavailable || readBody(t, unhealthy) != "unavailable" {
		t.Fatalf("gotValue %d %q, expectedValue 503 unavailable", unhealthy.Code, readBody(t, unhealthy))
	}
}

func TestHealthz_exposesNoConfigurationData(t *testing.T) {
	settings := tokenSettings()
	settings.LibraryAPI = "http://example.com/api"
	settings.DBPath = "/home/u/data/necromanciarr.db"
	health := &fakeHealth{err: errors.New("open /home/u/data/necromanciarr.db: " + testToken)}
	handler := newTestHandlerWith(t, &fakeService{}, health, settings, builtAssets())
	request := apiRequest(http.MethodGet, "/healthz", "")
	request.Host = "evil.example.com"

	response := serve(handler, request)

	body := readBody(t, response)
	if body != "unavailable" || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("gotValue %q (%s), expectedValue only unavailable as text/plain", body, response.Header().Get("Content-Type"))
	}
	for name, values := range response.Header() {
		for _, value := range values {
			if strings.Contains(value, "/home/u") || strings.Contains(value, testToken) || strings.Contains(value, "example.com") {
				t.Fatalf("gotValue header %s: %q, expectedValue no configuration data", name, value)
			}
		}
	}
}

// Routes sans action du registre, autorisées explicitement : /healthz (sonde du conteneur, hors /api, sans
// authentification), le service des fichiers de l'interface (« / », hors /api) et /api/jobs, /api/jobs/{id} (suivi des
// actions longues lancées par les routes d'action, qui portent chacune leur nom d'action). Ces dernières déclarent leur
// justification dans la table des routes ; le test refuse toute autre route sans action.
var nonActionRoutePrefixes = []string{"GET /api/jobs"}

func TestWeb_everyRouteMapsToRegisteredAction(t *testing.T) {
	registeredNames := map[string]bool{}
	mutatingNames := map[string]bool{}
	for _, action := range app.Actions(i18n.New(i18n.English, nil)) {
		registeredNames[action.Name] = true
		mutatingNames[action.Name] = action.Mutating
	}
	if len(apiRoutes) == 0 || len(registeredNames) == 0 {
		t.Fatalf("gotValue %d routes and %d actions, expectedValue both tables filled", len(apiRoutes), len(registeredNames))
	}
	coveredNames := map[string]bool{}
	for _, apiRoute := range apiRoutes {
		pattern := apiRoute.pattern()
		if apiRoute.action == "" {
			isWhitelisted := slices.ContainsFunc(nonActionRoutePrefixes, func(prefix string) bool { return strings.HasPrefix(pattern, prefix) })
			if !isWhitelisted || apiRoute.justification == "" {
				t.Fatalf("route %s has no registered action and is not whitelisted with a justification", pattern)
			}
			continue
		}
		if !registeredNames[apiRoute.action] {
			t.Fatalf("route %s maps to %q, which is not in the registry", pattern, apiRoute.action)
		}
		if !strings.HasPrefix(apiRoute.path, "/api/") {
			t.Fatalf("route %s is not under /api", pattern)
		}
		isReadMethod := apiRoute.method == http.MethodGet
		isWriteMethod := slices.Contains([]string{http.MethodPost, http.MethodPut, http.MethodDelete}, apiRoute.method)
		isExpectedMethod := (mutatingNames[apiRoute.action] && isWriteMethod) || (!mutatingNames[apiRoute.action] && isReadMethod)
		if !isExpectedMethod {
			t.Fatalf("route %s: gotValue %s, expectedValue GET for a read, POST, PUT or DELETE for a mutating=%v action", pattern, apiRoute.method, mutatingNames[apiRoute.action])
		}
		coveredNames[apiRoute.action] = true
	}
	for name := range registeredNames {
		if !coveredNames[name] {
			t.Fatalf("action %q has no web route", name)
		}
	}
}

// forbiddenImports sont les couches que le web ne doit jamais appeler directement : il passe par app.Service.
var forbiddenImports = []string{"store", "pipeline", "linker", "dedupe", "repair"}

func TestServer_neverImportsStoreOrPipeline(t *testing.T) {
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	nested, err := filepath.Glob(filepath.Join("*", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, source := range slices.Concat(sources, nested) {
		if strings.HasSuffix(source, "_test.go") {
			continue
		}
		checked++
		content, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), source, content, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imported := range parsed.Imports {
			importPath := strings.Trim(imported.Path.Value, `"`)
			for _, forbidden := range forbiddenImports {
				if importPath == "necromanciarr/internal/"+forbidden {
					t.Fatalf("%s imports %s, expectedValue only app.Service", source, importPath)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("gotValue no source checked, expectedValue the web sources")
	}
}

func TestServer_wrongMethodOnAKnownPathIs405WithAllowBehindAuth(t *testing.T) {
	handler := newTestHandler(t, &fakeService{}, tokenSettings())
	anonymous := serve(handler, apiRequest(http.MethodGet, "/api/proposals/3/accept", ""))
	expectError(t, "anonymous", readBody(t, anonymous), anonymous.Code, http.StatusUnauthorized, errorCodeUnauthorized)
	request := apiRequest(http.MethodGet, "/api/proposals/3/accept", "")
	request.Header.Set("Authorization", "Bearer "+testToken)

	response := serve(handler, request)

	expectError(t, "wrong method", readBody(t, response), response.Code, http.StatusMethodNotAllowed, errorCodeMethodNotAllowed)
	if gotValue := response.Header().Get("Allow"); gotValue != http.MethodPost {
		t.Fatalf("gotValue Allow %q, expectedValue POST", gotValue)
	}
	statusPost := apiRequest(http.MethodPost, "/api/status", `{}`)
	statusPost.Header.Set("Authorization", "Bearer "+testToken)
	if gotValue := serve(handler, statusPost).Header().Get("Allow"); gotValue != "GET, HEAD" {
		t.Fatalf("gotValue Allow %q, expectedValue GET, HEAD", gotValue)
	}
}

func TestServer_rejectsCrossSiteReads(t *testing.T) {
	fake := &fakeService{}
	handler := newTestHandler(t, fake, loopbackSettings())
	crossSite := apiRequest(http.MethodGet, "/api/status", "")
	crossSite.Header.Set("Sec-Fetch-Site", "cross-site")

	response := serve(handler, crossSite)

	expectError(t, "cross-site read", readBody(t, response), response.Code, http.StatusForbidden, errorCodeForbidden)
	if fake.called("Status") {
		t.Fatal("gotValue a Status call, expectedValue none for a cross-site read")
	}
	for _, site := range []string{"same-origin", "none"} {
		request := apiRequest(http.MethodGet, "/api/status", "")
		request.Header.Set("Sec-Fetch-Site", site)
		if gotValue := serve(handler, request).Code; gotValue != http.StatusOK {
			t.Fatalf("%s: gotValue %d, expectedValue 200", site, gotValue)
		}
	}
}

func TestServer_acceptsAPortlessHostWhenListeningOnPort80(t *testing.T) {
	handler := newTestHandler(t, &fakeService{}, config.Config{ListenAddr: "127.0.0.1:80"})
	expectedStatuses := map[string]int{
		"localhost": http.StatusOK, "127.0.0.1": http.StatusOK, "[::1]": http.StatusOK, "localhost:80": http.StatusOK,
		"evil.example.com": http.StatusForbidden,
	}
	for host, expectedValue := range expectedStatuses {
		request := apiRequest(http.MethodGet, "/api/status", "")
		request.Host = host
		if gotValue := serve(handler, request).Code; gotValue != expectedValue {
			t.Fatalf("host %q: gotValue %d, expectedValue %d", host, gotValue, expectedValue)
		}
	}
	otherPort := newTestHandler(t, &fakeService{}, loopbackSettings())
	portless := apiRequest(http.MethodGet, "/api/status", "")
	portless.Host = "localhost"
	if gotValue := serve(otherPort, portless).Code; gotValue != http.StatusForbidden {
		t.Fatalf("gotValue %d, expectedValue 403 for a portless host on port 8787", gotValue)
	}
}

func TestWeb_linksRouteRequiresAuthValidatesFiltersAndReturnsThePage(t *testing.T) {
	page := app.LinkPage{
		Items:  []app.LinkItem{{ID: 1, LinkPath: "/home/u/links/a.mkv", TargetPath: "/home/u/library/a.mkv", Kind: "symlink", State: app.LinkStateLost}},
		Total:  1,
		Counts: map[string]int{app.LinkStateOK: 0, app.LinkStateDiverged: 0, app.LinkStateLost: 1, app.LinkStateUnreadable: 0},
	}
	fake := &fakeService{linkPage: page}
	guarded := newTestHandler(t, fake, tokenSettings())
	anonymous := serve(guarded, apiRequest(http.MethodGet, "/api/links", ""))
	expectError(t, "no token", readBody(t, anonymous), anonymous.Code, http.StatusUnauthorized, errorCodeUnauthorized)
	if fake.called("ListLinks") {
		t.Fatal("gotValue a ListLinks call, expectedValue none without a valid token")
	}

	handler := newTestHandler(t, fake, loopbackSettings())
	response := serve(handler, apiRequest(http.MethodGet, "/api/links?state=lost&limit=5000&offset=3", ""))
	var gotValue app.LinkPage
	if err := json.Unmarshal([]byte(readBody(t, response)), &gotValue); err != nil || response.Code != http.StatusOK {
		t.Fatalf("gotValue %d %v, expectedValue 200 with a link page", response.Code, err)
	}
	if !reflect.DeepEqual(gotValue, page) {
		t.Fatalf("gotValue %+v, expectedValue the Service page verbatim %+v", gotValue, page)
	}
	if expectedFilter := (app.LinkFilter{State: "lost", Limit: 5000, Offset: 3}); fake.lastLinks != expectedFilter {
		t.Fatalf("gotValue filter %+v, expectedValue %+v (the Service clamps the limit)", fake.lastLinks, expectedFilter)
	}
	for label, target := range map[string]string{
		"bad limit": "/api/links?limit=many", "negative offset": "/api/links?offset=-1", "unknown parameter": "/api/links?decision=ok",
	} {
		rejected := serve(handler, apiRequest(http.MethodGet, target, ""))
		expectError(t, label, readBody(t, rejected), rejected.Code, http.StatusBadRequest, app.ErrorCodeUsage)
	}
}

func TestWeb_linksRouteRefusesAnUnknownStateAndClampsTheLimitThroughTheService(t *testing.T) {
	openedStore, err := store.Open(filepath.Join(t.TempDir(), "necromanciarr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = openedStore.Close() })
	for index := range app.MaxLinkLimit + 1 {
		link := store.Link{Kind: "symlink", LinkPath: fmt.Sprintf("/home/u/links/missing-%04d.mkv", index), TargetPath: "/home/u/library/x.mkv", State: store.LinkOK}
		if err := openedStore.RecordLink(t.Context(), link); err != nil {
			t.Fatal(err)
		}
	}
	serverContext, cancel := context.WithCancel(context.Background())
	handler := NewHandlerWithAssets(serverContext, app.New(app.Deps{Store: openedStore}), &fakeHealth{}, loopbackSettings(), builtAssets())
	t.Cleanup(func() {
		cancel()
		handler.Wait()
	})

	unknown := serve(handler, apiRequest(http.MethodGet, "/api/links?state=broken", ""))
	expectError(t, "unknown state", readBody(t, unknown), unknown.Code, http.StatusBadRequest, app.ErrorCodeUsage)
	clamped := serve(handler, apiRequest(http.MethodGet, "/api/links?state=lost&limit=5000", ""))
	var gotValue app.LinkPage
	if err := json.Unmarshal([]byte(readBody(t, clamped)), &gotValue); err != nil || clamped.Code != http.StatusOK {
		t.Fatalf("gotValue %d %v, expectedValue 200", clamped.Code, err)
	}
	if len(gotValue.Items) != app.MaxLinkLimit || gotValue.Total != app.MaxLinkLimit+1 || gotValue.Next != app.MaxLinkLimit {
		t.Fatalf("gotValue %d items, total %d, next %d, expectedValue the limit clamped to %d", len(gotValue.Items), gotValue.Total, gotValue.Next, app.MaxLinkLimit)
	}
}

func TestWeb_linksRouteAnswers409WhileAnInspectionRuns(t *testing.T) {
	fake := &fakeService{errors: map[string]error{"ListLinks": app.ErrInspectionBusy}}
	handler := newTestHandler(t, fake, loopbackSettings())

	response := serve(handler, apiRequest(http.MethodGet, "/api/links", ""))

	expectError(t, "busy", readBody(t, response), response.Code, http.StatusConflict, app.ErrorCodeInspectionBusy)
}

// Passkeys factices à faible entropie ; la seconde change sous l'échappement JSON et l'encodage en pourcentage.
const (
	webPasskeyA = "0000aaaa0000aaaa0000aaaa0000aaaa"
	webPasskeyB = `fake"pass&key/0000`
)

func trackerSettings(t *testing.T) config.Config {
	t.Helper()
	content := `{"trackers": {"tracker-a": "https://tracker-a.example.com/announce/0000aaaa0000aaaa0000aaaa0000aaaa",
		"tracker-b": {"announce": ["udp://tracker-b.example.com:6969/announce?pk=fake\"pass&key/0000"]}}}`
	settings, err := config.LoadWith(nil, func(string) (string, bool) { return "", false }, t.TempDir(), configtest.Opener(t, content))
	if err != nil {
		t.Fatal(err)
	}
	settings.ListenAddr = testListen
	return settings
}

func webLeakingText() string {
	return "announce https://tracker-a.example.com/announce/" + webPasskeyA + " refused; " +
		"HTTPS://TRACKER-A.EXAMPLE.COM/ANNOUNCE/" + strings.ToUpper(webPasskeyA) + "; " +
		"udp://tracker-b.example.com:6969/announce?pk=" + url.QueryEscape(webPasskeyB) + "; bare " + webPasskeyB + " " +
		strings.ToUpper(webPasskeyB) + " " + url.PathEscape(webPasskeyB)
}

func expectNoWebPasskey(t *testing.T, label, output string) {
	t.Helper()
	for _, form := range []string{
		webPasskeyA, strings.ToUpper(webPasskeyA), "0000aaaa", webPasskeyB, strings.ToUpper(webPasskeyB),
		url.QueryEscape(webPasskeyB), url.PathEscape(webPasskeyB), `fake\"pass`, `pass&key`, "pass&key", "pass%26key",
	} {
		if strings.Contains(output, form) {
			t.Fatalf("%s: gotValue %q in %q, expectedValue no form of the passkey", label, form, output)
		}
	}
}

func TestRedact_trackerPasskeyEverywhere(t *testing.T) {
	leak := webLeakingText()
	fake := &fakeService{
		language: i18n.English,
		status:   app.Status{Warnings: []app.Warning{{Code: "warning.test", Text: leak}}},
		errors:   map[string]error{"Reject": errors.New("reject failed: " + leak), "RunStage": errors.New("seed failed: " + leak)},
	}
	handler := newTestHandler(t, fake, trackerSettings(t))
	logged := captureLog(handler)

	statusBody := readBody(t, serve(handler, apiRequest(http.MethodGet, "/api/status", "")))
	errorBody := readBody(t, serve(handler, apiRequest(http.MethodPost, "/api/proposals/3/reject", `{}`)))
	job := waitJob(t, handler, startJob(t, handler, "/api/stages/scan/run", `{}`).ID)
	jobsBody := readBody(t, serve(handler, apiRequest(http.MethodGet, "/api/jobs", "")))
	panicking := &fakeService{language: i18n.English, panicValue: "boom near " + leak}
	panicHandler := newTestHandler(t, panicking, trackerSettings(t))
	panicLog := captureLog(panicHandler)
	waitJob(t, panicHandler, startJob(t, panicHandler, "/api/stages/scan/run", `{}`).ID)

	if !strings.Contains(statusBody, "tracker-a.example.com") || job.Error == nil {
		t.Fatalf("gotValue status %q and job %+v, expectedValue the redacted warning and a failed job", statusBody, job)
	}
	for label, output := range map[string]string{"status": statusBody, "error body": errorBody, "jobs": jobsBody, "log": logged.String(), "panic log": panicLog.String()} {
		expectNoWebPasskey(t, label, output)
	}
}

func TestWeb_sourcesListIsAReadAndTestIsAGuardedPost(t *testing.T) {
	rows := []app.SourceRow{{Order: 1, Source: "provider", Type: app.SourceTypeProvider, State: app.SourceStateOK}}
	fake := &fakeService{sourceRows: rows}
	handler := newTestHandler(t, fake, loopbackSettings())

	listed := serve(handler, apiRequest(http.MethodGet, "/api/sources", ""))
	tested := serve(handler, apiRequest(http.MethodPost, "/api/sources/torznab:a/test", `{}`))
	crossSite := apiRequest(http.MethodPost, "/api/sources/torznab:a/test", `{}`)
	crossSite.Header.Del("X-Requested-With")
	refused := serve(handler, crossSite)
	byGet := serve(handler, apiRequest(http.MethodGet, "/api/sources/torznab:a/test", ""))

	var gotRows []app.SourceRow
	var gotTest app.SourceTestResult
	listErr := json.Unmarshal([]byte(readBody(t, listed)), &gotRows)
	testErr := json.Unmarshal([]byte(readBody(t, tested)), &gotTest)
	isExpected := listed.Code == http.StatusOK && listErr == nil && reflect.DeepEqual(gotRows, rows) &&
		tested.Code == http.StatusOK && testErr == nil && gotTest.Error == "indexer_access_denied" &&
		refused.Code == http.StatusForbidden && byGet.Code == http.StatusMethodNotAllowed && fake.called("TestSource torznab:a")
	if !isExpected {
		t.Fatalf("valeurObtenue %d %v / %d %+v %v / cross-site %d / GET %d, calls %v", listed.Code, gotRows, tested.Code, gotTest, testErr,
			refused.Code, byGet.Code, fake.calls)
	}
	if strings.Count(strings.Join(fake.calls, "|"), "TestSource") != 1 {
		t.Fatalf("valeurObtenue calls %v, valeurAttendue one TestSource (the cross-site post refused)", fake.calls)
	}
	empty := serve(newTestHandler(t, &fakeService{}, loopbackSettings()), apiRequest(http.MethodGet, "/api/sources", ""))
	if body := strings.TrimSpace(readBody(t, empty)); body != "[]" {
		t.Fatalf("valeurObtenue %q, valeurAttendue an empty JSON array", body)
	}
}

func TestWeb_volumesListAndCommandsAreReadsAndProbeIsAGuardedPost(t *testing.T) {
	rows := []app.VolumeRow{{Type: app.VolumeTypeLocal, State: "ok"}, {Volume: "seedbox", Type: app.VolumeTypeSFTP, Address: "seedbox.example.com:22", State: "unknown"}}
	fake := &fakeService{volumeRows: rows}
	handler := newTestHandler(t, fake, loopbackSettings())

	listed := serve(handler, apiRequest(http.MethodGet, "/api/volumes", ""))
	commands := serve(handler, apiRequest(http.MethodGet, "/api/volumes/seedbox/commands", ""))
	probed := serve(handler, apiRequest(http.MethodPost, "/api/volumes/seedbox/probe", `{}`))
	crossSite := apiRequest(http.MethodPost, "/api/volumes/seedbox/probe", `{}`)
	crossSite.Header.Del("X-Requested-With")
	refused := serve(handler, crossSite)
	byGet := serve(handler, apiRequest(http.MethodGet, "/api/volumes/seedbox/probe", ""))

	var gotRows []app.VolumeRow
	var gotCommands app.VolumeCommands
	var gotProbe app.VolumeProbeResult
	listErr := json.Unmarshal([]byte(readBody(t, listed)), &gotRows)
	commandsErr := json.Unmarshal([]byte(readBody(t, commands)), &gotCommands)
	probeErr := json.Unmarshal([]byte(readBody(t, probed)), &gotProbe)
	isExpected := listed.Code == http.StatusOK && listErr == nil && reflect.DeepEqual(gotRows, rows) &&
		commands.Code == http.StatusOK && commandsErr == nil && gotCommands.Lines[0] == "stat\t--version" &&
		probed.Code == http.StatusOK && probeErr == nil && gotProbe.Error == "host_key_unknown" &&
		refused.Code == http.StatusForbidden && byGet.Code == http.StatusMethodNotAllowed
	if !isExpected {
		t.Fatalf("gotValue %d %v / %d %+v / %d %+v / cross-site %d / GET %d, calls %v", listed.Code, gotRows, commands.Code, gotCommands,
			probed.Code, gotProbe, refused.Code, byGet.Code, fake.calls)
	}
	if gotValue := strings.Count(strings.Join(fake.calls, "|"), "ProbeVolume"); gotValue != 1 {
		t.Fatalf("gotValue calls %v, expectedValue one ProbeVolume (the cross-site post refused)", fake.calls)
	}
	empty := serve(newTestHandler(t, &fakeService{}, loopbackSettings()), apiRequest(http.MethodGet, "/api/volumes", ""))
	if body := strings.TrimSpace(readBody(t, empty)); body != "[]" {
		t.Fatalf("gotValue %q, expectedValue an empty JSON array", body)
	}
}

// cliOnlyCommands : entrées de la CLI sans action du registre ni route : sftp trust écrit dans le
// known_hosts de l'opérateur, ce qu'aucune page web ne doit pouvoir déclencher (empreinte vue à distance, piège à
// hameçonnage, spec 4.C.5 et Q13). La CLI en garde la même liste (cmd/necromanciarr).
var cliOnlyCommands = []string{"sftp trust"}

func TestWeb_cliOnlyCommandsHaveNoRouteAndNoAction(t *testing.T) {
	for _, command := range cliOnlyCommands {
		for _, action := range app.Actions(i18n.New(i18n.English, nil)) {
			if action.Name == command {
				t.Fatalf("gotValue action %q in the registry, expectedValue a CLI-only command", command)
			}
		}
		words := strings.Fields(command)
		for _, apiRoute := range apiRoutes {
			if apiRoute.action == command || strings.Contains(apiRoute.path, words[len(words)-1]) {
				t.Fatalf("gotValue route %s for %q, expectedValue no web route", apiRoute.pattern(), command)
			}
		}
	}
}

func TestWeb_proposalsSourceQueryReachesTheFilter(t *testing.T) {
	fake := &fakeService{}
	handler := newTestHandler(t, fake, loopbackSettings())

	response := serve(handler, apiRequest(http.MethodGet, "/api/proposals?source=torznab:a", ""))

	if response.Code != http.StatusOK || fake.lastFilter.Source != "torznab:a" {
		t.Fatalf("valeurObtenue %d, filter %+v, valeurAttendue the source filter passed on", response.Code, fake.lastFilter)
	}
}
