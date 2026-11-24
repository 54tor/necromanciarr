package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
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
	markVerified(t, openedStore, candidateID)

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
	markVerified(t, openedStore, candidateID)
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

func markVerified(t *testing.T, openedStore *Store, candidateID int64) {
	t.Helper()
	for _, state := range []CandidateState{CandidateFetched, CandidateVerified} {
		if err := openedStore.SetCandidateState(context.Background(), candidateID, state, testNow); err != nil {
			t.Fatal(err)
		}
	}
}

func countRows(t *testing.T, database queryer, query string, arguments ...any) int {
	t.Helper()
	rows, err := database.QueryContext(context.Background(), query, arguments...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var rowCount int
	if !rows.Next() {
		t.Fatalf("no row for %s", query)
	}
	if err := rows.Scan(&rowCount); err != nil {
		t.Fatal(err)
	}
	return rowCount
}

func TestStore_refusedUpgradeLeavesLegacyTablesIntact(t *testing.T) {
	filledLegacyPath := createLegacyDatabase(t, `
INSERT INTO media_files (path, size, mtime) VALUES ('/home/u/media/a.mkv', 1, 1);
INSERT INTO candidates (media_file_id, tracker, infohash, state) VALUES (1, 't', 'h', 'matched');`)
	if _, err := Open(filledLegacyPath); !errors.Is(err, ErrLegacyPipelineRows) {
		t.Fatalf("gotValue %v, expectedValue ErrLegacyPipelineRows", err)
	}

	database, err := sql.Open("sqlite", dataSourceName(filledLegacyPath))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	for _, table := range []string{"proposals", "verifications", "candidates"} {
		if columns, err := tableColumns(database, table); err != nil || len(columns) == 0 {
			t.Fatalf("legacy table %s dropped by a refused upgrade (err %v)", table, err)
		}
	}
	if gotValue := countRows(t, database, `SELECT count(*) FROM candidates`); gotValue != 1 {
		t.Fatalf("gotValue %d legacy rows, expectedValue 1", gotValue)
	}
}

func TestStore_concurrentOpenersUpgradeOnce(t *testing.T) {
	emptyLegacyPath := createLegacyDatabase(t, "")
	var waitGroup sync.WaitGroup
	openErrors := make([]error, 2)
	for openerIndex := range openErrors {
		waitGroup.Go(func() {
			openedStore, err := Open(emptyLegacyPath)
			if err == nil {
				err = openedStore.Close()
			}
			openErrors[openerIndex] = err
		})
	}
	waitGroup.Wait()
	for openerIndex, err := range openErrors {
		if err != nil {
			t.Fatalf("opener %d: %v", openerIndex, err)
		}
	}
	reopened, err := Open(emptyLegacyPath)
	if err != nil {
		t.Fatalf("open twice: %v", err)
	}
	_ = reopened.Close()
}

func TestStore_upgradesCascadingProposalsKeyToRestrict(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "necromanciarr.db")
	firstStore, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", dataSourceName(databasePath))
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.Exec(`DROP TABLE proposals;
CREATE TABLE proposals (id INTEGER PRIMARY KEY, candidate_id INTEGER REFERENCES torrent_candidates(id) ON DELETE CASCADE,
	group_id INTEGER, kind TEXT NOT NULL, decision TEXT NOT NULL, warnings TEXT NOT NULL DEFAULT '[]',
	created_at INTEGER NOT NULL, decided_at INTEGER);`)
	if closeErr := database.Close(); err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}

	openedStore, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = openedStore.Close() }()
	if gotValue := countRows(t, openedStore.database, `SELECT count(*) FROM pragma_foreign_key_list('proposals') WHERE on_delete = 'RESTRICT'`); gotValue != 1 {
		t.Fatalf("gotValue %d restricting keys, expectedValue 1", gotValue)
	}
}

func TestSetCandidateState_enforcesTransitions(t *testing.T) {
	testCases := []struct {
		path          []CandidateState
		expectedValue bool
	}{
		{[]CandidateState{CandidateFetched, CandidateVerified}, true},
		{[]CandidateState{CandidateVerified}, false},
		{[]CandidateState{CandidateFailed}, true},
		{[]CandidateState{CandidateFetched, CandidateStale}, true},
		{[]CandidateState{CandidateStale, CandidateMatched}, false},
		{[]CandidateState{CandidateStale, CandidateFetched}, false},
		{[]CandidateState{CandidateFetched, CandidateMatched}, false},
	}
	for _, testCase := range testCases {
		openedStore := openTestStore(t)
		candidateID, _ := saveTestCandidate(t, openedStore, "aa", insertTestMediaFile(t, openedStore, "/home/u/media/a.mkv", 10))
		var err error
		for _, state := range testCase.path {
			if err = openedStore.SetCandidateState(context.Background(), candidateID, state, testNow); err != nil {
				break
			}
		}
		gotValue := err == nil
		if gotValue != testCase.expectedValue || (err != nil && !errors.Is(err, ErrInvalidTransition)) {
			t.Errorf("path %v: gotValue err %v, expectedValue allowed=%v", testCase.path, err, testCase.expectedValue)
		}
	}
}

func TestSetProposalDecision_enforcesTransitions(t *testing.T) {
	testCases := []struct {
		path          []ProposalDecision
		expectedValue bool
	}{
		{[]ProposalDecision{ProposalAccepted, ProposalApplied}, true},
		{[]ProposalDecision{ProposalAccepted, ProposalFailed}, true},
		{[]ProposalDecision{ProposalRejected}, true},
		{[]ProposalDecision{ProposalSuperseded}, true},
		{[]ProposalDecision{ProposalApplied}, false},
		{[]ProposalDecision{ProposalRejected, ProposalAccepted}, false},
		{[]ProposalDecision{ProposalAccepted, ProposalApplied, ProposalSuperseded}, false},
		{[]ProposalDecision{ProposalAccepted, ProposalFailed, ProposalPending}, false},
		{[]ProposalDecision{ProposalPending}, false},
	}
	for _, testCase := range testCases {
		openedStore := openTestStore(t)
		candidateID, _ := saveTestCandidate(t, openedStore, "aa", insertTestMediaFile(t, openedStore, "/home/u/media/a.mkv", 10))
		markVerified(t, openedStore, candidateID)
		proposalID, err := openedStore.CreateProposal(context.Background(), Proposal{CandidateID: candidateID, Kind: ProposalFull}, testNow)
		if err != nil {
			t.Fatal(err)
		}
		for _, decision := range testCase.path {
			if err = openedStore.SetProposalDecision(context.Background(), proposalID, decision, testNow); err != nil {
				break
			}
		}
		gotValue := err == nil
		if gotValue != testCase.expectedValue || (err != nil && !errors.Is(err, ErrInvalidTransition)) {
			t.Errorf("path %v: gotValue err %v, expectedValue allowed=%v", testCase.path, err, testCase.expectedValue)
		}
	}
}

func TestRetryProposal_onlyFromFailed(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	candidateID, _ := saveTestCandidate(t, openedStore, "aa", insertTestMediaFile(t, openedStore, "/home/u/media/a.mkv", 10))
	markVerified(t, openedStore, candidateID)
	proposalID, err := openedStore.CreateProposal(ctx, Proposal{CandidateID: candidateID, Kind: ProposalFull}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := openedStore.RetryProposal(ctx, proposalID, testNow); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("gotValue %v, expectedValue ErrInvalidTransition from pending", err)
	}
	for _, decision := range []ProposalDecision{ProposalAccepted, ProposalFailed} {
		if err := openedStore.SetProposalDecision(ctx, proposalID, decision, testNow); err != nil {
			t.Fatal(err)
		}
	}
	if err := openedStore.RetryProposal(ctx, proposalID, testNow); err != nil {
		t.Fatalf("gotValue %v, expectedValue retry allowed from failed", err)
	}
}

func TestSetProposalDecision_acceptRequiresVerifiedCandidateExceptWithoutCandidate(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	candidateID, _ := saveTestCandidate(t, openedStore, "aa", insertTestMediaFile(t, openedStore, "/home/u/media/a.mkv", 10))
	torrentProposal, err := openedStore.CreateProposal(ctx, Proposal{CandidateID: candidateID, Kind: ProposalFull}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	dedupeProposal, err := openedStore.CreateProposal(ctx, Proposal{Kind: ProposalDedupe}, testNow)
	if err != nil {
		t.Fatal(err)
	}

	if err := openedStore.SetProposalDecision(ctx, torrentProposal, ProposalAccepted, testNow); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("gotValue %v, expectedValue ErrInvalidTransition for a matched candidate", err)
	}
	if err := openedStore.SetProposalDecision(ctx, dedupeProposal, ProposalAccepted, testNow); err != nil {
		t.Fatalf("gotValue %v, expectedValue a dedupe proposal accepted without candidate", err)
	}
}

// proposalsByDecision crée une proposition par décision donnée, pour le candidat.
func proposalsByDecision(t *testing.T, openedStore *Store, candidateID int64, paths map[string][]ProposalDecision) map[string]int64 {
	t.Helper()
	identifiers := map[string]int64{}
	for name, path := range paths {
		proposalID, err := openedStore.CreateProposal(context.Background(), Proposal{CandidateID: candidateID, GroupID: candidateID, Kind: ProposalFull}, testNow)
		if err != nil {
			t.Fatal(err)
		}
		for _, decision := range path {
			if err := openedStore.SetProposalDecision(context.Background(), proposalID, decision, testNow); err != nil {
				t.Fatal(err)
			}
		}
		identifiers[name] = proposalID
	}
	return identifiers
}

func decisionsByID(t *testing.T, openedStore *Store) map[int64]ProposalDecision {
	t.Helper()
	proposals, err := openedStore.ListProposals(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	decisions := map[int64]ProposalDecision{}
	for _, proposal := range proposals {
		decisions[proposal.ID] = proposal.Decision
	}
	return decisions
}

func TestInvalidation_supersedesOpenProposalsKeepsAppliedAndClearsGroup(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	mediaFile := insertTestMediaFile(t, openedStore, "/home/u/media/a.mkv", 10)
	candidateID, _ := saveTestCandidate(t, openedStore, "aa", mediaFile)
	if err := openedStore.RegroupCandidates(ctx); err != nil {
		t.Fatal(err)
	}
	markVerified(t, openedStore, candidateID)
	identifiers := proposalsByDecision(t, openedStore, candidateID, map[string][]ProposalDecision{
		"pending": nil, "accepted": {ProposalAccepted}, "applied": {ProposalAccepted, ProposalApplied}, "rejected": {ProposalRejected},
	})

	if _, err := openedStore.InvalidateCandidatesForFile(ctx, mediaFile.ID, testNow); err != nil {
		t.Fatal(err)
	}

	decisions := decisionsByID(t, openedStore)
	expectedDecisions := map[string]ProposalDecision{
		"pending": ProposalSuperseded, "accepted": ProposalSuperseded, "applied": ProposalApplied, "rejected": ProposalRejected,
	}
	for name, expectedValue := range expectedDecisions {
		if gotValue := decisions[identifiers[name]]; gotValue != expectedValue {
			t.Errorf("%s proposal: gotValue %s, expectedValue %s", name, gotValue, expectedValue)
		}
	}
	stale, err := openedStore.ListCandidatesByState(ctx, CandidateStale)
	if err != nil || len(stale) != 1 || stale[0].GroupID != 0 {
		t.Fatalf("gotValue %+v %v, expectedValue one stale candidate without group", stale, err)
	}
}

func TestSaveCandidate_resetSupersedesOpenProposalsAndNeverResurrectsThem(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	mediaFile := insertTestMediaFile(t, openedStore, "/home/u/media/a.mkv", 10)
	candidateID, _ := saveTestCandidate(t, openedStore, "aa", mediaFile)
	markVerified(t, openedStore, candidateID)
	identifiers := proposalsByDecision(t, openedStore, candidateID, map[string][]ProposalDecision{"pending": nil})
	if err := openedStore.SetCandidateState(ctx, candidateID, CandidateStale, testNow); err != nil {
		t.Fatal(err)
	}

	_, outcome := saveTestCandidate(t, openedStore, "aa", mediaFile)

	candidates, err := openedStore.ListCandidatesByState(ctx, CandidateMatched)
	if err != nil || len(candidates) != 1 || outcome != SaveUpdated {
		t.Fatalf("gotValue %+v %s %v, expectedValue the stale candidate back to matched", candidates, outcome, err)
	}
	if gotValue := decisionsByID(t, openedStore)[identifiers["pending"]]; gotValue != ProposalSuperseded {
		t.Fatalf("gotValue %s, expectedValue superseded", gotValue)
	}
}

func TestInvalidation_isAtomicWhenProposalUpdateFails(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	mediaFile := insertTestMediaFile(t, openedStore, "/home/u/media/a.mkv", 10)
	candidateID, _ := saveTestCandidate(t, openedStore, "aa", mediaFile)
	markVerified(t, openedStore, candidateID)
	proposalsByDecision(t, openedStore, candidateID, map[string][]ProposalDecision{"pending": nil})
	// Le déclencheur fait échouer la clôture des propositions au milieu de la transaction.
	if _, err := openedStore.database.Exec(`CREATE TRIGGER fail_supersede BEFORE UPDATE ON proposals
BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}

	if _, err := openedStore.InvalidateCandidatesForFile(ctx, mediaFile.ID, testNow); err == nil {
		t.Fatal("expectedValue the injected failure")
	}

	verified, err := openedStore.ListCandidatesByState(ctx, CandidateVerified)
	if err != nil || len(verified) != 1 {
		t.Fatalf("gotValue %+v %v, expectedValue the candidate still verified after rollback", verified, err)
	}
}

func TestSetCandidateState_repeatingFetchedOrVerifiedIsANoOp(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	candidateID, _ := saveTestCandidate(t, openedStore, "aa", insertTestMediaFile(t, openedStore, "/home/u/media/a.mkv", 10))
	for _, state := range []CandidateState{CandidateFetched, CandidateFetched, CandidateVerified, CandidateVerified} {
		if err := openedStore.SetCandidateState(ctx, candidateID, state, testNow); err != nil {
			t.Fatalf("repeat %s: gotValue %v, expectedValue nil", state, err)
		}
	}
	if err := openedStore.SetCandidateState(ctx, candidateID, CandidateFetched, testNow); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("gotValue %v, expectedValue ErrInvalidTransition for verified -> fetched", err)
	}
}

func TestSetCandidateState_failedSupersedesOpenProposals(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	candidateID, _ := saveTestCandidate(t, openedStore, "aa", insertTestMediaFile(t, openedStore, "/home/u/media/a.mkv", 10))
	markVerified(t, openedStore, candidateID)
	identifiers := proposalsByDecision(t, openedStore, candidateID, map[string][]ProposalDecision{
		"pending": nil, "accepted": {ProposalAccepted}, "failed": {ProposalAccepted, ProposalFailed},
	})

	if err := openedStore.SetCandidateState(ctx, candidateID, CandidateFailed, testNow); err != nil {
		t.Fatal(err)
	}

	decisions := decisionsByID(t, openedStore)
	expectedDecisions := map[string]ProposalDecision{"pending": ProposalSuperseded, "accepted": ProposalSuperseded, "failed": ProposalFailed}
	for name, expectedValue := range expectedDecisions {
		if gotValue := decisions[identifiers[name]]; gotValue != expectedValue {
			t.Errorf("%s proposal: gotValue %s, expectedValue %s", name, gotValue, expectedValue)
		}
	}
	if err := openedStore.RetryProposal(ctx, identifiers["failed"], testNow); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("gotValue %v, expectedValue ErrInvalidTransition: retry needs a verified candidate", err)
	}
}

func TestInvalidation_supersedesFailedProposals(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	mediaFile := insertTestMediaFile(t, openedStore, "/home/u/media/a.mkv", 10)
	candidateID, _ := saveTestCandidate(t, openedStore, "aa", mediaFile)
	markVerified(t, openedStore, candidateID)
	identifiers := proposalsByDecision(t, openedStore, candidateID, map[string][]ProposalDecision{"failed": {ProposalAccepted, ProposalFailed}})

	if _, err := openedStore.InvalidateCandidatesForFile(ctx, mediaFile.ID, testNow); err != nil {
		t.Fatal(err)
	}

	if gotValue := decisionsByID(t, openedStore)[identifiers["failed"]]; gotValue != ProposalSuperseded {
		t.Fatalf("gotValue %s, expectedValue superseded", gotValue)
	}
}

func TestRetryCandidate_onlyFromFailedAndClearsVerification(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	candidateID, _ := saveTestCandidate(t, openedStore, "aa", insertTestMediaFile(t, openedStore, "/home/u/media/a.mkv", 10))
	if err := openedStore.RetryCandidate(ctx, candidateID, testNow); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("gotValue %v, expectedValue ErrInvalidTransition from matched", err)
	}
	if err := openedStore.SetCandidateState(ctx, candidateID, CandidateFetched, testNow); err != nil {
		t.Fatal(err)
	}
	verification := Verification{CandidateID: candidateID, TotalPieces: 4, State: VerificationInProgress}
	if err := openedStore.SaveVerification(ctx, verification, testNow); err != nil {
		t.Fatal(err)
	}
	pendingID, err := openedStore.CreateProposal(ctx, Proposal{CandidateID: candidateID, Kind: ProposalFull}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := openedStore.SetCandidateState(ctx, candidateID, CandidateFailed, testNow); err != nil {
		t.Fatal(err)
	}

	if err := openedStore.RetryCandidate(ctx, candidateID, testNow); err != nil {
		t.Fatal(err)
	}

	matched, err := openedStore.ListCandidatesByState(ctx, CandidateMatched)
	if err != nil {
		t.Fatal(err)
	}
	_, verificationKept, err := openedStore.LoadVerification(ctx, candidateID)
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 1 || verificationKept || decisionsByID(t, openedStore)[pendingID] != ProposalSuperseded {
		t.Fatalf("gotValue matched %d verification kept %v decision %s", len(matched), verificationKept, decisionsByID(t, openedStore)[pendingID])
	}
}
