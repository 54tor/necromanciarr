package config

import (
	"strings"
	"testing"
)

func TestRedactText_masksUrlsProxySegmentsAndKnownSecrets(t *testing.T) {
	text := "get http://user:pw@example.com/proxy/QUIKEY/x?apikey=abc failed, token=zzz, bare KNOWNSECRET"

	gotValue := RedactText(text, "KNOWNSECRET")

	for _, leaked := range []string{"pw@", "QUIKEY", "abc", "zzz", "KNOWNSECRET"} {
		if strings.Contains(gotValue, leaked) {
			t.Fatalf("%q leaked in %q", leaked, gotValue)
		}
	}
}

func TestKnownSecrets_listsProxyKeyAndPasswords(t *testing.T) {
	settings := Config{
		AuthToken:      "tok",
		TorrentClients: []TorrentClient{{BaseURL: "http://example.com/proxy/KEY/qbittorrent", Password: "pw"}},
		FSProviders:    []FSProvider{{RC: "http://u:rcpw@example.com"}},
	}

	gotValue := strings.Join(settings.KnownSecrets(), "|")

	for _, expectedFragment := range []string{"tok", "KEY", "pw", "rcpw"} {
		if !strings.Contains(gotValue, expectedFragment) {
			t.Fatalf("expectedValue %q in %q", expectedFragment, gotValue)
		}
	}
}

func TestRedactText_masksLongestSecretFirst(t *testing.T) {
	gotValue := RedactText("value ABCDEF end", "ABC", "ABCDEF")

	if strings.Contains(gotValue, "DEF") {
		t.Fatalf("gotValue %q", gotValue)
	}
}

func TestKnownSecrets_includesLibraryApiCredentials(t *testing.T) {
	settings := Config{LibraryAPI: "http://u:apipw@example.com/proxy/APIKEY/v1?token=QUERYTOKEN"}

	gotValue := strings.Join(settings.KnownSecrets(), "|")

	for _, expectedFragment := range []string{"apipw", "APIKEY", "QUERYTOKEN"} {
		if !strings.Contains(gotValue, expectedFragment) {
			t.Fatalf("expectedValue %q in %q", expectedFragment, gotValue)
		}
	}
}
