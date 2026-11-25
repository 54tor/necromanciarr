package pipeline

import (
	"testing"

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
