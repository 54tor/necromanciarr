package app

import (
	"bytes"
	"context"
	"crypto/sha1" // #nosec G505 -- SHA-1 imposé par le format BitTorrent v1
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"necromanciarr/internal/linker"
	"necromanciarr/internal/pipeline"
	"necromanciarr/internal/store"
)

const (
	straddlingMovieSize = 40
	missingNfoSize      = 5
)

// movieWithMissingNfo construit un torrent « Movie » : movie.mkv (40 octets) puis movie.nfo (5 octets) que la
// bibliothèque n'a pas ; la pièce 32..47 est à cheval sur les deux.
func movieWithMissingNfo(t *testing.T, movie []byte) ([]byte, string) {
	t.Helper()
	nfo := bytes.Repeat([]byte("n"), missingNfoSize)
	concatenated := append(slices.Clone(movie), nfo...)
	var pieces []byte
	for start := 0; start < len(concatenated); start += guardPieceLength {
		sum := sha1.Sum(concatenated[start:min(start+guardPieceLength, len(concatenated))]) // #nosec G401 -- SHA-1 imposé par BitTorrent v1
		pieces = append(pieces, sum[:]...)
	}
	info := metainfo.Info{Name: "Movie", PieceLength: guardPieceLength, Pieces: pieces, Files: []metainfo.FileInfo{
		{Path: []string{"movie.mkv"}, Length: int64(len(movie))}, {Path: []string{"movie.nfo"}, Length: missingNfoSize},
	}}
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := bencode.Marshal(metainfo.MetaInfo{InfoBytes: infoBytes})
	if err != nil {
		t.Fatal(err)
	}
	metaInfo, err := metainfo.Load(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return raw, metaInfo.HashInfoBytes().HexString()
}

// forceFixture mène un partial_annex réel jusqu'à seeded_paused_incomplete (Verify, Accept, Link, Seed), en
// vérifiant après chaque étape que la bibliothèque n'a pas bougé.
type forceFixture struct {
	t          *testing.T
	libraryDir string
	moviePath  string
	client     *completeClient
	service    Service
	proposalID int64
}

func newForceFixture(t *testing.T, prepareCopy func(source, destination, root string) (linker.PreparedCopy, error)) *forceFixture {
	t.Helper()
	ctx := context.Background()
	libraryDir, linkDir := t.TempDir(), t.TempDir()
	movie := bytes.Repeat([]byte("necromanciarr"), 4)[:straddlingMovieSize]
	moviePath := filepath.Join(libraryDir, "movie.mkv")
	if err := os.WriteFile(moviePath, movie, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(moviePath)
	if err != nil {
		t.Fatal(err)
	}
	openedStore := openTestStore(t)
	if err := openedStore.UpsertMediaFile(ctx, store.MediaFile{Path: moviePath, Size: info.Size(), ModTime: info.ModTime(), TMDBID: 603}); err != nil {
		t.Fatal(err)
	}
	mediaFiles, err := openedStore.ListMediaFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, infohash := movieWithMissingNfo(t, movie)
	files := []store.CandidateFile{
		{FileIndex: 0, Path: "Movie/movie.mkv", Size: info.Size(), MediaFileID: mediaFiles[0].ID, MediaSize: info.Size(), MediaModTime: info.ModTime()},
		{FileIndex: 1, Path: "Movie/movie.nfo", Size: missingNfoSize},
	}
	candidate := store.Candidate{Infohash: infohash, Name: "Movie", TotalSize: info.Size() + missingNfoSize}
	if _, _, err := openedStore.SaveCandidate(ctx, candidate, files, fixedNow); err != nil {
		t.Fatal(err)
	}
	if err := openedStore.RegroupCandidates(ctx); err != nil {
		t.Fatal(err)
	}
	client := &completeClient{added: map[string]bool{}}
	service := New(Deps{Store: openedStore, Client: client, Stages: pipeline.Deps{
		Store: openedStore, Fetcher: singleTorrentFetcher{raw: raw}, LinkDirs: []string{linkDir}, LibraryRoots: []string{libraryDir},
		LinkKinds: []linker.Kind{linker.Hardlink}, Client: client, PrepareCopy: prepareCopy,
	}})
	fixture := &forceFixture{t: t, libraryDir: libraryDir, moviePath: moviePath, client: client, service: service}
	fixture.prepare()
	return fixture
}

func (fixture *forceFixture) guarded(step string, action func() error) error {
	fixture.t.Helper()
	before := snapshotDirectory(fixture.t, fixture.libraryDir)
	err := action()
	assertSnapshotUnchanged(fixture.t, step, before, snapshotDirectory(fixture.t, fixture.libraryDir))
	return err
}

func (fixture *forceFixture) prepare() {
	fixture.t.Helper()
	ctx := context.Background()
	runStage := func(stage pipeline.Stage) func() error {
		return func() error { _, err := fixture.service.RunStage(ctx, stage); return err }
	}
	if err := fixture.guarded("verify", runStage(pipeline.StageVerify)); err != nil {
		fixture.t.Fatal(err)
	}
	views, err := fixture.service.ListProposals(ctx, ProposalFilter{Kind: store.ProposalPartialAnnex})
	if err != nil || len(views) != 1 {
		fixture.t.Fatalf("gotValue %+v %v, expectedValue one partial_annex proposal", views, err)
	}
	fixture.proposalID = views[0].ID
	for step, action := range map[string]func() error{"accept": func() error { return fixture.service.Accept(ctx, fixture.proposalID) }} {
		if err := fixture.guarded(step, action); err != nil {
			fixture.t.Fatal(err)
		}
	}
	for _, stage := range []pipeline.Stage{pipeline.StageLink, pipeline.StageSeed} {
		if err := fixture.guarded(string(stage), runStage(stage)); err != nil {
			fixture.t.Fatal(err)
		}
	}
	if gotStep := fixture.view().Step; gotStep != store.StepSeededPausedIncomplete {
		fixture.t.Fatalf("gotValue step %q, expectedValue seeded_paused_incomplete", gotStep)
	}
}

func (fixture *forceFixture) view() Proposal {
	fixture.t.Helper()
	views, err := fixture.service.ListProposals(context.Background(), ProposalFilter{})
	if err != nil {
		fixture.t.Fatal(err)
	}
	for _, view := range views {
		if view.ID == fixture.proposalID {
			return view
		}
	}
	fixture.t.Fatalf("proposal %d not found", fixture.proposalID)
	return Proposal{}
}

func (fixture *forceFixture) linkedMovie() string {
	fixture.t.Helper()
	links, err := fixture.service.(*service).deps.Store.ListLinks(context.Background(), "")
	if err != nil || len(links) != 1 {
		fixture.t.Fatalf("gotValue %+v %v, expectedValue one recorded link", links, err)
	}
	return links[0].LinkPath
}

func TestApp_forceReplacesTheStraddlingHardlinkByACopyBeforeResuming(t *testing.T) {
	fixture := newForceFixture(t, nil)
	linkPath := fixture.linkedMovie()

	var result ForceResult
	err := fixture.guarded("force", func() error {
		var forceErr error
		result, forceErr = fixture.service.Force(context.Background(), fixture.proposalID)
		return forceErr
	})

	if err != nil || !result.Forced || len(fixture.client.resumed) != 1 {
		t.Fatalf("gotValue %+v %v resumed=%v, expectedValue the torrent forced", result, err, fixture.client.resumed)
	}
	linkInfo, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	libraryInfo, err := os.Stat(fixture.moviePath)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(linkInfo, libraryInfo) || !linkInfo.Mode().IsRegular() {
		t.Fatal("the linked media still shares the library inode after Force")
	}
	links, err := fixture.service.(*service).deps.Store.ListLinks(context.Background(), "")
	isCopyKind := err == nil && len(links) == 1 && (links[0].Kind == linker.Copy.String() || links[0].Kind == linker.Reflink.String())
	if !isCopyKind {
		t.Fatalf("gotValue %+v %v, expectedValue the replacement recorded with its copy kind", links, err)
	}
	if len(result.Replaced) != 1 || result.Replaced[0].LinkPath != linkPath {
		t.Fatalf("gotValue %+v, expectedValue the replaced file reported", result.Replaced)
	}
	if expectedWarnings := fixture.view().Warnings; !slices.Equal(result.Warnings, expectedWarnings) || len(expectedWarnings) == 0 {
		t.Fatalf("gotValue %v, expectedValue the proposal warnings %v returned", result.Warnings, expectedWarnings)
	}
	if gotStep := fixture.view().Step; gotStep != store.StepForced {
		t.Fatalf("gotValue step %q, expectedValue forced", gotStep)
	}
}

func TestApp_forceRefusesWithoutChangeWhenNoCopyIsPossible(t *testing.T) {
	refuseCopy := func(string, string, string) (linker.PreparedCopy, error) {
		return linker.PreparedCopy{}, linker.ErrInsufficientSpace
	}
	fixture := newForceFixture(t, refuseCopy)
	linkPath := fixture.linkedMovie()

	err := fixture.guarded("force", func() error {
		_, forceErr := fixture.service.Force(context.Background(), fixture.proposalID)
		return forceErr
	})

	if !errors.Is(err, ErrForceIsolationImpossible) || len(fixture.client.resumed) != 0 {
		t.Fatalf("gotValue %v resumed=%v, expectedValue a typed refusal and nothing started", err, fixture.client.resumed)
	}
	linkInfo, statErr := os.Stat(linkPath)
	libraryInfo, libraryErr := os.Stat(fixture.moviePath)
	if statErr != nil || libraryErr != nil || !os.SameFile(linkInfo, libraryInfo) {
		t.Fatal("the refused Force changed the link")
	}
	if gotStep := fixture.view().Step; gotStep != store.StepSeededPausedIncomplete {
		t.Fatalf("gotValue step %q, expectedValue still paused", gotStep)
	}
}
