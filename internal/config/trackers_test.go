package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// Passkeys factices à faible entropie : la seconde porte des caractères qui changent sous l'échappement JSON et
// l'encodage en pourcentage, et dont aucun morceau n'atteint 16 caractères (seuls les secrets connus la masquent).
const (
	fakePasskeyA = "0000aaaa0000aaaa0000aaaa0000aaaa"
	fakePasskeyB = `fake"pass&key/0000`
)

func trackerEnvironment() map[string]string {
	return map[string]string{"PASSKEY_TRACKER_A": fakePasskeyA, "PASSKEY_TRACKER_B": fakePasskeyB}
}

func loadTrackerConfig(t *testing.T, trackers string, environment map[string]string) (Config, error) {
	t.Helper()
	return loadFromJSON(t, `{"trackers": `+trackers+`}`, environment)
}

const sampleTrackers = `{
  "tracker-a": "https://tracker-a.example.com/announce/${PASSKEY_TRACKER_A}",
  "tracker-b": {
    "announce": [
      "https://tracker-b.example.com/${PASSKEY_TRACKER_B}/announce",
      "udp://tracker-b.example.com:6969/announce?pk=${PASSKEY_TRACKER_B}"
    ],
    "aliases": ["TrackerB", "tracker-b.example.com"]
  }
}`

func TestNormalizeTracker_lowercasesAndRemovesSpaces(t *testing.T) {
	for raw, expectedValue := range map[string]string{"Tracker A": "trackera", " TRACKER-a\t": "tracker-a", "tracker-b.example.com": "tracker-b.example.com"} {
		if gotValue := NormalizeTracker(raw); gotValue != expectedValue {
			t.Fatalf("NormalizeTracker(%q): gotValue %q, expectedValue %q", raw, gotValue, expectedValue)
		}
	}
}

func TestConfig_trackerStringOrObjectForms(t *testing.T) {
	loadedConfig, err := loadTrackerConfig(t, sampleTrackers, trackerEnvironment())
	if err != nil {
		t.Fatal(err)
	}

	expectedValue := []Tracker{
		{
			Key: "tracker-a", Announce: []string{"https://tracker-a.example.com/announce/" + fakePasskeyA},
			Variables: []string{"PASSKEY_TRACKER_A"},
		},
		{
			Key: "tracker-b",
			Announce: []string{
				"https://tracker-b.example.com/" + fakePasskeyB + "/announce",
				"udp://tracker-b.example.com:6969/announce?pk=" + fakePasskeyB,
			},
			Aliases: []string{"TrackerB", "tracker-b.example.com"}, Variables: []string{"PASSKEY_TRACKER_B"},
		},
	}
	if len(loadedConfig.Trackers) != len(expectedValue) {
		t.Fatalf("gotValue %d trackers, expectedValue %d", len(loadedConfig.Trackers), len(expectedValue))
	}
	for trackerIndex, gotValue := range loadedConfig.Trackers {
		expected := expectedValue[trackerIndex]
		sameTracker := gotValue.Key == expected.Key && slices.Equal(gotValue.Announce, expected.Announce) &&
			slices.Equal(gotValue.Aliases, expected.Aliases) && slices.Equal(gotValue.Variables, expected.Variables)
		if !sameTracker {
			t.Fatalf("tracker %d: gotValue a different tracker (key %q, %d announces, aliases %v, variables %v), expectedValue key %q",
				trackerIndex, gotValue.Key, len(gotValue.Announce), gotValue.Aliases, gotValue.Variables, expected.Key)
		}
	}
}

func TestConfig_trackerObjectRefusesUnknownKeyOrEmptyAnnounce(t *testing.T) {
	for name, trackers := range map[string]string{
		"unknown key":       `{"tracker-a": {"announce": ["https://tracker-a.example.com/announce"], "tier": 2}}`,
		"empty announce":    `{"tracker-a": {"announce": []}}`,
		"missing announce":  `{"tracker-a": {"aliases": ["a"]}}`,
		"empty key":         `{" ": "https://tracker-a.example.com/announce"}`,
		"empty alias":       `{"tracker-a": {"announce": ["https://tracker-a.example.com/announce"], "aliases": [" "]}}`,
		"not an object":     `["https://tracker-a.example.com/announce"]`,
		"number entry":      `{"tracker-a": 12}`,
		"empty url":         `{"tracker-a": ""}`,
		"announce as value": `{"tracker-a": {"announce": "https://tracker-a.example.com/announce"}}`,
	} {
		if _, err := loadTrackerConfig(t, trackers, nil); err == nil || !strings.Contains(err.Error(), "trackers") {
			t.Fatalf("%s: gotValue %v, expectedValue a trackers error", name, err)
		}
	}
}

func TestConfig_refusesTrackerAliasCollision(t *testing.T) {
	for name, trackers := range map[string]string{
		"alias equals another key":  `{"tracker-a": "https://tracker-a.example.com/${PASSKEY_TRACKER_A}", "tracker-b": {"announce": ["https://tracker-b.example.com/${PASSKEY_TRACKER_A}"], "aliases": ["TRACKER-A"]}}`,
		"keys differing by case":    `{"Tracker-A": "https://tracker-a.example.com/${PASSKEY_TRACKER_A}", "tracker-a": "https://tracker-b.example.com/${PASSKEY_TRACKER_A}"}`,
		"keys differing by a space": `{"tracker a": "https://tracker-a.example.com/${PASSKEY_TRACKER_A}", "trackera": "https://tracker-b.example.com/${PASSKEY_TRACKER_A}"}`,
		"shared alias":              `{"tracker-a": {"announce": ["https://tracker-a.example.com/${PASSKEY_TRACKER_A}"], "aliases": ["shared"]}, "tracker-b": {"announce": ["https://tracker-b.example.com/${PASSKEY_TRACKER_A}"], "aliases": ["Shared"]}}`,
	} {
		_, err := loadTrackerConfig(t, trackers, trackerEnvironment())

		if err == nil || !strings.Contains(err.Error(), "trackers") {
			t.Fatalf("%s: gotValue %v, expectedValue a collision error", name, err)
		}
		message := err.Error()
		if strings.Contains(message, fakePasskeyA) || strings.Contains(message, "example.com") {
			t.Fatalf("%s: gotValue %q, expectedValue the keys named without any URL", name, message)
		}
	}
}

func TestConfig_trackerOwnAliasEqualToItsKeyIsAccepted(t *testing.T) {
	if _, err := loadTrackerConfig(t, `{"tracker-a": {"announce": ["https://tracker-a.example.com/x"], "aliases": ["Tracker-A"]}}`, nil); err != nil {
		t.Fatalf("gotValue %v, expectedValue an alias repeating its own key to be harmless", err)
	}
}

func TestConfig_trackerRefusesFragmentOrMissingHost(t *testing.T) {
	for name, announce := range map[string]string{
		"fragment":            "https://tracker-a.example.com/announce/${PASSKEY_TRACKER_A}#x",
		"empty fragment":      "https://tracker-a.example.com/announce/${PASSKEY_TRACKER_A}#",
		"missing host":        "https:///announce/${PASSKEY_TRACKER_A}",
		"opaque udp":          "udp:tracker-a.example.com/${PASSKEY_TRACKER_A}",
		"no scheme":           "tracker-a.example.com/announce/${PASSKEY_TRACKER_A}",
		"other scheme":        "wss://tracker-a.example.com/announce/${PASSKEY_TRACKER_A}",
		"port without host":   "https://:443/announce/${PASSKEY_TRACKER_A}",
		"unparsable":          "https://tracker-a.example.com:port/${PASSKEY_TRACKER_A}",
		"control character":   "https://tracker-a.example.com/\u0001/${PASSKEY_TRACKER_A}",
		"surrounding spaces":  " https://tracker-a.example.com/announce/${PASSKEY_TRACKER_A}",
		"passkey in fragment": "https://tracker-a.example.com/announce#${PASSKEY_TRACKER_A}",
	} {
		_, err := loadTrackerConfig(t, `{"tracker-a": "`+jsonText(announce)+`"}`, trackerEnvironment())

		if err == nil || !strings.Contains(err.Error(), "trackers") {
			t.Fatalf("%s: gotValue %v, expectedValue a refusal", name, err)
		}
		if strings.Contains(err.Error(), fakePasskeyA) || strings.Contains(err.Error(), "tracker-a.example.com") {
			t.Fatalf("%s: gotValue %q, expectedValue no URL nor passkey in the error", name, err)
		}
	}
}

func TestConfig_trackerAcceptsUppercaseSchemeAndIPv6Host(t *testing.T) {
	loadedConfig, err := loadTrackerConfig(t, `{"tracker-a": {"announce": ["HTTPS://tracker-a.example.com/x", "udp://[2001:db8::1]:6969/announce"]}}`, nil)
	if err != nil {
		t.Fatalf("gotValue %v, expectedValue both URLs accepted", err)
	}
	if gotValue := len(loadedConfig.Trackers[0].Announce); gotValue != 2 {
		t.Fatalf("gotValue %d announces, expectedValue 2", gotValue)
	}
}

func TestConfig_trackerUndefinedVariableNamesItOnly(t *testing.T) {
	_, err := loadTrackerConfig(t, `{"tracker-a": "https://tracker-a.example.com/announce/${PASSKEY_MISSING}"}`, nil)

	if err == nil || !strings.Contains(err.Error(), "PASSKEY_MISSING") || strings.Contains(err.Error(), "example.com") {
		t.Fatalf("gotValue %v, expectedValue an error naming the variable only", err)
	}
}

func TestConfig_trackerValuesAreKnownSecretsUnderEveryForm(t *testing.T) {
	loadedConfig, err := loadTrackerConfig(t, sampleTrackers, trackerEnvironment())
	if err != nil {
		t.Fatal(err)
	}

	gotValue := loadedConfig.KnownSecrets()

	for _, expectedForm := range []string{
		fakePasskeyA, strings.ToUpper(fakePasskeyA), fakePasskeyB, strings.ToUpper(fakePasskeyB),
		url.QueryEscape(fakePasskeyB), url.PathEscape(fakePasskeyB), url.QueryEscape(strings.ToUpper(fakePasskeyB)),
	} {
		if !slices.Contains(gotValue, expectedForm) {
			t.Fatalf("gotValue %d known secrets without %q, expectedValue every form of the passkey", len(gotValue), expectedForm)
		}
	}
}

func TestConfig_literalLongAnnounceSegmentIsAKnownSecret(t *testing.T) {
	loadedConfig, err := loadTrackerConfig(t, `{"tracker-a": "https://tracker-a.example.com/`+fakePasskeyA+`/announce"}`, nil)
	if err != nil {
		t.Fatal(err)
	}

	if gotValue := loadedConfig.KnownSecrets(); !slices.Contains(gotValue, fakePasskeyA) || slices.Contains(gotValue, "announce") {
		t.Fatalf("gotValue %v, expectedValue the literal passkey and not the word announce", gotValue)
	}
}

func TestWithDiscoveredSecrets_addsTransientValuesToTheKnownOnes(t *testing.T) {
	loadedConfig := Config{AuthToken: "fake-auth-token"}

	gotValue := loadedConfig.WithDiscoveredSecrets("fake&discovered", "")

	for _, expectedForm := range []string{"fake-auth-token", "fake&discovered", "FAKE&DISCOVERED", "fake%26discovered"} {
		if !slices.Contains(gotValue, expectedForm) {
			t.Fatalf("gotValue %v, expectedValue %q", gotValue, expectedForm)
		}
	}
	if slices.Contains(loadedConfig.KnownSecrets(), "fake&discovered") {
		t.Fatal("gotValue the transient value kept in the configuration, expectedValue it only in the returned list")
	}
}

func TestConfig_trackersRenderHostsAndVariableNamesOnly(t *testing.T) {
	loadedConfig, err := loadTrackerConfig(t, sampleTrackers, trackerEnvironment())
	if err != nil {
		t.Fatal(err)
	}
	redactedJSON, err := loadedConfig.RedactedJSON()
	if err != nil {
		t.Fatal(err)
	}
	elementJSON, err := json.Marshal(loadedConfig.Trackers)
	if err != nil {
		t.Fatal(err)
	}
	var logBuffer bytes.Buffer
	slog.New(slog.NewJSONHandler(&logBuffer, nil)).Info("config", "config", loadedConfig, "tracker", loadedConfig.Trackers[1])
	renderings := []string{string(redactedJSON), string(elementJSON), logBuffer.String()}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		renderings = append(renderings, fmt.Sprintf(verb, loadedConfig), fmt.Sprintf(verb, loadedConfig.Trackers),
			fmt.Sprintf(verb, loadedConfig.Trackers[1]), fmt.Sprintf(verb, &loadedConfig.Trackers[1]))
	}

	for _, rendering := range renderings {
		for _, leaked := range []string{fakePasskeyA, "0000aaaa", "pass&key", `pass&key`, "/announce"} {
			if strings.Contains(rendering, leaked) {
				t.Fatalf("gotValue %q in %q, expectedValue only hosts and variable names", leaked, rendering)
			}
		}
	}
	for _, expectedPart := range []string{`"key": "tracker-b"`, `"udp://tracker-b.example.com:6969/***"`, `"PASSKEY_TRACKER_B"`, `"TrackerB"`} {
		if !strings.Contains(string(redactedJSON), expectedPart) {
			t.Fatalf("gotValue %s, expectedValue it to contain %s", redactedJSON, expectedPart)
		}
	}
}

func TestConfig_shortTrackerValueIsNotALiteralSecret(t *testing.T) {
	environment := map[string]string{"UID": "a", "PASSKEY_TRACKER_A": fakePasskeyA}
	loadedConfig, err := loadTrackerConfig(t, `{"tracker-a": "https://tracker-a.example.com/announce?uid=${UID}&passkey=${PASSKEY_TRACKER_A}"}`, environment)
	if err != nil {
		t.Fatal(err)
	}
	const ordinaryText = "Radarr scan has a problem"

	gotText := RedactText(ordinaryText, loadedConfig.KnownSecrets()...)
	gotURL := RedactText("announce https://tracker-a.example.com/announce?uid=a&x=1", loadedConfig.KnownSecrets()...)

	if gotText != ordinaryText || strings.Contains(gotURL, "uid=a") {
		t.Fatalf("gotValue %q and %q, expectedValue the text untouched and the uid masked inside the URL", gotText, gotURL)
	}
	if gotValue := loadedConfig.Trackers[0].ShortVariables; !slices.Equal(gotValue, []string{"UID"}) {
		t.Fatalf("gotValue %v, expectedValue [UID] named as too short", gotValue)
	}
	if !slices.Contains(loadedConfig.KnownSecrets(), fakePasskeyA) {
		t.Fatal("gotValue the long passkey missing from the known secrets, expectedValue it kept")
	}
}

func TestWithDiscoveredSecrets_ignoresValuesShorterThanEightBytes(t *testing.T) {
	gotValue := Config{}.WithDiscoveredSecrets("1234567", "12345678")

	if slices.Contains(gotValue, "1234567") || !slices.Contains(gotValue, "12345678") {
		t.Fatalf("gotValue %v, expectedValue only the value of at least 8 bytes", gotValue)
	}
}
