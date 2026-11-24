package web

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"necromanciarr/internal/app"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/store"
)

type languageRequest struct {
	label          string
	build          func() *http.Request
	settingsToken  bool
	fakeErrors     map[string]error
	expectedStatus int
	expectedCode   string
}

func languageRequests() []languageRequest {
	return []languageRequest{
		{
			label: "typed refusal", build: func() *http.Request { return apiRequest(http.MethodPost, "/api/proposals/2/accept", `{}`) },
			fakeErrors: map[string]error{"Accept": app.ErrConflictNeedsChoose}, expectedStatus: http.StatusUnprocessableEntity, expectedCode: app.ErrorCodeRefused,
		},
		{
			label: "stale proposal", build: func() *http.Request { return apiRequest(http.MethodPost, "/api/proposals/2/accept", `{}`) },
			fakeErrors: map[string]error{"Accept": app.ErrStaleProposal}, expectedStatus: http.StatusConflict, expectedCode: app.ErrorCodeStale,
		},
		{
			label: "untyped failure", build: func() *http.Request { return apiRequest(http.MethodGet, "/api/status", "") },
			fakeErrors: map[string]error{"Status": context.DeadlineExceeded}, expectedStatus: http.StatusInternalServerError, expectedCode: app.ErrorCodeFailure,
		},
		{
			label: "unknown route", build: func() *http.Request { return apiRequest(http.MethodGet, "/api/nothing", "") },
			expectedStatus: http.StatusNotFound, expectedCode: errorCodeNotFound,
		},
		{
			label: "invalid id", build: func() *http.Request { return apiRequest(http.MethodPost, "/api/proposals/zero/accept", `{}`) },
			expectedStatus: http.StatusBadRequest, expectedCode: errorCodeUsage,
		},
		{
			label: "unauthorized before any language is known", settingsToken: true,
			build:          func() *http.Request { return apiRequest(http.MethodGet, "/api/status", "") },
			expectedStatus: http.StatusUnauthorized, expectedCode: errorCodeUnauthorized,
		},
	}
}

func errorOf(t *testing.T, language i18n.Language, testCase languageRequest) (int, errorEnvelope) {
	t.Helper()
	fake := &fakeService{errors: testCase.fakeErrors, language: language}
	settings := loopbackSettings()
	if testCase.settingsToken {
		settings = tokenSettings()
	}
	request := testCase.build()
	if testCase.settingsToken {
		request.Host = "localhost:8787"
	}
	recorder := serve(newTestHandler(t, fake, settings), request)
	return recorder.Code, decodeError(t, readBody(t, recorder))
}

func TestAPIErrors_codeStableAcrossLanguages(t *testing.T) {
	for _, testCase := range languageRequests() {
		t.Run(testCase.label, func(t *testing.T) {
			frenchStatus, frenchBody := errorOf(t, i18n.French, testCase)
			englishStatus, englishBody := errorOf(t, i18n.English, testCase)

			if frenchStatus != testCase.expectedStatus || englishStatus != testCase.expectedStatus {
				t.Fatalf("gotValue status fr %d en %d, expectedValue %d", frenchStatus, englishStatus, testCase.expectedStatus)
			}
			if frenchBody.Error.Code != testCase.expectedCode || englishBody.Error.Code != testCase.expectedCode {
				t.Fatalf("gotValue code fr %q en %q, expectedValue %q", frenchBody.Error.Code, englishBody.Error.Code, testCase.expectedCode)
			}
			if frenchBody.Error.Message == "" || frenchBody.Error.Message == englishBody.Error.Message {
				t.Fatalf("gotValue message fr %q en %q, expectedValue two distinct texts", frenchBody.Error.Message, englishBody.Error.Message)
			}
			if strings.HasPrefix(englishBody.Error.Message, "error.") || strings.HasPrefix(frenchBody.Error.Message, "error.") {
				t.Fatalf("gotValue a raw key: fr %q en %q", frenchBody.Error.Message, englishBody.Error.Message)
			}
		})
	}
}

func TestAPIErrors_jobFailureIsRenderedInTheLanguage(t *testing.T) {
	fake := &fakeService{errors: map[string]error{"RunStage": app.ErrNotConfigured}, language: i18n.French}
	handler := newTestHandler(t, fake, loopbackSettings())

	started := startJob(t, handler, "/api/stages/scan/run", `{}`)
	job := waitJob(t, handler, started.ID)

	if job.Error == nil || job.Error.Code != app.ErrorCodeNotConfigured || job.Error.Message != "composant non configuré" {
		t.Fatalf("gotValue %+v, expectedValue the French not_configured message with the stable code", job.Error)
	}
}

func TestStatus_reportsLanguage(t *testing.T) {
	openedStore, err := store.Open(filepath.Join(t.TempDir(), "necromanciarr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = openedStore.Close() })
	service := app.New(app.Deps{Store: openedStore, Translator: i18n.New(i18n.English, nil)})
	handler := NewHandlerWithAssets(t.Context(), service, &fakeHealth{}, loopbackSettings(), builtAssets())
	t.Cleanup(handler.Wait)

	response := serve(handler, apiRequest(http.MethodGet, "/api/status", ""))

	if body := readBody(t, response); response.Code != http.StatusOK || !strings.Contains(body, `"language": "en"`) {
		t.Fatalf("gotValue %d %q, expectedValue a status reporting language en", response.Code, body)
	}
}
