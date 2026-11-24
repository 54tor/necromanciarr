package pipeline

import (
	"bytes"
	"context"
	"testing"
	"time"

	"necromanciarr/internal/store"
)

var testSealedBlob = []byte("\x01fake-sealed-blob-0000")

// duplicatedAnnounce garde l'infohash (info intact) mais répète une clé de premier niveau : torrentcheck l'accepte,
// l'assainissement le refuse.
func duplicatedAnnounce(raw []byte) []byte {
	return append([]byte("d8:announce3:abc8:announce3:abc"), raw[1:]...)
}

func TestVerifyStage_sanitizesAProviderTorrentThatCarriesAnAnnounce(t *testing.T) {
	fixture := newVerifyFixture(t)
	candidate := fixture.addCandidate("Movie.2020", "", movieOnly())
	if !bytes.Contains(fixture.fetcher.torrents[candidate.Infohash], []byte("8:announce")) {
		t.Fatal("fixture torrent without announce: the test would prove nothing")
	}

	fixture.mustRun()

	if gotValue := fixture.candidate(candidate.Infohash).State; gotValue != store.CandidateVerified {
		t.Fatalf("gotValue state %s, expectedValue verified from the sanitized copy", gotValue)
	}
	stored, err := fixture.store.CandidateTorrent(context.Background(), candidate.ID)
	if err != nil || bytes.Contains(stored, []byte("announce")) {
		t.Fatalf("gotValue stored %q (err %v), expectedValue no announce", stored, err)
	}
	if gotValue := infohashOf(t, stored); gotValue != candidate.Infohash {
		t.Fatalf("gotValue infohash %s, expectedValue %s unchanged by sanitizing", gotValue, candidate.Infohash)
	}
}

func TestVerifyStage_unsanitizableTorrentFailsOnlyThatCandidate(t *testing.T) {
	fixture := newVerifyFixture(t)
	broken := fixture.addCandidate("Movie.2020", "", movieOnly())
	fixture.fetcher.torrents[broken.Infohash] = duplicatedAnnounce(fixture.fetcher.torrents[broken.Infohash])
	healthy := fixture.addCandidate("Other.2021", "", movieOnly())
	if broken.Infohash == healthy.Infohash {
		t.Fatal("fixture candidates share an infohash")
	}

	result, err := fixture.run(context.Background())

	if err != nil || result.Reasons[ReasonInvalidTorrent] != 1 {
		t.Fatalf("gotValue %+v %v, expectedValue one %s and the stage continuing", result, err, ReasonInvalidTorrent)
	}
	if gotValue := fixture.candidate(broken.Infohash).State; gotValue != store.CandidateFailed {
		t.Fatalf("gotValue state %s, expectedValue failed", gotValue)
	}
	if gotValue := fixture.candidate(healthy.Infohash).State; gotValue != store.CandidateVerified {
		t.Fatalf("gotValue state %s for the next candidate, expectedValue verified", gotValue)
	}
}

func TestVerifyStage_failureClearsTheSealedTorrent(t *testing.T) {
	fixture := newVerifyFixture(t)
	candidate := fixture.addCandidate("Movie.2020", "", movieOnly())
	fixture.fetcher.torrents[candidate.Infohash] = []byte("not a torrent")
	if err := fixture.store.SaveCandidateSealed(context.Background(), candidate.ID, testSealedBlob, fixedNow); err != nil {
		t.Fatal(err)
	}

	fixture.mustRun()

	_, found, err := fixture.store.CandidateSealed(context.Background(), candidate.ID)
	if err != nil || found || fixture.candidate(candidate.Infohash).State != store.CandidateFailed {
		t.Fatalf("gotValue sealed kept=%v (err %v), expectedValue cleared with the failure", found, err)
	}
}

func TestVerifyStage_purgesSealedTorrentsOlderThanTheRetention(t *testing.T) {
	fixture := newVerifyFixture(t)
	ctx := context.Background()
	old := fixture.addCandidate("Movie.2020", "", movieOnly())
	recent := fixture.addCandidate("Other.2021", "", movieOnly())
	sealedAt := map[int64]time.Time{old.ID: fixedNow.Add(-store.SealedRetention - time.Hour), recent.ID: fixedNow}
	for candidateID, moment := range sealedAt {
		if err := fixture.store.SaveCandidateSealed(ctx, candidateID, testSealedBlob, moment); err != nil {
			t.Fatal(err)
		}
	}

	fixture.mustRun()

	for candidateID, expectedValue := range map[int64]bool{old.ID: false, recent.ID: true} {
		_, gotValue, err := fixture.store.CandidateSealed(ctx, candidateID)
		if err != nil || gotValue != expectedValue {
			t.Fatalf("candidate %d: gotValue sealed %v (err %v), expectedValue %v", candidateID, gotValue, err, expectedValue)
		}
	}
}
