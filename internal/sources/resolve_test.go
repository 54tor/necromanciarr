package sources

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"necromanciarr/internal/config"
)

const (
	trackerAAnnounce = "https://tracker-a.example.com/announce/" + fakePasskey
	trackerBAnnounce = "udp://tracker-b.example.com:6969/announce?pk=" + fakePasskey
)

func configuredTrackers() []config.Tracker {
	return []config.Tracker{
		{Key: "tracker-a", Announce: []string{trackerAAnnounce}},
		{Key: "tracker-b", Announce: []string{trackerBAnnounce, "https://tracker-b.example.com/" + fakePasskey}, Aliases: []string{"TrackerB"}},
	}
}

func TestResolveAnnounce_configOverridesIndexerAnnounce(t *testing.T) {
	resolution := NewResolver(configuredTrackers()).Resolve("tracker-a", true)

	expectedTiers := [][]string{{trackerAAnnounce}}
	if resolution.Source != AnnounceConfig || resolution.TrackerKey != "tracker-a" ||
		!slices.EqualFunc(resolution.Tiers, expectedTiers, slices.Equal[[]string]) {
		t.Fatalf("gotValue %v, expectedValue the configured announce over the indexer one", resolution)
	}
}

func TestResolveAnnounce_fallsBackToSameInfohashIndexerTorrent(t *testing.T) {
	resolution := NewResolver(configuredTrackers()).Resolve("tracker-c", true)

	if resolution.Source != AnnounceIndexer || resolution.Tiers != nil || resolution.TrackerKey != "" {
		t.Fatalf("gotValue %v, expectedValue the sealed indexer torrent, no configured tiers", resolution)
	}
}

func TestResolveAnnounce_matchesNormalizedTrackerAndAliases(t *testing.T) {
	scenarios := map[string]string{
		"Tracker B":   "tracker-b",
		" trackerb ":  "tracker-b",
		"TRACKER-A":   "tracker-a",
		"Tracker -A":  "tracker-a",
		"tracker-b\t": "tracker-b",
	}
	for candidateTracker, expectedKey := range scenarios {
		t.Run(candidateTracker, func(t *testing.T) {
			resolution := NewResolver(configuredTrackers()).Resolve(candidateTracker, false)

			if resolution.Source != AnnounceConfig || resolution.TrackerKey != expectedKey {
				t.Fatalf("gotValue %v, expectedValue config entry %q", resolution, expectedKey)
			}
		})
	}
}

func TestResolveAnnounce_tiersAreACopyOfTheConfiguration(t *testing.T) {
	trackers := configuredTrackers()
	resolution := NewResolver(trackers).Resolve("tracker-b", false)

	resolution.Tiers[0][0] = "changed"

	if trackers[1].Announce[0] != trackerBAnnounce {
		t.Fatal("gotValue the configuration changed through the resolution, expectedValue a copy")
	}
}

func TestResolveAnnounce_noneWithoutConfigOrSealed(t *testing.T) {
	resolution := NewResolver(configuredTrackers()).Resolve("tracker-c", false)

	if resolution.Source != AnnounceNone || resolution.Tiers != nil {
		t.Fatalf("gotValue %v, expectedValue none", resolution)
	}
}

func TestResolveAnnounce_emptyTrackerUsesIndexerOnly(t *testing.T) {
	trackers := append(configuredTrackers(), config.Tracker{Key: " ", Announce: []string{trackerAAnnounce}})
	resolver := NewResolver(trackers)
	scenarios := map[string]struct {
		sealed         bool
		expectedSource AnnounceSource
	}{
		"sealed":     {true, AnnounceIndexer},
		"not sealed": {false, AnnounceNone},
	}
	for name, scenario := range scenarios {
		t.Run(name, func(t *testing.T) {
			for _, emptyTracker := range []string{"", "  ", "\t"} {
				resolution := resolver.Resolve(emptyTracker, scenario.sealed)
				if resolution.Source != scenario.expectedSource || resolution.Tiers != nil {
					t.Fatalf("gotValue %v, expectedValue %s", resolution, scenario.expectedSource)
				}
			}
		})
	}
}

func TestResolution_rendersNoTierURL(t *testing.T) {
	resolution := NewResolver(configuredTrackers()).Resolve("tracker-b", false)
	var logged bytes.Buffer
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("resolved", "resolution", resolution)
	encoded, err := json.Marshal(resolution)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	renderings := map[string]string{
		"%v":       fmt.Sprintf("%v", resolution),
		"%+v":      fmt.Sprintf("%+v", resolution),
		"%#v":      fmt.Sprintf("%#v", resolution),
		"%s":       fmt.Sprint(resolution),
		"pointer":  fmt.Sprintf("%v %+v %#v", &resolution, &resolution, &resolution),
		"in slice": fmt.Sprintf("%v %+v %#v", []Resolution{resolution}, []Resolution{resolution}, []Resolution{resolution}),
		"json":     string(encoded),
		"slog":     logged.String(),
	}
	for name, rendering := range renderings {
		if strings.Contains(rendering, fakePasskey) || strings.Contains(rendering, "/announce") {
			t.Fatalf("%s: gotValue %s, expectedValue no path or passkey", name, rendering)
		}
		if !strings.Contains(rendering, "udp://tracker-b.example.com:6969/***") || !strings.Contains(rendering, "tracker-b") {
			t.Fatalf("%s: gotValue %s, expectedValue the redacted announce and the key", name, rendering)
		}
	}
}
