package store

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
)

func TestStoredConfig_roundTripsSettingsAndOverrides(t *testing.T) {
	openedStore := openTestStore(t)
	ctx := context.Background()
	if err := openedStore.SetSetting(ctx, "server.port", []byte("8080")); err != nil {
		t.Fatal(err)
	}
	valeurAttendue := []ConfigOverride{
		{List: "indexers", ItemID: "b", Deleted: true},
		{List: "indexers", ItemID: "a", Value: []byte(`{"x":1}`), UpstreamFingerprint: "fp"},
		{List: "mounts", ItemID: "a", Value: []byte("v")},
	}
	for _, override := range []ConfigOverride{valeurAttendue[2], valeurAttendue[0], valeurAttendue[1]} {
		if err := openedStore.PutConfigOverride(ctx, override); err != nil {
			t.Fatal(err)
		}
	}

	settingsObtenus, err := openedStore.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(settingsObtenus["server.port"], []byte("8080")) || len(settingsObtenus) != 1 {
		t.Fatalf("valeurObtenue %v, valeurAttendue server.port=8080", settingsObtenus)
	}
	valeurObtenue, err := openedStore.ConfigOverrides(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(valeurObtenue, valeurAttendue) {
		t.Fatalf("valeurObtenue %+v, valeurAttendue %+v", valeurObtenue, valeurAttendue)
	}
}

func TestStoredConfig_upsertKeepsTheInsertionPosition(t *testing.T) {
	openedStore := openTestStore(t)
	ctx := context.Background()
	for _, itemID := range []string{"z", "a", "m"} {
		if err := openedStore.PutConfigOverride(ctx, ConfigOverride{List: "l", ItemID: itemID, Value: []byte("v1")}); err != nil {
			t.Fatal(err)
		}
	}
	if err := openedStore.PutConfigOverride(ctx, ConfigOverride{List: "l", ItemID: "z", Value: []byte("v2")}); err != nil {
		t.Fatal(err)
	}
	valeurAttendue := []string{"z", "a", "m"}

	overrides, err := openedStore.ConfigOverrides(ctx)
	if err != nil {
		t.Fatal(err)
	}

	var valeurObtenue []string
	for _, override := range overrides {
		valeurObtenue = append(valeurObtenue, override.ItemID)
	}
	if !reflect.DeepEqual(valeurObtenue, valeurAttendue) || string(overrides[0].Value) != "v2" {
		t.Fatalf("valeurObtenue %v (%q), valeurAttendue %v with z replaced in place", valeurObtenue, overrides[0].Value, valeurAttendue)
	}
}

func TestStoredConfig_upsertReplacesTheValue(t *testing.T) {
	openedStore := openTestStore(t)
	ctx := context.Background()
	for _, value := range []string{"old", "new"} {
		if err := openedStore.SetSetting(ctx, "k", []byte(value)); err != nil {
			t.Fatal(err)
		}
		if err := openedStore.PutConfigOverride(ctx, ConfigOverride{List: "l", ItemID: "i", Value: []byte(value)}); err != nil {
			t.Fatal(err)
		}
	}

	settingsObtenus, err := openedStore.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	overridesObtenus, err := openedStore.ConfigOverrides(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(settingsObtenus["k"]) != "new" || len(overridesObtenus) != 1 || string(overridesObtenus[0].Value) != "new" {
		t.Fatalf("valeurObtenue %v %+v, valeurAttendue une seule ligne « new »", settingsObtenus, overridesObtenus)
	}
}

func TestStoredConfig_deleteRemovesOnlyTheTargetRow(t *testing.T) {
	openedStore := openTestStore(t)
	ctx := context.Background()
	for _, key := range []string{"a", "b"} {
		if err := openedStore.SetSetting(ctx, key, []byte(key)); err != nil {
			t.Fatal(err)
		}
		if err := openedStore.PutConfigOverride(ctx, ConfigOverride{List: "l", ItemID: key, Value: []byte(key)}); err != nil {
			t.Fatal(err)
		}
	}

	if err := openedStore.DeleteSetting(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := openedStore.DeleteConfigOverride(ctx, "l", "a"); err != nil {
		t.Fatal(err)
	}

	settingsObtenus, err := openedStore.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	overridesObtenus, err := openedStore.ConfigOverrides(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(settingsObtenus) != 1 || string(settingsObtenus["b"]) != "b" {
		t.Fatalf("valeurObtenue %v, valeurAttendue seul b", settingsObtenus)
	}
	if len(overridesObtenus) != 1 || overridesObtenus[0].ItemID != "b" {
		t.Fatalf("valeurObtenue %+v, valeurAttendue seul l/b", overridesObtenus)
	}
}

func TestStoredConfig_readOnlyStoreSeesRowsWrittenByAnotherHandle(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "necromanciarr.db")
	writer, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	ctx := context.Background()
	if err := writer.SetSetting(ctx, "k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	if err := writer.PutConfigOverride(ctx, ConfigOverride{List: "l", ItemID: "i", Value: []byte("v")}); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenReadOnly(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })

	settingsObtenus, err := reader.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	overridesObtenus, err := reader.ConfigOverrides(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(settingsObtenus["k"]) != "v" || len(overridesObtenus) != 1 {
		t.Fatalf("valeurObtenue %v %+v, valeurAttendue k=v et une ligne", settingsObtenus, overridesObtenus)
	}
}

func TestStoredConfig_readOnlyDatabaseWithoutConfigTablesReadsAsEmpty(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "necromanciarr.db")
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
	reader, err := OpenReadOnly(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	ctx := context.Background()

	settingsObtenus, err := reader.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	overridesObtenus, err := reader.ConfigOverrides(ctx)
	if err != nil {
		t.Fatal(err)
	}
	valeurAttendue := 0
	if valeurObtenue := len(settingsObtenus) + len(overridesObtenus); valeurObtenue != valeurAttendue {
		t.Fatalf("valeurObtenue %d lignes, valeurAttendue %d", valeurObtenue, valeurAttendue)
	}
}
