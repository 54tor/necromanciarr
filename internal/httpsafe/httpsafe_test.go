package httpsafe

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewClient_hardenedDefaults(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:9")
	client := NewClient(3 * time.Second)

	transport, isTransport := client.Transport.(*http.Transport)
	if !isTransport || transport.Proxy != nil {
		t.Fatalf("transport = %#v", client.Transport)
	}
	if client.Timeout != 3*time.Second {
		t.Fatalf("gotValue %s", client.Timeout)
	}
}

func TestNewClient_refusesRedirects(t *testing.T) {
	server := httptest.NewServer(http.RedirectHandler("/elsewhere", http.StatusFound))
	defer server.Close()

	response, err := NewClient(time.Second).Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusFound {
		t.Fatalf("gotValue %d", response.StatusCode)
	}
}

func TestParseRetryAfter_readsSecondsAndDatesCappedAtOneHour(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cases := map[string]time.Duration{
		"30":     30 * time.Second,
		"999999": time.Hour,
		"-5":     0,
		"soon":   0,
		"":       0,
		now.Add(90 * time.Second).Format(http.TimeFormat): 90 * time.Second,
		now.Add(-time.Minute).Format(http.TimeFormat):     0,
		now.Add(48 * time.Hour).Format(http.TimeFormat):   time.Hour,
	}
	for header, expectedValue := range cases {
		if gotValue := ParseRetryAfter(header, now); gotValue != expectedValue {
			t.Errorf("header %q: expectedValue %v, gotValue %v", header, expectedValue, gotValue)
		}
	}
}
