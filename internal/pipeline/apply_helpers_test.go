package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"necromanciarr/internal/linker"
	"necromanciarr/internal/qbit"
	"necromanciarr/internal/store"
	"necromanciarr/internal/torrentcheck"
)

// applyFixture prolonge verifyFixture pour Link et Seed : un dossier de liens réel, un client torrent factice et un
// notificateur factice. stageStore remplace le store vu par les étapes (falsification ciblée d'une lecture).
type applyFixture struct {
	*verifyFixture
	linkDir     string
	linkKinds   []linker.Kind
	probe       func(directory string) (linker.Capabilities, error)
	client      *fakeTorrentClient
	notifier    *fakeNotifier
	skipRecheck bool
	stageStore  Store
	mapper      ClientPathMapper
}

func newApplyFixture(t *testing.T) *applyFixture {
	t.Helper()
	return &applyFixture{
		verifyFixture: newVerifyFixture(t), linkDir: t.TempDir(), linkKinds: []linker.Kind{linker.Hardlink},
		client: newFakeTorrentClient(), notifier: &fakeNotifier{},
	}
}

func (fixture *applyFixture) applyDeps() Deps {
	deps := fixture.deps()
	if fixture.stageStore != nil {
		deps.Store = fixture.stageStore
	}
	deps.LinkDirs, deps.LinkKinds, deps.ProbeLinks = []string{fixture.linkDir}, fixture.linkKinds, fixture.probe
	deps.Client, deps.Notifier, deps.SkipRecheck = fixture.client, fixture.notifier, fixture.skipRecheck
	deps.Category, deps.Tags, deps.ClientMapper = "necromanciarr", []string{"necromanciarr", "relinked"}, fixture.mapper
	deps.RecheckTimeout = time.Minute
	return deps
}

// runStage exécute l'étape et vérifie, comme pour Verify, que la bibliothèque n'a pas bougé.
func (fixture *applyFixture) runStage(stage Stage) (Result, error) {
	fixture.t.Helper()
	before := snapshotLibrary(fixture.t, fixture.root)
	result, err := Run(context.Background(), stage, fixture.applyDeps())
	assertLibraryUnchanged(fixture.t, before, snapshotLibrary(fixture.t, fixture.root))
	return result, err
}

func (fixture *applyFixture) mustRunStage(stage Stage) Result {
	fixture.t.Helper()
	result, err := fixture.runStage(stage)
	if err != nil {
		fixture.t.Fatalf("%s: %v (result %+v)", stage, err, result)
	}
	return result
}

// verifiedProposal lance Verify et renvoie la proposition de cette nature du candidat.
func (fixture *applyFixture) verifiedProposal(candidate store.Candidate, kind store.ProposalKind) store.Proposal {
	fixture.t.Helper()
	fixture.mustRun()
	return fixture.proposalOf(candidate.ID, kind)
}

func (fixture *applyFixture) accept(proposalID int64) {
	fixture.t.Helper()
	if err := fixture.store.SetProposalDecision(context.Background(), proposalID, store.ProposalAccepted, fixedNow); err != nil {
		fixture.t.Fatal(err)
	}
}

func (fixture *applyFixture) progressOf(proposalID int64) store.ProposalProgress {
	fixture.t.Helper()
	progress, err := fixture.store.ListProposalProgress(context.Background())
	if err != nil {
		fixture.t.Fatal(err)
	}
	return progress[proposalID]
}

func (fixture *applyFixture) decisionOf(proposalID int64) store.ProposalDecision {
	fixture.t.Helper()
	for _, proposal := range fixture.proposals() {
		if proposal.ID == proposalID {
			return proposal.Decision
		}
	}
	fixture.t.Fatalf("proposal %d not found", proposalID)
	return ""
}

func (fixture *applyFixture) libraryPath(name string) string {
	return filepath.Join(fixture.root, name)
}

func linkDirEntries(t *testing.T, linkDir string) []string {
	t.Helper()
	var names []string
	err := filepath.WalkDir(linkDir, func(entryPath string, _ os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(linkDir, entryPath)
		if err == nil && relative != "." {
			names = append(names, filepath.ToSlash(relative))
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func assertSameFile(t *testing.T, linkPath, libraryPath string) {
	t.Helper()
	linkInfo, err := os.Stat(linkPath)
	if err != nil {
		t.Fatalf("link %s: %v", linkPath, err)
	}
	libraryInfo, err := os.Stat(libraryPath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(linkInfo, libraryInfo) {
		t.Fatalf("%s does not resolve to %s", linkPath, libraryPath)
	}
}

// fakeTorrentClient imite qBittorrent : ajouts mémorisés, avancement choisi par le test, garde de recontrôle simulée.
type fakeTorrentClient struct {
	mutex        sync.Mutex
	added        map[string]fakeAdd
	progress     map[string]float64
	guardResults map[string]qbit.RecheckResult
	guardCalls   []string
	resumed      []string
	addErr       error
}

type fakeAdd struct {
	savePath string
	options  qbit.AddOptions
	torrent  []byte
}

func newFakeTorrentClient() *fakeTorrentClient {
	return &fakeTorrentClient{added: map[string]fakeAdd{}, progress: map[string]float64{}, guardResults: map[string]qbit.RecheckResult{}}
}

func (client *fakeTorrentClient) Add(_ context.Context, torrent []byte, savePath string, options qbit.AddOptions) error {
	client.mutex.Lock()
	defer client.mutex.Unlock()
	if client.addErr != nil {
		return client.addErr
	}
	infohash := torrentInfohash(torrent)
	client.added[infohash] = fakeAdd{savePath: savePath, options: options, torrent: torrent}
	if _, chosen := client.progress[infohash]; !chosen && options.SkipChecking {
		client.progress[infohash] = 1
	}
	return nil
}

func (client *fakeTorrentClient) Progress(_ context.Context, infoHash string) (float64, error) {
	client.mutex.Lock()
	defer client.mutex.Unlock()
	if _, present := client.added[infoHash]; !present {
		return 0, qbit.ErrNotFound
	}
	return client.progress[infoHash], nil
}

func (client *fakeTorrentClient) RecheckAndGuard(_ context.Context, infoHash string, _ time.Duration) (qbit.RecheckResult, error) {
	client.mutex.Lock()
	defer client.mutex.Unlock()
	client.guardCalls = append(client.guardCalls, infoHash)
	result := client.guardResults[infoHash]
	if result.Complete {
		result.Resumed = true
		client.resumed = append(client.resumed, infoHash)
	}
	return result, nil
}

func (client *fakeTorrentClient) Resume(_ context.Context, infoHash string) error {
	client.mutex.Lock()
	defer client.mutex.Unlock()
	client.resumed = append(client.resumed, infoHash)
	return nil
}

// torrentInfohash relit l'infohash du .torrent envoyé, comme le ferait le client.
func torrentInfohash(raw []byte) string {
	parsed, err := torrentcheck.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.InfoHash
}

// fakeNotifier compte les notifications et peut échouer.
type fakeNotifier struct {
	notified []string
	err      error
}

func (notifier *fakeNotifier) Notify(_ context.Context, infoHash string) error {
	notifier.notified = append(notifier.notified, infoHash)
	return notifier.err
}
