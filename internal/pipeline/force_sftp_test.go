package pipeline

import (
	"errors"
	"testing"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/fsystest"
	"necromanciarr/internal/linker"
	"necromanciarr/internal/store"
)

// assertForceRefused vérifie le refus typé de Force et qu'il n'a rien écrit ni rien changé : aucun appel d'écriture
// sur le volume distant, proposition et étape telles qu'avant.
func assertForceRefused(t *testing.T, err error, valeurAttendueReason string, valeurAttendueCause error) {
	t.Helper()
	var refusal *ForceRefusal
	if !errors.As(err, &refusal) || refusal.Reason != valeurAttendueReason || refusal.Volume != remoteVolumeName {
		t.Fatalf("valeurObtenue %v, valeurAttendue a ForceRefusal %s on %s", err, valeurAttendueReason, remoteVolumeName)
	}
	if !errors.Is(err, valeurAttendueCause) || !errors.Is(err, ErrIsolationImpossible) {
		t.Errorf("valeurObtenue %v, valeurAttendue %v under ErrIsolationImpossible", err, valeurAttendueCause)
	}
}

func TestForce_refusedOnSFTPWithoutServerCopy(t *testing.T) {
	fixture, volume := newRemoteApplyFixture(t)
	candidate, proposal, linkRoot := fixture.linkedPartialAnnex(movieWithAnnex(patternBytes(50, nfoSize)))
	writesBefore, progressBefore, decisionBefore := len(volume.Writes()), fixture.progressOf(proposal.ID), fixture.decisionOf(proposal.ID)

	result, err := fixture.isolate(candidate, proposal, linkRoot)

	assertForceRefused(t, err, ReasonServerCopyUnavailable, fsys.ErrServerCopyUnavailable)
	assertNothingChanged(t, fixture, volume.Writes()[writesBefore:], result, proposal.ID, progressBefore, decisionBefore)
}

func TestForce_refusedWhenFreeSpaceUnknown(t *testing.T) {
	fixture, volume := newRemoteApplyFixture(t)
	volume.ServerCopy.Store(true)
	candidate, proposal, linkRoot := fixture.linkedPartialAnnex(movieWithAnnex(patternBytes(50, nfoSize)))
	writesBefore, progressBefore, decisionBefore := len(volume.Writes()), fixture.progressOf(proposal.ID), fixture.decisionOf(proposal.ID)

	result, err := fixture.isolate(candidate, proposal, linkRoot)

	assertForceRefused(t, err, ReasonFreeSpaceUnknown, linker.ErrFreeSpaceUnknown)
	assertNothingChanged(t, fixture, volume.Writes()[writesBefore:], result, proposal.ID, progressBefore, decisionBefore)
}

func TestForce_remoteOutageIsNotARefusal(t *testing.T) {
	fixture, volume := newRemoteApplyFixture(t)
	candidate, proposal, linkRoot := fixture.linkedPartialAnnex(movieWithAnnex(patternBytes(50, nfoSize)))
	volume.Unavailable.Store(true)

	_, err := IsolateStraddlingMedia(t.Context(), fixture.applyDeps(), proposal, candidate, linkRoot)

	var refusal *ForceRefusal
	if !errors.Is(err, fsys.ErrRemoteUnavailable) || errors.As(err, &refusal) {
		t.Errorf("valeurObtenue %v, valeurAttendue remote_unavailable, never a refusal", err)
	}
}

func assertNothingChanged(t *testing.T, fixture *applyFixture, writes []fsystest.RecordedWrite, result IsolationResult, proposalID int64,
	progressBefore store.ProposalProgress, decisionBefore store.ProposalDecision,
) {
	t.Helper()
	if len(writes) != 0 || len(result.Replaced) != 0 {
		t.Errorf("valeurObtenue writes %+v replaced %+v, valeurAttendue nothing written", writes, result.Replaced)
	}
	if valeurObtenue := fixture.progressOf(proposalID); valeurObtenue.Step != progressBefore.Step || valeurObtenue.LinkRoot != progressBefore.LinkRoot {
		t.Errorf("valeurObtenue %+v, valeurAttendue the progress unchanged %+v", valeurObtenue, progressBefore)
	}
	if valeurObtenue := fixture.decisionOf(proposalID); valeurObtenue != decisionBefore {
		t.Errorf("valeurObtenue %s, valeurAttendue the decision unchanged %s", valeurObtenue, decisionBefore)
	}
}
