package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func clearSecretEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{envAuthToken, envLibraryProviderToken, envArrAPIKeys, envQbitPassword} {
		t.Setenv(name, "")
	}
}

func TestLoad_defaultsListenToLoopback(t *testing.T) {
	clearSecretEnvironment(t)
	expectedValue := "127.0.0.1:8787"

	loadedConfig, err := Load(writeConfig(t, "db_path: /home/u/data.db\n"))
	if err != nil {
		t.Fatal(err)
	}

	if gotValue := loadedConfig.ListenAddr; gotValue != expectedValue {
		t.Fatalf("ListenAddr = %q, want %q", gotValue, expectedValue)
	}
}

func TestLoad_rejectsNonLoopbackWithoutToken(t *testing.T) {
	clearSecretEnvironment(t)

	_, err := Load(writeConfig(t, "listen_addr: 0.0.0.0:8787\n"))

	if err == nil {
		t.Fatal("expected an error for a non-loopback address without token")
	}
}

func TestLoad_acceptsNonLoopbackWithTokenFromEnvironment(t *testing.T) {
	clearSecretEnvironment(t)
	t.Setenv(envAuthToken, "token-for-test")

	loadedConfig, err := Load(writeConfig(t, "listen_addr: 0.0.0.0:8787\n"))
	if err != nil {
		t.Fatal(err)
	}

	if loadedConfig.AuthToken != "token-for-test" {
		t.Fatal("AuthToken not read from environment")
	}
}

func TestLoad_readsSecretsFromEnvironmentAndMatchesArrKeys(t *testing.T) {
	clearSecretEnvironment(t)
	t.Setenv(envArrAPIKeys, "key-a,key-b")
	t.Setenv(envLibraryProviderToken, "provider-token")
	t.Setenv(envQbitPassword, "qbit-password")
	yamlContent := `arr_instances:
  - {kind: radarr, url: "http://example.com:7878"}
  - {kind: sonarr, url: "http://example.com:8989"}
`

	loadedConfig, err := Load(writeConfig(t, yamlContent))
	if err != nil {
		t.Fatal(err)
	}

	if len(loadedConfig.ArrAPIKeys) != 2 || loadedConfig.ArrAPIKeys[1] != "key-b" {
		t.Fatalf("ArrAPIKeys = %v", loadedConfig.ArrAPIKeys)
	}
	if loadedConfig.LibraryProviderToken != "provider-token" || loadedConfig.QbitPassword != "qbit-password" {
		t.Fatal("secrets not read from environment")
	}
}

func TestLoad_rejectsArrKeysCountMismatch(t *testing.T) {
	clearSecretEnvironment(t)
	t.Setenv(envArrAPIKeys, "key-a")
	yamlContent := `arr_instances:
  - {kind: radarr, url: "http://example.com:7878"}
  - {kind: sonarr, url: "http://example.com:8989"}
`

	if _, err := Load(writeConfig(t, yamlContent)); err == nil {
		t.Fatal("expected an error when keys and instances differ in count")
	}
}

func TestConfig_formattingNeverPrintsSecrets(t *testing.T) {
	secretConfig := Config{
		AuthToken:            "secret-auth",
		LibraryProviderToken: "secret-provider",
		ArrAPIKeys:           []string{"secret-key-one", "secret-key-two"},
		QbitPassword:         "secret-password",
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		gotValue := fmt.Sprintf(verb, secretConfig)
		if strings.Contains(gotValue, "secret-") {
			t.Errorf("verb %s leaked a secret: %s", verb, gotValue)
		}
		if !strings.Contains(gotValue, "[redacted]") {
			t.Errorf("verb %s: expectedValue redaction marker, gotValue %s", verb, gotValue)
		}
	}
}
