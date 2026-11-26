package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"necromanciarr/internal/app"
	"necromanciarr/internal/doctor"
	"necromanciarr/internal/i18n"
)

// Passkeys factices à faible entropie : la seconde change sous l'échappement JSON et l'encodage en pourcentage, et
// aucun de ses morceaux n'atteint 16 caractères, si bien que seuls les secrets connus la masquent.
const (
	trackerPasskeyA = "0000aaaa0000aaaa0000aaaa0000aaaa"
	trackerPasskeyB = `fake"pass&key/0000`
	trackersConfig  = `{"trackers": {
  "tracker-a": "https://tracker-a.example.com/announce/0000aaaa0000aaaa0000aaaa0000aaaa",
  "tracker-b": {
    "announce": ["https://tracker-b.example.com/x/announce", "udp://tracker-b.example.com:6969/announce?pk=fake\"pass&key/0000"],
    "aliases": ["TrackerB"]
  }
}}`
)

// percentEncodeEveryByte encode chaque octet, comme le ferait une couche basse qui réécrit l'URL entière.
func percentEncodeEveryByte(text string) string {
	var encoded strings.Builder
	for _, character := range []byte(text) {
		_, _ = fmt.Fprintf(&encoded, "%%%02X", character)
	}
	return encoded.String()
}

// leakingText met les passkeys sous toutes leurs formes dans des URL et dans le texte libre : ce qu'un message d'une
// couche basse (client, tracker, qBittorrent) pourrait recopier.
func leakingText() string {
	return strings.Join([]string{
		"announce https://tracker-a.example.com/announce/" + trackerPasskeyA + " refused",
		"retry HTTPS://TRACKER-A.EXAMPLE.COM/ANNOUNCE/" + strings.ToUpper(trackerPasskeyA),
		"encoded https://tracker-a.example.com/announce/" + percentEncodeEveryByte(trackerPasskeyA),
		"udp://tracker-b.example.com:6969/announce?pk=" + url.QueryEscape(trackerPasskeyB),
		"bare " + trackerPasskeyA + " and " + trackerPasskeyB + " and " + strings.ToUpper(trackerPasskeyB),
		"escaped " + url.QueryEscape(trackerPasskeyB) + " " + url.PathEscape(trackerPasskeyB),
	}, "; ")
}

// passkeyForms liste ce qui ne doit jamais sortir : valeurs, formes échappées JSON (avec ou sans échappement HTML),
// encodées en pourcentage et en capitales.
func passkeyForms() []string {
	forms := []string{
		trackerPasskeyA, strings.ToUpper(trackerPasskeyA), percentEncodeEveryByte(trackerPasskeyA), "0000aaaa",
		trackerPasskeyB, strings.ToUpper(trackerPasskeyB), url.QueryEscape(trackerPasskeyB), url.PathEscape(trackerPasskeyB),
		`fake\"pass`, `pass&key`, "pass&key", "PASS&KEY", "pass%26key",
	}
	return forms
}

func expectNoPasskey(t *testing.T, label, output string) {
	t.Helper()
	for _, form := range passkeyForms() {
		if strings.Contains(output, form) {
			t.Fatalf("%s: gotValue %q in %q, expectedValue no form of the passkey", label, form, output)
		}
	}
}

func TestRedact_trackerPasskeyEverywhere(t *testing.T) {
	configDir := writeDoctorConfig(t, trackersConfig)
	leak := leakingText()
	fake := &fakeService{
		status: app.Status{Warnings: []app.Warning{{Code: "warning.test", Text: leak}}},
		doctorReport: &doctor.Report{Checks: []doctor.Outcome{{
			ID: "config", TitleKey: i18n.KeyDoctorTitleConfig,
			Result: doctor.Result{Status: doctor.Fail, Code: "config_invalid", Detail: i18n.Message{Key: i18n.KeyDoctorConfigLoadFailed, Params: map[string]string{"error": leak}}},
		}}},
	}
	useFakeService(t, fake)

	for _, args := range [][]string{{"config", "check"}, {"status"}, {"status", "--json"}, {"doctor"}, {"doctor", "--json"}} {
		result := runCli(t, configDir, args...)
		if !strings.Contains(strings.ToLower(result.stdout+result.stderr), "tracker-a.example.com") {
			t.Fatalf("%v: gotValue exit %d stdout %q stderr %q, expectedValue the redacted output with the host", args, result.exitCode, result.stdout, result.stderr)
		}
		expectNoPasskey(t, strings.Join(args, " "), result.stdout+result.stderr)
	}
	fake.err = errors.New("add failed: " + leak)
	for _, args := range [][]string{{"proposals", "accept", "1"}, {"proposals", "accept", "1", "--json"}, {"scan"}, {"scan", "--json"}} {
		result := runCli(t, configDir, args...)
		if result.exitCode == successExitCode {
			t.Fatalf("%v: gotValue success, expectedValue the failure printed", args)
		}
		expectNoPasskey(t, strings.Join(args, " "), result.stdout+result.stderr)
	}
}

func TestRedact_refusedTrackerNeverEchoesTheURL(t *testing.T) {
	configDir := writeDoctorConfig(t, `{"trackers": {"tracker-a": "https://tracker-a.example.com/announce/0000aaaa0000aaaa0000aaaa0000aaaa#x"}}`)

	for _, args := range [][]string{{"config", "check"}, {"status", "--json"}} {
		result := runCli(t, configDir, args...)

		if result.exitCode == successExitCode || !strings.Contains(result.stdout+result.stderr, "trackers") {
			t.Fatalf("%v: gotValue exit %d, stdout %q, stderr %q, expectedValue a trackers refusal", args, result.exitCode, result.stdout, result.stderr)
		}
		expectNoPasskey(t, strings.Join(args, " "), result.stdout+result.stderr)
		if strings.Contains(result.stdout+result.stderr, "tracker-a.example.com") {
			t.Fatalf("%v: gotValue the URL host in %q, expectedValue a generic message", args, result.stderr)
		}
	}
}

func TestConfigCheck_showsTrackerHostsOnly(t *testing.T) {
	configDir := writeDoctorConfig(t, trackersConfig)

	result := runCli(t, configDir, "config", "check")

	var printed struct {
		Trackers []struct {
			Key      string   `json:"key"`
			Aliases  []string `json:"aliases"`
			Announce []string `json:"announce"`
		} `json:"trackers"`
	}
	if result.exitCode != successExitCode || json.Unmarshal([]byte(result.stdout), &printed) != nil {
		t.Fatalf("gotValue exit %d stdout %q stderr %q, expectedValue the configuration as JSON", result.exitCode, result.stdout, result.stderr)
	}
	expectedValue := `[{tracker-a [] [https://tracker-a.example.com/***]} {tracker-b [TrackerB] [https://tracker-b.example.com/*** udp://tracker-b.example.com:6969/***]}]`
	if gotValue := fmt.Sprint(printed.Trackers); gotValue != expectedValue {
		t.Fatalf("gotValue %s, expectedValue %s", gotValue, expectedValue)
	}
	expectNoPasskey(t, "config check", result.stdout+result.stderr)
	if strings.Contains(result.stdout, `"variables"`) {
		t.Fatalf("valeurObtenue %q, valeurAttendue aucun champ variables", result.stdout)
	}
}

func TestPrinter_controlByteCannotSplitASecretPastTheRedaction(t *testing.T) {
	out := printer{secrets: []string{"fake-known-secret-value"}}

	gotValue := out.redact("refused fake-known\x07-secret\x1b-value at https://tracker-a.example.com/0000aaaa\x000000aaaa0000aaaa0000aaaa/x")

	if strings.Contains(gotValue, "fake-known-secret-value") || strings.Contains(gotValue, "0000aaaa0000") {
		t.Fatalf("gotValue %q, expectedValue the secret and the passkey masked once the control bytes are gone", gotValue)
	}
}
