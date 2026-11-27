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
