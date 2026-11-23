package config

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestParseArrService_extractsApiKeyFromQuery(t *testing.T) {
	expectedValue := ArrService{Kind: "radarr", BaseURL: "http://example.com:7878", APIKey: "radarr-key"}

	gotValue, err := ParseArrService("radarr", "http://example.com:7878?apikey=radarr-key")
	if err != nil {
		t.Fatal(err)
	}

	if gotValue != expectedValue {
		t.Fatalf("gotValue %+v, expectedValue %+v", gotValue, expectedValue)
	}
}

func TestParseArrService_rejectsMissingKeyWithoutLeakingInput(t *testing.T) {
	_, err := ParseArrService("sonarr", "http://user:hidden-pass@example.com:8989")

	if err == nil || strings.Contains(err.Error(), "hidden-pass") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseTorrentClient_readsTypeReadonlyAndCredentials(t *testing.T) {
	expectedValue := TorrentClient{Type: "qbittorrent", ReadOnly: true, BaseURL: "http://example.com:8080", User: "alice", Password: "p@ss"}

	gotValue, err := ParseTorrentClient("qbittorrent:readonly:http://alice:p%40ss@example.com:8080")
	if err != nil {
		t.Fatal(err)
	}

	if gotValue != expectedValue {
		t.Fatalf("gotValue %+v, expectedValue %+v", gotValue, expectedValue)
	}
}

func TestParseTorrentClient_rejectsUnknownType(t *testing.T) {
	_, err := ParseTorrentClient("transmission:http://example.com:9091")

	if err == nil || !strings.Contains(err.Error(), "unknown type") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseTorrentClient_rejectsMissingTypePrefix(t *testing.T) {
	if _, err := ParseTorrentClient("http://example.com:8080"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestServices_redactCredentialsEverywhere(t *testing.T) {
	arr := ArrService{Kind: "radarr", BaseURL: "http://example.com", APIKey: "secret-arr-key"}
	client := TorrentClient{Type: "qbittorrent", BaseURL: "http://example.com", User: "alice", Password: "secret-client-pass"}
	for _, value := range []any{arr, client} {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
			assertNoSecret(t, fmt.Sprintf(verb, value))
		}
		assertNoSecret(t, slog.AnyValue(value).Resolve().String())
	}
}

func assertNoSecret(t *testing.T, rendered string) {
	t.Helper()
	if strings.Contains(rendered, "secret-") {
		t.Errorf("leaked a secret: %s", rendered)
	}
}
