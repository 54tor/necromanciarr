package app

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"necromanciarr/internal/config"
	"necromanciarr/internal/pipeline"
	"necromanciarr/internal/sources"
)

func sourcesSettings(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		ConfigDir: t.TempDir(), ReadBytesPerSecond: 1 << 20, TieBreakReadBytes: 1, LinkType: "hardlink", CrossSeedMode: "off",
		LibraryAPI: "https://library.example.com", LibraryProviderToken: "token-0000",
	}
}

func torznabEntry(name string) config.TorznabIndexer {
	return config.TorznabIndexer{
		URL: "https://" + name + ".example.com/api?apikey=fake-key-0000", Name: name, MinInterval: 3 * time.Second, Timeout: 30 * time.Second,
		MaxQueriesPerRun: 30, MaxDownloadsPerRun: 10, MaxQueriesPerDay: 200, MaxDownloadsPerDay: 50,
	}
}

func TestBuildDeps_withoutIndexerTheChainHoldsTheProviderAlone(t *testing.T) {
	deps, err := buildDeps(context.Background(), sourcesSettings(t), openTestStore(t))
	if err != nil {
		t.Fatal(err)
	}

	chain, isChain := deps.Stages.Sources.(*sources.Chain)
	if !isChain || !slices.Equal(chain.Order(), []sources.ID{sources.ProviderID}) {
		t.Fatalf("valeurObtenue %T, valeurAttendue a chain with the provider only", deps.Stages.Sources)
	}
}

func TestBuildDeps_buildsTheChainFromSourceOrderWithProviderFirst(t *testing.T) {
	settings := sourcesSettings(t)
	settings.Torznab = []config.TorznabIndexer{torznabEntry("a"), torznabEntry("b")}
	settings.SourceOrder = []string{"provider", "torznab:b", "torznab:a"}

	deps, err := buildDeps(context.Background(), settings, openTestStore(t))
	if err != nil {
		t.Fatal(err)
	}

	valeurAttendue := []sources.ID{"provider", "torznab:b", "torznab:a"}
	if valeurObtenue := deps.Stages.Sources.(*sources.Chain).Order(); !slices.Equal(valeurObtenue, valeurAttendue) {
		t.Fatalf("valeurObtenue %v, valeurAttendue %v", valeurObtenue, valeurAttendue)
	}
}

func TestBuildDeps_withoutSourceOrderTheIndexersFollowTheConfigurationOrder(t *testing.T) {
	settings := sourcesSettings(t)
	settings.Torznab = []config.TorznabIndexer{torznabEntry("a"), torznabEntry("b")}

	deps, err := buildDeps(context.Background(), settings, openTestStore(t))
	if err != nil {
		t.Fatal(err)
	}

	valeurAttendue := []sources.ID{"provider", "torznab:a", "torznab:b"}
	if valeurObtenue := deps.Stages.Sources.(*sources.Chain).Order(); !slices.Equal(valeurObtenue, valeurAttendue) {
		t.Fatalf("valeurObtenue %v, valeurAttendue %v", valeurObtenue, valeurAttendue)
	}
}

func TestIndexerSources_mapsTheConfiguredLimits(t *testing.T) {
	settings := sourcesSettings(t)
	entry := torznabEntry("a")
	entry.MinInterval, entry.MaxQueriesPerRun, entry.MaxDownloadsPerRun, entry.MaxQueriesPerDay, entry.MaxDownloadsPerDay = 5*time.Second, 1, 2, 3, 4
	settings.Torznab = []config.TorznabIndexer{entry}

	_, _, limits, err := indexerSources(settings)
	if err != nil {
		t.Fatal(err)
	}

	valeurAttendue := sources.Limits{MinInterval: 5 * time.Second, MaxQueriesPerRun: 1, MaxDownloadsPerRun: 2, MaxQueriesPerDay: 3, MaxDownloadsPerDay: 4}
	if valeurObtenue := limits["torznab:a"]; valeurObtenue != valeurAttendue {
		t.Fatalf("valeurObtenue %+v, valeurAttendue %+v", valeurObtenue, valeurAttendue)
	}
}

func TestBuildDeps_indexersWithoutLibraryApiKeepMatchAndVerifyNotConfigured(t *testing.T) {
	settings := sourcesSettings(t)
	settings.LibraryAPI, settings.LibraryProviderToken = "", ""
	settings.Torznab = []config.TorznabIndexer{torznabEntry("a")}
	deps, err := buildDeps(context.Background(), settings, openTestStore(t))
	if err != nil {
		t.Fatal(err)
	}
	if deps.Stages.Sources != nil {
		t.Fatalf("valeurObtenue %T, valeurAttendue no chain without the provider (B12 adds indexer-only search)", deps.Stages.Sources)
	}

	for _, stage := range []pipeline.Stage{pipeline.StageMatch, pipeline.StageVerify} {
		if _, err := New(deps).RunStage(context.Background(), stage); !errors.Is(err, ErrNotConfigured) {
			t.Fatalf("stage %s: valeurObtenue %v, valeurAttendue ErrNotConfigured", stage, err)
		}
	}
}

func TestIndexerTrackers_keepsOnlyIndexersWithATrackerAsWritten(t *testing.T) {
	settings := sourcesSettings(t)
	withTracker, withoutTracker := torznabEntry("a"), torznabEntry("b")
	withTracker.Tracker = "Tracker A"
	settings.Torznab = []config.TorznabIndexer{withTracker, withoutTracker}

	valeurObtenue := indexerTrackers(settings)

	if len(valeurObtenue) != 1 || valeurObtenue["torznab:a"] != "Tracker A" {
		t.Fatalf("valeurObtenue %v, valeurAttendue only torznab:a with its tracker as written", valeurObtenue)
	}
}
