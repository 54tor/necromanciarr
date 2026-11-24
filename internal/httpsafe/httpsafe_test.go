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
