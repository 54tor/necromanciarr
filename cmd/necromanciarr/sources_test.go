package main

import (
	"encoding/json"
	"strings"
	"testing"

	"necromanciarr/internal/app"
	"necromanciarr/internal/pipeline"
	"necromanciarr/internal/store"
)

func sampleSourceRows() []app.SourceRow {
	return []app.SourceRow{
		{Order: 1, Source: "provider", Type: app.SourceTypeProvider, State: app.SourceStateOK},
		{
			Order: 2, Source: "torznab:a", Type: app.SourceTypeTorznab, Tracker: "tracker-a", State: app.SourceStateBlocked,
			Queries: &app.SourceUsage{Used: 3, Cap: 200}, Downloads: &app.SourceUsage{Used: 1, Cap: 50}, LastError: "indexer_access_denied",
		},
	}
}

var failedSourceTest = app.SourceTestResult{Source: "torznab:a", Result: app.SourceTestFailed, Error: "indexer_unavailable"}

func TestCLI_proposalsListFiltersBySource(t *testing.T) {
	configDir := writeDoctorConfig(t, `{}`)
	fake := &fakeService{proposals: []app.Proposal{
		{ID: 1, Kind: store.ProposalFull, Decision: store.ProposalPending, Current: true, CandidateName: "From.Provider", Details: pipeline.ProposalDetails{Source: "provider"}},
		{ID: 2, Kind: store.ProposalFull, Decision: store.ProposalPending, Current: true, CandidateName: "From.Indexer", Details: pipeline.ProposalDetails{Source: "torznab:a"}},
	}}
	useFakeService(t, fake)

	result := runCli(t, configDir, "proposals", "list", "--source", "torznab:a", "--json")

	var gotValue []app.Proposal
	if err := json.Unmarshal([]byte(result.stdout), &gotValue); err != nil || result.exitCode != successExitCode {
		t.Fatalf("valeurObtenue exit %d, stdout %q (%v)", result.exitCode, result.stdout, err)
	}
	if fake.lastFilter.Source != "torznab:a" || len(gotValue) != 1 || gotValue[0].ID != 2 || gotValue[0].Details.Source != "torznab:a" {
		t.Fatalf("valeurObtenue filter %+v, proposals %+v, valeurAttendue only the torznab:a proposal", fake.lastFilter, gotValue)
	}
	text := runCli(t, configDir, "proposals", "list", "--source", "torznab:a")
	if strings.Contains(text.stdout, "From.Provider") || !strings.Contains(text.stdout, "From.Indexer") || !strings.Contains(text.stdout, "torznab:a") {
		t.Fatalf("valeurObtenue %q, valeurAttendue the indexer proposal with its source", text.stdout)
	}
}

func TestCLI_sourcesListPrintsTranslatedCodesAndStableJSON(t *testing.T) {
	configDir := writeDoctorConfig(t, `{}`)
	useFakeService(t, &fakeService{sourceRows: sampleSourceRows()})

	english := runCli(t, configDir, "sources", "list")
	french := runCli(t, configDir, "sources", "list", "--language", "fr")
	encoded := runCli(t, configDir, "sources", "list", "--json")

	for _, expected := range []string{"provider", "torznab:a", "tracker-a", "3/200", "1/50", "n/a", "blocked", "the indexer denies access"} {
		if !strings.Contains(english.stdout, expected) {
			t.Fatalf("valeurObtenue %q, valeurAttendue %q", english.stdout, expected)
		}
	}
	if !strings.Contains(french.stdout, "l'indexeur refuse l'accès") || english.exitCode != successExitCode {
		t.Fatalf("valeurObtenue %q (exit %d), valeurAttendue the French label", french.stdout, english.exitCode)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(encoded.stdout), &rows); err != nil || len(rows) != 2 {
		t.Fatalf("valeurObtenue %q (%v)", encoded.stdout, err)
	}
	for _, key := range []string{"order", "source", "type", "tracker", "state", "queries", "downloads", "last_error"} {
		if _, found := rows[0][key]; !found {
			t.Fatalf("valeurObtenue %v, valeurAttendue the key %q", rows[0], key)
		}
	}
}

func TestCLI_sourcesTestExitsOneOnAFailedTestWithTheTranslatedCode(t *testing.T) {
	configDir := writeDoctorConfig(t, `{}`)
	fake := &fakeService{sourceTest: &failedSourceTest}
	useFakeService(t, fake)

	result := runCli(t, configDir, "sources", "test", "a")

	if result.exitCode != failureExitCode || !fake.called("TestSource a") || !strings.Contains(result.stdout, "indexer unavailable") {
		t.Fatalf("valeurObtenue exit %d, calls %v, stdout %q, valeurAttendue exit 1 and the translated reason", result.exitCode, fake.calls, result.stdout)
	}
}

func TestCLI_sourcesTestRequiresAName(t *testing.T) {
	configDir := writeDoctorConfig(t, `{}`)
	useFakeService(t, &fakeService{})

	if result := runCli(t, configDir, "sources", "test"); result.exitCode != usageExitCode {
		t.Fatalf("valeurObtenue exit %d, valeurAttendue usage", result.exitCode)
	}
}
