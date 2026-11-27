package config

import (
	"errors"
	"strings"
	"testing"
)

const candidateTestAPIKey = "dummy-api-key"

func TestNewTorznabCandidate_buildsTheIndexerURLWithTheKeyLikeAStoredElement(t *testing.T) {
	valeurAttendue := "https://prowlarr.example.com:9696/7/api?apikey=" + candidateTestAPIKey

	indexer, err := NewTorznabCandidate("alpha", "https://prowlarr.example.com:9696/7/api", candidateTestAPIKey)

	if err != nil || indexer.URL != valeurAttendue || indexer.Name != "alpha" {
		t.Fatalf("valeurObtenue %+v %v, valeurAttendue url %q name alpha", indexer, err, valeurAttendue)
	}
}

func TestNewTorznabCandidate_refusesInvalidInputWithAUsageErrorWithoutTheKey(t *testing.T) {
	scenarios := []struct{ name, indexerName, rawURL, apiKey string }{
		{"empty name", "", "https://prowlarr.example.com/7/api", candidateTestAPIKey},
		{"uppercase name", "Alpha", "https://prowlarr.example.com/7/api", candidateTestAPIKey},
		{"long name", strings.Repeat("a", 65), "https://prowlarr.example.com/7/api", candidateTestAPIKey},
		{"empty key", "alpha", "https://prowlarr.example.com/7/api", ""},
		{"not http", "alpha", "ftp://prowlarr.example.com/7/api", candidateTestAPIKey},
		{"credentials", "alpha", "https://alice:secret@prowlarr.example.com/7/api", candidateTestAPIKey},
		{"query", "alpha", "https://prowlarr.example.com/7/api?apikey=" + candidateTestAPIKey, candidateTestAPIKey},
		{"fragment", "alpha", "https://prowlarr.example.com/7/api#x", candidateTestAPIKey},
		{"proxy segment", "alpha", "https://prowlarr.example.com/proxy/{proxyKey}/7/api", candidateTestAPIKey},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			_, err := NewTorznabCandidate(scenario.indexerName, scenario.rawURL, scenario.apiKey)

			if !errors.Is(err, ErrUsage) || strings.Contains(err.Error(), candidateTestAPIKey) {
				t.Fatalf("valeurObtenue %v, valeurAttendue a usage error without the key", err)
			}
		})
	}
}
