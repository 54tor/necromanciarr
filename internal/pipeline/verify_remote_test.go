package pipeline

import (
	"context"
	"testing"

	"necromanciarr/internal/fsys/fsystest"
	"necromanciarr/internal/store"
)

func TestVerify_remoteOutageNeverMarksTheCandidateStale(t *testing.T) {
	fixture, volume := newRemoteApplyFixture(t)
	candidate := fixture.addCandidate("Movie.2020", "", movieOnly())
	volume.Unavailable.Store(true)

	unavailable := fixture.mustRun()

	if valeurObtenue := fixture.candidate(candidate.Infohash).State; valeurObtenue != candidate.State {
		t.Errorf("valeurObtenue %s, valeurAttendue the candidate left %s", valeurObtenue, candidate.State)
	}
	if unavailable.Reasons[ReasonRemoteUnavailable] != 1 || unavailable.Reasons[ReasonChangedFile] != 0 || len(fixture.proposals()) != 0 {
		t.Errorf("valeurObtenue %+v, valeurAttendue remote_unavailable and nothing decided", unavailable)
	}
	volume.Unavailable.Store(false)

	if healed := fixture.mustRun(); healed.Reasons[ReasonRemoteUnavailable] != 0 || fixture.candidate(candidate.Infohash).State != store.CandidateVerified {
		t.Errorf("valeurObtenue %+v state %s, valeurAttendue the candidate verified once the volume is back", healed, fixture.candidate(candidate.Infohash).State)
	}
}

// outageOnFirstRead coupe le volume factice à la première lecture : les fichiers déjà ouverts restent lisibles, seul
// le contrôle de finish (Stat) voit la panne.
type outageOnFirstRead struct{ volume *fsystest.RemoteVolume }

func (limiter outageOnFirstRead) WaitN(context.Context, int) error {
	limiter.volume.Unavailable.Store(true)
	return nil
}

func TestVerify_remoteOutageAtFinishNeverMarksTheCandidateStale(t *testing.T) {
	fixture, volume := newRemoteApplyFixture(t)
	candidate := fixture.addCandidate("Movie.2020", "", movieOnly())
	fixture.limiter = outageOnFirstRead{volume: volume}

	result := fixture.mustRun()

	if valeurObtenue := fixture.candidate(candidate.Infohash).State; valeurObtenue == store.CandidateStale || valeurObtenue == store.CandidateVerified {
		t.Errorf("valeurObtenue %s, valeurAttendue the candidate neither stale nor verified", valeurObtenue)
	}
	if result.Reasons[ReasonRemoteUnavailable] != 1 || result.Reasons[ReasonChangedFile] != 0 || len(fixture.proposals()) != 0 {
		t.Errorf("valeurObtenue %+v, valeurAttendue remote_unavailable at finish and nothing decided", result)
	}
}

func TestVerify_remoteOutageKeepsAnOpenProposalAsItIs(t *testing.T) {
	fixture, volume := newRemoteApplyFixture(t)
	first := fixture.addCandidate("Movie.2020", "", movieOnly())
	open := fixture.verifiedProposal(first, store.ProposalFull)
	// Un second torrent du même film arrive : il partage le fichier de la bibliothèque et attend sa vérification.
	second := fixture.addCandidate("Movie.2020.Other", "", movieOnly())
	before := fixture.proposals()
	volume.Unavailable.Store(true)

	result := fixture.mustRun()

	if valeurObtenue := fixture.candidate(second.Infohash).State; valeurObtenue != second.State {
		t.Errorf("valeurObtenue %s, valeurAttendue the second candidate left %s", valeurObtenue, second.State)
	}
	if valeurObtenue := fixture.candidate(first.Infohash).State; valeurObtenue != store.CandidateVerified {
		t.Errorf("valeurObtenue %s, valeurAttendue the first candidate still verified", valeurObtenue)
	}
	after := fixture.proposals()
	isKept := len(after) == len(before) && after[0].ID == open.ID && after[0].Decision == before[0].Decision && after[0].Generation == before[0].Generation
	if !isKept || result.Reasons[ReasonRemoteUnavailable] != 1 {
		t.Errorf("valeurObtenue %+v (result %+v), valeurAttendue the open proposal %+v untouched", after, result, before)
	}
}
