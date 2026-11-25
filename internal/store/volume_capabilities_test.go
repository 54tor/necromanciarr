package store

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"necromanciarr/internal/fsys"
)

func TestStore_volumeCapabilitiesRoundTrip(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "volumes.db")
	probedAt := time.Unix(1700000000, 0).UTC()
	valeurAttendue := fsys.Capabilities{
		Symlink: true, Hardlink: true, VolumeIdentity: true, RemoteHash: []string{"sha1"},
		Reasons: map[string]string{"atomicReplace": "extension_missing", "serverCopy": "server_copy_unsupported_by_client"},
	}
	first := openStoreAt(t, databasePath)
	if _, _, found, err := first.VolumeCapabilities(ctx, "seedbox"); err != nil || found {
		t.Fatalf("unknown volume: found %v err %v", found, err)
	}
	if err := first.SaveVolumeCapabilities(ctx, "seedbox", valeurAttendue, probedAt); err != nil {
		t.Fatal(err)
	}
	closeStore(t, first)

	for reopening := range 2 {
		reopened := openStoreAt(t, databasePath)
		valeurObtenue, valeurObtenueAt, found, err := reopened.VolumeCapabilities(ctx, "seedbox")
		if err != nil || !found {
			t.Fatalf("reopening %d: found %v err %v", reopening, found, err)
		}
		if !reflect.DeepEqual(valeurObtenue, valeurAttendue) || !valeurObtenueAt.Equal(probedAt) {
			t.Errorf("reopening %d: valeurObtenue %+v at %v, valeurAttendue %+v at %v", reopening, valeurObtenue, valeurObtenueAt, valeurAttendue, probedAt)
		}
		closeStore(t, reopened)
	}
}

func TestStore_volumeCapabilitiesReplaceTheLastProbe(t *testing.T) {
	ctx := context.Background()
	testStore := openTestStore(t)
	for probeIndex, capabilities := range []fsys.Capabilities{{Hardlink: true}, {Symlink: true}} {
		if err := testStore.SaveVolumeCapabilities(ctx, "seedbox", capabilities, time.Unix(int64(probeIndex+1), 0)); err != nil {
			t.Fatal(err)
		}
	}
	valeurObtenue, probedAt, _, err := testStore.VolumeCapabilities(ctx, "seedbox")
	if err != nil || valeurObtenue.Hardlink || !valeurObtenue.Symlink || probedAt.Unix() != 2 {
		t.Errorf("valeurObtenue %+v at %v (err %v), valeurAttendue the second probe only", valeurObtenue, probedAt, err)
	}
}

func TestStore_volumeCapabilitiesStoreOnlyCodes(t *testing.T) {
	ctx := context.Background()
	testStore := openTestStore(t)
	for _, reasons := range []map[string]string{
		{"hardlink": "/srv/media/film.mkv"},
		{"sftp://seedbox/srv": "probe_failed"},
		{"hardlink": "user@example.com"},
	} {
		err := testStore.SaveVolumeCapabilities(ctx, "seedbox", fsys.Capabilities{Reasons: reasons}, time.Unix(1, 0))
		if !errors.Is(err, ErrVolumeCapabilityNotACode) {
			t.Errorf("reasons %v: valeurObtenue %v, valeurAttendue ErrVolumeCapabilityNotACode", reasons, err)
		}
	}
	if err := testStore.SaveVolumeCapabilities(ctx, "seedbox", fsys.Capabilities{RemoteHash: []string{"sha1 /etc"}}, time.Unix(1, 0)); !errors.Is(err, ErrVolumeCapabilityNotACode) {
		t.Errorf("valeurObtenue %v, valeurAttendue a hash name refused unless it is a code", err)
	}
	var stored string
	if err := testStore.SaveVolumeCapabilities(ctx, "seedbox", fsys.Capabilities{Hardlink: true}, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := testStore.database.QueryRowContext(ctx, `SELECT capabilities_json FROM volume_capabilities WHERE volume = ?`, "seedbox").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "seedbox") || !strings.Contains(stored, `"hardlink":true`) {
		t.Errorf("valeurObtenue %s, valeurAttendue booleans and codes only", stored)
	}
}

func openStoreAt(t *testing.T, databasePath string) *Store {
	t.Helper()
	opened, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	return opened
}

func closeStore(t *testing.T, opened *Store) {
	t.Helper()
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}
}
