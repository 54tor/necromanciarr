package doctor

import (
	"context"
	"testing"
	"time"

	"necromanciarr/internal/config"
)

const unreachableLocalURL = "http://127.0.0.1:9"

func TestCheckArrService_failsWithoutPanickingWhenNoDepsAreInjected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	service := config.ArrService{Kind: "radarr", BaseURL: unreachableLocalURL, APIKey: "dummy-api-key"}

	valeurObtenue := CheckArrService(ctx, service, Deps{}).Status

	if valeurAttendue := Fail; valeurObtenue != valeurAttendue {
		t.Fatalf("status = %q, want %q", valeurObtenue, valeurAttendue)
	}
}

func TestCheckTorrentClient_failsWithoutPanickingWhenNoDepsAreInjected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := config.TorrentClient{Type: "qbittorrent", BaseURL: unreachableLocalURL, User: "alice", Password: "dummy-password"}

	valeurObtenue := CheckTorrentClient(ctx, client, Deps{}).Status

	if valeurAttendue := Fail; valeurObtenue != valeurAttendue {
		t.Fatalf("status = %q, want %q", valeurObtenue, valeurAttendue)
	}
}

func TestCheckTorznabIndexer_failsWithoutPanickingWhenNoDepsAreInjected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	indexer := config.TorznabIndexer{Name: "alpha", URL: unreachableLocalURL + "/7/api?apikey=dummy-api-key", Timeout: 2 * time.Second}

	valeurObtenue := CheckTorznabIndexer(ctx, indexer, Deps{}).Status

	if valeurAttendue := Fail; valeurObtenue != valeurAttendue {
		t.Fatalf("status = %q, want %q", valeurObtenue, valeurAttendue)
	}
}

func TestCheckTorznabIndexer_ignoresTheCapsCacheAndTheSourceOrder(t *testing.T) {
	calls := 0
	deps := Deps{
		NewCapsReader: func(config.TorznabIndexer) (CapsReader, error) {
			calls++
			return fakeCaps{caps: modesOf("movie")}, nil
		},
		CapsCache: NewCapsCache(time.Hour, nil),
	}
	indexer := config.TorznabIndexer{Name: "alpha", URL: "http://example.com/7/api?apikey=dummy-api-key"}

	first := CheckTorznabIndexer(context.Background(), indexer, deps)
	second := CheckTorznabIndexer(context.Background(), indexer, deps)

	if first.Status != OK || second.Status != OK || calls != 2 {
		t.Fatalf("valeurObtenue %q/%q with %d caps reads, valeurAttendue ok/ok with 2", first.Status, second.Status, calls)
	}
}
