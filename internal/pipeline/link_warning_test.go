package pipeline

import (
	"context"
	"slices"
	"testing"

	"necromanciarr/internal/i18n"
	"necromanciarr/internal/linker"
	"necromanciarr/internal/store"
)

func TestFailureWarning_namesTheCauseAndHasAnActionKeyForLinkKindFailures(t *testing.T) {
	expectedKeys := map[string]i18n.Key{
		ReasonCrossDevice:           i18n.KeyWarningApplicationFailedCrossDevice,
		ReasonNoLinkDirOnSameDevice: i18n.KeyWarningApplicationFailedNoLinkDir,
		ReasonLinkKindRefused:       i18n.KeyWarningApplicationFailedKindRefused,
	}
	for reason, expectedKey := range expectedKeys {
		t.Run(reason, func(t *testing.T) {
			gotValue := failureWarning(reason)

			if gotValue.Key != expectedKey || gotValue.Params["reason"] != reason {
				t.Fatalf("gotValue %+v, expectedValue key %q and the reason as parameter", gotValue, expectedKey)
			}
		})
	}
	gotValue := failureWarning(ReasonLinkFailed)
	if gotValue.Key != i18n.KeyWarningApplicationFailed || gotValue.Params["reason"] != ReasonLinkFailed {
		t.Fatalf("gotValue %+v, expectedValue the generic warning for other reasons", gotValue)
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

	fixture.mustRunStage(StageLink)

	proposals, err := fixture.store.ListProposals(context.Background(), store.ProposalFailed)
	if err != nil || len(proposals) != 1 || !slices.Contains(warningKeys(proposals[0].Warnings), i18n.KeyWarningApplicationFailedKindRefused) {
		t.Fatalf("gotValue %+v %v, expectedValue the link kind refused warning", proposals, err)
	}
}
