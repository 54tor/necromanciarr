package pipeline

import (
	"bytes"
	"context"
	"testing"

	"necromanciarr/internal/libraryprovider"
	"necromanciarr/internal/store"
)

// providerOnly ajoute la recherche au faux fournisseur pour en faire un sources.ProviderSource.
type providerOnly struct{ *fakeFetcher }

func (providerOnly) Search(context.Context, libraryprovider.Query) ([]libraryprovider.Release, error) {
	return nil, nil
}

func TestVerifyStage_realChainKeepsTheReasonOfAnOversizedProviderTorrent(t *testing.T) {
	fixture := newVerifyFixture(t)
	fixture.realChain = true
	candidate := fixture.addCandidate("Movie.2020", "", movieOnly())
	fixture.fetcher.torrents[candidate.Infohash] = append([]byte("d"), bytes.Repeat([]byte("x"), store.MaxTorrentBytes+1)...)

	result := fixture.mustRun()

	if result.Reasons[ReasonTorrentTooLarge] != 1 || fixture.candidate(candidate.Infohash).State != store.CandidateFailed {
		t.Fatalf("valeurObtenue %+v, valeurAttendue failed with %s as before the chain", result, ReasonTorrentTooLarge)
	}
}

func TestVerifyStage_realChainReportsInvalidBencode(t *testing.T) {
	for name, raw := range map[string][]byte{"not bencode": []byte("dnot bencode"), "duplicate top-level key": nil} {
		t.Run(name, func(t *testing.T) {
			fixture := newVerifyFixture(t)
			fixture.realChain = true
			candidate := fixture.addCandidate("Movie.2020", "", movieOnly())
			if raw == nil {
				raw = duplicatedAnnounce(fixture.fetcher.torrents[candidate.Infohash])
			}
			fixture.fetcher.torrents[candidate.Infohash] = raw

			result := fixture.mustRun()

			if result.Reasons[ReasonInvalidTorrent] != 1 {
				t.Fatalf("valeurObtenue %+v, valeurAttendue %s", result, ReasonInvalidTorrent)
			}
		})
	}
}

func TestVerifyStage_realChainStoresTheSanitizedProviderTorrent(t *testing.T) {
	fixture := newVerifyFixture(t)
	fixture.realChain = true
	candidate := fixture.addCandidate("Movie.2020", "", movieOnly())
	if !bytes.Contains(fixture.fetcher.torrents[candidate.Infohash], []byte("8:announce")) {
		t.Fatal("fixture torrent without announce: the test would prove nothing")
	}

	fixture.mustRun()

	stored, err := fixture.store.CandidateTorrent(context.Background(), candidate.ID)
	if err != nil || bytes.Contains(stored, []byte("announce")) || infohashOf(t, stored) != candidate.Infohash {
		t.Fatalf("valeurObtenue stored %q (err %v), valeurAttendue the sanitized torrent with the same infohash", stored, err)
	}
	if fixture.candidate(candidate.Infohash).State != store.CandidateVerified {
		t.Fatal("valeurAttendue verified")
	}
}
