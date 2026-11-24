package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"necromanciarr/internal/store"
)

func TestProposalUpdatedAt_isTheLatestOfCreationDecisionAndStep(t *testing.T) {
	created := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	decided := created.Add(time.Hour)
	stepped := decided.Add(time.Hour)
	cases := []struct {
		name     string
		proposal store.Proposal
		progress store.ProposalProgress
		expected string
	}{
		{"created only", store.Proposal{CreatedAt: created}, store.ProposalProgress{}, "2026-01-01T10:00:00Z"},
		{"decision after creation", store.Proposal{CreatedAt: created, DecidedAt: decided}, store.ProposalProgress{}, "2026-01-01T11:00:00Z"},
		{"step after decision", store.Proposal{CreatedAt: created, DecidedAt: decided}, store.ProposalProgress{UpdatedAt: stepped}, "2026-01-01T12:00:00Z"},
		{"nothing known", store.Proposal{}, store.ProposalProgress{}, ""},
	}
	for _, scenario := range cases {
		t.Run(scenario.name, func(t *testing.T) {
			gotValue := proposalUpdatedAt(scenario.proposal, scenario.progress)
			if gotValue != scenario.expected {
				t.Fatalf("gotValue %q, expectedValue %q", gotValue, scenario.expected)
			}
		})
	}
}

func TestProposalJSON_carriesUpdatedAtAsAStableField(t *testing.T) {
	view := Proposal{UpdatedAt: "2026-01-01T10:00:00Z"}

	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(encoded), `"updated_at":"2026-01-01T10:00:00Z"`) {
		t.Fatalf("gotValue %s, expectedValue an updated_at field", encoded)
	}
}
