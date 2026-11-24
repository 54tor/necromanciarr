package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// legacyPipelineSchema reproduit les tables de la tâche 1, jamais écrites par aucun code.
const legacyPipelineSchema = `
CREATE TABLE media_files (id INTEGER PRIMARY KEY, path TEXT NOT NULL UNIQUE, size INTEGER NOT NULL, mtime INTEGER NOT NULL,
	tmdb_id INTEGER NOT NULL DEFAULT 0, tvdb_id INTEGER NOT NULL DEFAULT 0);
CREATE TABLE candidates (id INTEGER PRIMARY KEY, media_file_id INTEGER NOT NULL REFERENCES media_files(id),
	tracker TEXT NOT NULL, infohash TEXT NOT NULL, state TEXT NOT NULL);
CREATE TABLE verifications (id INTEGER PRIMARY KEY, candidate_id INTEGER NOT NULL REFERENCES candidates(id),
	verified_pieces INTEGER NOT NULL, total_pieces INTEGER NOT NULL, missing_pieces_json TEXT NOT NULL, state TEXT NOT NULL);
CREATE TABLE proposals (id INTEGER PRIMARY KEY, candidate_id INTEGER NOT NULL REFERENCES candidates(id),
	kind TEXT NOT NULL, decision TEXT NOT NULL);`

var testNow = time.Unix(1700000000, 0).UTC()

func createLegacyDatabase(t *testing.T, extraStatements string) string {
	t.Helper()
	databasePath := filepath.Join(t.TempDir(), "necromanciarr.db")
	database, err := sql.Open("sqlite", dataSourceName(databasePath))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	if _, err := database.Exec(legacyPipelineSchema + extraStatements); err != nil {
		t.Fatal(err)
	}
	return databasePath
}

func TestStore_upgradesOldPipelineTablesWhenEmptyAndRefusesWhenNot(t *testing.T) {
	emptyLegacyPath := createLegacyDatabase(t, "")
	upgradedStore, err := Open(emptyLegacyPath)
	if err != nil {
		t.Fatalf("empty legacy tables: %v", err)
	}
	for _, expectedColumn := range []struct{ table, column string }{
		{"torrent_candidates", "infohash"}, {"verifications", "verified_bitset"}, {"proposals", "group_id"},
	} {
		gotValue, err := hasColumn(upgradedStore.database, expectedColumn.table, expectedColumn.column)
		if err != nil || !gotValue {
			t.Errorf("%s.%s missing after upgrade (err %v)", expectedColumn.table, expectedColumn.column, err)
		}
	}
	legacyColumns, err := tableColumns(upgradedStore.database, "candidates")
	if err != nil || len(legacyColumns) != 0 {
		t.Errorf("legacy candidates table still present: %v %v", legacyColumns, err)
	}
	if err := upgradedStore.Close(); err != nil {
		t.Fatal(err)
	}

	filledLegacyPath := createLegacyDatabase(t, `
INSERT INTO media_files (path, size, mtime) VALUES ('/home/u/media/a.mkv', 1, 1);
INSERT INTO candidates (media_file_id, tracker, infohash, state) VALUES (1, 't', 'h', 'matched');`)
	_, err = Open(filledLegacyPath)
	if !errors.Is(err, ErrLegacyPipelineRows) {
		t.Fatalf("gotValue %v, expectedValue ErrLegacyPipelineRows", err)
	}
}

func saveTestCandidate(t *testing.T, openedStore *Store, infohash string, mediaFile MediaFile) (int64, SaveOutcome) {
	t.Helper()
	candidate := Candidate{Infohash: infohash, Tracker: "tracker-a", Name: "Film", TotalSize: mediaFile.Size}
	files := []CandidateFile{{FileIndex: 0, Path: "Film/film.mkv", Size: mediaFile.Size, MediaFileID: mediaFile.ID, MediaSize: mediaFile.Size, MediaModTime: mediaFile.ModTime}}
	candidateID, outcome, err := openedStore.SaveCandidate(context.Background(), candidate, files, testNow)
	if err != nil {
		t.Fatal(err)
	}
	return candidateID, outcome
}

func insertTestMediaFile(t *testing.T, openedStore *Store, path string, size int64) MediaFile {
	t.Helper()
	ctx := context.Background()
	if err := openedStore.UpsertMediaFile(ctx, MediaFile{Path: path, Size: size, ModTime: testNow, TMDBID: 1}); err != nil {
		t.Fatal(err)
	}
	mediaFiles, err := openedStore.ListMediaFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, mediaFile := range mediaFiles {
		if mediaFile.Path == path {
			return mediaFile
		}
	}
	t.Fatalf("media file %s not found", path)
	return MediaFile{}
}

func TestSaveCandidate_isIdempotentAndKeepsStateWhenFilesAreUnchanged(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	mediaFile := insertTestMediaFile(t, openedStore, "/home/u/media/film.mkv", 100)
	candidateID, firstOutcome := saveTestCandidate(t, openedStore, "aa", mediaFile)
	if err := openedStore.SetCandidateState(ctx, candidateID, CandidateVerified, testNow); err != nil {
		t.Fatal(err)
	}

	secondID, secondOutcome := saveTestCandidate(t, openedStore, "aa", mediaFile)

	verified, err := openedStore.ListCandidatesByState(ctx, CandidateVerified)
	if err != nil {
		t.Fatal(err)
	}
	if firstOutcome != SaveCreated || secondOutcome != SaveUnchanged || secondID != candidateID || len(verified) != 1 {
		t.Fatalf("gotValue outcomes %s/%s ids %d/%d verified %d", firstOutcome, secondOutcome, candidateID, secondID, len(verified))
	}
}

func TestInvalidateCandidatesForFile_marksStaleAndDropsVerification(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	mediaFile := insertTestMediaFile(t, openedStore, "/home/u/media/film.mkv", 100)
	candidateID, _ := saveTestCandidate(t, openedStore, "aa", mediaFile)
	verification := Verification{CandidateID: candidateID, TotalPieces: 8, VerifiedPieces: 3, CheckedBitset: []byte{0x07}, VerifiedBitset: []byte{0x07}, State: VerificationInProgress}
	if err := openedStore.SaveVerification(ctx, verification, testNow); err != nil {
		t.Fatal(err)
	}

	invalidatedCount, err := openedStore.InvalidateCandidatesForFile(ctx, mediaFile.ID, testNow)
	if err != nil {
		t.Fatal(err)
	}

	stale, err := openedStore.ListCandidatesByState(ctx, CandidateStale)
	if err != nil {
		t.Fatal(err)
	}
	_, found, err := openedStore.LoadVerification(ctx, candidateID)
	if err != nil {
		t.Fatal(err)
	}
	if invalidatedCount != 1 || len(stale) != 1 || found {
		t.Fatalf("gotValue invalidated %d stale %d verification kept %v", invalidatedCount, len(stale), found)
	}
}

func TestSaveVerification_roundTripsBitsets(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	candidateID, _ := saveTestCandidate(t, openedStore, "aa", insertTestMediaFile(t, openedStore, "/home/u/media/a.mkv", 10))
	expectedValue := Verification{
		CandidateID: candidateID, TotalPieces: 10, VerifiedPieces: 9, CheckedBitset: []byte{0xff, 0x03},
		VerifiedBitset: []byte{0xff, 0x01}, MediaComplete: true, Percent: 90, State: VerificationComplete,
	}
	if err := openedStore.SaveVerification(ctx, expectedValue, testNow); err != nil {
		t.Fatal(err)
	}

	gotValue, found, err := openedStore.LoadVerification(ctx, candidateID)
	if err != nil || !found {
		t.Fatalf("load: %v %v", found, err)
	}
	if !bytes.Equal(gotValue.VerifiedBitset, expectedValue.VerifiedBitset) || !bytes.Equal(gotValue.CheckedBitset, expectedValue.CheckedBitset) ||
		gotValue.Percent != 90 || !gotValue.MediaComplete || gotValue.State != VerificationComplete {
		t.Fatalf("gotValue %+v", gotValue)
	}
}

func TestSaveCandidateTorrent_refusesOversizedTorrent(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	candidateID, _ := saveTestCandidate(t, openedStore, "aa", insertTestMediaFile(t, openedStore, "/home/u/media/a.mkv", 10))

	err := openedStore.SaveCandidateTorrent(ctx, candidateID, make([]byte, MaxTorrentBytes+1), testNow)
	if !errors.Is(err, ErrTorrentTooLarge) {
		t.Fatalf("gotValue %v, expectedValue ErrTorrentTooLarge", err)
	}
	expectedValue := []byte("d4:infod4:name1:aee")
	if err := openedStore.SaveCandidateTorrent(ctx, candidateID, expectedValue, testNow); err != nil {
		t.Fatal(err)
	}
	gotValue, err := openedStore.CandidateTorrent(ctx, candidateID)
	if err != nil || !bytes.Equal(gotValue, expectedValue) {
		t.Fatalf("gotValue %q %v", gotValue, err)
	}
}

func TestSaveVerifiedFile_refusesOversizedDataAndStoresHash(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	candidateID, _ := saveTestCandidate(t, openedStore, "aa", insertTestMediaFile(t, openedStore, "/home/u/media/a.mkv", 10))

	oversized := VerifiedFile{CandidateID: candidateID, FileIndex: 1, Path: "Film/film.nfo", Data: make([]byte, MaxVerifiedFileBytes+1)}
	if err := openedStore.SaveVerifiedFile(ctx, oversized); !errors.Is(err, ErrVerifiedFileTooLarge) {
		t.Fatalf("gotValue %v, expectedValue ErrVerifiedFileTooLarge", err)
	}
	if err := openedStore.SaveVerifiedFile(ctx, VerifiedFile{CandidateID: candidateID, FileIndex: 1, Path: "Film/film.nfo", Data: []byte("nfo")}); err != nil {
		t.Fatal(err)
	}
	gotFiles, err := openedStore.ListVerifiedFiles(ctx, candidateID)
	if err != nil || len(gotFiles) != 1 || gotFiles[0].Size != 3 || len(gotFiles[0].SHA256) != 64 {
		t.Fatalf("gotValue %+v %v", gotFiles, err)
	}
}

func TestProposals_createListAndDecide(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	candidateID, _ := saveTestCandidate(t, openedStore, "aa", insertTestMediaFile(t, openedStore, "/home/u/media/a.mkv", 10))
	proposalID, err := openedStore.CreateProposal(ctx, Proposal{CandidateID: candidateID, GroupID: candidateID, Kind: ProposalFull, Warnings: []string{"w"}}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := openedStore.SetProposalDecision(ctx, proposalID, ProposalAccepted, testNow); err != nil {
		t.Fatal(err)
	}
	if err := openedStore.SetProposalDecision(ctx, proposalID+1, ProposalAccepted, testNow); !errors.Is(err, ErrProposalNotFound) {
		t.Fatalf("gotValue %v, expectedValue ErrProposalNotFound", err)
	}

	gotProposals, err := openedStore.ListProposals(ctx, ProposalAccepted)
	if err != nil || len(gotProposals) != 1 || gotProposals[0].Warnings[0] != "w" || gotProposals[0].DecidedAt.IsZero() {
		t.Fatalf("gotValue %+v %v", gotProposals, err)
	}
	if _, err := openedStore.CreateProposal(ctx, Proposal{CandidateID: candidateID, Kind: "bogus"}, testNow); !errors.Is(err, ErrUnknownProposalKind) {
		t.Fatalf("gotValue %v, expectedValue ErrUnknownProposalKind", err)
	}
}

func TestRegroupCandidates_sharesGroupForCompetingCandidates(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	sharedFile := insertTestMediaFile(t, openedStore, "/home/u/media/a.mkv", 10)
	aloneFile := insertTestMediaFile(t, openedStore, "/home/u/media/b.mkv", 20)
	firstID, _ := saveTestCandidate(t, openedStore, "aa", sharedFile)
	secondID, _ := saveTestCandidate(t, openedStore, "bb", sharedFile)
	aloneID, _ := saveTestCandidate(t, openedStore, "cc", aloneFile)

	if err := openedStore.RegroupCandidates(ctx); err != nil {
		t.Fatal(err)
	}

	candidates, err := openedStore.ListCandidatesByState(ctx, CandidateMatched)
	if err != nil {
		t.Fatal(err)
	}
	groupByID := map[int64]int64{}
	for _, candidate := range candidates {
		groupByID[candidate.ID] = candidate.GroupID
	}
	if groupByID[firstID] != firstID || groupByID[secondID] != firstID || groupByID[aloneID] != aloneID {
		t.Fatalf("gotValue groups %v", groupByID)
	}
}

func TestRunLock_isExclusiveUntilReleasedOrExpired(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	expiry := testNow.Add(time.Minute)
	if err := openedStore.AcquireRunLock(ctx, "holder-a", "scan", testNow, expiry); err != nil {
		t.Fatal(err)
	}
	if err := openedStore.AcquireRunLock(ctx, "holder-b", "match", testNow, expiry); !errors.Is(err, ErrRunLocked) {
		t.Fatalf("gotValue %v, expectedValue ErrRunLocked", err)
	}
	if err := openedStore.AcquireRunLock(ctx, "holder-b", "match", expiry.Add(time.Second), expiry.Add(time.Hour)); err != nil {
		t.Fatalf("expired lock not taken over: %v", err)
	}
	if err := openedStore.ReleaseRunLock(ctx, "holder-b"); err != nil {
		t.Fatal(err)
	}
	if err := openedStore.AcquireRunLock(ctx, "holder-c", "scan", testNow, expiry); err != nil {
		t.Fatalf("released lock not free: %v", err)
	}
}

func TestUpsertMediaFile_keepsKnownIDsWhenScanBringsNone(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	identified := insertTestMediaFile(t, openedStore, "/home/u/media/a.mkv", 10)

	if err := openedStore.UpsertMediaFile(ctx, MediaFile{Path: identified.Path, Size: 11, ModTime: testNow}); err != nil {
		t.Fatal(err)
	}

	gotFiles, err := openedStore.ListMediaFiles(ctx)
	if err != nil || len(gotFiles) != 1 || gotFiles[0].TMDBID != 1 || gotFiles[0].Size != 11 {
		t.Fatalf("gotValue %+v %v", gotFiles, err)
	}
}
