package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// testSealedBlob est un bloc opaque : le store ne déchiffre rien, seuls ses octets comptent ici.
var testSealedBlob = []byte("\x01fake-sealed-blob-0000")

func sealTestCandidate(t *testing.T, openedStore *Store, candidateID int64) {
	t.Helper()
	if err := openedStore.SaveCandidateSealed(context.Background(), candidateID, testSealedBlob, testNow); err != nil {
		t.Fatal(err)
	}
}

func hasSealed(t *testing.T, openedStore *Store, candidateID int64) bool {
	t.Helper()
	blob, found, err := openedStore.CandidateSealed(context.Background(), candidateID)
	if err != nil {
		t.Fatal(err)
	}
	if found && !bytes.Equal(blob, testSealedBlob) {
		t.Fatalf("gotValue blob %q, expectedValue the saved blob", blob)
	}
	return found
}

func pendingTestProposal(t *testing.T, openedStore *Store) (candidateID, proposalID int64) {
	t.Helper()
	candidateID, _ = saveTestCandidate(t, openedStore, "aa", insertTestMediaFile(t, openedStore, "/home/u/media/a.mkv", 10))
	markVerified(t, openedStore, candidateID)
	proposalID, err := openedStore.CreateProposal(context.Background(), Proposal{CandidateID: candidateID, Kind: ProposalFull}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	return candidateID, proposalID
}

func TestStore_clearsSealedTorrentWhenApplied(t *testing.T) {
	openedStore := openTestStore(t)
	candidateID, proposalID := acceptedTestProposal(t, openedStore)
	sealTestCandidate(t, openedStore, candidateID)

	if err := openedStore.ApplyProposal(context.Background(), proposalID, StepSeeded, "/home/u/links/a", testNow); err != nil {
		t.Fatal(err)
	}
	if hasSealed(t, openedStore, candidateID) {
		t.Fatal("gotValue a sealed torrent after applied, expectedValue none")
	}
}

func TestStore_clearsSealedTorrentWhenRejected(t *testing.T) {
	openedStore := openTestStore(t)
	candidateID, proposalID := pendingTestProposal(t, openedStore)
	sealTestCandidate(t, openedStore, candidateID)

	if err := openedStore.SetProposalDecision(context.Background(), proposalID, ProposalRejected, testNow); err != nil {
		t.Fatal(err)
	}
	if hasSealed(t, openedStore, candidateID) {
		t.Fatal("gotValue a sealed torrent after rejected, expectedValue none")
	}
}

func TestStore_clearsSealedTorrentWhenSuperseded(t *testing.T) {
	ctx := context.Background()
	supersedingPaths := map[string]func(openedStore *Store, candidateID, proposalID int64) error{
		"SetProposalDecision": func(openedStore *Store, _, proposalID int64) error {
			return openedStore.SetProposalDecision(ctx, proposalID, ProposalSuperseded, testNow)
		},
		"candidate stale": func(openedStore *Store, candidateID, _ int64) error {
			return openedStore.SetCandidateState(ctx, candidateID, CandidateStale, testNow)
		},
		"candidate failed": func(openedStore *Store, candidateID, _ int64) error {
			return openedStore.SetCandidateState(ctx, candidateID, CandidateFailed, testNow)
		},
		"media file deleted": func(openedStore *Store, candidateID, _ int64) error {
			files, err := openedStore.ListCandidateFiles(ctx, candidateID)
			if err != nil {
				return err
			}
			return openedStore.DeleteMediaFile(ctx, files[0].MediaFileID, testNow)
		},
		"files reassociated": func(openedStore *Store, _, _ int64) error {
			otherFile := insertTestMediaFile(t, openedStore, "/home/u/media/b.mkv", 10)
			saveTestCandidate(t, openedStore, "aa", otherFile)
			return nil
		},
	}
	for pathName, supersede := range supersedingPaths {
		t.Run(pathName, func(t *testing.T) {
			openedStore := openTestStore(t)
			candidateID, proposalID := pendingTestProposal(t, openedStore)
			sealTestCandidate(t, openedStore, candidateID)

			if err := supersede(openedStore, candidateID, proposalID); err != nil {
				t.Fatal(err)
			}
			if gotValue := decisionsByID(t, openedStore)[proposalID]; gotValue != ProposalSuperseded {
				t.Fatalf("gotValue decision %s, expectedValue superseded", gotValue)
			}
			if hasSealed(t, openedStore, candidateID) {
				t.Fatal("gotValue a sealed torrent after superseded, expectedValue none")
			}
		})
	}
}

// keepingDecisions sont les décisions qui laissent la proposition ouverte : le bloc scellé peut encore servir au seed.
var keepingDecisions = []ProposalDecision{ProposalPending, ProposalAccepted, ProposalFailed}

// pathTo mène une proposition neuve (pending) jusqu'à la décision voulue.
var pathTo = map[ProposalDecision][]ProposalDecision{
	ProposalPending:  nil,
	ProposalAccepted: {ProposalAccepted},
	ProposalRejected: {ProposalRejected},
}

// TestStore_everyDecisionIntoAClosedStateClearsTheSealed parcourt la table des transitions du store : une décision
// ajoutée sans être classée, ou une transition vers un état clos qui garde le bloc, fait échouer le test.
func TestStore_everyDecisionIntoAClosedStateClearsTheSealed(t *testing.T) {
	ctx := context.Background()
	for fromDecision, targets := range proposalTransitions {
		for _, toDecision := range targets {
			clearing := slices.Contains(sealedClearingDecisions, toDecision)
			if !clearing && !slices.Contains(keepingDecisions, toDecision) {
				t.Fatalf("decision %s is neither clearing nor keeping: classify it", toDecision)
			}
			t.Run(string(fromDecision)+"->"+string(toDecision), func(t *testing.T) {
				openedStore := openTestStore(t)
				candidateID, proposalID := pendingTestProposal(t, openedStore)
				path, known := pathTo[fromDecision]
				if !known {
					t.Fatalf("no path to %s", fromDecision)
				}
				for _, decision := range path {
					if err := openedStore.SetProposalDecision(ctx, proposalID, decision, testNow); err != nil {
						t.Fatal(err)
					}
				}
				sealTestCandidate(t, openedStore, candidateID)
				if err := openedStore.SetProposalDecision(ctx, proposalID, toDecision, testNow); err != nil {
					t.Fatal(err)
				}
				if gotValue := hasSealed(t, openedStore, candidateID); gotValue == clearing {
					t.Fatalf("gotValue sealed kept=%v, expectedValue kept=%v", gotValue, !clearing)
				}
			})
		}
	}
}

func TestStore_keepsTheSealedWhileAnotherProposalOfTheCandidateIsOpen(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	candidateID, fullID := pendingTestProposal(t, openedStore)
	conflictID, err := openedStore.CreateProposal(ctx, Proposal{CandidateID: candidateID, Kind: ProposalConflict}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	sealTestCandidate(t, openedStore, candidateID)

	if err := openedStore.SetProposalDecision(ctx, conflictID, ProposalRejected, testNow); err != nil {
		t.Fatal(err)
	}
	if !hasSealed(t, openedStore, candidateID) {
		t.Fatal("gotValue the sealed cleared by the conflict, expectedValue kept for the pending full proposal")
	}
	if err := openedStore.SetProposalDecision(ctx, fullID, ProposalRejected, testNow); err != nil {
		t.Fatal(err)
	}
	if hasSealed(t, openedStore, candidateID) {
		t.Fatal("gotValue a sealed torrent once every proposal is closed, expectedValue none")
	}
}

func TestStore_sealedClearIsPartOfTheDecisionTransaction(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	candidateID, proposalID := pendingTestProposal(t, openedStore)
	sealTestCandidate(t, openedStore, candidateID)

	transaction, err := openedStore.database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `UPDATE proposals SET decision = 'rejected' WHERE id = ?`, proposalID); err != nil {
		t.Fatal(err)
	}
	var sealedInsideTransaction []byte
	if err := transaction.QueryRowContext(ctx, `SELECT torrent_sealed FROM torrent_candidates WHERE id = ?`, candidateID).
		Scan(&sealedInsideTransaction); err != nil {
		t.Fatal(err)
	}
	if sealedInsideTransaction != nil {
		t.Fatal("gotValue the sealed still present inside the decision transaction, expectedValue cleared by the same statement")
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	if !hasSealed(t, openedStore, candidateID) {
		t.Fatal("gotValue the sealed cleared by a rolled back decision, expectedValue kept")
	}
}

func TestStore_clearsSealedForConfiguredTrackers(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	mediaFile := insertTestMediaFile(t, openedStore, "/home/u/media/a.mkv", 10)
	candidateIDs := map[string]int64{}
	for _, tracker := range []string{"tracker-a", "Tracker-A", "tracker-b"} {
		candidateID, _, err := openedStore.SaveCandidate(ctx, Candidate{Infohash: "aa", Tracker: tracker, Name: "Film", TotalSize: 10},
			[]CandidateFile{{Path: "Film/film.mkv", Size: 10, MediaFileID: mediaFile.ID, MediaSize: 10, MediaModTime: mediaFile.ModTime}}, testNow)
		if err != nil {
			t.Fatal(err)
		}
		sealTestCandidate(t, openedStore, candidateID)
		candidateIDs[tracker] = candidateID
	}

	gotValue, err := openedStore.ClearSealedForTrackers(ctx, []string{"tracker-a", "unknown"})
	if err != nil || gotValue != 1 {
		t.Fatalf("gotValue %d cleared (err %v), expectedValue 1", gotValue, err)
	}
	for tracker, expectedValue := range map[string]bool{"tracker-a": false, "Tracker-A": true, "tracker-b": true} {
		if gotValue := hasSealed(t, openedStore, candidateIDs[tracker]); gotValue != expectedValue {
			t.Errorf("tracker %q: gotValue sealed %v, expectedValue %v (byte-exact compare)", tracker, gotValue, expectedValue)
		}
	}
	if gotValue, err := openedStore.ClearSealedForTrackers(ctx, nil); err != nil || gotValue != 0 {
		t.Fatalf("gotValue %d cleared for no tracker (err %v), expectedValue 0", gotValue, err)
	}
}

// v1Schema est le schéma complet d'avant ce lot : celui d'aujourd'hui sans les deux colonnes ajoutées.
func v1Schema(t *testing.T) string {
	t.Helper()
	legacy := schema
	for _, addedColumn := range []string{
		"\tsource TEXT NOT NULL DEFAULT 'provider',\n",
		"\ttorrent_sealed BLOB CHECK (torrent_sealed IS NULL OR length(torrent_sealed) <= 8388736),\n",
	} {
		if !strings.Contains(legacy, addedColumn) {
			t.Fatalf("schema lost the column %q: update the v1 fixture", addedColumn)
		}
		legacy = strings.Replace(legacy, addedColumn, "", 1)
	}
	return legacy
}

func createV1Database(t *testing.T) string {
	t.Helper()
	databasePath := filepath.Join(t.TempDir(), "necromanciarr.db")
	database, err := sql.Open("sqlite", dataSourceName(databasePath))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	if _, err := database.Exec(v1Schema(t) + `
INSERT INTO media_files (path, size, mtime) VALUES ('/home/u/media/a.mkv', 10, 1);
INSERT INTO torrent_candidates (infohash, tracker, name, total_size, torrent, state, created_at, updated_at, generation)
	VALUES ('aa', 'tracker-a', 'Film', 10, x'64656565', 'verified', 1, 2, 3);
INSERT INTO proposals (candidate_id, group_id, kind, decision, created_at, generation) VALUES (1, 1, 'full', 'pending', 1, 3);`); err != nil {
		t.Fatal(err)
	}
	return databasePath
}

func TestStore_upgradesV1DatabaseWithSourceDefaultProvider(t *testing.T) {
	ctx := context.Background()
	databasePath := createV1Database(t)
	for openingIndex := range 2 {
		openedStore, err := Open(databasePath)
		if err != nil {
			t.Fatalf("opening %d: %v", openingIndex, err)
		}
		candidates, err := openedStore.ListCandidatesByState(ctx)
		if err != nil || len(candidates) != 1 {
			t.Fatalf("opening %d: gotValue %+v (err %v)", openingIndex, candidates, err)
		}
		gotValue := candidates[0]
		if gotValue.Source != SourceProvider || gotValue.Infohash != "aa" || gotValue.State != CandidateVerified || gotValue.Generation != 3 {
			t.Fatalf("opening %d: gotValue %+v, expectedValue the v1 row intact with source provider", openingIndex, gotValue)
		}
		raw, err := openedStore.CandidateTorrent(ctx, gotValue.ID)
		if err != nil || string(raw) != "deee" {
			t.Fatalf("opening %d: gotValue torrent %q (err %v)", openingIndex, raw, err)
		}
		if hasSealed(t, openedStore, gotValue.ID) {
			t.Fatalf("opening %d: gotValue a sealed torrent on a v1 row", openingIndex)
		}
		sealTestCandidate(t, openedStore, gotValue.ID)
		proposals, err := openedStore.ListProposals(ctx, ProposalPending)
		if err != nil || len(proposals) != 1 {
			t.Fatalf("opening %d: gotValue proposals %+v (err %v)", openingIndex, proposals, err)
		}
		if err := openedStore.SetProposalDecision(ctx, proposals[0].ID, ProposalRejected, testNow); err != nil {
			t.Fatal(err)
		}
		if hasSealed(t, openedStore, gotValue.ID) {
			t.Fatalf("opening %d: gotValue the clear trigger missing after upgrade", openingIndex)
		}
		if _, err := openedStore.database.ExecContext(ctx, `UPDATE proposals SET decision = 'pending'`); err != nil {
			t.Fatal(err)
		}
		if err := openedStore.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStore_upgradedSealedColumnKeepsItsLengthCheck(t *testing.T) {
	openedStore, err := Open(createV1Database(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = openedStore.Close() })
	_, err = openedStore.database.ExecContext(context.Background(), `UPDATE torrent_candidates SET torrent_sealed = ?`,
		make([]byte, MaxSealedBytes+1))
	if err == nil {
		t.Fatal("gotValue an oversized sealed blob accepted by an upgraded base, expectedValue the CHECK to refuse it")
	}
}

func TestStore_concurrentOpenersUpgradeAV1DatabaseOnce(t *testing.T) {
	databasePath := createV1Database(t)
	var waitGroup sync.WaitGroup
	openErrors := make([]error, 2)
	for openerIndex := range openErrors {
		waitGroup.Go(func() {
			openedStore, err := Open(databasePath)
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
}

func TestSaveCandidate_recordsTheSourceAndKeepsTheFirstOne(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	mediaFile := insertTestMediaFile(t, openedStore, "/home/u/media/a.mkv", 10)
	files := []CandidateFile{{Path: "Film/film.mkv", Size: 10, MediaFileID: mediaFile.ID, MediaSize: 10, MediaModTime: mediaFile.ModTime}}
	for _, candidate := range []Candidate{
		{Infohash: "aa", Tracker: "tracker-a", Name: "Film", TotalSize: 10},
		{Infohash: "bb", Tracker: "tracker-a", Name: "Film", TotalSize: 10, Source: "torznab:alpha"},
		{Infohash: "bb", Tracker: "tracker-a", Name: "Film", TotalSize: 10, Source: "torznab:beta"},
	} {
		if _, _, err := openedStore.SaveCandidate(ctx, candidate, files, testNow); err != nil {
			t.Fatal(err)
		}
	}
	candidates, err := openedStore.ListCandidatesByState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gotValue := []string{candidates[0].Source, candidates[1].Source}
	if expectedValue := []string{SourceProvider, "torznab:alpha"}; !slices.Equal(gotValue, expectedValue) {
		t.Fatalf("gotValue %v, expectedValue %v", gotValue, expectedValue)
	}
}

func TestCandidateSealed_reportsMissingCandidateAndMissingBlob(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	if _, _, err := openedStore.CandidateSealed(ctx, 42); !errors.Is(err, ErrCandidateNotFound) {
		t.Fatalf("gotValue %v, expectedValue ErrCandidateNotFound", err)
	}
	if err := openedStore.SaveCandidateSealed(ctx, 42, testSealedBlob, testNow); !errors.Is(err, ErrCandidateNotFound) {
		t.Fatalf("gotValue %v, expectedValue ErrCandidateNotFound", err)
	}
	candidateID, _ := saveTestCandidate(t, openedStore, "aa", insertTestMediaFile(t, openedStore, "/home/u/media/a.mkv", 10))
	if hasSealed(t, openedStore, candidateID) {
		t.Fatal("gotValue a sealed torrent before any save")
	}
	if err := openedStore.SaveCandidateSealed(ctx, candidateID, make([]byte, MaxSealedBytes+1), testNow); !errors.Is(err, ErrSealedTooLarge) {
		t.Fatalf("gotValue %v, expectedValue ErrSealedTooLarge", err)
	}
	if err := openedStore.SaveCandidateSealed(ctx, candidateID, make([]byte, MaxSealedBytes), testNow); err != nil {
		t.Fatalf("gotValue %v for a blob at the limit, expectedValue the CHECK of the column to match MaxSealedBytes", err)
	}
	sealTestCandidate(t, openedStore, candidateID)
	if !hasSealed(t, openedStore, candidateID) {
		t.Fatal("gotValue no sealed torrent after save")
	}
	if err := openedStore.SaveCandidateSealed(ctx, candidateID, nil, testNow); err != nil || hasSealed(t, openedStore, candidateID) {
		t.Fatalf("gotValue err %v or a blob kept, expectedValue an empty save to clear it", err)
	}
}

func saveSealedCandidates(t *testing.T, openedStore *Store, infohashes ...string) []int64 {
	t.Helper()
	mediaFile := insertTestMediaFile(t, openedStore, "/home/u/media/a.mkv", 10)
	files := []CandidateFile{{Path: "Film/film.mkv", Size: 10, MediaFileID: mediaFile.ID, MediaSize: 10, MediaModTime: mediaFile.ModTime}}
	var candidateIDs []int64
	for _, infohash := range infohashes {
		candidateID, _, err := openedStore.SaveCandidate(context.Background(), Candidate{
			Infohash: infohash, Tracker: "tracker-a", Name: "Film", TotalSize: 10, Source: "torznab:alpha",
		}, files, testNow)
		if err != nil {
			t.Fatal(err)
		}
		candidateIDs = append(candidateIDs, candidateID)
	}
	return candidateIDs
}

// visitedSealed parcourt les blocs scellés et compte les octets remis au visiteur.
func visitedSealed(t *testing.T, openedStore *Store, limit int, maxBytes int64) (visited []SealedTorrent, visitedBytes int64, unvisited int) {
	t.Helper()
	unvisited, err := openedStore.EachSealedTorrent(context.Background(), limit, maxBytes, func(torrent SealedTorrent) error {
		visited = append(visited, torrent)
		visitedBytes += int64(len(torrent.Blob))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return visited, visitedBytes, unvisited
}

func TestEachSealedTorrent_visitsBlobsOneAtATimeWithTheStoredBinding(t *testing.T) {
	openedStore := openTestStore(t)
	candidateIDs := saveSealedCandidates(t, openedStore, "aa", "bb", "cc")
	sealTestCandidate(t, openedStore, candidateIDs[0])
	sealTestCandidate(t, openedStore, candidateIDs[2])

	gotValue, _, unvisited := visitedSealed(t, openedStore, 10, 1<<20)

	expectedValue := []SealedTorrent{
		{CandidateID: candidateIDs[0], Infohash: "aa", Tracker: "tracker-a", Source: "torznab:alpha", Blob: testSealedBlob},
		{CandidateID: candidateIDs[2], Infohash: "cc", Tracker: "tracker-a", Source: "torznab:alpha", Blob: testSealedBlob},
	}
	if !reflect.DeepEqual(gotValue, expectedValue) || unvisited != 0 {
		t.Fatalf("gotValue %+v (unvisited %d), expectedValue %+v", gotValue, unvisited, expectedValue)
	}
}

func TestEachSealedTorrent_stopsAtTheCountOrByteBudgetAndCountsTheRest(t *testing.T) {
	openedStore := openTestStore(t)
	for _, candidateID := range saveSealedCandidates(t, openedStore, "aa", "bb", "cc") {
		sealTestCandidate(t, openedStore, candidateID)
	}
	blobSize := int64(len(testSealedBlob))
	testCases := []struct {
		name                   string
		limit                  int
		maxBytes               int64
		expectedVisited        int
		expectedUnvisitedBlobs int
	}{
		{"count limit", 1, 1 << 20, 1, 2},
		{"byte budget", 10, 2*blobSize + 1, 2, 1},
		{"budget below one blob", 10, blobSize - 1, 0, 3},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			visited, visitedBytes, unvisited := visitedSealed(t, openedStore, testCase.limit, testCase.maxBytes)
			if len(visited) != testCase.expectedVisited || unvisited != testCase.expectedUnvisitedBlobs || visitedBytes > testCase.maxBytes {
				t.Fatalf("gotValue %d visited, %d bytes, %d unvisited, expectedValue %d visited, %d unvisited, at most %d bytes",
					len(visited), visitedBytes, unvisited, testCase.expectedVisited, testCase.expectedUnvisitedBlobs, testCase.maxBytes)
			}
		})
	}
}

func TestEachSealedTorrent_stopsOnVisitorErrorAndOnCancellation(t *testing.T) {
	openedStore := openTestStore(t)
	for _, candidateID := range saveSealedCandidates(t, openedStore, "aa", "bb") {
		sealTestCandidate(t, openedStore, candidateID)
	}
	expectedValue := errors.New("visitor stopped")
	visits := 0
	_, err := openedStore.EachSealedTorrent(context.Background(), 10, 1<<20, func(SealedTorrent) error {
		visits++
		return expectedValue
	})
	if !errors.Is(err, expectedValue) || visits != 1 {
		t.Fatalf("gotValue %v after %d visits, expectedValue the visitor error after 1", err, visits)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := openedStore.EachSealedTorrent(cancelled, 10, 1<<20, func(SealedTorrent) error { return nil }); err == nil {
		t.Fatal("gotValue no error on a cancelled context, expectedValue the cancellation")
	}
}

func TestStore_clearsSealedWhenTheCandidateFailsOrGoesStale(t *testing.T) {
	ctx := context.Background()
	transitions := map[string]struct {
		withProposal bool
		transition   func(openedStore *Store, candidateID int64) error
	}{
		"failed without proposal": {false, func(openedStore *Store, candidateID int64) error {
			return openedStore.SetCandidateState(ctx, candidateID, CandidateFailed, testNow)
		}},
		"failed with a pending proposal": {true, func(openedStore *Store, candidateID int64) error {
			return openedStore.SetCandidateState(ctx, candidateID, CandidateFailed, testNow)
		}},
		"stale without proposal": {false, func(openedStore *Store, candidateID int64) error {
			return openedStore.SetCandidateState(ctx, candidateID, CandidateStale, testNow)
		}},
		"local file invalidated without proposal": {false, func(openedStore *Store, candidateID int64) error {
			files, err := openedStore.ListCandidateFiles(ctx, candidateID)
			if err != nil {
				return err
			}
			_, err = openedStore.InvalidateCandidatesForFile(ctx, files[0].MediaFileID, testNow)
			return err
		}},
	}
	for transitionName, scenario := range transitions {
		t.Run(transitionName, func(t *testing.T) {
			openedStore := openTestStore(t)
			candidateID, _ := saveTestCandidate(t, openedStore, "aa", insertTestMediaFile(t, openedStore, "/home/u/media/a.mkv", 10))
			if scenario.withProposal {
				markVerified(t, openedStore, candidateID)
				if _, err := openedStore.CreateProposal(ctx, Proposal{CandidateID: candidateID, Kind: ProposalFull}, testNow); err != nil {
					t.Fatal(err)
				}
			}
			sealTestCandidate(t, openedStore, candidateID)

			if err := scenario.transition(openedStore, candidateID); err != nil {
				t.Fatal(err)
			}
			if hasSealed(t, openedStore, candidateID) {
				t.Fatal("gotValue a sealed torrent kept on a failed or stale candidate, expectedValue none")
			}
		})
	}
}

func TestStore_keepsTheSealedOnMatchedFetchedAndVerifiedCandidates(t *testing.T) {
	openedStore := openTestStore(t)
	candidateID, _ := saveTestCandidate(t, openedStore, "aa", insertTestMediaFile(t, openedStore, "/home/u/media/a.mkv", 10))
	sealTestCandidate(t, openedStore, candidateID)
	markVerified(t, openedStore, candidateID)
	if !hasSealed(t, openedStore, candidateID) {
		t.Fatal("gotValue the sealed cleared by fetched or verified, expectedValue kept")
	}
}

func TestStore_purgesSealedTorrentsOlderThanTheRetention(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	candidateIDs := saveSealedCandidates(t, openedStore, "aa", "bb")
	sealedAt := map[int64]time.Time{candidateIDs[0]: testNow, candidateIDs[1]: testNow.Add(20 * 24 * time.Hour)}
	for candidateID, moment := range sealedAt {
		if err := openedStore.SaveCandidateSealed(ctx, candidateID, testSealedBlob, moment); err != nil {
			t.Fatal(err)
		}
	}
	fakeNow := testNow.Add(SealedRetention + time.Hour)

	gotValue, err := openedStore.PurgeSealedOlderThan(ctx, fakeNow, SealedRetention)
	if err != nil || gotValue != 1 {
		t.Fatalf("gotValue %d purged (err %v), expectedValue 1", gotValue, err)
	}
	if hasSealed(t, openedStore, candidateIDs[0]) || !hasSealed(t, openedStore, candidateIDs[1]) {
		t.Fatal("gotValue the wrong blob purged, expectedValue only the one older than the retention")
	}
	if SealedRetention != 30*24*time.Hour {
		t.Fatalf("gotValue retention %s, expectedValue the documented 30 days", SealedRetention)
	}
}

func TestStore_ignoresConflictProposalsWhenKeepingTheSealed(t *testing.T) {
	ctx := context.Background()
	openedStore := openTestStore(t)
	candidateID, fullID := pendingTestProposal(t, openedStore)
	if _, err := openedStore.CreateProposal(ctx, Proposal{CandidateID: candidateID, Kind: ProposalConflict}, testNow); err != nil {
		t.Fatal(err)
	}
	sealTestCandidate(t, openedStore, candidateID)

	if err := openedStore.SetProposalDecision(ctx, fullID, ProposalRejected, testNow); err != nil {
		t.Fatal(err)
	}
	if hasSealed(t, openedStore, candidateID) {
		t.Fatal("gotValue the sealed kept by a pending conflict, expectedValue cleared: a conflict is never seeded")
	}
}

func schemaVersion(t *testing.T, databasePath string) int {
	t.Helper()
	database, err := sql.Open("sqlite", dataSourceName(databasePath))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	return countRows(t, database, `PRAGMA schema_version`)
}

func TestOpen_leavesAnUpToDateSchemaUntouched(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "necromanciarr.db")
	for range 2 {
		openedStore, err := Open(databasePath)
		if err != nil {
			t.Fatal(err)
		}
		_ = openedStore.Close()
	}
	before := schemaVersion(t, databasePath)
	openedStore, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	_ = openedStore.Close()
	if gotValue := schemaVersion(t, databasePath); gotValue != before {
		t.Fatalf("gotValue schema version %d after reopening, expectedValue %d (no trigger rewrite)", gotValue, before)
	}
}

func TestOpenReadOnly_neverWritesTheSchemaNorTheRows(t *testing.T) {
	ctx := context.Background()
	databasePath := createV1Database(t)
	before := schemaVersion(t, databasePath)

	readOnly, err := OpenReadOnly(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = readOnly.Close() })
	if gotValue := schemaVersion(t, databasePath); gotValue != before {
		t.Fatalf("gotValue schema version %d, expectedValue %d: a read-only open must not upgrade", gotValue, before)
	}
	if _, err := readOnly.database.ExecContext(ctx, `UPDATE torrent_candidates SET name = 'x'`); err == nil {
		t.Fatal("gotValue a write accepted by a read-only store, expectedValue refused")
	}
}

func TestStore_sealedBytesNeverReachACandidateOrJSON(t *testing.T) {
	candidateType := reflect.TypeFor[Candidate]()
	for fieldIndex := range candidateType.NumField() {
		if candidateType.Field(fieldIndex).Type == reflect.TypeFor[[]byte]() {
			t.Fatalf("Candidate.%s is a byte slice: the sealed blob must never be exposed", candidateType.Field(fieldIndex).Name)
		}
	}
	encoded, err := json.Marshal(SealedTorrent{CandidateID: 1, Blob: testSealedBlob})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("Blob")) || bytes.Contains(encoded, []byte("fake-sealed")) {
		t.Fatalf("gotValue %s, expectedValue no blob in JSON", encoded)
	}
}

func TestStore_sealedColumnNeverHoldsPlaintextAnnounce(t *testing.T) {
	ctx := context.Background()
	accepted := map[string]string{
		"sanitized indexer torrent":         "d4:infod4:name1:aee",
		"provider torrent without announce": "d10:created by4:tool4:infod6:lengthi1e4:name1:aee",
		"file named announce inside info":   "d4:infod5:filesld6:lengthi1e4:pathl8:announceeee4:name1:aee",
	}
	refused := map[string]string{
		"announce":            "d8:announce20:http://example.com/a4:infod4:name1:aee",
		"announce-list only":  "d13:announce-listll20:http://example.com/aee4:infod4:name1:aee",
		"announce after info": "d4:infod4:name1:ae8:announce20:http://example.com/ae",
		"not bencode":         "8:announce",
		"not a dictionary":    "l8:announcee",
		"url-list":            "d4:infod4:name1:ae8:url-list20:http://example.com/ae",
		"httpseeds":           "d9:httpseedsl20:http://example.com/ae4:infod4:name1:aee",
		"comment":             "d7:comment20:http://example.com/a4:infod4:name1:aee",
	}
	for caseName, raw := range accepted {
		t.Run(caseName, func(t *testing.T) {
			openedStore := openTestStore(t)
			candidateID, _ := saveTestCandidate(t, openedStore, "aa", insertTestMediaFile(t, openedStore, "/home/u/media/a.mkv", 10))
			if err := openedStore.SaveCandidateTorrent(ctx, candidateID, []byte(raw), testNow); err != nil {
				t.Fatalf("gotValue %v, expectedValue accepted", err)
			}
			stored, err := openedStore.CandidateTorrent(ctx, candidateID)
			if err != nil || string(stored) != raw {
				t.Fatalf("gotValue %q (err %v)", stored, err)
			}
		})
	}
	for caseName, raw := range refused {
		t.Run(caseName, func(t *testing.T) {
			openedStore := openTestStore(t)
			candidateID, _ := saveTestCandidate(t, openedStore, "aa", insertTestMediaFile(t, openedStore, "/home/u/media/a.mkv", 10))
			err := openedStore.SaveCandidateTorrent(ctx, candidateID, []byte(raw), testNow)
			if !errors.Is(err, ErrUnsanitizedTorrent) {
				t.Fatalf("gotValue %v, expectedValue ErrUnsanitizedTorrent", err)
			}
			if strings.Contains(err.Error(), "example.com") {
				t.Fatalf("gotValue %q, expectedValue an error that never quotes the torrent", err)
			}
			stored, err := openedStore.CandidateTorrent(ctx, candidateID)
			if err != nil || stored != nil {
				t.Fatalf("gotValue stored %q (err %v), expectedValue nothing", stored, err)
			}
		})
	}
}
