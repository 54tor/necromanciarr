package main

import (
	"strings"
	"testing"
)

func TestConfigCheck_masksTorznabKeysAndSecretKey(t *testing.T) {
	t.Setenv("INDEXER_KEY_A", "secret-indexer-a")
	t.Setenv("INDEXER_KEY_B", "secret-prowlarr-b")
	configDir := writeDoctorConfig(t, `{
  "torznab": [
    "https://prowlarr.example.com:9696/12/api?apikey=${INDEXER_KEY_A}",
    {"url": "https://prowlarr.example.com:9696/7/api?apikey=secret-b%26%22c%5Cd", "name": "tracker-a"}
  ],
  "prowlarr": "https://prowlarr.example.com:9696?apikey=${INDEXER_KEY_B}"
}`)

	for _, language := range []string{"fr", "en"} {
		result := runCli(t, configDir, "config", "check", "--language", language)

		if result.exitCode != 0 {
			t.Fatalf("gotValue exit %d stderr %q, expectedValue 0", result.exitCode, result.stderr)
		}
		everything := result.stdout + result.stderr
		for _, secret := range []string{"secret-indexer-a", "secret-prowlarr-b", "secret-b", `b&"c`, `b&\"c`, `b&`} {
			if strings.Contains(everything, secret) {
				t.Fatalf("gotValue a leaked %q in %q", secret, everything)
			}
		}
		for _, expectedPart := range []string{`"name": "tracker-a"`, `"name": "12"`, "prowlarr.example.com", `"sourceOrder"`, "torznab:tracker-a"} {
			if !strings.Contains(result.stdout, expectedPart) {
				t.Fatalf("gotValue %q, expectedValue it to contain %q", result.stdout, expectedPart)
			}
		}
	}
}

func TestConfigCheck_refusesAMisplacedProviderWithoutPrintingTheKey(t *testing.T) {
	configDir := writeDoctorConfig(t, `{
  "torznab": ["https://prowlarr.example.com:9696/12/api?apikey=secret-indexer-a"],
  "sourceOrder": ["torznab:12", "provider"]
}`)

	result := runCli(t, configDir, "config", "check")

	if result.exitCode == 0 || !strings.Contains(result.stderr, "provider must be the first source") || strings.Contains(result.stderr+result.stdout, "secret-") {
		t.Fatalf("gotValue exit %d stderr %q, expectedValue a refusal naming the provider rule without the key", result.exitCode, result.stderr)
	}
}
