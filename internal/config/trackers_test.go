package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// Passkeys factices à faible entropie : la seconde porte des caractères qui changent sous l'échappement JSON et
// l'encodage en pourcentage, et dont aucun morceau n'atteint 16 caractères (seuls les secrets connus la masquent).
const (
	fakePasskeyA = "0000aaaa0000aaaa0000aaaa0000aaaa"
	fakePasskeyB = `fake"pass&key/0000`
	// fakeStoredPasskeyB est une passkey littérale de la base, assez longue pour être reconnue comme secret.
	fakeStoredPasskeyB = "1111bbbb1111bbbb1111bbbb"
)

func trackerEnvironment() map[string]string {
	return map[string]string{"PASSKEY_TRACKER_A": fakePasskeyA, "PASSKEY_TRACKER_B": fakePasskeyB}
}

func loadTrackerConfig(t *testing.T, trackers string, environment map[string]string) (Config, error) {
	t.Helper()
	return loadFromJSON(t, `{"trackers": `+trackers+`}`, environment)
}

const sampleTrackers = `{
  "tracker-a": "https://tracker-a.example.com/announce/0000aaaa0000aaaa0000aaaa0000aaaa",
  "tracker-b": {
    "announce": [
      "https://tracker-b.example.com/1111bbbb1111bbbb1111bbbb/announce",
      "udp://tracker-b.example.com:6969/announce?pk=1111bbbb1111bbbb1111bbbb"
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
		},
		{
			Key: "tracker-b",
			Announce: []string{
				"https://tracker-b.example.com/" + fakeStoredPasskeyB + "/announce",
				"udp://tracker-b.example.com:6969/announce?pk=" + fakeStoredPasskeyB,
			},
			Aliases: []string{"TrackerB", "tracker-b.example.com"},
		},
	}
	if len(loadedConfig.Trackers) != len(expectedValue) {
		t.Fatalf("gotValue %d trackers, expectedValue %d", len(loadedConfig.Trackers), len(expectedValue))
	}
	for trackerIndex, gotValue := range loadedConfig.Trackers {
		expected := expectedValue[trackerIndex]
		sameTracker := gotValue.Key == expected.Key && slices.Equal(gotValue.Announce, expected.Announce) &&
			slices.Equal(gotValue.Aliases, expected.Aliases)
		if !sameTracker {
			t.Fatalf("tracker %d: gotValue a different tracker (key %q, %d announces, aliases %v), expectedValue key %q",
				trackerIndex, gotValue.Key, len(gotValue.Announce), gotValue.Aliases, expected.Key)
		}
	}
}

func TestConfig_trackerObjectRefusesUnknownKeyOrEmptyAnnounce(t *testing.T) {
	for name, trackers := range map[string]string{
		"unknown key":      `{"tracker-a": {"announce": ["https://tracker-a.example.com/announce"], "tier": 2}}`,
		"empty announce":   `{"tracker-a": {"announce": []}}`,
		"missing announce": `{"tracker-a": {"aliases": ["a"]}}`,
		"empty key":        `{" ": "https://tracker-a.example.com/announce"}`,
		"empty alias":      `{"tracker-a": {"announce": ["https://tracker-a.example.com/announce"], "aliases": [" "]}}`,
		"number entry":     `{"tracker-a": 12}`,
		"empty url":        `{"tracker-a": ""}`,
	} {
		if _, err := loadTrackerConfig(t, trackers, nil); err == nil || !strings.Contains(err.Error(), "trackers") {
			t.Fatalf("%s: gotValue %v, expectedValue a trackers error", name, err)
		}
	}
}

func TestConfig_refusesTrackerAliasCollision(t *testing.T) {
	for name, trackers := range map[string]string{
		"alias equals another key":  `{"tracker-a": "https://tracker-a.example.com/0000aaaa0000aaaa0000aaaa0000aaaa", "tracker-b": {"announce": ["https://tracker-b.example.com/0000aaaa0000aaaa0000aaaa0000aaaa"], "aliases": ["TRACKER-A"]}}`,
		"keys differing by case":    `{"Tracker-A": "https://tracker-a.example.com/0000aaaa0000aaaa0000aaaa0000aaaa", "tracker-a": "https://tracker-b.example.com/0000aaaa0000aaaa0000aaaa0000aaaa"}`,
		"keys differing by a space": `{"tracker a": "https://tracker-a.example.com/0000aaaa0000aaaa0000aaaa0000aaaa", "trackera": "https://tracker-b.example.com/0000aaaa0000aaaa0000aaaa0000aaaa"}`,
		"shared alias":              `{"tracker-a": {"announce": ["https://tracker-a.example.com/0000aaaa0000aaaa0000aaaa0000aaaa"], "aliases": ["shared"]}, "tracker-b": {"announce": ["https://tracker-b.example.com/0000aaaa0000aaaa0000aaaa0000aaaa"], "aliases": ["Shared"]}}`,
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
		"fragment":            "https://tracker-a.example.com/announce/0000aaaa0000aaaa0000aaaa0000aaaa#x",
		"empty fragment":      "https://tracker-a.example.com/announce/0000aaaa0000aaaa0000aaaa0000aaaa#",
		"missing host":        "https:///announce/0000aaaa0000aaaa0000aaaa0000aaaa",
		"opaque udp":          "udp:tracker-a.example.com/0000aaaa0000aaaa0000aaaa0000aaaa",
		"no scheme":           "tracker-a.example.com/announce/0000aaaa0000aaaa0000aaaa0000aaaa",
		"other scheme":        "wss://tracker-a.example.com/announce/0000aaaa0000aaaa0000aaaa0000aaaa",
		"port without host":   "https://:443/announce/0000aaaa0000aaaa0000aaaa0000aaaa",
		"unparsable":          "https://tracker-a.example.com:port/0000aaaa0000aaaa0000aaaa0000aaaa",
		"control character":   "https://tracker-a.example.com/\u0001/0000aaaa0000aaaa0000aaaa0000aaaa",
		"surrounding spaces":  " https://tracker-a.example.com/announce/0000aaaa0000aaaa0000aaaa0000aaaa",
		"passkey in fragment": "https://tracker-a.example.com/announce#0000aaaa0000aaaa0000aaaa0000aaaa",
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

func TestConfig_trackerValuesAreKnownSecretsUnderEveryForm(t *testing.T) {
	loadedConfig, err := loadTrackerConfig(t, sampleTrackers, trackerEnvironment())
	if err != nil {
		t.Fatal(err)
	}

	gotValue := loadedConfig.KnownSecrets()

	for _, expectedForm := range []string{
		fakePasskeyA, strings.ToUpper(fakePasskeyA), fakeStoredPasskeyB, strings.ToUpper(fakeStoredPasskeyB),
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

func TestConfig_trackerPlainWordSegmentIsNotMaskedOutsideTheURL(t *testing.T) {
	loadedConfig, err := loadTrackerConfig(t, `{"tracker-a": "https://tracker-a.example.com/announce/`+fakePasskeyA+`"}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	const ordinaryText = "the announce failed"

	valeurObtenue := RedactText(ordinaryText+" for "+strings.ToUpper(fakePasskeyA), loadedConfig.KnownSecrets()...)

	valeurAttendue := ordinaryText + " for ***"
	if valeurObtenue != valeurAttendue {
		t.Fatalf("valeurObtenue %q, valeurAttendue %q", valeurObtenue, valeurAttendue)
	}
}

func TestConfig_trackersRenderHostsOnly(t *testing.T) {
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
		for _, leaked := range []string{fakePasskeyA, "0000aaaa", "pass&key", `pass&key`, fakeStoredPasskeyB, "/announce"} {
			if strings.Contains(rendering, leaked) {
				t.Fatalf("gotValue %q in %q, expectedValue only hosts and variable names", leaked, rendering)
			}
		}
	}
	for _, expectedPart := range []string{`"key": "tracker-b"`, `"udp://tracker-b.example.com:6969/***"`, `"TrackerB"`} {
		if !strings.Contains(string(redactedJSON), expectedPart) {
			t.Fatalf("gotValue %s, expectedValue it to contain %s", redactedJSON, expectedPart)
		}
	}
}

func TestWithDiscoveredSecrets_ignoresValuesShorterThanEightBytes(t *testing.T) {
	gotValue := Config{}.WithDiscoveredSecrets("1234567", "12345678")

	if slices.Contains(gotValue, "1234567") || !slices.Contains(gotValue, "12345678") {
		t.Fatalf("gotValue %v, expectedValue only the value of at least 8 bytes", gotValue)
	}
}

func TestConfig_torznabTrackerMatchesTrackersWithNormalizeTracker(t *testing.T) {
	loadedConfig, err := loadTorznabConfig(t, `{
		"torznab": [{"url": "https://prowlarr.example.com/12/api?apikey=k1", "tracker": " Tracker A "}],
		"trackers": {"trackera": "https://tracker-a.example.com/announce/0000aaaa0000aaaa0000aaaa0000aaaa", "tracker-b": {"announce": ["https://tracker-b.example.com/x"], "aliases": ["Tracker B"]}}
	}`, trackerEnvironment())
	if err != nil {
		t.Fatal(err)
	}

	byIndexer, foundByIndexer := loadedConfig.TrackerFor(loadedConfig.Torznab[0].Tracker)
	byAlias, foundByAlias := loadedConfig.TrackerFor("TRACKERB")
	_, foundUnknown := loadedConfig.TrackerFor("tracker-c")

	if !foundByIndexer || byIndexer.Key != "trackera" || !foundByAlias || byAlias.Key != "tracker-b" || foundUnknown {
		t.Fatalf("gotValue %q/%v, %q/%v, unknown %v, expectedValue trackera, tracker-b and no match", byIndexer.Key, foundByIndexer, byAlias.Key, foundByAlias, foundUnknown)
	}
	if gotValue := loadedConfig.Torznab[0].Tracker; gotValue != "Tracker A" {
		t.Fatalf("gotValue %q, expectedValue the stored spelling unchanged (trimmed)", gotValue)
	}
}

// announceRuleCases sont refusées par la même règle au chargement (config) et à l'injection (sources).
func announceRuleCases() map[string]string {
	return map[string]string{
		"space inside":           "https://tracker-a.example.com/announce /x",
		"longer than 2048 bytes": "https://tracker-a.example.com/" + strings.Repeat("a", MaxAnnounceURLBytes),
		"no-break space":         "https://tracker-a.example.com/announce x",
		"newline":                "https://tracker-a.example.com/announce\nx",
		"invalid utf-8":          "https://tracker-a.example.com/\xff",
		"fragment":               "https://tracker-a.example.com/announce#x",
		"other scheme":           "wss://tracker-a.example.com/announce",
		"missing host":           "https:///announce",
		"empty":                  "",
	}
}

func TestValidAnnounceURL_refusesSpacesControlAndLongURLs(t *testing.T) {
	for name, announce := range announceRuleCases() {
		if err := ValidAnnounceURL(announce); !errors.Is(err, ErrInvalidAnnounceURL) || strings.Contains(err.Error(), "tracker-a") {
			t.Fatalf("%s: gotValue %v, expectedValue ErrInvalidAnnounceURL without the URL", name, err)
		}
	}
	for _, announce := range []string{"HTTPS://tracker-a.example.com/x", "udp://[2001:db8::1]:6969/announce", "https://t.example/" + fakePasskeyB} {
		if err := ValidAnnounceURL(announce); err != nil {
			t.Fatalf("gotValue %v, expectedValue accepted", err)
		}
	}
}

func TestConfig_trackerRefusesSpacesAndLongURLsAtLoad(t *testing.T) {
	for name, announce := range announceRuleCases() {
		if !utf8.ValidString(announce) {
			continue
		}
		_, err := loadTrackerConfig(t, `{"tracker-a": "`+jsonText(announce)+`"}`, nil)

		if err == nil || !strings.Contains(err.Error(), `trackers["tracker-a"]`) || strings.Contains(err.Error(), "tracker-a.example.com") {
			t.Fatalf("%s: gotValue %v, expectedValue a refusal naming the key only", name, err)
		}
	}
}

func TestConfig_trackerRefusesMoreThanTheAnnounceLimit(t *testing.T) {
	announces := make([]string, MaxAnnounceURLs+1)
	for announceIndex := range announces {
		announces[announceIndex] = fmt.Sprintf("https://t%d.example.com/a", announceIndex)
	}
	encoded, _ := json.Marshal(map[string]any{"tracker-a": map[string]any{"announce": announces}})

	if _, err := loadTrackerConfig(t, string(encoded), nil); err == nil {
		t.Fatal("gotValue accepted, expectedValue a refusal above MaxAnnounceURLs")
	}
}
