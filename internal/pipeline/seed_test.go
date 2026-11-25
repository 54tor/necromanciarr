package pipeline

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"necromanciarr/internal/config"
	"necromanciarr/internal/crossseed"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/linker"
	"necromanciarr/internal/qbit"
	"necromanciarr/internal/store"
)

// clientPrefixMapper traduit le préfixe de ce processus vers celui que voit le client.
type clientPrefixMapper struct{ from, to string }

func (mapper clientPrefixMapper) Translate(original string) (string, error) {
	rest, found := strings.CutPrefix(original, mapper.from)
	if !found {
		return "", errors.New("no rule")
	}
	return mapper.to + rest, nil
}

// linkedProposal vérifie, accepte et lie le candidat ; elle renvoie sa proposition de la nature voulue.
func (fixture *applyFixture) linkedProposal(entries []torrentEntry, kind store.ProposalKind) (store.Candidate, store.Proposal) {
	fixture.t.Helper()
	candidate := fixture.addCandidate("Movie.2020", "", entries)
	proposal := fixture.verifiedProposal(candidate, kind)
	fixture.accept(proposal.ID)
	if result := fixture.mustRunStage(StageLink); result.Linked != 1 {
		fixture.t.Fatalf("link: gotValue %+v, expectedValue one torrent linked", result)
	}
	return candidate, proposal
}

// warningsOf renvoie les clés des avertissements de la proposition.
func (fixture *applyFixture) warningsOf(proposalID int64) []i18n.Key {
	fixture.t.Helper()
	for _, proposal := range fixture.proposals() {
		if proposal.ID == proposalID {
			return warningKeys(proposal.Warnings)
		}
	}
	return nil
}

func TestSeedStage_addsPausedWithCategoryTagsAndMappedPath(t *testing.T) {
	fixture := newApplyFixture(t)
	fixture.mapper = clientPrefixMapper{from: fixture.linkDir, to: "/data/links"}
	candidate, proposal := fixture.linkedProposal(movieOnly(), store.ProposalFull)
	fixture.client.guardResults[candidate.Infohash] = qbit.RecheckResult{Progress: 1, Complete: true}

	fixture.mustRunStage(StageSeed)

	added, present := fixture.client.added[candidate.Infohash]
	linkRoot := fixture.progressOf(proposal.ID).LinkRoot
	expectedPath := "/data/links" + strings.TrimPrefix(linkRoot, fixture.linkDir)
	isExpected := present && added.savePath == expectedPath && !added.options.Start && added.options.Category == "necromanciarr" &&
		slices.Equal(added.options.Tags, []string{"necromanciarr", "relinked"})
	if !isExpected {
		t.Fatalf("gotValue %+v, expectedValue a stopped add at %s with category and tags", added, expectedPath)
	}
}

func TestSeedStage_resumesOnlyAfterGuardConfirms100Percent(t *testing.T) {
	for name, guard := range map[string]qbit.RecheckResult{
		"incomplete": {Progress: 0.99},
		"complete":   {Progress: 1, Complete: true},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newApplyFixture(t)
			candidate, proposal := fixture.linkedProposal(movieOnly(), store.ProposalFull)
			fixture.client.guardResults[candidate.Infohash] = guard

			result := fixture.mustRunStage(StageSeed)

			gotResumed := slices.Contains(fixture.client.resumed, candidate.Infohash)
			expectedStep := store.StepSeededPausedIncomplete
			if guard.Complete {
				expectedStep = store.StepSeeded
			}
			isExpected := gotResumed == guard.Complete && len(fixture.client.guardCalls) == 1 &&
				fixture.progressOf(proposal.ID).Step == expectedStep && fixture.decisionOf(proposal.ID) == store.ProposalApplied
			if !isExpected {
				t.Fatalf("gotValue resumed=%v step=%s result %+v, expectedValue resumed only when the guard says complete",
					gotResumed, fixture.progressOf(proposal.ID).Step, result)
			}
		})
	}
}

func TestSeedStage_partialAnnexStaysPausedAndIsNeverResumed(t *testing.T) {
	fixture := newApplyFixture(t)
	fixture.skipRecheck = true
	candidate, proposal := fixture.linkedProposal(movieWithAnnex(patternBytes(50, nfoSize)), store.ProposalPartialAnnex)
	fixture.client.guardResults[candidate.Infohash] = qbit.RecheckResult{Progress: 1, Complete: true}
	fixture.client.progress[candidate.Infohash] = 1

	result := fixture.mustRunStage(StageSeed)

	added := fixture.client.added[candidate.Infohash]
	isPaused := !added.options.Start && !added.options.SkipChecking && len(fixture.client.resumed) == 0 && len(fixture.client.guardCalls) == 0
	if !isPaused || fixture.progressOf(proposal.ID).Step != store.StepSeededPausedIncomplete || result.SeededPausedIncomplete != 1 {
		t.Fatalf("gotValue %+v resumed=%v step=%s, expectedValue a paused partial_annex never resumed", added, fixture.client.resumed,
			fixture.progressOf(proposal.ID).Step)
	}
	if !slices.Contains(fixture.warningsOf(proposal.ID), i18n.KeyWarningAnnexPiecesNotProven) {
		t.Fatalf("gotValue %v, expectedValue the paused warning kept", fixture.warningsOf(proposal.ID))
	}
}

func TestSeedStage_skipCheckingOnlyWhenOurVerificationWas100Percent(t *testing.T) {
	cases := map[string]struct {
		entries      []torrentEntry
		kind         store.ProposalKind
		expectedSkip bool
	}{
		"full":          {movieOnly(), store.ProposalFull, true},
		"partial annex": {movieWithAnnex(patternBytes(50, nfoSize)), store.ProposalPartialAnnex, false},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			fixture := newApplyFixture(t)
			fixture.skipRecheck = true
			candidate, _ := fixture.linkedProposal(testCase.entries, testCase.kind)

			fixture.mustRunStage(StageSeed)

			added := fixture.client.added[candidate.Infohash]
			gotResumedWithoutGuard := slices.Contains(fixture.client.resumed, candidate.Infohash) && len(fixture.client.guardCalls) == 0
			if added.options.SkipChecking != testCase.expectedSkip || gotResumedWithoutGuard != testCase.expectedSkip {
				t.Fatalf("gotValue %+v resumedWithoutGuard=%v, expectedValue skip_checking=%v", added.options, gotResumedWithoutGuard, testCase.expectedSkip)
			}
		})
	}
}

func TestSeedStage_readOnlyClientIsRefused(t *testing.T) {
	fixture := newApplyFixture(t)
	_, proposal := fixture.linkedProposal(movieOnly(), store.ProposalFull)
	fixture.client.addErr = qbit.ErrReadOnly

	result, err := fixture.runStage(StageSeed)

	if !errors.Is(err, ErrClientReadOnly) || result.Reasons[ReasonClientReadOnly] != 1 {
		t.Fatalf("gotValue %v %+v, expectedValue ErrClientReadOnly", err, result)
	}
	if fixture.decisionOf(proposal.ID) != store.ProposalAccepted || fixture.progressOf(proposal.ID).Step != store.StepLinked {
		t.Fatal("expectedValue the proposal left accepted and linked, ready once the client is writable")
	}
}

var errUnreachableDaemon = errors.New("crossseed: request failed")

func TestSeedStage_notifierFailureIsAWarningNotAFailure(t *testing.T) {
	fixture := newApplyFixture(t)
	fixture.skipRecheck = true
	fixture.notifier.err = errUnreachableDaemon
	_, proposal := fixture.linkedProposal(movieOnly(), store.ProposalFull)

	result := fixture.mustRunStage(StageSeed)

	isSeeded := fixture.progressOf(proposal.ID).Step == store.StepSeeded && fixture.decisionOf(proposal.ID) == store.ProposalApplied
	if !isSeeded || result.Failed != 0 || result.Reasons[ReasonCrossSeedNotifyFailed] != 1 {
		t.Fatalf("gotValue %+v step=%s, expectedValue a seeded proposal with a notification reason", result, fixture.progressOf(proposal.ID).Step)
	}
	if !slices.Contains(fixture.warningsOf(proposal.ID), i18n.KeyWarningCrossSeedNotifyFailed) {
		t.Fatalf("gotValue %v, expectedValue the notification warning", fixture.warningsOf(proposal.ID))
	}
}

func TestSeedStage_delegateModeNotifiesWebhookNativeModeDoesNot(t *testing.T) {
	for _, behavior := range []crossseed.Behavior{crossseed.Delegate, crossseed.Native, crossseed.Disabled} {
		fixture := newApplyFixture(t)
		fixture.skipRecheck = true
		fixture.crossSeed = behavior
		candidate, _ := fixture.linkedProposal(movieOnly(), store.ProposalFull)

		fixture.mustRunStage(StageSeed)

		gotNotified := slices.Equal(fixture.notifier.notified, []string{candidate.Infohash})
		if gotNotified != (behavior == crossseed.Delegate) {
			t.Fatalf("behavior %v: gotValue %v, expectedValue a notification only in Delegate", behavior, fixture.notifier.notified)
		}
	}
}

func TestSeedStage_reportsTheCrossSeedModeWarningAndNeedsAClient(t *testing.T) {
	deps := Deps{Store: openTestStore(t), CrossSeedWarnings: []i18n.Message{{Key: i18n.KeyWarningCrossSeedFallbackNative}}}
	result, err := Run(context.Background(), StageSeed, deps)
	if !errors.Is(err, ErrNoTorrentClient) || !slices.Equal(warningKeys(result.Warnings), []i18n.Key{i18n.KeyWarningCrossSeedFallbackNative}) {
		t.Fatalf("gotValue %v %+v, expectedValue ErrNoTorrentClient with the mode warning", err, result)
	}
}

func TestSeedStage_neverSeedsAnUnlinkedProposal(t *testing.T) {
	fixture := newApplyFixture(t)
	candidate := fixture.addCandidate("Movie.2020", "", movieOnly())
	proposal := fixture.verifiedProposal(candidate, store.ProposalFull)
	fixture.accept(proposal.ID)

	result := fixture.mustRunStage(StageSeed)

	if len(fixture.client.added) != 0 || result.Reasons[ReasonNotLinked] != 1 {
		t.Fatalf("gotValue %+v added=%v, expectedValue nothing added before Link", result, fixture.client.added)
	}
}

func TestSeedStage_incompleteTorrentRunningAfterAddFailsTheProposal(t *testing.T) {
	cases := map[string]struct {
		reportedState  string
		expectedFailed bool
	}{
		"stopped as asked":        {reportedState: "stoppedDL", expectedFailed: false},
		"checking before pausing": {reportedState: "checkingDL", expectedFailed: false},
		"running despite pause":   {reportedState: "downloading", expectedFailed: true},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			fixture := newApplyFixture(t)
			candidate, proposal := fixture.linkedProposal(movieWithAnnex(patternBytes(50, nfoSize)), store.ProposalPartialAnnex)
			fixture.client.reportedState = testCase.reportedState

			result := fixture.mustRunStage(StageSeed)

			pausedAfterAdd := slices.Contains(fixture.client.paused, candidate.Infohash)
			gotFailed := fixture.decisionOf(proposal.ID) == store.ProposalFailed && result.Reasons[ReasonNotPausedAfterAdd] == 1
			if !pausedAfterAdd || gotFailed != testCase.expectedFailed {
				t.Fatalf("gotValue paused=%v failed=%v result %+v, expectedValue a pause and failed=%v", pausedAfterAdd, gotFailed,
					result, testCase.expectedFailed)
			}
			expectedRemoved := []string(nil)
			if testCase.expectedFailed {
				expectedRemoved = []string{candidate.Infohash}
			}
			if !slices.Equal(fixture.client.removed, expectedRemoved) || (testCase.expectedFailed && len(fixture.client.paused) != 2) {
				t.Fatalf("gotValue removed %v paused %v, expectedValue a second pause then removal (data kept) only when still running",
					fixture.client.removed, fixture.client.paused)
			}
		})
	}
}

func warningKeys(warnings []i18n.Message) []i18n.Key {
	keys := make([]i18n.Key, 0, len(warnings))
	for _, warning := range warnings {
		keys = append(keys, warning.Key)
	}
	return keys
}

// remoteClientMapper traduit le dossier de liens distant vers la vue du client.
func remoteClientMapper() *linker.Mapper {
	return linker.NewMapper([]config.PathRule{{From: "sftp://seedbox/srv/links", To: "/downloads/links"}})
}

func TestSeed_remoteLinkForcesClientRecheck(t *testing.T) {
	for name, fileIdentityKnown := range map[string]bool{"file identity unknown": false, "file identity known": true} {
		t.Run(name, func(t *testing.T) {
			fixture, volume := newRemoteApplyFixture(t)
			volume.FileIDKnown.Store(fileIdentityKnown)
			fixture.skipRecheck = true
			fixture.mapper = remoteClientMapper()
			candidate, proposal := fixture.linkedProposal(movieOnly(), store.ProposalFull)
			fixture.client.guardResults[candidate.Infohash] = qbit.RecheckResult{Progress: 1, Complete: true}

			result := fixture.mustRunStage(StageSeed)

			added, present := fixture.client.added[candidate.Infohash]
			valeurAttendueChemin := "/downloads/links/Movie.2020 [" + candidate.Infohash + "]"
			isRechecked := present && !added.options.SkipChecking && !added.options.Start && len(fixture.client.guardCalls) == 1
			if !isRechecked || added.savePath != valeurAttendueChemin || result.Reasons[ReasonIdentityUnproven] != 1 {
				t.Fatalf("valeurObtenue %+v guard=%v reasons %v, valeurAttendue a paused add at %s without skip_checking, "+
					"rechecked by the client", added, fixture.client.guardCalls, result.Reasons, valeurAttendueChemin)
			}
			if fixture.progressOf(proposal.ID).Step != store.StepSeeded {
				t.Fatalf("valeurObtenue step %s, valeurAttendue seeded once the client confirmed 100 %%", fixture.progressOf(proposal.ID).Step)
			}
		})
	}
}

func TestSeed_remoteLinkForcesClientRecheckEvenWithRemoteStat(t *testing.T) {
	fixture, volume := newRemoteApplyFixture(t)
	volume.FileIDKnown.Store(true)
	volume.RemoteStat.Store(true)
	fixture.skipRecheck = true
	fixture.mapper = remoteClientMapper()
	candidate, _ := fixture.linkedProposal(movieOnly(), store.ProposalFull)
	fixture.client.guardResults[candidate.Infohash] = qbit.RecheckResult{Progress: 1, Complete: true}

	result := fixture.mustRunStage(StageSeed)

	added, present := fixture.client.added[candidate.Infohash]
	isRechecked := present && !added.options.SkipChecking && !added.options.Start && len(fixture.client.guardCalls) == 1
	if !isRechecked || result.Reasons[ReasonIdentityUnproven] != 1 {
		t.Fatalf("valeurObtenue %+v guard=%v reasons %v, valeurAttendue un ajout en pause sans skip_checking, revérifié par le "+
			"client, même avec stat distant (I4)", added, fixture.client.guardCalls, result.Reasons)
	}
	for _, link := range fixture.linksOf() {
		if !link.IdentityUnproven() || link.InodeReliable {
			t.Fatalf("valeurObtenue %+v, valeurAttendue un lien distant jamais prouvé, inode jamais fiable", link)
		}
	}
}

func TestSeed_refusesUnmappedRemoteLinkDir(t *testing.T) {
	cases := map[string]ClientPathMapper{
		"no mapper":                   nil,
		"local rule with same path":   linker.NewMapper([]config.PathRule{{From: "/srv/links", To: "/downloads/links"}}),
		"sftp rule on another volume": linker.NewMapper([]config.PathRule{{From: "sftp://other/srv/links", To: "/downloads/links"}}),
	}
	for name, mapper := range cases {
		t.Run(name, func(t *testing.T) {
			fixture, _ := newRemoteApplyFixture(t)
			fixture.mapper = mapper
			_, proposal := fixture.linkedProposal(movieOnly(), store.ProposalFull)

			result := fixture.mustRunStage(StageSeed)

			if len(fixture.client.added) != 0 || result.Reasons[ReasonClientPathUnmapped] != 1 || fixture.decisionOf(proposal.ID) != store.ProposalFailed {
				t.Fatalf("valeurObtenue added=%v reasons %v, valeurAttendue nothing added and client_path_unmapped", fixture.client.added, result.Reasons)
			}
		})
	}
}

func TestSeed_linkRecordedAsUnprovenNeverGetsSkipChecking(t *testing.T) {
	fixture := newApplyFixture(t)
	fixture.skipRecheck = true
	candidate, _ := fixture.linkedProposal(movieOnly(), store.ProposalFull)
	for _, link := range fixture.linksOf() {
		link.Volume = remoteVolumeName
		if err := fixture.store.RecordLink(context.Background(), link); err != nil {
			t.Fatal(err)
		}
	}

	fixture.mustRunStage(StageSeed)

	added := fixture.client.added[candidate.Infohash]
	if added.options.SkipChecking || added.options.Start || len(fixture.client.guardCalls) != 1 {
		t.Fatalf("valeurObtenue %+v guard=%v, valeurAttendue the recorded unproven identity to force the client recheck",
			added.options, fixture.client.guardCalls)
	}
}

func TestSeed_unprovenPartialAnnexStaysPausedAndCannotBeIsolated(t *testing.T) {
	fixture, _ := newRemoteApplyFixture(t)
	fixture.skipRecheck = true
	fixture.mapper = remoteClientMapper()
	candidate, proposal, linkRoot := fixture.linkedPartialAnnex(movieWithAnnex(patternBytes(50, nfoSize)))

	result := fixture.mustRunStage(StageSeed)

	added := fixture.client.added[candidate.Infohash]
	isPaused := !added.options.Start && !added.options.SkipChecking && len(fixture.client.resumed) == 0
	if !isPaused || result.SeededPausedIncomplete != 1 || fixture.progressOf(proposal.ID).Step != store.StepSeededPausedIncomplete {
		t.Fatalf("valeurObtenue %+v result %+v, valeurAttendue a paused partial_annex without skip_checking", added, result)
	}
	if _, err := fixture.isolate(candidate, proposal, linkRoot); !errors.Is(err, ErrIsolationImpossible) {
		t.Fatalf("valeurAttendue ErrIsolationImpossible on the remote link dir, valeurObtenue %v", err)
	}
}

func TestSeed_remoteUnavailableLeavesTheProposalLinkedForTheNextRun(t *testing.T) {
	fixture, volume := newRemoteApplyFixture(t)
	fixture.mapper = remoteClientMapper()
	candidate, proposal := fixture.linkedProposal(movieOnly(), store.ProposalFull)
	fixture.client.guardResults[candidate.Infohash] = qbit.RecheckResult{Progress: 1, Complete: true}
	volume.Unavailable.Store(true)

	unavailable := fixture.mustRunStage(StageSeed)

	isKept := fixture.decisionOf(proposal.ID) == store.ProposalAccepted && fixture.progressOf(proposal.ID).Step == store.StepLinked
	if len(fixture.client.added) != 0 || unavailable.Reasons[ReasonRemoteUnavailable] != 1 || !isKept {
		t.Fatalf("valeurObtenue added=%v result %+v, valeurAttendue remote_unavailable with the proposal kept linked", fixture.client.added, unavailable)
	}
	volume.Unavailable.Store(false)

	healed := fixture.mustRunStage(StageSeed)

	if healed.Seeded != 1 || fixture.progressOf(proposal.ID).Step != store.StepSeeded {
		t.Fatalf("valeurObtenue %+v, valeurAttendue the proposal seeded on the next run", healed)
	}
}
