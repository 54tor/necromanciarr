package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"necromanciarr/internal/app"
	"necromanciarr/internal/fsys/sftpfs"
)

func sampleVolumeRows() []app.VolumeRow {
	probedAt := time.Date(2026, 11, 20, 8, 30, 0, 0, time.UTC)
	return []app.VolumeRow{
		{Type: app.VolumeTypeLocal, State: sftpfs.StateOK},
		{
			Volume: "seedbox", Type: app.VolumeTypeSFTP, Address: "seedbox.example.com:22", State: sftpfs.StateBroken,
			Capabilities: &app.VolumeCapabilities{Symlink: true, Hardlink: true, Identity: true, Hashes: []string{"sha1"}, Reasons: map[string]string{}},
			ProbedAt:     &probedAt,
		},
		{Volume: "other", Type: app.VolumeTypeSFTP, Address: "other.example.com:2222", State: sftpfs.StateUnknown},
	}
}

var failedVolumeProbe = app.VolumeProbeResult{Volume: "seedbox", Result: app.VolumeProbeFailed, Error: sftpfs.CodeHostKeyUnknown}

func TestCLI_volumesListPrintsATableAndStableJSON(t *testing.T) {
	configDir := writeDoctorConfig(t, `{}`)
	useFakeService(t, &fakeService{volumeRows: sampleVolumeRows()})

	english := runCli(t, configDir, "volumes", "list")
	french := runCli(t, configDir, "volumes", "list", "--language", "fr")
	encoded := runCli(t, configDir, "volumes", "list", "--json")

	for _, expected := range []string{"local", "seedbox", "seedbox.example.com:22", "broken", "symlink,hardlink,identity", "2026-11-20", "not probed", "ADDRESS"} {
		if !strings.Contains(english.stdout, expected) {
			t.Fatalf("gotValue %q, expectedValue %q", english.stdout, expected)
		}
	}
	if !strings.Contains(french.stdout, "non sondé") || !strings.Contains(french.stdout, "ADRESSE") || english.exitCode != successExitCode {
		t.Fatalf("gotValue %q (exit %d), expectedValue the French table", french.stdout, english.exitCode)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(encoded.stdout), &rows); err != nil || len(rows) != 3 {
		t.Fatalf("gotValue %q (%v)", encoded.stdout, err)
	}
	for _, key := range []string{"volume", "type", "address", "state", "capabilities", "probed_at"} {
		if _, found := rows[1][key]; !found {
			t.Fatalf("gotValue %v, expectedValue the key %q", rows[1], key)
		}
	}
}

func TestCLI_volumesProbeExitsOneOnAFailedProbeWithTheTranslatedCode(t *testing.T) {
	configDir := writeDoctorConfig(t, `{}`)
	fake := &fakeService{volumeProbe: &failedVolumeProbe}
	useFakeService(t, fake)

	result := runCli(t, configDir, "volumes", "probe", "seedbox")

	if result.exitCode != failureExitCode || !fake.called("ProbeVolume seedbox") || !strings.Contains(result.stdout, sftpfs.CodeHostKeyUnknown) ||
		!strings.Contains(result.stdout, "knownHosts") {
		t.Fatalf("gotValue exit %d, stdout %q, calls %v", result.exitCode, result.stdout, fake.calls)
	}
}

func TestCLI_volumesCommandsPrintsTheExactLinesRaw(t *testing.T) {
	configDir := writeDoctorConfig(t, `{}`)
	useFakeService(t, &fakeService{})

	text := runCli(t, configDir, "volumes", "commands", "seedbox")
	encoded := runCli(t, configDir, "volumes", "commands", "seedbox", "--json")

	expectedValue := strings.Join(sftpfs.GuardLines(), "\n") + "\n"
	if text.exitCode != successExitCode || text.stdout != expectedValue {
		t.Fatalf("gotValue %q, expectedValue the guard lines alone on stdout, tabs kept:\n%q", text.stdout, expectedValue)
	}
	if !strings.Contains(text.stderr, app.GuardScriptPath) {
		t.Fatalf("gotValue stderr %q, expectedValue the guard script hint", text.stderr)
	}
	var commands app.VolumeCommands
	if err := json.Unmarshal([]byte(encoded.stdout), &commands); err != nil || strings.Join(commands.Lines, "\n")+"\n" != expectedValue {
		t.Fatalf("gotValue %q (%v), expectedValue the same lines in JSON", encoded.stdout, err)
	}
}
