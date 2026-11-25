package store

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

const (
	sanitizedTestTorrent   = "d4:infod4:name5:a.mkvee"
	unsanitizedTestTorrent = "d8:announce24:https://t.example.com/pk4:infod4:name5:a.mkvee"
)

func candidateSource(t *testing.T, openedStore *Store, candidateID int64) string {
	t.Helper()
	candidates, err := openedStore.ListCandidatesByState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range candidates {
		if candidate.ID == candidateID {
			return candidate.Source
		}
	}
	t.Fatalf("candidate %d not found", candidateID)
	return ""
}

func TestSaveCandidateFetched_writesSourceTorrentAndSealedTogether(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	candidateID, _ := saveTestCandidate(t, openedStore, "aa", insertTestMediaFile(t, openedStore, "/home/u/media/a.mkv", 10))

	if err := openedStore.SaveCandidateFetched(ctx, candidateID, "torznab:alpha", []byte(sanitizedTestTorrent), testSealedBlob, testNow); err != nil {
		t.Fatal(err)
	}

	if gotValue := candidateSource(t, openedStore, candidateID); gotValue != "torznab:alpha" {
		t.Fatalf("gotValue source %q, expectedValue torznab:alpha", gotValue)
	}
	torrent, err := openedStore.CandidateTorrent(ctx, candidateID)
	if err != nil || string(torrent) != sanitizedTestTorrent {
		t.Fatalf("gotValue torrent %q (err %v), expectedValue the sanitized copy", torrent, err)
	}
	sealed, found, err := openedStore.CandidateSealed(ctx, candidateID)
	if err != nil || !found || !bytes.Equal(sealed, testSealedBlob) {
		t.Fatalf("gotValue sealed %v %v (err %v), expectedValue the sealed blob", sealed, found, err)
	}
}

// Une annonce fournie par trackers rend le scellement inutile : la copie assainie seule est gardée, un ancien bloc effacé.
func TestSaveCandidateFetched_withoutSealedCopyClearsTheBlob(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	candidateID, _ := saveTestCandidate(t, openedStore, "aa", insertTestMediaFile(t, openedStore, "/home/u/media/a.mkv", 10))
	sealTestCandidate(t, openedStore, candidateID)

	if err := openedStore.SaveCandidateFetched(ctx, candidateID, "torznab:alpha", []byte(sanitizedTestTorrent), nil, testNow); err != nil {
		t.Fatal(err)
	}

	torrent, _ := openedStore.CandidateTorrent(ctx, candidateID)
	if hasSealed(t, openedStore, candidateID) || string(torrent) != sanitizedTestTorrent || candidateSource(t, openedStore, candidateID) != "torznab:alpha" {
		t.Fatal("gotValue a leftover blob or a missing write, expectedValue the sanitized copy and source only")
	}
}

func TestSaveCandidateFetched_refusesWithoutWritingAnything(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	candidateID, _ := saveTestCandidate(t, openedStore, "aa", insertTestMediaFile(t, openedStore, "/home/u/media/a.mkv", 10))
	for name, testCase := range map[string]struct {
		torrent, sealed []byte
		expectedErr     error
	}{
		"announce in the clear copy": {[]byte(unsanitizedTestTorrent), testSealedBlob, ErrUnsanitizedTorrent},
		"sealed copy too large":      {[]byte(sanitizedTestTorrent), make([]byte, MaxSealedBytes+1), ErrSealedTooLarge},
		"torrent too large":          {make([]byte, MaxTorrentBytes+1), testSealedBlob, ErrTorrentTooLarge},
	} {
		t.Run(name, func(t *testing.T) {
			err := openedStore.SaveCandidateFetched(ctx, candidateID, "torznab:alpha", testCase.torrent, testCase.sealed, testNow)

			if !errors.Is(err, testCase.expectedErr) {
				t.Fatalf("gotValue %v, expectedValue %v", err, testCase.expectedErr)
			}
			torrent, _ := openedStore.CandidateTorrent(ctx, candidateID)
			if candidateSource(t, openedStore, candidateID) != SourceProvider || hasSealed(t, openedStore, candidateID) || torrent != nil {
				t.Fatal("gotValue a partial write, expectedValue nothing written")
			}
		})
	}
	if err := openedStore.SaveCandidateFetched(ctx, 4242, "torznab:alpha", []byte(sanitizedTestTorrent), testSealedBlob, testNow); !errors.Is(err, ErrCandidateNotFound) {
		t.Fatalf("gotValue %v, expectedValue ErrCandidateNotFound", err)
	}
}
