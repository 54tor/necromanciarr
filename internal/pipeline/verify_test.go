package pipeline

import (
	"bytes"
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent/metainfo"

	"necromanciarr/internal/crossseed"
	"necromanciarr/internal/libraryprovider"
	"necromanciarr/internal/store"
)

// Disposition de référence : movie.mkv [0,40) puis l'annexe [40,45) ; la pièce 2 [32,45) chevauche les deux.
const (
	movieSize = 40
	nfoSize   = 5
)

func movieWithAnnex(nfoContent []byte) []torrentEntry {
	return []torrentEntry{
		{path: "movie.mkv", content: patternBytes(1, movieSize), localName: "movie.mkv"},
		{path: "movie.nfo", content: nfoContent},
	}
}

func movieOnly() []torrentEntry {
	return []torrentEntry{{path: "movie.mkv", content: patternBytes(1, movieSize), localName: "movie.mkv"}}
}

func singleProposalOfKind(t *testing.T, proposals []store.Proposal, kind store.ProposalKind) store.Proposal {
	t.Helper()
	var found []store.Proposal
	for _, proposal := range proposals {
		if proposal.Kind == kind {
			found = append(found, proposal)
		}
	}
	if len(found) != 1 {
		t.Fatalf("gotValue %d %s proposals in %+v, expectedValue 1", len(found), kind, proposals)
	}
	return found[0]
}

func TestVerifyStage_recordsPercentageAndMissingPieces(t *testing.T) {
	fixture := newVerifyFixture(t)
	candidate := fixture.addCandidate("Movie.2020", "", movieWithAnnex(patternBytes(50, nfoSize)))

	result := fixture.mustRun()

	verification, found := fixture.verification(candidate.ID)
	expectedPercent := 200.0 / 3
	isExpected := found && verification.State == store.VerificationComplete && verification.TotalPieces == 3 &&
		verification.VerifiedPieces == 2 && verification.MediaComplete && math.Abs(verification.Percent-expectedPercent) < 1e-9
	if !isExpected {
		t.Fatalf("gotValue %+v (found %v), expectedValue 2 of 3 pieces, %.2f %%, media complete", verification, found, expectedPercent)
	}
	if gotValue := fixture.candidate(candidate.Infohash).State; gotValue != store.CandidateVerified {
		t.Fatalf("gotValue state %s, expectedValue verified", gotValue)
	}
	if result.Verified != 1 || result.PartialAnnex != 1 {
		t.Fatalf("gotValue %+v, expectedValue one verified partial_annex", result)
	}
}

func TestVerifyStage_resumesAfterInterruption(t *testing.T) {
	fixture := newVerifyFixture(t)
	const pieceCount = 20
	candidate := fixture.addCandidate("Movie.2020", "", []torrentEntry{
		{path: "movie.mkv", content: patternBytes(3, pieceCount*verifyPieceLength), localName: "movie.mkv"},
	})
	fixture.opener.failAfterBytes = 100

	firstResult := fixture.mustRun()

	interrupted, found := fixture.verification(candidate.ID)
	if !found || interrupted.State != store.VerificationInProgress || interrupted.VerifiedPieces == 0 || firstResult.Reasons[ReasonReadError] != 1 {
		t.Fatalf("gotValue %+v (found %v) result %+v, expectedValue partial progress kept", interrupted, found, firstResult)
	}
	if gotValue := fixture.candidate(candidate.Infohash).State; gotValue != store.CandidateFetched {
		t.Fatalf("gotValue state %s, expectedValue fetched (resumable)", gotValue)
	}
	firstBytes := fixture.opener.total()
	fixture.opener.failAfterBytes = 0

	secondResult := fixture.mustRun()

	secondBytes := fixture.opener.total() - firstBytes
	expectedValue := int64((pieceCount - interrupted.VerifiedPieces) * verifyPieceLength)
	if secondBytes != expectedValue {
		t.Fatalf("gotValue %d bytes read on resume, expectedValue %d (verified pieces are not re-read)", secondBytes, expectedValue)
	}
	completed, _ := fixture.verification(candidate.ID)
	if completed.VerifiedPieces != pieceCount || secondResult.Resumed != 1 || secondResult.Verified != 1 {
		t.Fatalf("gotValue %+v result %+v, expectedValue all pieces verified after one resume", completed, secondResult)
	}
}

func TestVerifyStage_completesNfoFromProviderWhenVariantMatches(t *testing.T) {
	fixture := newVerifyFixture(t)
	torrentNfo := []byte("ab\r\ncd")
	candidate := fixture.addCandidate("Movie.2020", "", movieWithAnnex(torrentNfo))
	providerNfo := append([]byte{0xEF, 0xBB, 0xBF}, []byte("ab\ncd")...)
	fixture.fetcher.nfos[candidate.Infohash] = []libraryprovider.Nfo{{Label: "canonical", Data: providerNfo}}

	fixture.mustRun()

	verifiedFiles, err := fixture.store.ListVerifiedFiles(context.Background(), candidate.ID)
	if err != nil || len(verifiedFiles) != 1 || !bytes.Equal(verifiedFiles[0].Data, torrentNfo) || verifiedFiles[0].FileIndex != 1 {
		t.Fatalf("gotValue %+v %v, expectedValue the CRLF variant stored for file 1", verifiedFiles, err)
	}
	verification, _ := fixture.verification(candidate.ID)
	if verification.VerifiedPieces != verification.TotalPieces {
		t.Fatalf("gotValue %+v, expectedValue every piece verified", verification)
	}
	singleProposalOfKind(t, fixture.proposalsOf(candidate.ID), store.ProposalFull)
}

func TestVerifyStage_nfoThatDoesNotVerifyStaysMissing(t *testing.T) {
	fixture := newVerifyFixture(t)
	candidate := fixture.addCandidate("Movie.2020", "", movieWithAnnex([]byte("ab\r\ncd")))
	fixture.fetcher.nfos[candidate.Infohash] = []libraryprovider.Nfo{{Label: "other", Data: []byte("zz\r\nzz")}}

	fixture.mustRun()

	verifiedFiles, err := fixture.store.ListVerifiedFiles(context.Background(), candidate.ID)
	if err != nil || len(verifiedFiles) != 0 {
		t.Fatalf("gotValue %+v %v, expectedValue no verified annex", verifiedFiles, err)
	}
	if fixture.fetcher.nfoCalls != 1 {
		t.Fatalf("gotValue %d nfo calls, expectedValue 1", fixture.fetcher.nfoCalls)
	}
	singleProposalOfKind(t, fixture.proposalsOf(candidate.ID), store.ProposalPartialAnnex)
}

func TestVerifyStage_createsFullProposalFor100Percent(t *testing.T) {
	fixture := newVerifyFixture(t)
	candidate := fixture.addCandidate("Movie.2020", "", movieOnly())

	result := fixture.mustRun()

	proposal := singleProposalOfKind(t, fixture.proposals(), store.ProposalFull)
	details := decodeDetails(t, proposal)
	isExpected := proposal.CandidateID == candidate.ID && proposal.Decision == store.ProposalPending && proposal.Primary &&
		details.Percent == 100 && details.MissingPieces == 0 && proposal.GroupID == candidate.GroupID
	if !isExpected || result.ProposalsCreated != 1 {
		t.Fatalf("gotValue %+v details %+v result %+v, expectedValue one pending primary full proposal", proposal, details, result)
	}
}

func TestVerifyStage_createsPartialAnnexProposalWithWarning(t *testing.T) {
	fixture := newVerifyFixture(t)
	candidate := fixture.addCandidate("Movie.2020", "", movieWithAnnex(patternBytes(50, nfoSize)))

	fixture.mustRun()

	proposal := singleProposalOfKind(t, fixture.proposalsOf(candidate.ID), store.ProposalPartialAnnex)
	details := decodeDetails(t, proposal)
	hasPausedWarning := len(proposal.Warnings) == 1 && strings.Contains(proposal.Warnings[0], "paused")
	if !hasPausedWarning || details.MissingPieces != 1 || math.Abs(details.Percent-200.0/3) > 1e-9 || !proposal.Primary {
		t.Fatalf("gotValue %+v details %+v, expectedValue a paused warning, 1 missing piece and the percentage", proposal, details)
	}
}

func TestVerifyStage_createsPartialPackProposalWhenMediaFileMissing(t *testing.T) {
	fixture := newVerifyFixture(t)
	candidate := fixture.addCandidate("Show.S01", "", []torrentEntry{
		{path: "ep1.mkv", content: patternBytes(5, 32), localName: "ep1.mkv"},
		{path: "ep2.mkv", content: patternBytes(9, 32)},
	})

	result := fixture.mustRun()

	proposals := fixture.proposalsOf(candidate.ID)
	proposal := singleProposalOfKind(t, proposals, store.ProposalPartialPack)
	details := decodeDetails(t, proposal)
	if len(proposals) != 1 || len(proposal.Warnings) == 0 || !slices.Equal(details.MissingMediaFiles, []string{"ep2.mkv"}) || result.PartialPack != 1 {
		t.Fatalf("gotValue %+v details %+v, expectedValue only a partial_pack naming ep2.mkv", proposals, details)
	}
}

// addCompetingPair enregistre deux torrents qui revendiquent le même movie.mkv : A complet, B avec une annexe absente.
func addCompetingPair(fixture *verifyFixture) (complete, partial store.Candidate) {
	complete = fixture.addCandidate("Movie.A", "tracker-a", movieOnly())
	partial = fixture.addCandidate("Movie.B", "tracker-b", movieWithAnnex(patternBytes(50, nfoSize)))
	return fixture.candidate(complete.Infohash), fixture.candidate(partial.Infohash)
}

func TestVerifyStage_listsAllTorrentsOfAGroupAndMarksOnePrimary(t *testing.T) {
	fixture := newVerifyFixture(t)
	complete, partial := addCompetingPair(fixture)
	if complete.GroupID == 0 || complete.GroupID != partial.GroupID {
		t.Fatalf("gotValue groups %d and %d, expectedValue one shared group", complete.GroupID, partial.GroupID)
	}

	result := fixture.mustRun()

	proposals := fixture.proposals()
	var primaries []store.Proposal
	for _, proposal := range proposals {
		if proposal.GroupID != complete.GroupID {
			t.Fatalf("gotValue group %d on %+v, expectedValue %d", proposal.GroupID, proposal, complete.GroupID)
		}
		if proposal.Primary {
			primaries = append(primaries, proposal)
		}
	}
	if len(primaries) != 1 || primaries[0].CandidateID != complete.ID || primaries[0].Kind != store.ProposalFull {
		t.Fatalf("gotValue primaries %+v, expectedValue only the full proposal of %d", primaries, complete.ID)
	}
	singleProposalOfKind(t, proposals, store.ProposalPartialAnnex)
	conflict := singleProposalOfKind(t, proposals, store.ProposalConflict)
	var listedIDs []int64
	for _, member := range decodeDetails(t, conflict).Candidates {
		listedIDs = append(listedIDs, member.CandidateID)
	}
	if !slices.Equal(listedIDs, []int64{complete.ID, partial.ID}) || result.Conflicts != 1 {
		t.Fatalf("gotValue listed %v result %+v, expectedValue both candidates listed in one conflict", listedIDs, result)
	}
}

func TestVerifyStage_crossSeedProposalsOnlyInNativeMode(t *testing.T) {
	expectedCrossSeeds := map[crossseed.Behavior]int{crossseed.Native: 1, crossseed.Delegate: 0, crossseed.Disabled: 0}
	for behavior, expectedValue := range expectedCrossSeeds {
		fixture := newVerifyFixture(t)
		fixture.crossSeed = behavior
		first := fixture.addCandidate("Movie.A", "tracker-a", movieOnly())
		second := fixture.addCandidate("Movie.B", "tracker-b", movieOnly())

		fixture.mustRun()

		var crossSeeds []store.Proposal
		for _, proposal := range fixture.proposals() {
			if proposal.Kind == store.ProposalCrossSeed {
				crossSeeds = append(crossSeeds, proposal)
			}
		}
		if len(crossSeeds) != expectedValue {
			t.Fatalf("behavior %d: gotValue %d cross_seed proposals, expectedValue %d", behavior, len(crossSeeds), expectedValue)
		}
		if expectedValue == 1 && crossSeeds[0].CandidateID != second.ID {
			t.Fatalf("gotValue cross_seed for %d, expectedValue the non-primary %d (primary %d)", crossSeeds[0].CandidateID, second.ID, first.ID)
		}
		if gotValue := len(fixture.proposalsOf(first.ID)); gotValue < 2 {
			t.Fatalf("behavior %d: gotValue %d proposals for the primary, expectedValue its full proposal and the group conflict", behavior, gotValue)
		}
	}
}

func TestVerifyStage_ambiguousAssignmentIsAConflictNeverResolved(t *testing.T) {
	fixture := newVerifyFixture(t)
	candidate := fixture.addCandidate("Movie.2020", "", []torrentEntry{
		{path: "movie.mkv", content: patternBytes(1, movieSize), localName: "copy-a.mkv", twinLocalName: "copy-b.mkv"},
	})

	result := fixture.mustRun()

	proposals := fixture.proposalsOf(candidate.ID)
	if !slices.Equal(kindsOf(proposals), []store.ProposalKind{store.ProposalConflict}) {
		t.Fatalf("gotValue %+v, expectedValue only a conflict proposal", proposals)
	}
	details := decodeDetails(t, proposals[0])
	expectedValue := []string{filepath.Join(fixture.root, "copy-a.mkv"), filepath.Join(fixture.root, "copy-b.mkv")}
	if len(details.AmbiguousFiles) != 1 || !slices.Equal(details.AmbiguousFiles[0].LocalFiles, expectedValue) || proposals[0].Primary {
		t.Fatalf("gotValue %+v, expectedValue both copies listed, nothing chosen", details)
	}
	if result.Conflicts != 1 {
		t.Fatalf("gotValue %+v, expectedValue one conflict", result)
	}
}

func TestVerifyStage_changedFileMarksCandidateStaleAndDropsBitset(t *testing.T) {
	fixture := newVerifyFixture(t)
	candidate := fixture.addCandidate("Movie.2020", "", []torrentEntry{
		{path: "movie.mkv", content: patternBytes(3, 20*verifyPieceLength), localName: "movie.mkv"},
	})
	fixture.opener.failAfterBytes = 100
	fixture.mustRun()
	if _, found := fixture.verification(candidate.ID); !found {
		t.Fatal("expected partial progress after the interrupted run")
	}
	fixture.opener.failAfterBytes = 0
	changedTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(fixture.root, "movie.mkv"), changedTime, changedTime); err != nil {
		t.Fatal(err)
	}

	result := fixture.mustRun()

	if gotValue := fixture.candidate(candidate.Infohash).State; gotValue != store.CandidateStale {
		t.Fatalf("gotValue state %s, expectedValue stale", gotValue)
	}
	if _, found := fixture.verification(candidate.ID); found || result.Reasons[ReasonChangedFile] != 1 {
		t.Fatalf("gotValue verification found=%v result %+v, expectedValue dropped bitset", found, result)
	}
	if proposals := fixture.proposals(); len(proposals) != 0 {
		t.Fatalf("gotValue %+v, expectedValue no proposal", proposals)
	}
}

func TestVerifyStage_neverWritesLibraryFiles(t *testing.T) {
	fixture := newVerifyFixture(t)
	addCompetingPair(fixture)
	nfoCandidate := fixture.addCandidate("Movie.C", "", []torrentEntry{
		{path: "movie.mkv", content: patternBytes(1, movieSize), localName: "movie.mkv"},
		{path: "movie.nfo", content: []byte("ab\r\ncd")},
	})
	fixture.fetcher.nfos[nfoCandidate.Infohash] = []libraryprovider.Nfo{{Data: []byte("ab\ncd")}}
	fixture.addCandidate("Show.S01", "", []torrentEntry{
		{path: "ep1.mkv", content: patternBytes(5, 32), localName: "ep1.mkv", localContent: patternBytes(6, 32)},
	})
	fixture.guardLibrary()

	fixture.mustRun()
	fixture.mustRun()
}

func TestVerifyStage_oversizedTorrentFailsCleanly(t *testing.T) {
	fixture := newVerifyFixture(t)
	candidate := fixture.addCandidate("Movie.2020", "", movieOnly())
	fixture.fetcher.torrents[candidate.Infohash] = append([]byte("d"), make([]byte, store.MaxTorrentBytes)...)

	result, err := fixture.run(context.Background())

	if err != nil || result.Failed != 1 || result.Reasons[ReasonTorrentTooLarge] != 1 {
		t.Fatalf("gotValue %+v %v, expectedValue one candidate failed with %s", result, err, ReasonTorrentTooLarge)
	}
	if gotValue := fixture.candidate(candidate.Infohash).State; gotValue != store.CandidateFailed || len(fixture.proposals()) != 0 {
		t.Fatalf("gotValue state %s, expectedValue failed without proposal", gotValue)
	}
}

func TestVerifyStage_v2OnlyTorrentFails(t *testing.T) {
	fixture := newVerifyFixture(t)
	raw := encodeTestTorrent(t, metainfo.Info{Name: "movie.mkv", PieceLength: verifyPieceLength, MetaVersion: 2})
	candidate := fixture.addCandidateWithTorrent("Movie.2020", "", movieOnly(), raw)

	result := fixture.mustRun()

	if gotValue := fixture.candidate(candidate.Infohash).State; gotValue != store.CandidateFailed || result.Reasons[ReasonV2Only] != 1 {
		t.Fatalf("gotValue state %s result %+v, expectedValue failed with %s", gotValue, result, ReasonV2Only)
	}
}

func TestVerifyStage_isIdempotentAndDoesNotDuplicateProposals(t *testing.T) {
	fixture := newVerifyFixture(t)
	fixture.crossSeed = crossseed.Native
	addCompetingPair(fixture)
	fixture.addCandidate("Movie.C", "tracker-c", movieOnly())
	fixture.mustRun()
	expectedValue := fixture.proposals()

	secondResult := fixture.mustRun()

	gotValue := fixture.proposals()
	if len(gotValue) != len(expectedValue) || secondResult.ProposalsCreated != 0 || secondResult.Verified != 0 {
		t.Fatalf("gotValue %d proposals (result %+v), expectedValue the same %d", len(gotValue), secondResult, len(expectedValue))
	}
	for proposalIndex := range expectedValue {
		isSame := gotValue[proposalIndex].ID == expectedValue[proposalIndex].ID && gotValue[proposalIndex].Primary == expectedValue[proposalIndex].Primary &&
			bytes.Equal(gotValue[proposalIndex].Details, expectedValue[proposalIndex].Details)
		if !isSame {
			t.Fatalf("gotValue %+v, expectedValue %+v", gotValue[proposalIndex], expectedValue[proposalIndex])
		}
	}
}

func TestVerifyStage_stopsPromptlyOnCancel(t *testing.T) {
	fixture := newVerifyFixture(t)
	first := fixture.addCandidate("Movie.A", "", movieOnly())
	second := fixture.addCandidate("Other.B", "", []torrentEntry{{path: "other.mkv", content: patternBytes(7, 48), localName: "other.mkv"}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	limiter := &cancellingLimiter{cancel: cancel}
	fixture.limiter = limiter

	_, err := fixture.run(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("gotValue %v, expectedValue context.Canceled", err)
	}
	if limiter.calls != 1 || fixture.fetcher.torrentCalls != 1 {
		t.Fatalf("gotValue %d limiter calls and %d downloads, expectedValue 1 and 1", limiter.calls, fixture.fetcher.torrentCalls)
	}
	if gotValue := fixture.candidate(second.Infohash).State; gotValue != store.CandidateMatched {
		t.Fatalf("gotValue second state %s, expectedValue matched", gotValue)
	}
	if gotValue := fixture.candidate(first.Infohash).State; gotValue != store.CandidateFetched || len(fixture.proposals()) != 0 {
		t.Fatalf("gotValue first state %s, expectedValue fetched without proposal", gotValue)
	}
}

func TestVerifyStage_corruptedByteIsNeverVerified(t *testing.T) {
	fixture := newVerifyFixture(t)
	corrupted := patternBytes(1, movieSize)
	corrupted[20] ^= 0x01
	candidate := fixture.addCandidate("Movie.2020", "", []torrentEntry{
		{path: "movie.mkv", content: patternBytes(1, movieSize), localName: "movie.mkv", localContent: corrupted},
	})

	result := fixture.mustRun()

	verification, _ := fixture.verification(candidate.ID)
	corruptedPieceVerified := verification.VerifiedBitset[0]&(1<<1) != 0
	if corruptedPieceVerified || verification.VerifiedPieces != 2 || verification.MediaComplete {
		t.Fatalf("gotValue %+v, expectedValue piece 1 unverified and media incomplete", verification)
	}
	if len(fixture.proposals()) != 0 || result.Reasons[ReasonMediaNotVerified] != 1 {
		t.Fatalf("gotValue %+v result %+v, expectedValue no proposal", fixture.proposals(), result)
	}
}
