package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"necromanciarr/internal/libraryprovider"
	"necromanciarr/internal/store"
)

// openPrimaries renvoie les propositions principales ouvertes (pending ou accepted), celles que l'index du store borne.
func (fixture *verifyFixture) openPrimaries() []store.Proposal {
	fixture.t.Helper()
	var primaries []store.Proposal
	for _, proposal := range fixture.proposals() {
		if proposal.Primary && isOpenDecision(proposal.Decision) {
			primaries = append(primaries, proposal)
		}
	}
	return primaries
}

func (fixture *verifyFixture) proposalOf(candidateID int64, kind store.ProposalKind) store.Proposal {
	fixture.t.Helper()
	return singleProposalOfKind(fixture.t, fixture.proposalsOf(candidateID), kind)
}

func TestVerifyStage_partialThenFullInALaterRunElectsTheFull(t *testing.T) {
	fixture := newVerifyFixture(t)
	partial := fixture.addCandidate("Movie.B", "tracker-b", movieWithAnnex(patternBytes(50, nfoSize)))
	fixture.mustRun()
	if primaries := fixture.openPrimaries(); len(primaries) != 0 {
		t.Fatalf("gotValue %+v, expectedValue no primary without a full proposal", primaries)
	}

	complete := fixture.addCandidate("Movie.A", "tracker-a", movieOnly())
	fixture.mustRun()

	primaries := fixture.openPrimaries()
	expectedValue := fixture.proposalOf(complete.ID, store.ProposalFull).ID
	if len(primaries) != 1 || primaries[0].ID != expectedValue || fixture.proposalOf(partial.ID, store.ProposalPartialAnnex).Primary {
		t.Fatalf("gotValue %+v, expectedValue only the full proposal %d", primaries, expectedValue)
	}
}

func TestVerifyStage_groupWithoutFullHasNoPrimary(t *testing.T) {
	fixture := newVerifyFixture(t)
	fixture.addCandidate("Movie.A", "tracker-a", movieWithAnnex(patternBytes(50, nfoSize)))
	fixture.addCandidate("Movie.B", "tracker-b", movieWithAnnex(patternBytes(60, nfoSize)))

	fixture.mustRun()

	if primaries := fixture.openPrimaries(); len(primaries) != 0 {
		t.Fatalf("gotValue %+v, expectedValue no primary", primaries)
	}
	singleProposalOfKind(t, fixture.proposals(), store.ProposalConflict)
}

func TestVerifyStage_mergingGroupsKeepsExactlyOnePrimary(t *testing.T) {
	fixture := newVerifyFixture(t)
	other := torrentEntry{path: "other.mkv", content: patternBytes(7, 48), localName: "other.mkv"}
	first := fixture.addCandidate("Movie.A", "", movieOnly())
	fixture.addCandidate("Other.B", "", []torrentEntry{other})
	fixture.mustRun()
	if primaries := fixture.openPrimaries(); len(primaries) != 2 {
		t.Fatalf("gotValue %d primaries, expectedValue one per separate group", len(primaries))
	}

	fixture.addCandidate("Pack.C", "", append(movieOnly(), other))
	fixture.mustRun()

	primaries := fixture.openPrimaries()
	if len(primaries) != 1 || primaries[0].CandidateID != first.ID || primaries[0].GroupID != fixture.candidate(first.Infohash).GroupID {
		t.Fatalf("gotValue %+v, expectedValue one primary for %d in the merged group", primaries, first.ID)
	}
}

func TestVerifyStage_operatorPrimaryStaysSticky(t *testing.T) {
	fixture := newVerifyFixture(t)
	fixture.addCandidate("Movie.A", "tracker-a", movieOnly())
	chosen := fixture.addCandidate("Movie.B", "tracker-b", movieOnly())
	fixture.mustRun()
	chosenProposal := fixture.proposalOf(chosen.ID, store.ProposalFull)
	if err := fixture.store.SetGroupPrimary(context.Background(), chosenProposal.GroupID, chosenProposal.ID, store.PrimaryOperator); err != nil {
		t.Fatal(err)
	}

	fixture.addCandidate("Movie.C", "tracker-c", movieOnly())
	fixture.mustRun()

	primaries := fixture.openPrimaries()
	if len(primaries) != 1 || primaries[0].ID != chosenProposal.ID || primaries[0].PrimarySource != store.PrimaryOperator {
		t.Fatalf("gotValue %+v, expectedValue the operator choice %d kept", primaries, chosenProposal.ID)
	}
}

// Disposition : movie.mkv [0,40), movie.nfo [40,60) sur les pièces 2 et 3, puis éventuellement sub.srt [60,64).
func nfoOverTwoPieces(withTrailingSubtitle bool) ([]torrentEntry, []byte) {
	nfo := []byte("line one\r\nline two\r\n")
	entries := []torrentEntry{
		{path: "movie.mkv", content: patternBytes(1, movieSize), localName: "movie.mkv"},
		{path: "movie.nfo", content: nfo},
	}
	if withTrailingSubtitle {
		entries = append(entries, torrentEntry{path: "sub.srt", content: patternBytes(80, 4)})
	}
	return entries, nfo
}

func TestVerifyStage_nfoSharingAPieceWithAnAbsentFileStaysMissing(t *testing.T) {
	fixture := newVerifyFixture(t)
	entries, nfo := nfoOverTwoPieces(true)
	candidate := fixture.addCandidate("Movie.2020", "", entries)
	fixture.fetcher.nfos[candidate.Infohash] = []libraryprovider.Nfo{{Data: nfo}}

	fixture.mustRun()

	verifiedFiles, err := fixture.store.ListVerifiedFiles(context.Background(), candidate.ID)
	if err != nil || len(verifiedFiles) != 0 {
		t.Fatalf("gotValue %+v %v, expectedValue nothing stored: piece 3 cannot be tested", verifiedFiles, err)
	}
}

func TestVerifyStage_nfoWhoseEveryPieceIsTestableIsAccepted(t *testing.T) {
	fixture := newVerifyFixture(t)
	entries, nfo := nfoOverTwoPieces(false)
	candidate := fixture.addCandidate("Movie.2020", "", entries)
	fixture.fetcher.nfos[candidate.Infohash] = []libraryprovider.Nfo{{Data: nfo}}

	fixture.mustRun()

	verifiedFiles, err := fixture.store.ListVerifiedFiles(context.Background(), candidate.ID)
	if err != nil || len(verifiedFiles) != 1 {
		t.Fatalf("gotValue %+v %v, expectedValue the nfo stored", verifiedFiles, err)
	}
	fixture.proposalOf(candidate.ID, store.ProposalFull)
}

func TestVerifyStage_presentAnnexWithAWrongHashIsExcluded(t *testing.T) {
	fixture := newVerifyFixture(t)
	candidate := fixture.addCandidate("Movie.2020", "", []torrentEntry{
		{path: "movie.mkv", content: patternBytes(1, 32), localName: "movie.mkv"},
		{path: "sub.srt", content: patternBytes(90, 16), localName: "sub.srt", localContent: patternBytes(91, 16)},
	})

	fixture.mustRun()

	proposal := fixture.proposalOf(candidate.ID, store.ProposalPartialAnnex)
	details := decodeDetails(t, proposal)
	expectedValue := []ExcludedFile{{TorrentPath: "sub.srt", Reason: ExclusionAnnexHashFailed}}
	if !slices.Equal(details.ExcludedFiles, expectedValue) || len(proposal.Warnings) != 2 {
		t.Fatalf("gotValue %+v warnings %v, expectedValue sub.srt excluded with a warning", details, proposal.Warnings)
	}
	assignment, _, err := fixture.store.LoadVerifiedAssignment(context.Background(), candidate.ID)
	if err != nil || len(assignment.Files) != 1 || assignment.Files[0].TorrentFileIndex != 0 {
		t.Fatalf("gotValue %+v %v, expectedValue only movie.mkv in the proven assignment", assignment, err)
	}
}

func TestVerifyStage_reverifiedCandidateGetsAFreshProposalAndKeepsHistory(t *testing.T) {
	fixture := newVerifyFixture(t)
	candidate := fixture.addCandidate("Movie.2020", "", movieOnly())
	fixture.mustRun()
	rejected := fixture.proposalOf(candidate.ID, store.ProposalFull)
	if err := fixture.store.SetProposalDecision(context.Background(), rejected.ID, store.ProposalRejected, fixedNow); err != nil {
		t.Fatal(err)
	}
	changedTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(fixture.root, "movie.mkv"), changedTime, changedTime); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.MarkChangedCandidatesStale(context.Background(), fixedNow); err != nil {
		t.Fatal(err)
	}
	fixture.addCandidate("Movie.2020", "", movieOnly())

	fixture.mustRun()

	var pending, kept int
	for _, proposal := range fixture.proposalsOf(candidate.ID) {
		switch {
		case proposal.Kind == store.ProposalFull && proposal.Decision == store.ProposalPending && proposal.Generation > rejected.Generation:
			pending++
		case proposal.ID == rejected.ID && proposal.Decision == store.ProposalRejected:
			kept++
		}
	}
	if pending != 1 || kept != 1 {
		t.Fatalf("gotValue %+v, expectedValue one fresh pending full and the rejected one kept", fixture.proposalsOf(candidate.ID))
	}
}

func TestVerifyStage_decidedConflictIsReplacedWhenTheGroupGrows(t *testing.T) {
	fixture := newVerifyFixture(t)
	addCompetingPair(fixture)
	fixture.mustRun()
	original := singleProposalOfKind(t, fixture.proposals(), store.ProposalConflict)
	if err := fixture.store.SetProposalDecision(context.Background(), original.ID, store.ProposalRejected, fixedNow); err != nil {
		t.Fatal(err)
	}

	fixture.addCandidate("Movie.C", "tracker-c", movieOnly())
	fixture.mustRun()

	var replacement []store.Proposal
	for _, proposal := range fixture.proposals() {
		if proposal.ID == original.ID && proposal.Decision != store.ProposalSuperseded {
			t.Fatalf("gotValue %+v, expectedValue the rejected conflict superseded", proposal)
		}
		if proposal.Kind == store.ProposalConflict && proposal.Decision == store.ProposalPending {
			replacement = append(replacement, proposal)
		}
	}
	if len(replacement) != 1 || len(decodeDetails(t, replacement[0]).Candidates) != 3 {
		t.Fatalf("gotValue %+v, expectedValue one pending conflict listing the three torrents", replacement)
	}
}

func TestVerifyStage_providerOversizeErrorFailsAsTooLarge(t *testing.T) {
	fixture := newVerifyFixture(t)
	candidate := fixture.addCandidate("Movie.2020", "", movieOnly())
	fixture.fetcher.torrentErrs[candidate.Infohash] = libraryprovider.ErrResponseTooLarge

	result := fixture.mustRun()

	if result.Reasons[ReasonTorrentTooLarge] != 1 || fixture.candidate(candidate.Infohash).State != store.CandidateFailed {
		t.Fatalf("gotValue %+v, expectedValue failed with %s", result, ReasonTorrentTooLarge)
	}
}

func TestVerifyStage_cancelDuringAPeriodicSaveStillSavesTheProgress(t *testing.T) {
	fixture := newVerifyFixture(t)
	candidate := fixture.addCandidate("Movie.2020", "", []torrentEntry{
		{path: "movie.mkv", content: patternBytes(3, 4*verifyPieceLength), localName: "movie.mkv"},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fixture.limiter = &cancellingLimiter{cancel: cancel, letReadThrough: true}

	_, _ = fixture.run(ctx)

	verification, found := fixture.verification(candidate.ID)
	if !found || verification.VerifiedPieces != 1 || verification.State != store.VerificationInProgress {
		t.Fatalf("gotValue %+v found=%v, expectedValue the first piece saved despite the cancelled periodic save", verification, found)
	}
}
