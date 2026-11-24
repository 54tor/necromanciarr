package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"necromanciarr/internal/arr"
	"necromanciarr/internal/libraryprovider"
	"necromanciarr/internal/store"
)

var (
	hashA = strings.Repeat("a", 40)
	hashB = strings.Repeat("b", 40)
	hashC = strings.Repeat("c", 40)
)

func writeMediaFile(t *testing.T, root, name string, size int) string {
	t.Helper()
	filePath := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	return filePath
}

func scanDeps(mediaStore *store.Store, root string, sources ...ItemsSource) Deps {
	return Deps{
		Store: mediaStore, ScanRoot: LibraryScanner(mediaStore), LibraryRoots: []string{root}, ArrSources: sources,
		Now: func() time.Time { return fixedNow },
	}
}

func mediaFilesByPath(t *testing.T, mediaStore *store.Store) map[string]store.MediaFile {
	t.Helper()
	mediaFiles, err := mediaStore.ListMediaFiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]store.MediaFile{}
	for _, mediaFile := range mediaFiles {
		byPath[mediaFile.Path] = mediaFile
	}
	return byPath
}

func TestScanStage_joinsArrItemsAndReportsUnidentifiedFiles(t *testing.T) {
	mediaStore := openTestStore(t)
	root := t.TempDir()
	moviePath := writeMediaFile(t, root, "movies/Film.mkv", 10)
	episodePath := writeMediaFile(t, root, "tv/Show/S01E01.mkv", 20)
	writeMediaFile(t, root, "other/Unknown.mkv", 30)
	radarr := fakeArr{items: []arr.Item{{Path: moviePath, TMDBID: 603}}}
	sonarr := fakeArr{items: []arr.Item{{Path: episodePath, TVDBID: 81189}}}

	gotResult, err := Run(context.Background(), StageScan, scanDeps(mediaStore, root, radarr, sonarr))
	if err != nil {
		t.Fatal(err)
	}

	byPath := mediaFilesByPath(t, mediaStore)
	if byPath[moviePath].TMDBID != 603 || byPath[episodePath].TVDBID != 81189 {
		t.Fatalf("gotValue ids %+v", byPath)
	}
	expectedSamples := []string{"Unknown.mkv"}
	if gotResult.UnidentifiedFiles != 1 || !slices.Equal(gotResult.UnidentifiedSamples, expectedSamples) || gotResult.Processed != 3 {
		t.Fatalf("gotValue %+v, expectedValue 1 unidentified %v among 3", gotResult, expectedSamples)
	}
}

func TestScanStage_translatesArrPathsWithTheMapper(t *testing.T) {
	mediaStore := openTestStore(t)
	root := t.TempDir()
	moviePath := writeMediaFile(t, root, "movies/Film.mkv", 10)
	radarr := fakeArr{items: []arr.Item{{Path: "/data/media/movies/Film.mkv", TMDBID: 603}}}
	deps := scanDeps(mediaStore, root, radarr)
	deps.Mapper = prefixMapper{localPrefix: filepath.ToSlash(root), clientPrefix: "/data/media"}

	if _, err := Run(context.Background(), StageScan, deps); err != nil {
		t.Fatal(err)
	}

	if gotValue := mediaFilesByPath(t, mediaStore)[moviePath].TMDBID; gotValue != 603 {
		t.Fatalf("gotValue tmdb %d, expectedValue 603", gotValue)
	}
}

func TestScanStage_removesVanishedFilesAndInvalidatesTheirCandidates(t *testing.T) {
	ctx := context.Background()
	mediaStore := openTestStore(t)
	root := t.TempDir()
	vanishingPath := writeMediaFile(t, root, "movies/Gone.mkv", 10)
	writeMediaFile(t, root, "movies/Kept.mkv", 20)
	radarr := fakeArr{items: []arr.Item{{Path: vanishingPath, TMDBID: 1}}}
	if _, err := Run(ctx, StageScan, scanDeps(mediaStore, root, radarr)); err != nil {
		t.Fatal(err)
	}
	gone := mediaFilesByPath(t, mediaStore)[vanishingPath]
	candidateFile := store.CandidateFile{Path: "Gone.mkv", Size: 10, MediaFileID: gone.ID, MediaSize: gone.Size, MediaModTime: gone.ModTime}
	if _, _, err := mediaStore.SaveCandidate(ctx, store.Candidate{Infohash: hashA, Name: "Gone"}, []store.CandidateFile{candidateFile}, fixedNow); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(vanishingPath); err != nil {
		t.Fatal(err)
	}

	gotResult, err := Run(ctx, StageScan, scanDeps(mediaStore, root, radarr))
	if err != nil {
		t.Fatal(err)
	}

	if _, stillListed := mediaFilesByPath(t, mediaStore)[vanishingPath]; stillListed || gotResult.Reasons[ReasonMissingFileRemoved] != 1 {
		t.Fatalf("gotValue %+v, vanished file still listed %v", gotResult, stillListed)
	}
	if gotState := candidatesByHash(t, mediaStore)[hashA].State; gotState != store.CandidateStale {
		t.Fatalf("gotValue state %s, expectedValue stale", gotState)
	}
}

func TestScanStage_neverPrunesARootThatLooksEmpty(t *testing.T) {
	ctx := context.Background()
	mediaStore := openTestStore(t)
	root := t.TempDir()
	filmPath := writeMediaFile(t, root, "movies/Film.mkv", 10)
	if _, err := Run(ctx, StageScan, scanDeps(mediaStore, root)); err != nil {
		t.Fatal(err)
	}
	// Un montage décroché se présente comme un dossier vide : rien ne doit être retiré de l'inventaire.
	if err := os.Remove(filmPath); err != nil {
		t.Fatal(err)
	}

	gotResult, err := Run(ctx, StageScan, scanDeps(mediaStore, root))
	if err != nil {
		t.Fatal(err)
	}

	if _, stillListed := mediaFilesByPath(t, mediaStore)[filmPath]; !stillListed || gotResult.Reasons[ReasonEmptyRootNotPruned] != 1 {
		t.Fatalf("gotValue %+v, file kept %v", gotResult, stillListed)
	}
}

func TestMatchStage_createsCandidatesForExactSizeOnly(t *testing.T) {
	mediaStore := openTestStore(t)
	addMediaFile(t, mediaStore, "/home/u/media/a.mkv", 100, 1)
	addMediaFile(t, mediaStore, "/home/u/media/b.mkv", 300, 1)
	provider := &fakeProvider{honourSizes: true, releases: []libraryprovider.Release{
		release(hashA, "t1", 1, 100, 50), release(hashB, "t1", 1, 101),
	}}

	gotResult, err := Run(context.Background(), StageMatch, matchDeps(mediaStore, provider))
	if err != nil {
		t.Fatal(err)
	}

	byHash := candidatesByHash(t, mediaStore)
	if len(byHash) != 1 || byHash[hashA].State != store.CandidateMatched || gotResult.CandidatesCreated != 1 {
		t.Fatalf("gotValue %+v / %+v", byHash, gotResult)
	}
	expectedSizes := []int64{100, 300}
	if len(provider.queries) != 1 || provider.queries[0].TMDBID != 1 || !slices.Equal(provider.queries[0].Sizes, expectedSizes) {
		t.Fatalf("gotValue queries %+v, expectedValue one tmdb query with sizes %v", provider.queries, expectedSizes)
	}
	files, err := mediaStore.ListCandidateFiles(context.Background(), byHash[hashA].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].MediaFileID == 0 || files[0].Ambiguous || files[1].MediaFileID != 0 {
		t.Fatalf("gotValue candidate files %+v", files)
	}
}

func TestMatchStage_appliesLocalSizeFilterWhenProviderIgnoresSize(t *testing.T) {
	mediaStore := openTestStore(t)
	addMediaFile(t, mediaStore, "/home/u/media/a.mkv", 100, 1)
	provider := &fakeProvider{honourSizes: false, releases: []libraryprovider.Release{
		release(hashA, "t1", 1, 100), release(hashB, "t1", 1, 101), release(hashC, "t1", 1, 99, 102),
	}}

	gotResult, err := Run(context.Background(), StageMatch, matchDeps(mediaStore, provider))
	if err != nil {
		t.Fatal(err)
	}

	byHash := candidatesByHash(t, mediaStore)
	if _, kept := byHash[hashA]; !kept || len(byHash) != 1 || gotResult.Reasons[ReasonNoExactSize] != 2 {
		t.Fatalf("gotValue %+v / %+v", byHash, gotResult)
	}
}

func TestMatchStage_listsAllTorrentsForOneFileInOneGroup(t *testing.T) {
	mediaStore := openTestStore(t)
	addMediaFile(t, mediaStore, "/home/u/media/a.mkv", 100, 1)
	addMediaFile(t, mediaStore, "/home/u/media/other.mkv", 500, 2)
	provider := &fakeProvider{honourSizes: true, releases: []libraryprovider.Release{
		release(hashA, "t1", 1, 100), release(hashA, "t2", 1, 100), release(hashB, "t1", 1, 100, 7), release(hashC, "t1", 2, 500),
	}}

	if _, err := Run(context.Background(), StageMatch, matchDeps(mediaStore, provider)); err != nil {
		t.Fatal(err)
	}

	candidates, err := mediaStore.ListCandidatesByState(context.Background(), store.CandidateMatched)
	if err != nil {
		t.Fatal(err)
	}
	groups := map[int64][]string{}
	for _, candidate := range candidates {
		groups[candidate.GroupID] = append(groups[candidate.GroupID], candidate.Infohash+"@"+candidate.Tracker)
	}
	if len(candidates) != 4 || len(groups) != 2 {
		t.Fatalf("gotValue %d candidates in groups %v, expectedValue 4 candidates in 2 groups", len(candidates), groups)
	}
	for _, members := range groups {
		if len(members) != 3 && !(len(members) == 1 && members[0] == hashC+"@t1") {
			t.Fatalf("gotValue groups %v", groups)
		}
	}
}

func TestMatchStage_isIdempotentAndMarksStaleCandidates(t *testing.T) {
	ctx := context.Background()
	mediaStore := openTestStore(t)
	addMediaFile(t, mediaStore, "/home/u/media/a.mkv", 100, 1)
	addMediaFile(t, mediaStore, "/home/u/media/b.mkv", 200, 1)
	provider := &fakeProvider{honourSizes: true, releases: []libraryprovider.Release{release(hashA, "t1", 1, 100), release(hashB, "t1", 1, 200)}}
	deps := matchDeps(mediaStore, provider)
	if _, err := Run(ctx, StageMatch, deps); err != nil {
		t.Fatal(err)
	}

	secondResult, err := Run(ctx, StageMatch, deps)
	if err != nil {
		t.Fatal(err)
	}
	if gotCount := len(candidatesByHash(t, mediaStore)); gotCount != 2 || secondResult.CandidatesUnchanged != 2 || secondResult.CandidatesCreated != 0 {
		t.Fatalf("gotValue %d candidates, result %+v", gotCount, secondResult)
	}

	addMediaFile(t, mediaStore, "/home/u/media/b.mkv", 250, 1)
	thirdResult, err := Run(ctx, StageMatch, deps)
	if err != nil {
		t.Fatal(err)
	}

	byHash := candidatesByHash(t, mediaStore)
	if byHash[hashB].State != store.CandidateStale || byHash[hashA].State != store.CandidateMatched || thirdResult.CandidatesStale != 1 {
		t.Fatalf("gotValue %+v / %+v", byHash, thirdResult)
	}
}

func TestMatchStage_stopsCleanlyOnRateLimit(t *testing.T) {
	mediaStore := openTestStore(t)
	addMediaFile(t, mediaStore, "/home/u/media/a.mkv", 100, 1)
	addMediaFile(t, mediaStore, "/home/u/media/b.mkv", 200, 2)
	addMediaFile(t, mediaStore, "/home/u/media/c.mkv", 300, 3)
	provider := &fakeProvider{
		honourSizes: true, releases: []libraryprovider.Release{release(hashA, "t1", 1, 100), release(hashC, "t1", 3, 300)},
		failOnCall: 2, failure: &libraryprovider.RateLimitedError{RetryAfter: 30 * time.Second},
	}

	_, err := Run(context.Background(), StageMatch, matchDeps(mediaStore, provider))

	var resumable *ResumableError
	if !errors.As(err, &resumable) || resumable.RetryAfter != 30*time.Second || resumable.Stage != StageMatch {
		t.Fatalf("gotValue %v, expectedValue a resumable error after 30s", err)
	}
	if gotCalls := len(provider.queries); gotCalls != 2 {
		t.Fatalf("gotValue %d provider calls, expectedValue 2 (stop at the first rate limit)", gotCalls)
	}
	if _, kept := candidatesByHash(t, mediaStore)[hashA]; !kept {
		t.Fatal("candidate found before the rate limit was lost")
	}
}

func TestRun_exclusiveLockPreventsConcurrentStages(t *testing.T) {
	ctx := context.Background()
	mediaStore := openTestStore(t)
	deps := matchDeps(mediaStore, &fakeProvider{})
	if err := mediaStore.AcquireRunLock(ctx, "other-process", "verify", fixedNow, fixedNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	if _, err := Run(ctx, StageMatch, deps); !errors.Is(err, ErrStageRunning) {
		t.Fatalf("gotValue %v, expectedValue ErrStageRunning", err)
	}

	deps.Now = func() time.Time { return fixedNow.Add(2 * time.Minute) }
	if _, err := Run(ctx, StageMatch, deps); err != nil {
		t.Fatalf("stale lock not taken over: %v", err)
	}
	if _, err := Run(ctx, StageMatch, deps); err != nil {
		t.Fatalf("lock not released after the run: %v", err)
	}
}

func TestRun_unknownStageIsAnError(t *testing.T) {
	if _, err := Run(context.Background(), Stage("bogus"), Deps{}); !errors.Is(err, ErrUnknownStage) {
		t.Fatalf("gotValue %v, expectedValue ErrUnknownStage", err)
	}
	if _, err := ParseStage("bogus"); !errors.Is(err, ErrUnknownStage) {
		t.Fatalf("gotValue %v, expectedValue ErrUnknownStage", err)
	}
	gotStage, err := ParseStage("Match")
	if err != nil || gotStage != StageMatch {
		t.Fatalf("gotValue %q %v, expectedValue match", gotStage, err)
	}
	if _, err := Run(context.Background(), StageVerify, matchDeps(openTestStore(t), &fakeProvider{})); !errors.Is(err, ErrStageNotImplemented) {
		t.Fatalf("gotValue %v, expectedValue ErrStageNotImplemented", err)
	}
}
