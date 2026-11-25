package app

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"necromanciarr/internal/config"
	"necromanciarr/internal/doctor"
	"necromanciarr/internal/sources"
	"necromanciarr/internal/store"
)

// sealedTestInfohash est un infohash v1 factice (40 caractères hexadécimaux minuscules, exigés par la liaison).
var sealedTestInfohash = strings.Repeat("ab", 20)

// sealedDatabase crée une base dont un candidat porte un bloc scellé avec sealKey, lié à sa ligne.
func sealedDatabase(t *testing.T, sealKey []byte) config.Config {
	t.Helper()
	ctx := context.Background()
	configDir := t.TempDir()
	settings := config.Config{
		ConfigDir: configDir, DBPath: "necromanciarr.db",
		Torznab: []config.TorznabIndexer{{URL: "http://example.com/12/api?apikey=fake-key-value", Name: "12"}},
	}
	openedStore, err := store.Open(filepath.Join(configDir, "necromanciarr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = openedStore.Close() }()
	candidate := store.Candidate{Infohash: sealedTestInfohash, Tracker: "tracker-a", Name: "Film", TotalSize: 1, Source: "torznab:12"}
	candidateID, _, err := openedStore.SaveCandidate(ctx, candidate, nil, time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := sources.NewSealer(sealKey)
	if err != nil {
		t.Fatal(err)
	}
	binding := sources.Binding{Infohash: sealedTestInfohash, Tracker: "tracker-a", Source: "torznab:12"}
	blob, err := sealer.Seal([]byte("d4:infod4:name1:aee"), binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := openedStore.SaveCandidateSealed(ctx, candidateID, blob, time.Unix(1700000000, 0)); err != nil {
		t.Fatal(err)
	}
	return settings
}

func secretKeyResult(t *testing.T, settings config.Config) doctor.Result {
	t.Helper()
	report, err := New(Deps{Config: settings}).Doctor(context.Background(), DoctorOptions{CheckIDs: []string{doctor.IDSecretKey}})
	if err != nil || len(report.Checks) != 1 {
		t.Fatalf("gotValue %+v (err %v), expectedValue the secret-key check", report, err)
	}
	return report.Checks[0].Result
}

func TestDoctor_secretKeyUnsealsTheStoredBlobsThroughTheWiredProbe(t *testing.T) {
	sealKey := bytes.Repeat([]byte{0x01}, sources.KeySize)
	settings := sealedDatabase(t, sealKey)

	settings.SecretKey = sealKey
	if gotValue := secretKeyResult(t, settings); gotValue.Status != doctor.OK || gotValue.Code != doctor.CodeSecretKeyOK {
		t.Fatalf("right key: gotValue %+v, expectedValue ok (probe wired, no sealed_torrents_unchecked)", gotValue)
	}
	settings.SecretKey = bytes.Repeat([]byte{0x02}, sources.KeySize)
	if gotValue := secretKeyResult(t, settings); gotValue.Status != doctor.Fail || gotValue.Code != doctor.CodeSealedTorrentUnreadable {
		t.Fatalf("wrong key: gotValue %+v, expectedValue fail sealed_torrent_unreadable", gotValue)
	}
}

func TestDoctor_secretKeyProbeNeverCreatesAMissingDatabase(t *testing.T) {
	configDir := t.TempDir()
	settings := config.Config{
		ConfigDir: configDir, DBPath: "necromanciarr.db", SecretKey: bytes.Repeat([]byte{0x01}, sources.KeySize),
		Torznab: []config.TorznabIndexer{{URL: "http://example.com/12/api?apikey=fake-key-value", Name: "12"}},
	}
	if gotValue := secretKeyResult(t, settings); gotValue.Code != doctor.CodeSecretKeyOK {
		t.Fatalf("gotValue %+v, expectedValue ok with no blob", gotValue)
	}
	if matches, _ := filepath.Glob(filepath.Join(configDir, "*")); len(matches) != 0 {
		t.Fatalf("gotValue %v, expectedValue no database created by the probe", matches)
	}
}

func TestBuildDeps_wiresTheSealedProbeOnTheOpenStore(t *testing.T) {
	openedStore := openTestStore(t)
	settings := config.Config{ConfigDir: t.TempDir(), ReadBytesPerSecond: 1 << 20, TieBreakReadBytes: 1, LinkType: "hardlink", CrossSeedMode: "off"}

	deps, _, err := buildDeps(context.Background(), settings, openedStore)
	if err != nil {
		t.Fatal(err)
	}

	probe, isStoreProbe := deps.DoctorDeps.SealedProbe.(openStoreProbe)
	if !isStoreProbe || probe.store != openedStore {
		t.Fatalf("gotValue %T, expectedValue the probe on the already open store", deps.DoctorDeps.SealedProbe)
	}
}

func databaseSchemaVersion(t *testing.T, databasePath string) int {
	t.Helper()
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	var version int
	if err := database.QueryRow(`PRAGMA schema_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

// dropTrigger simule une base à mettre à niveau : une ouverture en écriture recréerait le déclencheur.
func dropTrigger(t *testing.T, databasePath string) {
	t.Helper()
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	if _, err := database.Exec(`DROP TRIGGER proposals_clear_sealed`); err != nil {
		t.Fatal(err)
	}
}

func TestDoctor_sealedProbeByPathNeverWritesTheSchemaAndHonoursTheContext(t *testing.T) {
	sealKey := bytes.Repeat([]byte{0x01}, sources.KeySize)
	settings := sealedDatabase(t, sealKey)
	settings.SecretKey = sealKey
	databasePath := config.ResolveDBPath(settings)
	dropTrigger(t, databasePath)
	before := databaseSchemaVersion(t, databasePath)
	probe := pathProbe{databasePath: databasePath}

	visits := 0
	if _, err := probe.EachSealedTorrent(context.Background(), 10, 1<<20, func(doctor.SealedTorrent) error { visits++; return nil }); err != nil || visits != 1 {
		t.Fatalf("gotValue %d visits (err %v), expectedValue 1", visits, err)
	}
	if gotValue := databaseSchemaVersion(t, databasePath); gotValue != before {
		t.Fatalf("gotValue schema version %d, expectedValue %d: the probe must open read-only", gotValue, before)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := probe.EachSealedTorrent(cancelled, 10, 1<<20, func(doctor.SealedTorrent) error { return nil }); err == nil {
		t.Fatal("gotValue no error on a cancelled context, expectedValue the cancellation")
	}
}
