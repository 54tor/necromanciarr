package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"necromanciarr/internal/app"
	"necromanciarr/internal/i18n"
)

// lockedBuffer reçoit le journal depuis la goroutine du job pendant que le test lit.
type lockedBuffer struct {
	mutex   sync.Mutex
	builder strings.Builder
}

func (buffer *lockedBuffer) Write(data []byte) (int, error) {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	return buffer.builder.Write(data)
}

func (buffer *lockedBuffer) String() string {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	return buffer.builder.String()
}

// captureLog branche sur le handler un journal texte lisible par le test.
func captureLog(handler *Handler) *lockedBuffer {
	buffer := &lockedBuffer{}
	handler.UseLogger(slog.New(slog.NewTextHandler(buffer, nil)))
	return buffer
}

func failingJobLog(t *testing.T, language i18n.Language) string {
	t.Helper()
	const providerToken = "fake-provider-token-value"
	fake := &fakeService{language: language, errors: map[string]error{"RunStage": errors.New("disk failed near " + providerToken)}}
	settings := loopbackSettings()
	settings.LibraryProviderToken = providerToken
	handler := newTestHandler(t, fake, settings)
	logged := captureLog(handler)

	waitJob(t, handler, startJob(t, handler, "/api/stages/scan/run", `{}`).ID)

	if strings.Contains(logged.String(), providerToken) {
		t.Fatalf("language %s: gotValue %q, expectedValue the known secret masked", language, logged.String())
	}
	return logged.String()
}

func TestJobLog_lineHasStableCodeAndTranslatedText(t *testing.T) {
	french, english := failingJobLog(t, i18n.French), failingJobLog(t, i18n.English)

	for language, line := range map[i18n.Language]string{i18n.French: french, i18n.English: english} {
		if !strings.Contains(line, "code=job_failed") || !strings.Contains(line, "disk failed near") {
			t.Fatalf("language %s: gotValue %q, expectedValue the stable code and the redacted detail", language, line)
		}
	}
	if !strings.Contains(french, "a échoué") || !strings.Contains(english, "failed") || strings.Contains(english, "a échoué") {
		t.Fatalf("gotValue %q and %q, expectedValue one line per language", french, english)
	}
}

func TestJobLog_panicIsRedacted(t *testing.T) {
	const providerToken = "fake-provider-token-value"
	fake := &fakeService{panicValue: "boom near " + providerToken, language: i18n.French}
	settings := loopbackSettings()
	settings.LibraryProviderToken = providerToken
	handler := newTestHandler(t, fake, settings)
	logged := captureLog(handler)

	waitJob(t, handler, startJob(t, handler, "/api/stages/scan/run", `{}`).ID)

	if line := logged.String(); strings.Contains(line, providerToken) || !strings.Contains(line, "code=job_panicked") || !strings.Contains(line, "level=ERROR") {
		t.Fatalf("gotValue %q, expectedValue an error line with the panic code and no secret", line)
	}
}

func TestServerLog_everyLineCarriesACode(t *testing.T) {
	fake := &fakeService{errors: map[string]error{"Status": errors.New("database is down")}}
	handler := newTestHandler(t, fake, tokenSettings())
	logged := captureLog(handler)

	serve(handler, apiRequest(http.MethodGet, "/api/status", ""))
	authorized := apiRequest(http.MethodGet, "/api/status", "")
	authorized.Header.Set("Authorization", "Bearer "+testToken)
	serve(handler, authorized)

	lines := strings.Split(strings.TrimSpace(logged.String()), "\n")
	expectedCodes := []string{"code=api_unauthorized", "code=api_internal_error"}
	if len(lines) != len(expectedCodes) {
		t.Fatalf("gotValue %q, expectedValue %d lines", lines, len(expectedCodes))
	}
	for position, expectedCode := range expectedCodes {
		if !strings.Contains(lines[position], expectedCode) {
			t.Fatalf("line %d: gotValue %q, expectedValue %s", position, lines[position], expectedCode)
		}
	}
}

func TestJobLog_urlCredentialsAndApiKeyQueryAreMaskedWithoutAKnownSecret(t *testing.T) {
	const leaked = "dial http://alice:hunter2pass@example.com/x?apikey=aaaaaaaaaaaa failed"
	fake := &fakeService{language: i18n.English, errors: map[string]error{"RunStage": errors.New(leaked)}}
	handler := newTestHandler(t, fake, loopbackSettings())
	logged := captureLog(handler)

	waitJob(t, handler, startJob(t, handler, "/api/stages/scan/run", `{}`).ID)

	if line := logged.String(); strings.Contains(line, "hunter2pass") || strings.Contains(line, "aaaaaaaaaaaa") || !strings.Contains(line, "code=job_failed") {
		t.Fatalf("gotValue %q, expectedValue the URL credentials and the apikey masked", line)
	}
}

func TestEndpoint_panicInASynchronousHandlerIsLoggedOnceWithItsCodeAndAnswersGenerically(t *testing.T) {
	const providerToken = "fake-provider-token-value"
	settings := loopbackSettings()
	settings.LibraryProviderToken = providerToken
	handler := newTestHandler(t, &fakeService{language: i18n.English}, settings)
	logged := captureLog(handler)
	panicking := handler.endpoint(func(*Handler, *http.Request) (any, int, error) {
		panic("boom near " + providerToken + " in /home/u/library")
	})

	response := serve(panicking, apiRequest(http.MethodGet, "/api/status", ""))

	lines := strings.Split(strings.TrimSpace(logged.String()), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], "code=api_panicked") || strings.Contains(lines[0], providerToken) {
		t.Fatalf("gotValue %q, expectedValue one api_panicked line without the secret", lines)
	}
	body := readBody(t, response)
	expectError(t, "panic", body, response.Code, http.StatusInternalServerError, app.ErrorCodeFailure)
	if strings.Contains(body, "boom") || strings.Contains(body, "/home/u") || strings.Contains(body, providerToken) {
		t.Fatalf("gotValue %q, expectedValue the generic internal-error body", body)
	}
}

func TestServerErrorLog_goesThroughTheCodedRedactedLogger(t *testing.T) {
	const providerToken = "fake-provider-token-value"
	settings := loopbackSettings()
	settings.LibraryProviderToken = providerToken
	handler := newTestHandler(t, &fakeService{language: i18n.French}, settings)
	logged := captureLog(handler)

	NewServer(t.Context(), handler).ErrorLog.Print("http: TLS handshake error near " + providerToken + "\n")

	line := logged.String()
	if !strings.Contains(line, "code=http_server_error") || strings.Contains(line, providerToken) || strings.Count(line, "\n") != 1 || !strings.Contains(line, "erreur") {
		t.Fatalf("gotValue %q, expectedValue one translated http_server_error line without the secret", line)
	}
}
