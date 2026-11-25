package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"necromanciarr/internal/config"
	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/sftpfs"
	"necromanciarr/internal/fsys/sftpfs/sftptest"
	"necromanciarr/internal/pipeline"
)

// stateVolume est un volume sftp de test dont seul l'état compte.
type stateVolume struct{ state string }

func (volume stateVolume) State() string { return volume.state }
func (stateVolume) Release()             {}
func (stateVolume) Capabilities(context.Context) (fsys.Capabilities, error) {
	return fsys.Capabilities{}, nil
}

func hostileProvider() config.FSProvider {
	return config.FSProvider{
		Type: config.FSProviderTypeSFTP, Name: seedboxName, Address: "alice:hunter2secret@seedbox.example.com:2222",
		User: "alice", Password: "hunter2secret", IdentityPassphrase: "passphrase-secret",
		IdentityFile: "/home/u/keys/id_ed25519", KnownHosts: "/home/u/ssh/known_hosts",
	}
}

func volumesService(t *testing.T, providers []config.FSProvider, volumes map[string]VolumeHandle) Service {
	t.Helper()
	return New(Deps{Store: openTestStore(t), Config: config.Config{FSProviders: providers}, Volumes: volumes})
}

func TestVolumes_listMasksAddressSecrets(t *testing.T) {
	service := volumesService(t, []config.FSProvider{hostileProvider()}, nil)

	rows, err := service.ListVolumes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}

	for _, secret := range []string{"alice", "hunter2secret", "passphrase-secret", "/home/u", "@"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("gotValue %s, expectedValue no %q", encoded, secret)
		}
	}
	expectedValue := []VolumeRow{
		{Volume: "", Type: VolumeTypeLocal, State: sftpfs.StateOK},
		{Volume: seedboxName, Type: VolumeTypeSFTP, Address: "seedbox.example.com:2222", State: sftpfs.StateUnknown},
	}
	if !slices.EqualFunc(rows, expectedValue, sameVolumeRow) {
		t.Fatalf("gotValue %+v, expectedValue %+v", rows, expectedValue)
	}
}

func sameVolumeRow(gotRow, expectedRow VolumeRow) bool {
	return gotRow.Volume == expectedRow.Volume && gotRow.Type == expectedRow.Type && gotRow.Address == expectedRow.Address &&
		gotRow.State == expectedRow.State
}

func TestVolumes_listReportsTheVolumeStateAndTheStoredProbe(t *testing.T) {
	provider := hostileProvider()
	provider.Address = "seedbox.example.com:22"
	openedStore := openTestStore(t)
	probedAt := time.Date(2026, 11, 20, 8, 30, 0, 0, time.UTC)
	stored := fsys.Capabilities{
		Symlink: true, Hardlink: true, AtomicReplace: true, RemoteStat: true, RemoteHash: []string{"sha1", "md5"},
		Reasons: map[string]string{"serverCopy": "extension_missing"},
	}
	if err := openedStore.SaveVolumeCapabilities(context.Background(), seedboxName, stored, probedAt); err != nil {
		t.Fatal(err)
	}
	service := New(Deps{
		Store: openedStore, Config: config.Config{FSProviders: []config.FSProvider{provider}},
		Volumes: map[string]VolumeHandle{seedboxName: stateVolume{state: sftpfs.StateBroken}},
	})

	rows, err := service.ListVolumes(context.Background())
	if err != nil || len(rows) != 2 {
		t.Fatalf("gotValue %+v, %v, expectedValue the local and the seedbox rows", rows, err)
	}

	remote := rows[1]
	expectedCapabilities := VolumeCapabilities{
		Symlink: true, Hardlink: true, AtomicReplace: true, Identity: true, Hashes: []string{"sha1", "md5"},
		Reasons: map[string]string{"serverCopy": "extension_missing"},
	}
	if remote.State != sftpfs.StateBroken || remote.Capabilities == nil || remote.ProbedAt == nil || !remote.ProbedAt.Equal(probedAt) {
		t.Fatalf("gotValue %+v, expectedValue broken with the stored probe of %v", remote, probedAt)
	}
	gotJSON, _ := json.Marshal(remote.Capabilities)
	expectedJSON, _ := json.Marshal(expectedCapabilities)
	if !bytes.Equal(gotJSON, expectedJSON) {
		t.Fatalf("gotValue %s, expectedValue %s", gotJSON, expectedJSON)
	}
}

func TestVolumes_commandsReturnExactTableLines(t *testing.T) {
	service := volumesService(t, []config.FSProvider{hostileProvider()}, nil)

	commands, err := service.VolumeCommands(context.Background(), seedboxName)
	if err != nil {
		t.Fatal(err)
	}

	expectedValue := sftpfs.GuardLines()
	if !slices.Equal(commands.Lines, expectedValue) || commands.Volume != seedboxName || commands.GuardScript != GuardScriptPath {
		t.Fatalf("gotValue %+v, expectedValue the %d guard lines byte for byte", commands, len(expectedValue))
	}
	encoded, err := json.Marshal(commands)
	if err != nil {
		t.Fatal(err)
	}
	var decoded VolumeCommands
	if err := json.Unmarshal(encoded, &decoded); err != nil || !slices.Equal(decoded.Lines, expectedValue) {
		t.Fatalf("gotValue %q, expectedValue the same bytes after a JSON round trip", decoded.Lines)
	}
	if !strings.Contains(string(encoded), `\t`) {
		t.Fatalf("gotValue %s, expectedValue the TAB bytes escaped as \\t", encoded)
	}
}

func TestVolumes_unknownVolumeIsNotFound(t *testing.T) {
	service := volumesService(t, []config.FSProvider{hostileProvider()}, nil)

	_, commandsErr := service.VolumeCommands(context.Background(), "elsewhere")
	_, probeErr := service.ProbeVolume(context.Background(), "elsewhere")
	_, localErr := service.ProbeVolume(context.Background(), "")

	for _, err := range []error{commandsErr, probeErr, localErr} {
		if code, _ := Classify(err); code != ErrorCodeNotFound {
			t.Fatalf("gotValue %v (%s), expectedValue not_found", err, code)
		}
	}
}

func seedboxService(t *testing.T, settings config.Config) Service {
	t.Helper()
	service, closer, err := NewFromConfig(context.Background(), settings)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closer.Close() })
	return service
}

func TestVolumes_probeRunsInTheFirstLinkDirAndStoresTheCapabilities(t *testing.T) {
	server := startSeedbox(t)
	service := seedboxService(t, seedboxSettings(t, server.Provider(t, seedboxName)))

	result, err := service.ProbeVolume(context.Background(), seedboxName)

	if err != nil || result.Result != VolumeProbeOK || result.Capabilities == nil || !result.Capabilities.Hardlink || result.ProbedAt == nil {
		t.Fatalf("gotValue %+v, %v, expectedValue a successful probe with its capabilities", result, err)
	}
	if writes := writeRequestsUnder(server, seedboxLibrary); len(writes) != 0 {
		t.Fatalf("gotValue %+v, expectedValue no write request in the library", writes)
	}
	if writes := writeRequestsUnder(server, seedboxLinks); len(writes) == 0 {
		t.Fatalf("gotValue no write, expectedValue the probe entries in the link folder")
	}
	rows, err := service.ListVolumes(context.Background())
	if err != nil || rows[1].Capabilities == nil || !rows[1].ProbedAt.Equal(*result.ProbedAt) {
		t.Fatalf("gotValue %+v, %v, expectedValue the stored probe listed", rows, err)
	}
	if open := waitUntilClosed(server); open != 0 {
		t.Fatalf("gotValue %d connections left open, expectedValue none after the probe", open)
	}
}

func TestVolumes_probeWithoutLinkDirWritesNothing(t *testing.T) {
	server := startSeedbox(t)
	settings := seedboxSettings(t, server.Provider(t, seedboxName))
	settings.LinkDirs = nil
	service := seedboxService(t, settings)

	result, err := service.ProbeVolume(context.Background(), seedboxName)

	if err != nil || result.Result != VolumeProbeFailed || result.Error != ReasonNoLinkDir {
		t.Fatalf("gotValue %+v, %v, expectedValue failed %s", result, err, ReasonNoLinkDir)
	}
	if writes := writeRequestsUnder(server, "/"); len(writes) != 0 {
		t.Fatalf("gotValue %+v, expectedValue no write request", writes)
	}
}

func TestVolumes_probeNeverRunsInALibraryRoot(t *testing.T) {
	server := startSeedbox(t)
	settings := seedboxSettings(t, server.Provider(t, seedboxName))
	settings.LinkDirs = []string{"sftp://" + seedboxName + seedboxLibrary}
	service := seedboxService(t, settings)

	result, err := service.ProbeVolume(context.Background(), seedboxName)

	if err != nil || result.Result != VolumeProbeFailed || result.Error != pipeline.ReasonLinkDirOverlapsLibrary {
		t.Fatalf("gotValue %+v, %v, expectedValue failed %s", result, err, pipeline.ReasonLinkDirOverlapsLibrary)
	}
	if writes := writeRequestsUnder(server, seedboxLibrary); len(writes) != 0 {
		t.Fatalf("gotValue %+v, expectedValue no write request in the library", writes)
	}
}

func TestVolumes_probeGivesTheHostKeyCodeNeverTheErrorText(t *testing.T) {
	server := startSeedbox(t)
	provider := server.Provider(t, seedboxName)
	sftptest.WriteFile(t, provider.KnownHosts, []byte(otherHostKeyLine(t, server.Address)+"\n"), 0o600)
	service := seedboxService(t, seedboxSettings(t, provider))

	result, err := service.ProbeVolume(context.Background(), seedboxName)

	if err != nil || result.Result != VolumeProbeFailed || result.Error != sftpfs.CodeHostKeyMismatch || result.Capabilities != nil {
		t.Fatalf("gotValue %+v, %v, expectedValue failed host_key_mismatch", result, err)
	}
}

func TestVolumes_probeIsRefusedWhileAStageHoldsTheLock(t *testing.T) {
	server := startSeedbox(t)
	settings := seedboxSettings(t, server.Provider(t, seedboxName))
	probing := seedboxService(t, settings)
	holding, release := make(chan struct{}), make(chan struct{})
	lockErr := make(chan error, 1)
	go func() {
		lockErr <- pipeline.WithLock(context.Background(), probing.(*service).deps.Stages, "test", func(context.Context) error {
			close(holding)
			<-release
			return nil
		})
	}()
	<-holding

	_, err := probing.ProbeVolume(context.Background(), seedboxName)
	close(release)

	if code, _ := Classify(err); code != ErrorCodeStageRunning {
		t.Fatalf("gotValue %v (%s), expectedValue stage_running", err, code)
	}
	if lockFailure := <-lockErr; lockFailure != nil {
		t.Fatal(lockFailure)
	}
}

func TestVolumes_secondConcurrentProbeOfTheSameVolumeIsRefused(t *testing.T) {
	probing := volumesService(t, []config.FSProvider{hostileProvider()}, nil)
	probing.(*service).volumeProbes.Store(seedboxName, struct{}{})

	_, err := probing.ProbeVolume(context.Background(), seedboxName)

	if !errors.Is(err, ErrVolumeProbeRunning) {
		t.Fatalf("gotValue %v, expectedValue ErrVolumeProbeRunning", err)
	}
	if code, _ := Classify(err); code != ErrorCodeRefused {
		t.Fatalf("gotValue %s, expectedValue refused", code)
	}
}

func TestStatus_countsBrokenVolumes(t *testing.T) {
	service := volumesService(t, nil, map[string]VolumeHandle{
		"one": stateVolume{state: sftpfs.StateBroken}, "two": stateVolume{state: sftpfs.StateOK},
		"three": stateVolume{state: sftpfs.StateBroken},
	})

	status, err := service.Status(context.Background())

	if err != nil || status.VolumesBroken != 2 {
		t.Fatalf("gotValue %d, %v, expectedValue 2 broken volumes", status.VolumesBroken, err)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"volumes_broken":2`)) {
		t.Fatalf("gotValue %s, expectedValue the JSON key volumes_broken", encoded)
	}
}
