package store

import (
	"bytes"
	"context"
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
		{List: "indexers", ItemID: "a", Value: []byte(`{"x":1}`), UpstreamFingerprint: "fp"},
		{List: "indexers", ItemID: "b", Deleted: true},
		{List: "mounts", ItemID: "a", Value: []byte("v")},
	}
	for _, override := range []ConfigOverride{valeurAttendue[2], valeurAttendue[1], valeurAttendue[0]} {
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
