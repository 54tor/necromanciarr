package web

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

func TestSetupTestRoute_neverEchoesTheAPIKey(t *testing.T) {
	const apiKey = "candidate-key-123"
	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	fake := &fakeService{}
	handler := newTestHandler(t, fake, loopbackSettings())

	answered := serve(handler, apiRequest(http.MethodPost, "/api/setup/test", `{"kind":"radarr","url":"http://example.com:7878","apiKey":"`+apiKey+`"}`))
	body := readBody(t, answered)

	isExpected := answered.Code == http.StatusOK && fake.called("TestCandidate radarr") && !strings.Contains(body, apiKey) && !strings.Contains(logged.String(), apiKey)
	if !isExpected {
		t.Fatalf("valeurObtenue %d %q calls %v, valeurAttendue 200 without the key in body or logs", answered.Code, body, fake.calls)
	}
}

func TestSetupTestRoute_refusesCrossSiteAndGet(t *testing.T) {
	handler := newTestHandler(t, &fakeService{}, loopbackSettings())
	crossSite := apiRequest(http.MethodPost, "/api/setup/test", `{}`)
	crossSite.Header.Del("X-Requested-With")

	refused := serve(handler, crossSite)
	byGet := serve(handler, apiRequest(http.MethodGet, "/api/setup/test", ""))

	if refused.Code != http.StatusForbidden || byGet.Code != http.StatusMethodNotAllowed {
		t.Fatalf("valeurObtenue %d and %d, valeurAttendue 403 and 405", refused.Code, byGet.Code)
	}
}
