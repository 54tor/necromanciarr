package main

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"

	"necromanciarr/internal/app"
	"necromanciarr/internal/config"
	"necromanciarr/internal/store"
)

// openConfigStore ouvre la base que la CLI utilisera pour ce dossier de configuration.
func openConfigStore(t *testing.T, configDir string) *store.Store {
	t.Helper()
	settings, err := config.Load([]string{"--config-dir", configDir}, os.LookupEnv, "")
	if err != nil {
		t.Fatal(err)
	}
	openedStore, err := store.Open(config.ResolveDBPath(settings))
	if err != nil {
		t.Fatal(err)
	}
	return openedStore
}

func createMaintenance(t *testing.T, openedStore *store.Store, kind store.ProposalKind, details any) int64 {
	t.Helper()
	encoded, err := json.Marshal(details)
	if err != nil {
		t.Fatal(err)
	}
	proposalID, err := openedStore.CreateProposal(context.Background(), store.Proposal{Kind: kind, Details: encoded}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return proposalID
}

func decisionsByID(t *testing.T, configDir string) map[int64]store.ProposalDecision {
	t.Helper()
	openedStore := openConfigStore(t, configDir)
	defer func() { _ = openedStore.Close() }()
	proposals, err := openedStore.ListProposals(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	decisions := map[int64]store.ProposalDecision{}
	for _, proposal := range proposals {
		decisions[proposal.ID] = proposal.Decision
	}
	return decisions
}

func TestCli_applyAliasesRefuseAWrongKindOrAClosedProposalWithoutChangingIt(t *testing.T) {
	configDir := writeDoctorConfig(t, `{"crossSeedMode": "off"}`)
	openedStore := openConfigStore(t, configDir)
	ctx := context.Background()
	repairID := createMaintenance(t, openedStore, store.ProposalRepair, app.RepairDetails{Status: app.RepairStatusRepairable, LinkPath: "/home/u/links/a.mkv", Actionable: true})
	dedupeDetails := app.DedupeDetails{TorrentPath: "/home/u/torrents/a.mkv", LibraryPath: "/home/u/library/a.mkv", Kind: "symlink", Actionable: true}
	rejectedID := createMaintenance(t, openedStore, store.ProposalDedupe, dedupeDetails)
	supersededID := createMaintenance(t, openedStore, store.ProposalDedupe, dedupeDetails)
	if err := openedStore.SetProposalDecision(ctx, rejectedID, store.ProposalRejected, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := openedStore.SetProposalDecision(ctx, supersededID, store.ProposalSuperseded, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := openedStore.Close(); err != nil {
		t.Fatal(err)
	}
	testCases := []struct {
		name             string
		args             []string
		proposalID       int64
		expectedDecision store.ProposalDecision
	}{
		{"repair id through dedupe apply", []string{"dedupe", "apply", fmtID(repairID)}, repairID, store.ProposalPending},
		{"dedupe id through repair apply", []string{"repair", "apply", fmtID(rejectedID)}, rejectedID, store.ProposalRejected},
		{"rejected", []string{"dedupe", "apply", fmtID(rejectedID)}, rejectedID, store.ProposalRejected},
		{"superseded", []string{"dedupe", "apply", fmtID(supersededID)}, supersededID, store.ProposalSuperseded},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			result := runCli(t, configDir, append(testCase.args, "--json")...)

			var decoded struct {
				Error struct{ Code string } `json:"error"`
			}
			decodeErr := json.Unmarshal([]byte(result.stdout), &decoded)
			gotDecision := decisionsByID(t, configDir)[testCase.proposalID]
			if result.exitCode != refusedExitCode || decodeErr != nil || decoded.Error.Code != errorCodeRefused || gotDecision != testCase.expectedDecision {
				t.Fatalf("gotValue exit %d %q decision %s, expectedValue refused and %s", result.exitCode, result.stdout, gotDecision, testCase.expectedDecision)
			}
		})
	}
}

func fmtID(proposalID int64) string { return strconv.FormatInt(proposalID, 10) }
