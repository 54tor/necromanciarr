package pipeline

import (
	"context"
	"path/filepath"
	"testing"

	"necromanciarr/internal/linker"
	"necromanciarr/internal/store"
)

// runLinkWithMissingLibraryRoot ajoute aux racines de bibliothèque une racine absente (volume non monté) : le
// registre refuse alors toute racine d'écriture.
func runLinkWithMissingLibraryRoot(t *testing.T, fixture *applyFixture) (Result, store.Proposal) {
	t.Helper()
	candidate := fixture.addCandidate("Movie.2020", "", movieOnly())
	proposal := fixture.verifiedProposal(candidate, store.ProposalFull)
	fixture.accept(proposal.ID)
	deps := fixture.applyDeps()
	deps.LibraryRoots = append(deps.LibraryRoots, filepath.Join(t.TempDir(), "unmounted"))

	result, err := Run(context.Background(), StageLink, deps)
	if err != nil {
		t.Fatal(err)
	}
	return result, proposal
}

func TestLink_namesAMissingLibraryRootWhenTheProbeIsRefused(t *testing.T) {
	fixture := newApplyFixture(t)

	result, proposal := runLinkWithMissingLibraryRoot(t, fixture)

	if result.Reasons[ReasonLibraryRootUnavailable] != 1 || fixture.decisionOf(proposal.ID) != store.ProposalFailed {
		t.Fatalf("expectedValue a failed proposal with %s, gotValue %+v", ReasonLibraryRootUnavailable, result)
	}
	if entries := linkDirEntries(t, fixture.linkDir); len(entries) != 0 {
		t.Fatalf("expectedValue nothing created, gotValue %v", entries)
	}
}

func TestLink_namesAMissingLibraryRootWhenTheWriteRootIsRefused(t *testing.T) {
	fixture := newApplyFixture(t)
	fixture.probe = func(string) (linker.Capabilities, error) { return linker.Capabilities{Hardlink: true}, nil }

	result, proposal := runLinkWithMissingLibraryRoot(t, fixture)

	if result.Reasons[ReasonLibraryRootUnavailable] != 1 || fixture.decisionOf(proposal.ID) != store.ProposalFailed {
		t.Fatalf("expectedValue a failed proposal with %s, gotValue %+v", ReasonLibraryRootUnavailable, result)
	}
}
