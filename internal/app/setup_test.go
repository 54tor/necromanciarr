package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"necromanciarr/internal/arr"
	"necromanciarr/internal/config"
	"necromanciarr/internal/doctor"
)

const candidateAPIKey = "candidate-key-123"

type countingPinger struct {
	err   error
	calls *int
}

func (pinger countingPinger) Ping(context.Context) error {
	*pinger.calls++
	return pinger.err
}

func serviceWithPinger(pingError error) (Service, *int, *config.ArrService) {
	calls := 0
	var seen config.ArrService
	deps := doctor.Deps{NewPinger: func(candidate config.ArrService) doctor.Pinger {
		seen = candidate
		return countingPinger{err: pingError, calls: &calls}
	}}
	return New(Deps{DoctorDeps: deps}), &calls, &seen
}

func TestTestCandidate_arrRejectedKeyReturnsFailWithKeyHint(t *testing.T) {
	service, _, _ := serviceWithPinger(arr.ErrAccessDenied)

	valeurObtenue, err := service.TestCandidate(context.Background(), SetupCandidate{Kind: "radarr", URL: "http://example.com:7878", APIKey: candidateAPIKey})

	if err != nil || valeurObtenue.Status != doctor.Fail || valeurObtenue.Code != doctor.CodeArrKeyRejected || valeurObtenue.Hint == "" {
		t.Fatalf("valeurObtenue %+v %v, valeurAttendue Fail with CodeArrKeyRejected and a hint", valeurObtenue, err)
	}
}

func TestTestCandidate_arrOkReturnsOK(t *testing.T) {
	service, _, seen := serviceWithPinger(nil)

	valeurObtenue, err := service.TestCandidate(context.Background(), SetupCandidate{Kind: "sonarr", URL: "http://example.com:8989", APIKey: candidateAPIKey})

	isExpected := err == nil && valeurObtenue.Status == doctor.OK && seen.Kind == "sonarr" && seen.BaseURL == "http://example.com:8989" && seen.APIKey == candidateAPIKey
	if !isExpected {
		t.Fatalf("valeurObtenue %+v %v seen %+v, valeurAttendue OK for the sonarr candidate", valeurObtenue, err, seen)
	}
}

func TestTestCandidate_emptyAPIKeyOrBadURLIsUsageErrorWithoutNetwork(t *testing.T) {
	service, calls, _ := serviceWithPinger(nil)
	candidates := []SetupCandidate{
		{Kind: "radarr", URL: "http://example.com:7878", APIKey: ""},
		{Kind: "radarr", URL: "ftp://example.com", APIKey: candidateAPIKey},
		{Kind: "radarr", URL: "example.com:7878", APIKey: candidateAPIKey},
		{Kind: "qbittorrent", URL: "", Username: "alice"},
	}
	for _, candidate := range candidates {
		_, err := service.TestCandidate(context.Background(), candidate)
		if !errors.Is(err, config.ErrUsage) || strings.Contains(err.Error(), candidateAPIKey) {
			t.Fatalf("candidate %+v: valeurObtenue %v, valeurAttendue a usage error without the key", candidate, err)
		}
	}
	if *calls != 0 {
		t.Fatalf("valeurObtenue %d pings, valeurAttendue none", *calls)
	}
}

func TestTestCandidate_unknownKindIsUsageError(t *testing.T) {
	service, calls, _ := serviceWithPinger(nil)

	_, err := service.TestCandidate(context.Background(), SetupCandidate{Kind: "lidarr", URL: "http://example.com", APIKey: candidateAPIKey})

	if !errors.Is(err, config.ErrUsage) || *calls != 0 {
		t.Fatalf("valeurObtenue %v with %d pings, valeurAttendue a usage error and no ping", err, *calls)
	}
}

type fixedTorrentClient struct{}

func (fixedTorrentClient) Version(context.Context) (string, error) { return "5.0", nil }
func (fixedTorrentClient) CountTorrents(context.Context) (int, bool, error) {
	return 3, false, nil
}

func TestTestCandidate_qbittorrentUsesTheCredentialsOfTheCandidate(t *testing.T) {
	var seen config.TorrentClient
	deps := doctor.Deps{NewTorrentClient: func(client config.TorrentClient) (doctor.TorrentClient, error) {
		seen = client
		return fixedTorrentClient{}, nil
	}}
	service := New(Deps{DoctorDeps: deps})

	valeurObtenue, err := service.TestCandidate(context.Background(), SetupCandidate{Kind: "qbittorrent", URL: "http://example.com:8080", Username: "alice", Password: "pw"})

	isExpected := err == nil && valeurObtenue.Status == doctor.OK && seen.BaseURL == "http://example.com:8080" && seen.User == "alice" && seen.Password == "pw" && !seen.ReadOnly
	if !isExpected {
		t.Fatalf("valeurObtenue %+v %v seen %+v, valeurAttendue OK with the candidate credentials", valeurObtenue, err, seen)
	}
}
