package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"necromanciarr/internal/config"
	"necromanciarr/internal/store"
)

const testDatabaseName = "necromanciarr.db"

func openTestDatabase(t *testing.T, configDir string) *store.Store {
	t.Helper()
	openedStore, err := store.Open(filepath.Join(configDir, testDatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = openedStore.Close() })
	return openedStore
}

// sealTestSetting scelle un réglage avec la clé de test fakeSecretKey, comme le fait l'édition de la configuration.
func sealTestSetting(t *testing.T, key, plain string) []byte {
	t.Helper()
	secretKey, err := base64.RawURLEncoding.DecodeString(fakeSecretKey)
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := config.NewSecretSealer(secretKey)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sealer.SealSetting(key, plain)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(sealed)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestLoadConfig_firstRunWithoutDatabaseStartsWithDefaults(t *testing.T) {
	configDir := t.TempDir()
	valeurAttendue := "127.0.0.1:8787"

	loadedConfig, err := loadConfig([]string{"--config-dir", configDir})
	if err != nil {
		t.Fatal(err)
	}

	if valeurObtenue := loadedConfig.ListenAddr; valeurObtenue != valeurAttendue {
		t.Fatalf("valeurObtenue %q, valeurAttendue %q", valeurObtenue, valeurAttendue)
	}
	if _, err := os.Stat(filepath.Join(configDir, testDatabaseName)); !os.IsNotExist(err) {
		t.Fatalf("valeurObtenue une base créée (%v), valeurAttendue aucune base au chargement", err)
	}
}

func TestLoadConfig_readsOverridesWrittenByAnotherStoreHandle(t *testing.T) {
	configDir := t.TempDir()
	writer := openTestDatabase(t, configDir)
	if err := writer.SetSetting(context.Background(), "linkCategory", []byte(`"category-a"`)); err != nil {
		t.Fatal(err)
	}
	valeurAttendue := "category-a"

	loadedConfig, err := loadConfig([]string{"--config-dir", configDir})
	if err != nil {
		t.Fatal(err)
	}

	if valeurObtenue := loadedConfig.LinkCategory; valeurObtenue != valeurAttendue {
		t.Fatalf("valeurObtenue %q, valeurAttendue %q", valeurObtenue, valeurAttendue)
	}
}

func TestLoadConfig_readsADatabaseCreatedBeforeTheConfigTables(t *testing.T) {
	configDir := t.TempDir()
	writeOlderDatabase(t, filepath.Join(configDir, testDatabaseName))
	valeurAttendue := "127.0.0.1:8787"

	loadedConfig, err := loadConfig([]string{"--config-dir", configDir})
	if err != nil {
		t.Fatal(err)
	}

	if valeurObtenue := loadedConfig.ListenAddr; valeurObtenue != valeurAttendue {
		t.Fatalf("valeurObtenue %q, valeurAttendue %q", valeurObtenue, valeurAttendue)
	}
}

func TestRunConfigCheck_printsOriginsAndNoSecret(t *testing.T) {
	const clearSecret = "clear-prowlarr-secret"
	t.Setenv("NECROMANCIARR_SECRET_KEY", fakeSecretKey)
	configDir := t.TempDir()
	writer := openTestDatabase(t, configDir)
	ctx := context.Background()
	sealedProwlarr := sealTestSetting(t, "prowlarr", "https://prowlarr.example.com:9696?apikey="+clearSecret)
	if err := writer.SetSetting(ctx, "prowlarr", sealedProwlarr); err != nil {
		t.Fatal(err)
	}
	orphan := store.ConfigOverride{List: "unknown-list", ItemID: "item-a", Value: []byte(`{}`)}
	if err := writer.PutConfigOverride(ctx, orphan); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer

	exitCode := run([]string{"config", "check", "--config-dir", configDir, "--language", "en"}, &stdout, &stderr)

	if exitCode != successExitCode || strings.Contains(stdout.String()+stderr.String(), clearSecret) {
		t.Fatalf("valeurObtenue exit %d stdout %q stderr %q, valeurAttendue 0 sans le secret", exitCode, stdout.String(), stderr.String())
	}
	var printed struct {
		Origins         []config.ValueOrigin
		OrphanOverrides []config.OrphanOverride
	}
	if err := json.Unmarshal(stdout.Bytes(), &printed); err != nil {
		t.Fatal(err)
	}
	valeurAttendue := config.ValueOrigin{Key: "prowlarr", Origin: "override"}
	if valeurObtenue := originNamed(printed.Origins, "prowlarr"); valeurObtenue != valeurAttendue {
		t.Fatalf("valeurObtenue %+v, valeurAttendue %+v", valeurObtenue, valeurAttendue)
	}
	orphelineAttendue := []config.OrphanOverride{{List: "unknown-list", ItemID: "item-a"}}
	if len(printed.OrphanOverrides) != 1 || printed.OrphanOverrides[0] != orphelineAttendue[0] {
		t.Fatalf("valeurObtenue %+v, valeurAttendue %+v", printed.OrphanOverrides, orphelineAttendue)
	}
	if !strings.Contains(stderr.String(), "Language: en (source: flag)") {
		t.Fatalf("valeurObtenue stderr %q, valeurAttendue la langue et son origine", stderr.String())
	}
}

func originNamed(origins []config.ValueOrigin, key string) config.ValueOrigin {
	for _, origin := range origins {
		if origin.Key == key && origin.ItemID == "" {
			return origin
		}
	}
	return config.ValueOrigin{}
}

// writeOlderDatabase crée une base d'avant la configuration en base : une table, ni settings ni list_overrides.
func writeOlderDatabase(t *testing.T, databasePath string) {
	t.Helper()
	olderDatabase, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := olderDatabase.Exec(`CREATE TABLE media_files (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := olderDatabase.Close(); err != nil {
		t.Fatal(err)
	}
}
