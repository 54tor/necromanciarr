package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"necromanciarr/internal/config"
	"necromanciarr/internal/dedupe"
	"necromanciarr/internal/doctor"
	"necromanciarr/internal/linker"
	"necromanciarr/internal/pipeline"
	"necromanciarr/internal/qbit"
	"necromanciarr/internal/repair"
	"necromanciarr/internal/store"
)

// agedModTime est assez ancien pour que dedupe ne tienne aucun fichier pour « en cours d'écriture ».
var agedModTime = time.Now().Add(-time.Hour).Truncate(time.Second)

// clientTorrentRoot est la vue du client torrent sur la racine torrent du test.
const clientTorrentRoot = "/data/torrents"

// fakeMaintenanceClient enregistre les chemins recontrôlés (vue du client) ; il n'a aucun moyen de reprendre un torrent.
type fakeMaintenanceClient struct {
	recheckedPaths []string
	recheckErr     error
}

func (client *fakeMaintenanceClient) RecheckPath(_ context.Context, path string) error {
	client.recheckedPaths = append(client.recheckedPaths, path)
	return client.recheckErr
}

func (*fakeMaintenanceClient) TorrentForPath(context.Context, string) (repair.TorrentRef, bool, error) {
	return repair.TorrentRef{}, false, nil
}

type maintenanceFixture struct {
	t           *testing.T
	store       *store.Store
	libraryDir  string
	torrentRoot string
	linkDir     string
	moviePath   string
	copyPath    string
	client      *fakeMaintenanceClient
	resumer     *fakeResumer
	settings    config.Config
}

func writeAgedFile(t *testing.T, path string, content []byte, modTime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

// newMaintenanceFixture : un film dans la bibliothèque, sa copie identique sous la racine torrent.
func newMaintenanceFixture(t *testing.T) *maintenanceFixture {
	t.Helper()
	fixture := &maintenanceFixture{
		t: t, store: openTestStore(t), libraryDir: t.TempDir(), torrentRoot: t.TempDir(), linkDir: t.TempDir(),
		client: &fakeMaintenanceClient{}, resumer: &fakeResumer{},
	}
	content := bytes.Repeat([]byte("necromanciarr"), 512)
	fixture.moviePath = filepath.Join(fixture.libraryDir, "movie.mkv")
	fixture.copyPath = filepath.Join(fixture.torrentRoot, "Movie", "movie.mkv")
	writeAgedFile(t, fixture.moviePath, content, agedModTime)
	writeAgedFile(t, fixture.copyPath, content, agedModTime)
	fixture.addMedia(fixture.moviePath)
	fixture.settings = config.Config{
		LibraryRoots: []string{fixture.libraryDir}, LinkDirs: []string{fixture.linkDir},
		Dedupe: config.Dedupe{TorrentRoots: []string{fixture.torrentRoot}, Kinds: []string{"symlink"}, RelativeSymlinks: true, RecheckAfter: true, Verify: "full"},
	}
	return fixture
}

func (fixture *maintenanceFixture) addMedia(path string) {
	fixture.t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		fixture.t.Fatal(err)
	}
	if err := fixture.store.UpsertMediaFile(context.Background(), store.MediaFile{Path: path, Size: info.Size(), ModTime: info.ModTime()}); err != nil {
		fixture.t.Fatal(err)
	}
}

func (fixture *maintenanceFixture) service() Service {
	mapper := linker.NewMapper([]config.PathRule{{From: fixture.torrentRoot, To: clientTorrentRoot}})
	return New(Deps{
		Store: fixture.store, Stages: pipeline.Deps{Store: fixture.store, ClientMapper: mapper}, Client: fixture.resumer,
		Config: fixture.settings, Maintenance: fixture.client, Now: func() time.Time { return fixedNow },
	})
}

func (fixture *maintenanceFixture) proposalsOfKind(kind store.ProposalKind) []store.Proposal {
	fixture.t.Helper()
	proposals, err := fixture.store.ListProposals(context.Background(), "")
	if err != nil {
		fixture.t.Fatal(err)
	}
	var ofKind []store.Proposal
	for _, proposal := range proposals {
		if proposal.Kind == kind {
			ofKind = append(ofKind, proposal)
		}
	}
	return ofKind
}

func (fixture *maintenanceFixture) decisionOf(proposalID int64) store.ProposalDecision {
	fixture.t.Helper()
	for _, proposal := range fixture.proposalsOfKind(store.ProposalDedupe) {
		if proposal.ID == proposalID {
			return proposal.Decision
		}
	}
	for _, proposal := range fixture.proposalsOfKind(store.ProposalRepair) {
		if proposal.ID == proposalID {
			return proposal.Decision
		}
	}
	fixture.t.Fatalf("proposal %d not found", proposalID)
	return ""
}

func (fixture *maintenanceFixture) planOne(service Service) DedupeProposal {
	fixture.t.Helper()
	proposals, err := service.DedupePlan(context.Background())
	if err != nil || len(proposals) != 1 {
		fixture.t.Fatalf("gotValue %+v %v, expectedValue one dedupe proposal", proposals, err)
	}
	return proposals[0]
}

func isRegularFile(t *testing.T, path string) bool {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().IsRegular()
}

func TestApp_dedupePlanPersistsIdempotentProposalsAfterRecover(t *testing.T) {
	fixture := newMaintenanceFixture(t)
	service := fixture.service()
	leftover := fixture.copyPath + dedupe.TemporaryMarker + "6656-interrupted"
	writeAgedFile(t, leftover, []byte("leftover"), agedModTime)

	first := fixture.planOne(service)

	if _, err := os.Lstat(leftover); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("gotValue %v, expectedValue the interrupted entry removed by Recover before the plan", err)
	}
	details := first.Details
	isExpected := first.Decision == store.ProposalPending && details.TorrentPath == fixture.copyPath &&
		details.LibraryPath == fixture.moviePath && details.Kind == "symlink" && details.Actionable &&
		details.SavedBytes == details.Size && details.BytesToRead == 2*details.Size && details.Method == "full"
	if !isExpected {
		t.Fatalf("gotValue %+v, expectedValue a pending actionable symlink proposal with its read estimate", first)
	}
	second := fixture.planOne(service)
	if second.ID != first.ID || len(fixture.proposalsOfKind(store.ProposalDedupe)) != 1 {
		t.Fatalf("gotValue %d (%d stored), expectedValue the same proposal %d on rerun", second.ID, len(fixture.proposalsOfKind(store.ProposalDedupe)), first.ID)
	}

	changedModTime := agedModTime.Add(-time.Minute)
	if err := os.Chtimes(fixture.copyPath, changedModTime, changedModTime); err != nil {
		t.Fatal(err)
	}
	third := fixture.planOne(service)

	if third.ID == first.ID || fixture.decisionOf(first.ID) != store.ProposalSuperseded {
		t.Fatalf("gotValue %d and old %s, expectedValue a new proposal superseding the old one", third.ID, fixture.decisionOf(first.ID))
	}
}

func TestApp_dedupePlanRefusesTorrentRootsInsideTheLibraryBeforeRecover(t *testing.T) {
	fixture := newMaintenanceFixture(t)
	leftover := filepath.Join(fixture.libraryDir, "movie.mkv"+dedupe.TemporaryMarker+"6656-x")
	writeAgedFile(t, leftover, []byte("x"), agedModTime)
	fixture.settings.Dedupe.TorrentRoots = []string{fixture.libraryDir}

	_, err := fixture.service().DedupePlan(context.Background())

	if _, statErr := os.Lstat(leftover); !errors.Is(err, dedupe.ErrTorrentRootOverlapsLibrary) || statErr != nil {
		t.Fatalf("gotValue %v (leftover stat %v), expectedValue a refusal before any library change", err, statErr)
	}
}

func TestApp_dedupeAcceptAppliesOnlyAcceptedProposals(t *testing.T) {
	fixture := newMaintenanceFixture(t)
	service := fixture.service()
	ctx := context.Background()
	proposal := fixture.planOne(service)

	if _, err := service.DedupeAccept(ctx, proposal.ID); !errors.Is(err, ErrNotAccepted) || !isRegularFile(t, fixture.copyPath) {
		t.Fatalf("pending: gotValue %v, expectedValue ErrNotAccepted and no change", err)
	}
	if err := service.Choose(ctx, proposal.ID); !errors.Is(err, ErrNotATorrentProposal) {
		t.Fatalf("choose: gotValue %v, expectedValue ErrNotATorrentProposal", err)
	}
	if _, err := service.Force(ctx, proposal.ID); !errors.Is(err, ErrNotATorrentProposal) {
		t.Fatalf("force: gotValue %v, expectedValue ErrNotATorrentProposal", err)
	}
	if err := service.Accept(ctx, proposal.ID); err != nil {
		t.Fatal(err)
	}
	dryRun, err := service.DedupeAccept(ctx, proposal.ID, DryRun)
	if err != nil || !dryRun.DryRun || dryRun.Applied || dryRun.LinkTarget == "" || !isRegularFile(t, fixture.copyPath) ||
		fixture.decisionOf(proposal.ID) != store.ProposalAccepted {
		t.Fatalf("dry run: gotValue %+v %v, expectedValue checks only", dryRun, err)
	}

	applied, err := service.DedupeAccept(ctx, proposal.ID)

	if err != nil || !applied.Applied || fixture.decisionOf(proposal.ID) != store.ProposalApplied || isRegularFile(t, fixture.copyPath) {
		t.Fatalf("gotValue %+v %v, expectedValue the copy replaced by a link and the proposal applied", applied, err)
	}
	resolved, err := filepath.EvalSymlinks(fixture.copyPath)
	if expectedValue, _ := filepath.EvalSymlinks(fixture.moviePath); err != nil || resolved != expectedValue {
		t.Fatalf("gotValue %s %v, expectedValue a link to the library file", resolved, err)
	}
	links, err := fixture.store.ListLinks(ctx, "")
	if err != nil || len(links) != 1 || links[0].LinkPath != fixture.copyPath {
		t.Fatalf("gotValue %+v %v, expectedValue the link recorded", links, err)
	}
}

func TestApp_acceptRefusesADedupeProposalListedForInformationOnly(t *testing.T) {
	fixture := newMaintenanceFixture(t)
	details, err := json.Marshal(DedupeDetails{TorrentPath: fixture.copyPath, Ambiguous: []string{"/home/u/a.mkv", "/home/u/b.mkv"}})
	if err != nil {
		t.Fatal(err)
	}
	proposalID, err := fixture.store.CreateProposal(context.Background(), store.Proposal{Kind: store.ProposalDedupe, Details: details}, fixedNow)
	if err != nil {
		t.Fatal(err)
	}

	if err := fixture.service().Accept(context.Background(), proposalID); !errors.Is(err, ErrNotActionable) {
		t.Fatalf("gotValue %v, expectedValue ErrNotActionable", err)
	}
}

func TestApp_dedupeRechecksThroughClientViewPathAndKeepsIncompleteTorrentsPaused(t *testing.T) {
	fixture := newMaintenanceFixture(t)
	fixture.client.recheckErr = fmt.Errorf("%w: aa (progress 0.9)", qbit.ErrIncompleteAfterRecheck)
	service := fixture.service()
	ctx := context.Background()
	proposal := fixture.planOne(service)
	if err := service.Accept(ctx, proposal.ID); err != nil {
		t.Fatal(err)
	}

	result, err := service.DedupeAccept(ctx, proposal.ID)

	expectedPath := clientTorrentRoot + "/Movie/movie.mkv"
	if err != nil || !result.Applied || !slices.Equal(fixture.client.recheckedPaths, []string{expectedPath}) {
		t.Fatalf("gotValue %+v %v %v, expectedValue one recheck of %s", result, err, fixture.client.recheckedPaths, expectedPath)
	}
	stored := fixture.proposalsOfKind(store.ProposalDedupe)[0]
	if !slices.Contains(result.Warnings, WarningPausedIncompleteAfterRecheck) || !slices.Contains(stored.Warnings, WarningPausedIncompleteAfterRecheck) ||
		len(fixture.resumer.resumed) != 0 {
		t.Fatalf("gotValue %+v and stored %v, resumed %v, expectedValue the paused warning and no resume", result, stored.Warnings, fixture.resumer.resumed)
	}

	quiet := newMaintenanceFixture(t)
	quiet.settings.Dedupe.RecheckAfter = false
	quietService := quiet.service()
	quietProposal := quiet.planOne(quietService)
	if err := quietService.Accept(ctx, quietProposal.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := quietService.DedupeAccept(ctx, quietProposal.ID); err != nil || len(quiet.client.recheckedPaths) != 0 {
		t.Fatalf("recheckAfter off: gotValue %v %v, expectedValue no recheck", err, quiet.client.recheckedPaths)
	}
}

// brokenLink pose sous directory un lien symbolique vers une cible absente et l'enregistre avec la taille, la date
// et l'empreinte du fichier renommé attendu.
func (fixture *maintenanceFixture) brokenLink(directory, name, expectedPath string) string {
	fixture.t.Helper()
	linkPath := filepath.Join(directory, name)
	oldTarget := filepath.Join(fixture.libraryDir, "old-"+name)
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o750); err != nil {
		fixture.t.Fatal(err)
	}
	if err := os.Symlink(oldTarget, linkPath); err != nil {
		fixture.t.Fatal(err)
	}
	info, err := os.Stat(expectedPath)
	if err != nil {
		fixture.t.Fatal(err)
	}
	fingerprint, _, err := dedupe.HeadTailFingerprint(context.Background(), nil, nil, expectedPath, info.Size(), info.ModTime(), true)
	if err != nil {
		fixture.t.Fatal(err)
	}
	link := store.Link{
		Kind: "symlink", LinkPath: linkPath, TargetPath: oldTarget, Size: info.Size(), Fingerprint: fingerprint,
		State: store.LinkOK, ModTime: info.ModTime(), CreatedAt: fixedNow, VerifiedAt: fixedNow,
	}
	if err := fixture.store.RecordLink(context.Background(), link); err != nil {
		fixture.t.Fatal(err)
	}
	return linkPath
}

// repairLayout : un lien réparable (cible renommée), un lien ambigu (deux fichiers identiques) et un lien perdu.
func (fixture *maintenanceFixture) repairLayout() (repairable, ambiguous, lost string) {
	fixture.t.Helper()
	renamed := filepath.Join(fixture.libraryDir, "renamed.mkv")
	writeAgedFile(fixture.t, renamed, bytes.Repeat([]byte("renamed"), 300), agedModTime)
	twinContent := bytes.Repeat([]byte("twin"), 700)
	firstTwin, secondTwin := filepath.Join(fixture.libraryDir, "twin-a.mkv"), filepath.Join(fixture.libraryDir, "twin-b.mkv")
	writeAgedFile(fixture.t, firstTwin, twinContent, agedModTime)
	writeAgedFile(fixture.t, secondTwin, twinContent, agedModTime)
	for _, path := range []string{renamed, firstTwin, secondTwin} {
		fixture.addMedia(path)
	}
	repairable = fixture.brokenLink(fixture.linkDir, "Movie/movie.mkv", renamed)
	ambiguous = fixture.brokenLink(fixture.linkDir, "Twin/twin.mkv", firstTwin)
	lostTarget := filepath.Join(fixture.t.TempDir(), "unknown.mkv")
	writeAgedFile(fixture.t, lostTarget, []byte("no library file of this size"), agedModTime)
	lost = fixture.brokenLink(fixture.linkDir, "Lost/lost.mkv", lostTarget)
	return repairable, ambiguous, lost
}

func repairByLink(proposals []RepairProposal, linkPath string) RepairProposal {
	for _, proposal := range proposals {
		if proposal.Details.LinkPath == linkPath {
			return proposal
		}
	}
	return RepairProposal{}
}

func TestApp_repairScanListsAmbiguousButNotActionable(t *testing.T) {
	fixture := newMaintenanceFixture(t)
	repairablePath, ambiguousPath, lostPath := fixture.repairLayout()
	service := fixture.service()
	ctx := context.Background()

	proposals, err := service.RepairScan(ctx, "")

	repairable, ambiguous, lost := repairByLink(proposals, repairablePath), repairByLink(proposals, ambiguousPath), repairByLink(proposals, lostPath)
	isExpected := err == nil && len(proposals) == 3 && repairable.Details.Actionable &&
		repairable.Details.NewTarget == filepath.Join(fixture.libraryDir, "renamed.mkv") && !ambiguous.Details.Actionable &&
		len(ambiguous.Details.Candidates) == 2 && lost.Details.Status == RepairStatusLost && !lost.Details.Actionable
	if !isExpected {
		t.Fatalf("gotValue %+v %v, expectedValue one repairable, one ambiguous and one lost link", proposals, err)
	}
	for _, informative := range []int64{ambiguous.ID, lost.ID} {
		if err := service.Accept(ctx, informative); !errors.Is(err, ErrNotActionable) {
			t.Fatalf("proposal %d: gotValue %v, expectedValue ErrNotActionable", informative, err)
		}
	}
	rerun, err := service.RepairScan(ctx, "")
	if err != nil || repairByLink(rerun, repairablePath).ID != repairable.ID || len(fixture.proposalsOfKind(store.ProposalRepair)) != 3 {
		t.Fatalf("gotValue %+v %v, expectedValue the same three proposals", rerun, err)
	}
	if _, err := service.RepairScan(ctx, fixture.libraryDir); !errors.Is(err, ErrDirectoryInLibrary) {
		t.Fatalf("library dir: gotValue %v, expectedValue ErrDirectoryInLibrary", err)
	}
}

func TestApp_repairAcceptReplacesOnlyBrokenLinksInsideRoots(t *testing.T) {
	fixture := newMaintenanceFixture(t)
	renamed := filepath.Join(fixture.libraryDir, "renamed.mkv")
	writeAgedFile(t, renamed, bytes.Repeat([]byte("renamed"), 300), agedModTime)
	fixture.addMedia(renamed)
	insideLink := fixture.brokenLink(fixture.linkDir, "Movie/movie.mkv", renamed)
	outsideLink := fixture.brokenLink(t.TempDir(), "Elsewhere/movie.mkv", renamed)
	service := fixture.service()
	ctx := context.Background()
	proposals, err := service.RepairScan(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	inside, outside := repairByLink(proposals, insideLink), repairByLink(proposals, outsideLink)

	if _, err := service.RepairAccept(ctx, inside.ID); !errors.Is(err, ErrNotAccepted) {
		t.Fatalf("pending: gotValue %v, expectedValue ErrNotAccepted", err)
	}
	for _, proposalID := range []int64{inside.ID, outside.ID} {
		if err := service.Accept(ctx, proposalID); err != nil {
			t.Fatal(err)
		}
	}
	applied, err := service.RepairAccept(ctx, inside.ID)
	resolved, resolveErr := filepath.EvalSymlinks(insideLink)
	expectedTarget, _ := filepath.EvalSymlinks(renamed)
	if err != nil || !applied.Applied || resolveErr != nil || resolved != expectedTarget || fixture.decisionOf(inside.ID) != store.ProposalApplied {
		t.Fatalf("inside: gotValue %+v %v (%s %v), expectedValue the link repaired", applied, err, resolved, resolveErr)
	}
	links, err := fixture.store.ListLinks(ctx, "")
	if err != nil || !slices.ContainsFunc(links, func(link store.Link) bool { return link.LinkPath == insideLink && link.TargetPath == renamed }) {
		t.Fatalf("gotValue %+v %v, expectedValue the record pointing at the new target", links, err)
	}

	_, err = service.RepairAccept(ctx, outside.ID)

	target, readErr := os.Readlink(outsideLink)
	if !errors.Is(err, repair.ErrOutsideRoots) || fixture.decisionOf(outside.ID) != store.ProposalFailed || readErr != nil ||
		!strings.Contains(target, "old-") {
		t.Fatalf("outside: gotValue %v (%s), expectedValue ErrOutsideRoots, a failed proposal and the link untouched", err, target)
	}
}

func TestApp_doctorWrapsDoctorPackage(t *testing.T) {
	libraryDir := t.TempDir()
	writeAgedFile(t, filepath.Join(libraryDir, "film.mkv"), []byte("x"), agedModTime)
	settings := config.Config{
		LibraryRoots: []string{libraryDir}, Radarr: []config.ArrService{{Kind: "radarr", BaseURL: "http://example.com:7878"}},
	}
	blocking := doctor.Deps{NewPinger: func(config.ArrService) doctor.Pinger { return blockingPinger{} }}
	service := New(Deps{Config: settings, DoctorDeps: blocking})
	ctx := context.Background()

	report, err := service.Doctor(ctx, DoctorOptions{CheckIDs: []string{"library-roots"}})
	if err != nil || len(report.Checks) != 1 || report.Checks[0].Status != doctor.OK || report.Worst != doctor.OK {
		t.Fatalf("gotValue %+v %v, expectedValue one passing library check", report, err)
	}
	if _, err := service.Doctor(ctx, DoctorOptions{CheckIDs: []string{"nope"}}); !errors.Is(err, ErrUnknownCheck) || !strings.Contains(err.Error(), "library-roots") {
		t.Fatalf("gotValue %v, expectedValue ErrUnknownCheck listing the valid ids", err)
	}
	started := time.Now()
	report, err = service.Doctor(ctx, DoctorOptions{CheckIDs: []string{"arr:radarr:0"}, Timeout: 20 * time.Millisecond})
	if err != nil || report.Worst != doctor.Fail || !strings.Contains(report.Checks[0].Detail, "did not finish") || time.Since(started) > 5*time.Second {
		t.Fatalf("gotValue %+v %v, expectedValue the check stopped by its timeout", report, err)
	}
}

type blockingPinger struct{}

func (blockingPinger) Ping(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}
