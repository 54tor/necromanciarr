package crossseed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
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

func TestNotify_refusesRedirectAndEnvProxy(t *testing.T) {
	var redirectedHits, proxyHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirectedHits.Add(1) }))
	defer target.Close()
	redirecting := httptest.NewServer(http.RedirectHandler(target.URL, http.StatusTemporaryRedirect))
	defer redirecting.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { proxyHits.Add(1) }))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	// Une adresse non loopback : net/http ne passe jamais par le proxy pour 127.0.0.1.
	redirectingURL := strings.Replace(redirecting.URL, "127.0.0.1", "localhost.", 1)

	gotError := mustWebhook(t, redirectingURL).Notify(context.Background(), testInfoHash)

	if !errors.Is(gotError, ErrUnexpectedStatus) && !errors.Is(gotError, ErrUnavailable) {
		t.Fatalf("gotValue %v", gotError)
	}
	if redirectedHits.Load() != 0 || proxyHits.Load() != 0 {
		t.Fatalf("redirect followed (%d) or proxy used (%d)", redirectedHits.Load(), proxyHits.Load())
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

func TestPipelineLikeFlow_seedSucceedsIdenticallyWithNoopAndWithUnreachableNotifier(t *testing.T) {
	closedListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedURL := "http://" + closedListener.Addr().String()
	_ = closedListener.Close()
	unreachable := mustWebhook(t, closedURL, WithTimeout(time.Second))

	for name, notifier := range map[string]Notifier{"noop": Noop{}, "unreachable": unreachable} {
		seeded, warnings := seedLikePipeline(notifier)

		if !seeded {
			t.Fatalf("%s: seed failed", name)
		}
		if (name == "noop") != (len(warnings) == 0) {
			t.Fatalf("%s: warnings %v", name, warnings)
		}
	}
}

// seedLikePipeline imite l'appel en meilleur effort de la tâche 8 : l'échec de Notify n'annule pas le seed.
func seedLikePipeline(notifier Notifier) (bool, []string) {
	var warnings []string
	if err := notifier.Notify(context.Background(), testInfoHash); err != nil {
		warnings = append(warnings, "cross-seed notification failed")
	}
	return true, warnings
}
