package pipeline

import (
	"context"
	"testing"

	"necromanciarr/internal/store"
)

// Les pourcentages de pièces manquantes de référence : movieWithAnnex en a 1 sur 3 (33,3 %), packWithMissingEpisode
// 2 sur 4 (50 %).
func packWithMissingEpisode() []torrentEntry {
	return []torrentEntry{
		{path: "ep1.mkv", content: patternBytes(5, 32), localName: "ep1.mkv"},
		{path: "ep2.mkv", content: patternBytes(9, 32)},
	}
}

func capOf(percent float64) *float64 { return &percent }

func (fixture *verifyFixture) holds() []store.CandidateHold {
	fixture.t.Helper()
	holds, err := fixture.store.ListCandidateHolds(context.Background())
	if err != nil {
		fixture.t.Fatal(err)
	}
	return holds
}

func TestVerifyStage_partialBelowTheCapIsProposed(t *testing.T) {
	fixture := newVerifyFixture(t)
	fixture.partialCap = capOf(40)
	candidate := fixture.addCandidate("Movie.2020", "", movieWithAnnex(patternBytes(50, nfoSize)))

	fixture.mustRun()

	singleProposalOfKind(t, fixture.proposalsOf(candidate.ID), store.ProposalPartialAnnex)
	if gotValue := fixture.holds(); len(gotValue) != 0 {
		t.Fatalf("gotValue %+v, expectedValue no hold below the cap", gotValue)
	}
}

func TestVerifyStage_partialExactlyAtTheCapIsProposed(t *testing.T) {
	fixture := newVerifyFixture(t)
	fixture.partialCap = capOf(50)
	candidate := fixture.addCandidate("Show.S01", "", packWithMissingEpisode())

	fixture.mustRun()

	singleProposalOfKind(t, fixture.proposalsOf(candidate.ID), store.ProposalPartialPack)
}

func TestVerifyStage_partialAboveTheCapIsHeldWithANamedReasonAndNoProposal(t *testing.T) {
	fixture := newVerifyFixture(t)
	fixture.partialCap = capOf(30)
	candidate := fixture.addCandidate("Movie.2020", "", movieWithAnnex(patternBytes(50, nfoSize)))

	result := fixture.mustRun()

	holds := fixture.holds()
	isHeld := len(holds) == 1 && holds[0].CandidateID == candidate.ID && holds[0].Reason == ReasonMissingPiecesAboveCap &&
		holds[0].Kind == store.ProposalPartialAnnex
	if !isHeld || len(fixture.proposalsOf(candidate.ID)) != 0 || result.Reasons[ReasonMissingPiecesAboveCap] != 1 {
		t.Fatalf("gotValue holds %+v proposals %+v result %+v, expectedValue one held partial_annex and no proposal",
			holds, fixture.proposalsOf(candidate.ID), result)
	}
	if gotValue := fixture.candidate(candidate.Infohash).State; gotValue != store.CandidateVerified {
		t.Fatalf("gotValue %s, expectedValue the held candidate stays verified", gotValue)
	}
}

func TestVerifyStage_capZeroKeepsOnlyFullyVerifiedTorrents(t *testing.T) {
	fixture := newVerifyFixture(t)
	fixture.partialCap = capOf(0)
	complete := fixture.addCandidate("Movie.A", "tracker-a", movieOnly())
	pack := fixture.addCandidate("Show.S01", "", packWithMissingEpisode())

	fixture.mustRun()

	singleProposalOfKind(t, fixture.proposalsOf(complete.ID), store.ProposalFull)
	if gotValue := fixture.proposalsOf(pack.ID); len(gotValue) != 0 || len(fixture.holds()) != 1 {
		t.Fatalf("gotValue %+v holds %+v, expectedValue the partial pack held", gotValue, fixture.holds())
	}
}

func TestVerifyStage_capHundredKeepsTheCurrentBehaviour(t *testing.T) {
	fixture := newVerifyFixture(t)
	fixture.partialCap = capOf(100)
	pack := fixture.addCandidate("Show.S01", "", packWithMissingEpisode())

	fixture.mustRun()

	singleProposalOfKind(t, fixture.proposalsOf(pack.ID), store.ProposalPartialPack)
}

func TestVerifyStage_raisingTheCapThenRerunningProposesTheHeldCandidate(t *testing.T) {
	fixture := newVerifyFixture(t)
	fixture.partialCap = capOf(30)
	candidate := fixture.addCandidate("Movie.2020", "", movieWithAnnex(patternBytes(50, nfoSize)))
	fixture.mustRun()

	fixture.partialCap = capOf(40)
	result := fixture.mustRun()

	singleProposalOfKind(t, fixture.proposalsOf(candidate.ID), store.ProposalPartialAnnex)
	if gotValue := fixture.holds(); len(gotValue) != 0 || result.ProposalsCreated != 1 {
		t.Fatalf("gotValue holds %+v result %+v, expectedValue the hold replaced by one proposal", gotValue, result)
	}
}

func TestVerifyStage_loweringTheCapWithdrawsAPendingPartialButNeverADecidedOne(t *testing.T) {
	fixture := newVerifyFixture(t)
	pending := fixture.addCandidate("Movie.B", "tracker-b", movieWithAnnex(patternBytes(50, nfoSize)))
	accepted := fixture.addCandidate("Show.S01", "", packWithMissingEpisode())
	fixture.mustRun()
	acceptedProposal := singleProposalOfKind(t, fixture.proposalsOf(accepted.ID), store.ProposalPartialPack)
	if err := fixture.store.SetProposalDecision(context.Background(), acceptedProposal.ID, store.ProposalAccepted, fixedNow); err != nil {
		t.Fatal(err)
	}

	fixture.partialCap = capOf(10)
	fixture.mustRun()

	withdrawn := singleProposalOfKind(t, fixture.proposalsOf(pending.ID), store.ProposalPartialAnnex)
	kept := singleProposalOfKind(t, fixture.proposalsOf(accepted.ID), store.ProposalPartialPack)
	holds := fixture.holds()
	isExpected := withdrawn.Decision == store.ProposalSuperseded && kept.Decision == store.ProposalAccepted &&
		len(holds) == 1 && holds[0].CandidateID == pending.ID
	if !isExpected {
		t.Fatalf("gotValue withdrawn %s kept %s holds %+v, expectedValue only the pending partial withdrawn and held",
			withdrawn.Decision, kept.Decision, holds)
	}
}
