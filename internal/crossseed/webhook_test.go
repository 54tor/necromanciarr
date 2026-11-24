package crossseed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testInfoHash = "0123456789abcdef0123456789abcdef01234567"
	testAPIKey   = "secret-key-for-test"
)

type recordedRequest struct {
	method, path, rawQuery, apiKey, contentType string
	body                                        []byte
}

func newRecordingServer(t *testing.T, status int, recorded *recordedRequest) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		*recorded = recordedRequest{
			request.Method, request.URL.Path, request.URL.RawQuery,
			request.Header.Get("X-Api-Key"), request.Header.Get("Content-Type"), body,
		}
		writer.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	return server
}

func mustWebhook(t *testing.T, baseURL string, options ...WebhookOption) *Webhook {
	t.Helper()
	webhook, err := NewWebhook(baseURL, testAPIKey, options...)
	if err != nil {
		t.Fatal(err)
	}
	return webhook
}

func TestNotify_postsInfoHashJsonAndAcceptsNoContent(t *testing.T) {
	var recorded recordedRequest
	server := newRecordingServer(t, http.StatusNoContent, &recorded)

	err := mustWebhook(t, server.URL).Notify(context.Background(), testInfoHash)
	if err != nil {
		t.Fatal(err)
	}
	if recorded.method != http.MethodPost || recorded.path != "/api/webhook" {
		t.Fatalf("gotValue %s %s", recorded.method, recorded.path)
	}
	var payload map[string]string
	if err := json.Unmarshal(recorded.body, &payload); err != nil || payload["infoHash"] != testInfoHash || len(payload) != 1 {
		t.Fatalf("unexpected body %q", recorded.body)
	}
	if !strings.HasPrefix(recorded.contentType, "application/json") {
		t.Fatalf("gotValue content type %q", recorded.contentType)
	}
}

func TestNotify_sendsApiKeyInHeaderNotInUrl(t *testing.T) {
	var recorded recordedRequest
	server := newRecordingServer(t, http.StatusNoContent, &recorded)

	if err := mustWebhook(t, server.URL).Notify(context.Background(), testInfoHash); err != nil {
		t.Fatal(err)
	}

	if recorded.apiKey != testAPIKey {
		t.Fatalf("header X-Api-Key not sent")
	}
	if recorded.rawQuery != "" || strings.Contains(recorded.path, testAPIKey) {
		t.Fatalf("key leaked into the url: %q %q", recorded.path, recorded.rawQuery)
	}
}

func TestNotify_sendsApiKeyWithoutLeakingIt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte("echo " + testAPIKey))
	}))
	defer server.Close()
	webhook := mustWebhook(t, server.URL)

	err := webhook.Notify(context.Background(), testInfoHash)

	outputs := []string{
		fmt.Sprint(err), webhook.String(), webhook.GoString(), fmt.Sprintf("%+v", webhook),
		fmt.Sprintf("%#v", webhook), fmt.Sprintf("%v", *webhook), webhook.LogValue().String(),
	}
	for _, output := range outputs {
		if strings.Contains(output, testAPIKey) || strings.Contains(output, server.URL) {
			t.Fatalf("leak in %q", output)
		}
	}
}

func TestNotify_returnsTypedErrorOnFailureStatus(t *testing.T) {
	cases := []struct {
		status        int
		expectedError error
	}{
		{http.StatusOK, ErrUnexpectedStatus},
		{http.StatusCreated, ErrUnexpectedStatus},
		{http.StatusMovedPermanently, ErrUnexpectedStatus},
		{http.StatusUnauthorized, ErrAccessDenied},
		{http.StatusForbidden, ErrAccessDenied},
		{http.StatusNotFound, ErrNotFound},
		{http.StatusConflict, ErrUnavailable},
		{http.StatusTooManyRequests, ErrUnavailable},
		{http.StatusInternalServerError, ErrUnavailable},
	}
	for _, testCase := range cases {
		t.Run(fmt.Sprint(testCase.status), func(t *testing.T) {
			var recorded recordedRequest
			server := newRecordingServer(t, testCase.status, &recorded)

			gotError := mustWebhook(t, server.URL).Notify(context.Background(), testInfoHash)

			if !errors.Is(gotError, testCase.expectedError) {
				t.Fatalf("gotValue %v, expectedValue %v", gotError, testCase.expectedError)
			}
		})
	}
}

func TestNotify_respectsContextTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer server.Close()
	defer close(release)

	start := time.Now()
	gotError := mustWebhook(t, server.URL).Notify(contextWithTimeout(t, 100*time.Millisecond), testInfoHash)

	if !errors.Is(gotError, ErrUnavailable) || time.Since(start) > 3*time.Second {
		t.Fatalf("gotValue %v after %s", gotError, time.Since(start))
	}
}

func TestNotify_respectsConfiguredTimeoutOption(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer server.Close()
	defer close(release)

	gotError := mustWebhook(t, server.URL, WithTimeout(100*time.Millisecond)).Notify(context.Background(), testInfoHash)

	if !errors.Is(gotError, ErrUnavailable) {
		t.Fatalf("gotValue %v", gotError)
	}
}

func contextWithTimeout(t *testing.T, timeout time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	t.Cleanup(cancel)
	return ctx
}

func TestNotify_refusesRedirect(t *testing.T) {
	var secondHits atomic.Int32
	second := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { secondHits.Add(1) }))
	defer second.Close()
	first := httptest.NewServer(http.RedirectHandler(second.URL, http.StatusFound))
	defer first.Close()

	gotError := mustWebhook(t, first.URL).Notify(context.Background(), testInfoHash)

	if !errors.Is(gotError, ErrUnexpectedStatus) || secondHits.Load() != 0 {
		t.Fatalf("gotValue %v, second server hits %d", gotError, secondHits.Load())
	}
}

func TestNotify_defaultTransportIgnoresProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:9")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9")

	webhook := mustWebhook(t, "http://example.com")

	transport, isTransport := webhook.httpClient.Transport.(*http.Transport)
	if !isTransport || transport.Proxy != nil {
		t.Fatalf("transport = %#v", webhook.httpClient.Transport)
	}
}

func TestNewWebhook_rejectsControlCharactersInApiKey(t *testing.T) {
	for _, badKey := range []string{"a\r\nX-Evil: 1", "a\nb", "a\x00b", "a\x7fb", "tab\tkey"} {
		_, err := NewWebhook("http://example.com", badKey)

		if !errors.Is(err, ErrInvalidAPIKey) || strings.Contains(err.Error(), "Evil") {
			t.Fatalf("key %q: gotValue %v", badKey, err)
		}
	}
}

func TestWithTimeout_ignoresNonPositiveValues(t *testing.T) {
	webhook := mustWebhook(t, "http://example.com", WithTimeout(0), WithTimeout(-time.Second))

	if webhook.httpClient.Timeout != defaultWebhookTimeout {
		t.Fatalf("gotValue %s", webhook.httpClient.Timeout)
	}
}

func TestNotify_keepsContextCause(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer server.Close()
	defer close(release)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	gotError := mustWebhook(t, server.URL).Notify(cancelled, testInfoHash)

	if !errors.Is(gotError, context.Canceled) || !errors.Is(gotError, ErrUnavailable) || strings.Contains(gotError.Error(), server.URL) {
		t.Fatalf("gotValue %v", gotError)
	}
}

func TestNotify_rejectsInvalidInfoHashBeforeRequest(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer server.Close()
	webhook := mustWebhook(t, server.URL)

	for _, invalidHash := range []string{"", "abc", testInfoHash + "0", strings.Repeat("g", 40), testInfoHash + "\n", "../" + testInfoHash} {
		if gotError := webhook.Notify(context.Background(), invalidHash); !errors.Is(gotError, ErrInvalidInfoHash) {
			t.Fatalf("hash %q: gotValue %v", invalidHash, gotError)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("%d requests sent", hits.Load())
	}
}

func TestNewWebhook_rejectsUnsafeBaseURL(t *testing.T) {
	for _, baseURL := range []string{"", "ftp://example.com", "http://user:pw@example.com", "http://example.com#x", "example.com", "http://example.com/?apikey=x"} {
		if _, err := NewWebhook(baseURL, testAPIKey); err == nil {
			t.Fatalf("%q accepted", baseURL)
		}
	}
	if _, err := NewWebhook("http://example.com", ""); err == nil {
		t.Fatal("empty api key accepted")
	}
}

func TestNoop_doesNothing(t *testing.T) {
	var notifier Notifier = Noop{}

	if gotError := notifier.Notify(context.Background(), "not-even-a-hash"); gotError != nil {
		t.Fatal(gotError)
	}
}

func TestWebhook_bodyContainsOnlyInfoHash(t *testing.T) {
	var recorded recordedRequest
	server := newRecordingServer(t, http.StatusNoContent, &recorded)

	if err := mustWebhook(t, server.URL).Notify(context.Background(), testInfoHash); err != nil {
		t.Fatal(err)
	}

	// Mode delegate : aucune annonce ni passkey ne quitte Necromanciarr, cross-seed injecte ses propres annonces.
	var payload map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(recorded.body)))
	if err := decoder.Decode(&payload); err != nil || decoder.More() {
		t.Fatalf("gotValue body %q (%v), expectedValue one JSON object", recorded.body, err)
	}
	expectedValue := map[string]any{"infoHash": testInfoHash}
	if fmt.Sprint(payload) != fmt.Sprint(expectedValue) || recorded.rawQuery != "" || strings.Contains(string(recorded.body), "://") {
		t.Fatalf("gotValue body %q query %q, expectedValue only the infoHash field", recorded.body, recorded.rawQuery)
	}
}
