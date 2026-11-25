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

func fetchedTestCandidate(mediaFile MediaFile) (Candidate, []CandidateFile) {
	candidate := Candidate{Infohash: "bb", Tracker: "tracker-a", Name: "Film", TotalSize: mediaFile.Size, Source: "torznab:alpha"}
	files := []CandidateFile{{FileIndex: 0, Path: "Film/film.mkv", Size: mediaFile.Size, MediaFileID: mediaFile.ID, MediaSize: mediaFile.Size, MediaModTime: mediaFile.ModTime}}
	return candidate, files
}

func TestSaveFetchedCandidate_createsAFetchedCandidateWithItsFilesAndCopies(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	mediaFile := insertTestMediaFile(t, openedStore, "/home/u/media/a.mkv", 10)
	candidate, files := fetchedTestCandidate(mediaFile)

	candidateID, outcome, err := openedStore.SaveFetchedCandidate(ctx, candidate, files, []byte(sanitizedTestTorrent), testSealedBlob, testNow)
	if err != nil || outcome != SaveCreated {
		t.Fatalf("gotValue outcome %q err %v, expectedValue created", outcome, err)
	}

	stored, err := openedStore.ListCandidatesByState(ctx, CandidateFetched)
	if err != nil || len(stored) != 1 || stored[0].ID != candidateID || stored[0].Source != "torznab:alpha" || !stored[0].HasSealed {
		t.Fatalf("gotValue %+v err %v, expectedValue one fetched candidate from torznab:alpha with its sealed copy", stored, err)
	}
	storedFiles, err := openedStore.ListCandidateFiles(ctx, candidateID)
	if err != nil || len(storedFiles) != 1 || storedFiles[0].MediaFileID != mediaFile.ID {
		t.Fatalf("gotValue files %+v err %v, expectedValue the association", storedFiles, err)
	}
	torrent, err := openedStore.CandidateTorrent(ctx, candidateID)
	if err != nil || string(torrent) != sanitizedTestTorrent {
		t.Fatalf("gotValue torrent %q err %v, expectedValue the sanitized copy", torrent, err)
	}
}

// La première source de la chaîne reste : un candidat déjà connu n'est ni réécrit ni scellé de nouveau.
func TestSaveFetchedCandidate_keepsAnExistingCandidateUntouched(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	mediaFile := insertTestMediaFile(t, openedStore, "/home/u/media/a.mkv", 10)
	existingID, _ := saveTestCandidate(t, openedStore, "bb", mediaFile)
	candidate, files := fetchedTestCandidate(mediaFile)

	candidateID, outcome, err := openedStore.SaveFetchedCandidate(ctx, candidate, files, []byte(sanitizedTestTorrent), testSealedBlob, testNow)

	if err != nil || outcome != SaveUnchanged || candidateID != existingID {
		t.Fatalf("gotValue id %d outcome %q err %v, expectedValue the existing candidate unchanged", candidateID, outcome, err)
	}
	if gotValue := candidateSource(t, openedStore, existingID); gotValue != SourceProvider {
		t.Fatalf("gotValue source %q, expectedValue %q kept", gotValue, SourceProvider)
	}
	if _, found, err := openedStore.CandidateSealed(ctx, existingID); err != nil || found {
		t.Fatalf("gotValue sealed found %v err %v, expectedValue no sealed copy", found, err)
	}
}

func TestSaveFetchedCandidate_refusesBeforeWriting(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	candidate, files := fetchedTestCandidate(insertTestMediaFile(t, openedStore, "/home/u/media/a.mkv", 10))
	providerCandidate := candidate
	providerCandidate.Source = SourceProvider

	refusals := []error{}
	_, _, err := openedStore.SaveFetchedCandidate(ctx, candidate, files, []byte(unsanitizedTestTorrent), nil, testNow)
	refusals = append(refusals, err)
	_, _, err = openedStore.SaveFetchedCandidate(ctx, candidate, files, []byte(sanitizedTestTorrent), make([]byte, MaxSealedBytes+1), testNow)
	refusals = append(refusals, err)
	_, _, err = openedStore.SaveFetchedCandidate(ctx, providerCandidate, files, []byte(sanitizedTestTorrent), nil, testNow)
	refusals = append(refusals, err)

	if !errors.Is(refusals[0], ErrUnsanitizedTorrent) || !errors.Is(refusals[1], ErrSealedTooLarge) || refusals[2] == nil {
		t.Fatalf("gotValue %v, expectedValue unsanitized, sealed too large and missing source refused", refusals)
	}
	if stored, err := openedStore.ListCandidatesByState(ctx); err != nil || len(stored) != 0 {
		t.Fatalf("gotValue %d candidates err %v, expectedValue nothing written", len(stored), err)
	}
}
