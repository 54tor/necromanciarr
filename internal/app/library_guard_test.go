package app

import (
	"bytes"
	"context"
	"crypto/sha1" // #nosec G505 -- SHA-1 imposé par le format BitTorrent v1
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"necromanciarr/internal/libraryprovider"
	"necromanciarr/internal/linker"
	"necromanciarr/internal/pipeline"
	"necromanciarr/internal/qbit"
	"necromanciarr/internal/store"
)

const guardPieceLength = 16

func singleFileTorrent(t *testing.T, name string, content []byte) ([]byte, string) {
	t.Helper()
	var pieces []byte
	for start := 0; start < len(content); start += guardPieceLength {
		sum := sha1.Sum(content[start:min(start+guardPieceLength, len(content))]) // #nosec G401 -- SHA-1 imposé par BitTorrent v1
		pieces = append(pieces, sum[:]...)
	}
	infoBytes, err := bencode.Marshal(metainfo.Info{Name: name, PieceLength: guardPieceLength, Length: int64(len(content)), Pieces: pieces})
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

type singleTorrentFetcher struct{ raw []byte }

func (fetcher singleTorrentFetcher) Torrent(context.Context, string, string) ([]byte, error) {
	return fetcher.raw, nil
}

func (singleTorrentFetcher) Nfos(context.Context, string, string) ([]libraryprovider.Nfo, error) {
	return nil, libraryprovider.ErrNotFound
}

// completeClient accepte tout ajout ; un torrent ajouté sans recontrôle est vu à 100 %.
type completeClient struct {
	added    map[string]bool
	resumed  []string
	hash     string
	savePath string
}

func (client *completeClient) Add(_ context.Context, raw []byte, savePath string, _ qbit.AddOptions) error {
	client.added["any"] = true
	client.savePath = savePath
	metaInfo, err := metainfo.Load(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	client.hash = metaInfo.HashInfoBytes().HexString()
	return nil
}

func (client *completeClient) Torrents(context.Context) ([]qbit.TorrentInfo, error) {
	if !client.added["any"] {
		return nil, nil
	}
	return []qbit.TorrentInfo{{Hash: client.hash, SavePath: client.savePath, Progress: 1}}, nil
}

func (client *completeClient) Pause(context.Context, string) error { return nil }

func (client *completeClient) RecheckAndGuard(context.Context, string, time.Duration) (qbit.RecheckResult, error) {
	return qbit.RecheckResult{Progress: 1, Complete: true, Resumed: true}, nil
}

func (client *completeClient) Resume(_ context.Context, infoHash string) error {
	client.resumed = append(client.resumed, infoHash)
	return nil
}

type fileSnapshot struct {
	content []byte
	info    os.FileInfo
}

func snapshotDirectory(t *testing.T, directory string) map[string]fileSnapshot {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := map[string]fileSnapshot{}
	for _, entry := range entries {
		entryPath := filepath.Join(directory, entry.Name())
		content, err := os.ReadFile(entryPath) // #nosec G304 -- chemin de test sous t.TempDir()
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(entryPath)
		if err != nil {
			t.Fatal(err)
		}
		snapshot[entry.Name()] = fileSnapshot{content: content, info: info}
	}
	return snapshot
}

func assertSnapshotUnchanged(t *testing.T, step string, before, after map[string]fileSnapshot) {
	t.Helper()
	if len(after) != len(before) {
		t.Fatalf("%s: library entries changed: gotValue %d, expectedValue %d", step, len(after), len(before))
	}
	for name, expectedValue := range before {
		gotValue, present := after[name]
		unchanged := present && bytes.Equal(gotValue.content, expectedValue.content) &&
			gotValue.info.ModTime().Equal(expectedValue.info.ModTime()) && os.SameFile(gotValue.info, expectedValue.info)
		if !unchanged {
			t.Fatalf("%s: library file %s changed", step, name)
		}
	}
}

func TestApp_neverWritesLibraryFiles(t *testing.T) {
	ctx := context.Background()
	libraryDir, linkDir := t.TempDir(), t.TempDir()
	content := bytes.Repeat([]byte("necromanciarr"), 4)
	moviePath := filepath.Join(libraryDir, "movie.mkv")
	if err := os.WriteFile(moviePath, content, 0o600); err != nil {
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
	raw, infohash := singleFileTorrent(t, "movie.mkv", content)
	candidateFile := store.CandidateFile{Path: "movie.mkv", Size: info.Size(), MediaFileID: mediaFiles[0].ID, MediaSize: info.Size(), MediaModTime: info.ModTime()}
	if _, _, err := openedStore.SaveCandidate(ctx, store.Candidate{Infohash: infohash, Name: "movie.mkv", TotalSize: info.Size()}, []store.CandidateFile{candidateFile}, fixedNow); err != nil {
		t.Fatal(err)
	}
	if err := openedStore.RegroupCandidates(ctx); err != nil {
		t.Fatal(err)
	}
	client := &completeClient{added: map[string]bool{}}
	service := New(Deps{Store: openedStore, Client: client, Stages: pipeline.Deps{
		Store: openedStore, Fetcher: singleTorrentFetcher{raw: raw}, LinkDirs: []string{linkDir},
		LinkKinds: []linker.Kind{linker.Hardlink}, Client: client, SkipRecheck: true,
	}})

	guarded := func(step string, action func() error) {
		t.Helper()
		before := snapshotDirectory(t, libraryDir)
		if err := action(); err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		assertSnapshotUnchanged(t, step, before, snapshotDirectory(t, libraryDir))
	}
	runStage := func(stage pipeline.Stage) func() error {
		return func() error { _, err := service.RunStage(ctx, stage); return err }
	}
	guarded("verify", runStage(pipeline.StageVerify))
	views, err := service.ListProposals(ctx, ProposalFilter{Kind: store.ProposalFull})
	if err != nil || len(views) != 1 {
		t.Fatalf("gotValue %+v %v, expectedValue one full proposal", views, err)
	}
	guarded("accept", func() error { return service.Accept(ctx, views[0].ID) })
	guarded("link", runStage(pipeline.StageLink))
	guarded("seed", runStage(pipeline.StageSeed))
	guarded("rerun", runStage(pipeline.StageLink))

	final, err := service.ListProposals(ctx, ProposalFilter{})
	if err != nil || final[0].Step != store.StepSeeded || final[0].Decision != store.ProposalApplied || len(client.resumed) != 1 {
		t.Fatalf("gotValue %+v %v, expectedValue the torrent seeded from links only", final, err)
	}
}
