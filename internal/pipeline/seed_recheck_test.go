package pipeline

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"necromanciarr/internal/qbit"
	"necromanciarr/internal/store"
)

func TestSeedStage_sourceReplacedAfterLinkMarksTheCandidateStaleAndAddsNothing(t *testing.T) {
	fixture := newApplyFixture(t)
	fixture.skipRecheck = true
	candidate, proposal := fixture.linkedProposal(movieOnly(), store.ProposalFull)
	replaceWithCopy(t, fixture.libraryPath("movie.mkv"), patternBytes(7, movieSize))

	result := fixture.mustRunStage(StageSeed)

	if len(fixture.client.added) != 0 || result.Reasons[ReasonChangedFile] != 1 {
		t.Fatalf("gotValue %+v added=%v, expectedValue nothing added", result, fixture.client.added)
	}
	if fixture.candidate(candidate.Infohash).State != store.CandidateStale || !slices.Contains(fixture.warningsOf(proposal.ID), warningSourcesChanged) {
		t.Fatal("expectedValue a stale candidate and the sources-changed warning")
	}
}

func TestSeedStage_damagedLinkRootFailsAndClearsTheStep(t *testing.T) {
	cases := map[string]struct {
		entries        []torrentEntry
		damage         func(t *testing.T, linkRoot string)
		expectedReason string
	}{
		"link deleted": {movieOnly(), func(t *testing.T, linkRoot string) {
			if err := os.Remove(filepath.Join(linkRoot, "Movie.2020", "movie.mkv")); err != nil {
				t.Fatal(err)
			}
		}, ReasonLinkChanged},
		"annex altered": {nil, func(t *testing.T, linkRoot string) {
			replaceWithCopy(t, filepath.Join(linkRoot, "Movie.2020", "movie.nfo"), []byte("zz\r\nzz"))
		}, ReasonAnnexChanged},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			fixture := newApplyFixture(t)
			fixture.skipRecheck = true
			var proposal store.Proposal
			if testCase.entries == nil {
				proposal, _ = fixture.linkedNfoTorrent([]byte("ab\r\ncd"))
			} else {
				_, proposal = fixture.linkedProposal(testCase.entries, store.ProposalFull)
			}
			testCase.damage(t, fixture.progressOf(proposal.ID).LinkRoot)

			result := fixture.mustRunStage(StageSeed)

			isRefused := result.Reasons[testCase.expectedReason] == 1 && len(fixture.client.added) == 0 &&
				fixture.decisionOf(proposal.ID) == store.ProposalFailed && fixture.progressOf(proposal.ID).Step == ""
			if !isRefused {
				t.Fatalf("gotValue %+v step=%q, expectedValue %s, nothing added and the step cleared", result,
					fixture.progressOf(proposal.ID).Step, testCase.expectedReason)
			}
		})
	}
}

func TestSeedStage_aLinkProvenOnlyByItsFingerprintIsRechecked(t *testing.T) {
	fixture := newApplyFixture(t)
	fixture.skipRecheck = true
	candidate, proposal := fixture.linkedProposal(movieOnly(), store.ProposalFull)
	replaceWithCopy(t, filepath.Join(fixture.progressOf(proposal.ID).LinkRoot, "Movie.2020", "movie.mkv"), patternBytes(1, movieSize))
	fixture.client.guardResults[candidate.Infohash] = qbit.RecheckResult{Progress: 1, Complete: true}

	fixture.mustRunStage(StageSeed)

	if fixture.client.added[candidate.Infohash].options.SkipChecking || len(fixture.client.guardCalls) != 1 {
		t.Fatalf("gotValue %+v guard=%v, expectedValue no skip_checking and a guarded recheck", fixture.client.added, fixture.client.guardCalls)
	}
}

func TestSeedStage_neverTrustsATorrentAlreadyInTheClient(t *testing.T) {
	cases := map[string]struct {
		entries      []torrentEntry
		kind         store.ProposalKind
		foreign      bool
		expectedStep store.ProposalStep
	}{
		"foreign torrent at another path":    {movieOnly(), store.ProposalFull, true, ""},
		"running incomplete torrent of ours": {movieWithAnnex(patternBytes(50, nfoSize)), store.ProposalPartialAnnex, false, store.StepSeededPausedIncomplete},
		"complete torrent of ours":           {movieOnly(), store.ProposalFull, false, store.StepSeeded},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			fixture := newApplyFixture(t)
			fixture.skipRecheck = true
			candidate, proposal := fixture.linkedProposal(testCase.entries, testCase.kind)
			savePath := fixture.progressOf(proposal.ID).LinkRoot
			if testCase.foreign {
				savePath = "/home/u/elsewhere"
			}
			fixture.client.existing = []qbit.TorrentInfo{{Hash: candidate.Infohash, SavePath: savePath + "/", State: "downloading", Progress: 0.5}}
			fixture.client.guardResults[candidate.Infohash] = qbit.RecheckResult{Progress: 1, Complete: true}

			result := fixture.mustRunStage(StageSeed)

			gotStep := fixture.progressOf(proposal.ID).Step
			if testCase.foreign {
				gotStep = ""
			}
			untouchedClient := len(fixture.client.paused) == 0 && len(fixture.client.resumed) == 0 && len(fixture.client.guardCalls) == 0
			switch {
			case len(fixture.client.added) != 0:
				t.Fatalf("gotValue %v, expectedValue no second add", fixture.client.added)
			case testCase.foreign && (result.Reasons[ReasonAlreadyInClient] != 1 || !untouchedClient || fixture.decisionOf(proposal.ID) != store.ProposalFailed):
				t.Fatalf("foreign: gotValue %+v paused=%v resumed=%v, expectedValue already_in_client and nothing touched", result,
					fixture.client.paused, fixture.client.resumed)
			case testCase.kind == store.ProposalPartialAnnex && (!slices.Equal(fixture.client.paused, []string{candidate.Infohash}) || len(fixture.client.resumed) != 0):
				t.Fatalf("incomplete: gotValue paused=%v resumed=%v, expectedValue paused and never resumed", fixture.client.paused, fixture.client.resumed)
			case gotStep != testCase.expectedStep:
				t.Fatalf("gotValue step %q, expectedValue %q", gotStep, testCase.expectedStep)
			}
		})
	}
}

func TestSeedStage_refusesAWindowsClientPathWithInvalidCharacters(t *testing.T) {
	fixture := newApplyFixture(t)
	fixture.mapper = clientPrefixMapper{from: fixture.linkDir, to: `D:\links\a?b`}
	_, proposal := fixture.linkedProposal(movieOnly(), store.ProposalFull)

	result := fixture.mustRunStage(StageSeed)

	if result.Reasons[ReasonClientPathInvalid] != 1 || len(fixture.client.added) != 0 || fixture.decisionOf(proposal.ID) != store.ProposalFailed {
		t.Fatalf("gotValue %+v, expectedValue client_path_invalid and nothing added", result)
	}
}
