package main

import (
	"context"
	"io"
	"path/filepath"
	"testing"

	"necromanciarr/internal/store"
)

// writeStoredRows écrit en base, sans validation, un réglage et une surcharge que le chargement refuse.
func writeStoredRows(t *testing.T, configDir string, settingValue string, override *store.ConfigOverride) {
	t.Helper()
	openedStore, err := store.Open(filepath.Join(configDir, testDatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = openedStore.Close() }()
	ctx := context.Background()
	if settingValue != "" {
		if err := openedStore.SetSetting(ctx, "linkCategory", []byte(settingValue)); err != nil {
			t.Fatal(err)
		}
	}
	if override != nil {
		if err := openedStore.PutConfigOverride(ctx, *override); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConfigCommand_unsetRepairsAnInvalidStoredSetting(t *testing.T) {
	useEnvironment(t, nil)
	configDir := writeDoctorConfig(t, `{}`)
	writeStoredRows(t, configDir, "123", nil)

	stillInvalid := runCli(t, configDir, "config", "set", "linkCategory", "456")
	view := runCli(t, configDir, "config", "get", "--json")
	status := runCli(t, configDir, "status")
	served := run([]string{"serve", "--config-dir", configDir, "--language", "en"}, io.Discard, io.Discard)
	repaired := runCli(t, configDir, "config", "unset", "linkCategory")
	_, reloadErr := loadConfig([]string{"--config-dir", configDir})

	if stillInvalid.exitCode == successExitCode {
		t.Fatalf("valeurObtenue exit %d, valeurAttendue une écriture encore invalide refusée", stillInvalid.exitCode)
	}
	if view.exitCode != successExitCode {
		t.Fatalf("valeurObtenue exit %d (%s), valeurAttendue config get lisible sur une base invalide", view.exitCode, view.stderr)
	}
	if status.exitCode != failureExitCode || served == successExitCode {
		t.Fatalf("valeurObtenue status %d serve %d, valeurAttendue les autres actions toujours en échec", status.exitCode, served)
	}
	if repaired.exitCode != successExitCode || reloadErr != nil {
		t.Fatalf("valeurObtenue exit %d (%s) puis %v, valeurAttendue une réparation puis un chargement normal", repaired.exitCode, repaired.stderr, reloadErr)
	}
}

func TestConfigCommand_itemDeleteRepairsAnInvalidStoredItem(t *testing.T) {
	useEnvironment(t, nil)
	configDir := writeDoctorConfig(t, `{}`)
	broken := store.ConfigOverride{List: "radarr", ItemID: testRadarrURL, Value: []byte(`{"url": 5}`)}
	writeStoredRows(t, configDir, "", &broken)

	repaired := runCli(t, configDir, "config", "item", "delete", "radarr", testRadarrURL)
	_, reloadErr := loadConfig([]string{"--config-dir", configDir})

	if repaired.exitCode != successExitCode || reloadErr != nil {
		t.Fatalf("valeurObtenue exit %d (%s) puis %v, valeurAttendue une suppression puis un chargement normal", repaired.exitCode, repaired.stderr, reloadErr)
	}
}

func TestConfigCommand_unknownSettingKeyStaysAUsageError(t *testing.T) {
	useEnvironment(t, nil)
	valeurAttendue := usageExitCode

	valeurObtenue := runCli(t, writeDoctorConfig(t, `{}`), "config", "set", "notASetting", "1").exitCode

	if valeurObtenue != valeurAttendue {
		t.Fatalf("valeurObtenue exit %d, valeurAttendue %d", valeurObtenue, valeurAttendue)
	}
}
