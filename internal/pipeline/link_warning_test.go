package pipeline

import (
	"context"
	"slices"
	"strings"
	"testing"

	"necromanciarr/internal/linker"
	"necromanciarr/internal/store"
)

func TestFailureWarning_namesTheCauseAndTheOperatorActionForLinkKindFailures(t *testing.T) {
	for _, reason := range []string{ReasonCrossDevice, ReasonNoLinkDirOnSameDevice, ReasonLinkKindRefused} {
		t.Run(reason, func(t *testing.T) {
			gotValue := failureWarning(reason)

			namesTheAction := strings.Contains(gotValue, "linkType") && strings.Contains(gotValue, reason)
			if !namesTheAction {
				t.Fatalf("gotValue %q, expectedValue the reason and the linkType action", gotValue)
			}
		})
	}
	if gotValue := failureWarning(ReasonLinkFailed); gotValue != "application failed: "+ReasonLinkFailed {
		t.Fatalf("gotValue %q, expectedValue the generic warning for other reasons", gotValue)
	}
}

func TestLinkStage_refusedKindLeavesAnExplicitWarningOnTheFailedProposal(t *testing.T) {
	fixture := newApplyFixture(t)
	fixture.probe = func(string) (linker.Capabilities, error) {
		return linker.Capabilities{Symlink: true, Reasons: map[linker.Kind]error{linker.Hardlink: linker.ErrLinkRefused}}, nil
	}
	candidate := fixture.addCandidate("Movie.2020", "", movieOnly())
	proposal := fixture.verifiedProposal(candidate, store.ProposalFull)
	fixture.accept(proposal.ID)
	expectedValue := failureWarning(ReasonLinkKindRefused)

	fixture.mustRunStage(StageLink)

	proposals, err := fixture.store.ListProposals(context.Background(), store.ProposalFailed)
	if err != nil || len(proposals) != 1 || !slices.Contains(proposals[0].Warnings, expectedValue) {
		t.Fatalf("gotValue %+v %v, expectedValue the warning %q", proposals, err, expectedValue)
	}
}
