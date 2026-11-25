package pipeline

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/local"
	"necromanciarr/internal/libraryprovider"
	"necromanciarr/internal/linker"
	"necromanciarr/internal/store"
)

// tamperedStore falsifie une lecture du store pour un scénario ; les écritures restent réelles.
type tamperedStore struct {
	*store.Store
	candidates func([]store.Candidate) []store.Candidate
	assignment func(store.VerifiedAssignment) store.VerifiedAssignment
	// forgetProgress simule un arrêt après l'enregistrement des liens et avant celui de l'étape linked.
	forgetProgress bool
	// forgetLinks simule un arrêt avant même l'enregistrement des liens.
	forgetLinks bool
}

func (tampered *tamperedStore) ListLinks(ctx context.Context, state store.LinkState) ([]store.Link, error) {
	if tampered.forgetLinks {
		return nil, nil
	}
	return tampered.Store.ListLinks(ctx, state)
}

func (tampered *tamperedStore) ListCandidatesByState(ctx context.Context, states ...store.CandidateState) ([]store.Candidate, error) {
	candidates, err := tampered.Store.ListCandidatesByState(ctx, states...)
	if err != nil || tampered.candidates == nil {
		return candidates, err
	}
	return tampered.candidates(candidates), nil
}

func (tampered *tamperedStore) LoadVerifiedAssignment(ctx context.Context, candidateID int64) (store.VerifiedAssignment, bool, error) {
	assignment, found, err := tampered.Store.LoadVerifiedAssignment(ctx, candidateID)
	if err != nil || !found || tampered.assignment == nil {
		return assignment, found, err
	}
	return tampered.assignment(assignment), true, nil
}

func (tampered *tamperedStore) ListProposalProgress(ctx context.Context) (map[int64]store.ProposalProgress, error) {
	if tampered.forgetProgress {
		return map[int64]store.ProposalProgress{}, nil
	}
	return tampered.Store.ListProposalProgress(ctx)
}

func TestLinkStage_neverRunsWithoutAcceptedProposal(t *testing.T) {
	fixture := newApplyFixture(t)
	candidate := fixture.addCandidate("Movie.2020", "", movieOnly())
	proposal := fixture.verifiedProposal(candidate, store.ProposalFull)

	result := fixture.mustRunStage(StageLink)

	if entries := linkDirEntries(t, fixture.linkDir); len(entries) != 0 || result.Linked != 0 {
		t.Fatalf("gotValue %v linked=%d, expectedValue nothing linked for a pending proposal", entries, result.Linked)
	}
	if fixture.decisionOf(proposal.ID) != store.ProposalPending {
		t.Fatal("pending proposal changed by Link")
	}
}

func TestLinkStage_refusesStaleGenerationAndUnverifiedCandidate(t *testing.T) {
	cases := map[string]struct {
		tamper         func([]store.Candidate) []store.Candidate
		expectedReason string
	}{
		"stale generation": {func(candidates []store.Candidate) []store.Candidate {
			for index := range candidates {
				candidates[index].Generation++
			}
			return candidates
		}, ReasonStaleGeneration},
		"unverified candidate": {func([]store.Candidate) []store.Candidate { return nil }, ReasonCandidateNotVerified},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			fixture := newApplyFixture(t)
			candidate := fixture.addCandidate("Movie.2020", "", movieOnly())
			fixture.accept(fixture.verifiedProposal(candidate, store.ProposalFull).ID)
			fixture.stageStore = &tamperedStore{Store: fixture.store, candidates: testCase.tamper}

			result := fixture.mustRunStage(StageLink)

			if result.Reasons[testCase.expectedReason] != 1 || len(linkDirEntries(t, fixture.linkDir)) != 0 {
				t.Fatalf("gotValue %+v, expectedValue nothing linked and reason %s", result, testCase.expectedReason)
			}
		})
	}
}

func twinEpisodes() []torrentEntry {
	return []torrentEntry{
		{path: "episode.01.mkv", content: patternBytes(1, movieSize), localName: "a.mkv"},
		{path: "episode.02.mkv", content: patternBytes(2, movieSize), localName: "b.mkv"},
	}
}

func TestLinkStage_usesExactlyTheProvenAssignmentAndNeverReassigns(t *testing.T) {
	fixture := newApplyFixture(t)
	candidate := fixture.addCandidate("Show.S01", "", twinEpisodes())
	proposal := fixture.verifiedProposal(candidate, store.ProposalFull)
	fixture.accept(proposal.ID)
	// Association enregistrée volontairement croisée : Link doit la suivre telle quelle, sans rehacher.
	fixture.stageStore = &tamperedStore{Store: fixture.store, assignment: func(assignment store.VerifiedAssignment) store.VerifiedAssignment {
		assignment.Files[0].TorrentFileIndex, assignment.Files[1].TorrentFileIndex = assignment.Files[1].TorrentFileIndex, assignment.Files[0].TorrentFileIndex
		return assignment
	}}
	bytesReadBefore := fixture.opener.total()

	result := fixture.mustRunStage(StageLink)

	linkRoot := fixture.progressOf(proposal.ID).LinkRoot
	if result.Linked != 1 || fixture.opener.total() != bytesReadBefore {
		t.Fatalf("gotValue %+v, expectedValue one torrent linked without reading through the matcher", result)
	}
	assertSameFile(t, filepath.Join(linkRoot, "Show.S01", "episode.01.mkv"), fixture.libraryPath("b.mkv"))
	assertSameFile(t, filepath.Join(linkRoot, "Show.S01", "episode.02.mkv"), fixture.libraryPath("a.mkv"))
}

func TestLinkStage_marksCandidateStaleWhenAFileChanged(t *testing.T) {
	fixture := newApplyFixture(t)
	candidate := fixture.addCandidate("Movie.2020", "", movieOnly())
	proposal := fixture.verifiedProposal(candidate, store.ProposalFull)
	fixture.accept(proposal.ID)
	changedTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(fixture.libraryPath("movie.mkv"), changedTime, changedTime); err != nil {
		t.Fatal(err)
	}

	result := fixture.mustRunStage(StageLink)

	if result.Reasons[ReasonChangedFile] != 1 || len(linkDirEntries(t, fixture.linkDir)) != 0 {
		t.Fatalf("gotValue %+v, expectedValue nothing linked and changed_file_stale", result)
	}
	if fixture.candidate(candidate.Infohash).State != store.CandidateStale || fixture.decisionOf(proposal.ID) != store.ProposalSuperseded {
		t.Fatal("expectedValue a stale candidate and a superseded proposal")
	}
}

func TestLinkStage_writesVerifiedNfoNextToLinkedFilesAndNeverInTheLibrary(t *testing.T) {
	fixture := newApplyFixture(t)
	torrentNfo := []byte("ab\r\ncd")
	candidate := fixture.addCandidate("Movie.2020", "", movieWithAnnex(torrentNfo))
	fixture.fetcher.nfos[candidate.Infohash] = []libraryprovider.Nfo{{Label: "canonical", Data: []byte("ab\ncd")}}
	proposal := fixture.verifiedProposal(candidate, store.ProposalFull)
	fixture.accept(proposal.ID)

	fixture.mustRunStage(StageLink)

	linkRoot := fixture.progressOf(proposal.ID).LinkRoot
	gotValue, err := os.ReadFile(filepath.Join(linkRoot, "Movie.2020", "movie.nfo")) // #nosec G304 -- chemin de test
	if err != nil || !bytes.Equal(gotValue, torrentNfo) {
		t.Fatalf("gotValue %q %v, expectedValue the verified nfo bytes", gotValue, err)
	}
	if _, err := os.Lstat(fixture.libraryPath("movie.nfo")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("nfo written into the library")
	}
	assertSameFile(t, filepath.Join(linkRoot, "Movie.2020", "movie.mkv"), fixture.libraryPath("movie.mkv"))
	if filepath.Dir(linkRoot) != fixture.linkDir || fixture.progressOf(proposal.ID).Step != store.StepLinked {
		t.Fatalf("gotValue %+v, expectedValue a linked step under the link directory", fixture.progressOf(proposal.ID))
	}
}

func TestLinkStage_refusesInconsistentGroup(t *testing.T) {
	fixture := newApplyFixture(t)
	candidate := fixture.addCandidate("Movie.2020", "", movieOnly())
	proposal := fixture.verifiedProposal(candidate, store.ProposalFull)
	fixture.accept(proposal.ID)
	if err := fixture.store.SetProposalGroup(context.Background(), proposal.ID, candidate.GroupID+99); err != nil {
		t.Fatal(err)
	}

	_, err := fixture.runStage(StageLink)

	if !errors.Is(err, ErrGroupInconsistent) || len(linkDirEntries(t, fixture.linkDir)) != 0 {
		t.Fatalf("gotValue %v, expectedValue ErrGroupInconsistent and nothing linked", err)
	}
}

func TestLinkStage_excludedFilesAreNotLinked(t *testing.T) {
	fixture := newApplyFixture(t)
	candidate := fixture.addCandidate("Movie.2020", "", []torrentEntry{
		{path: "movie.mkv", content: patternBytes(1, 32), localName: "movie.mkv"},
		{path: "sub.srt", content: patternBytes(90, 16), localName: "sub.srt", localContent: patternBytes(91, 16)},
	})
	proposal := fixture.verifiedProposal(candidate, store.ProposalPartialAnnex)
	fixture.accept(proposal.ID)
	// Défense en profondeur : même si l'association le contenait, un fichier écarté par Verify n'est jamais lié.
	subtitle := mediaFilesByPath(t, fixture.store)[fixture.libraryPath("sub.srt")]
	fixture.stageStore = &tamperedStore{Store: fixture.store, assignment: func(assignment store.VerifiedAssignment) store.VerifiedAssignment {
		assignment.Files = append(assignment.Files, store.AssignedFile{
			TorrentFileIndex: 1, MediaFileID: subtitle.ID, Path: subtitle.Path,
			Size: subtitle.Size, ModTime: subtitle.ModTime,
		})
		return assignment
	}}

	fixture.mustRunStage(StageLink)

	expectedValue := []string{
		"Movie.2020 [" + candidate.Infohash + "]", "Movie.2020 [" + candidate.Infohash + "]/Movie.2020",
		"Movie.2020 [" + candidate.Infohash + "]/Movie.2020/movie.mkv",
	}
	if gotValue := linkDirEntries(t, fixture.linkDir); !slices.Equal(gotValue, expectedValue) {
		t.Fatalf("gotValue %v, expectedValue %v", gotValue, expectedValue)
	}
}

func TestLinkStage_rerunDoesNotReapplyAlreadyLinkedTorrent(t *testing.T) {
	fixture := newApplyFixture(t)
	fixture.linkKinds = []linker.Kind{linker.Symlink}
	candidate := fixture.addCandidate("Movie.2020", "", movieOnly())
	proposal := fixture.verifiedProposal(candidate, store.ProposalFull)
	fixture.accept(proposal.ID)
	fixture.mustRunStage(StageLink)
	linkPath := filepath.Join(fixture.progressOf(proposal.ID).LinkRoot, "Movie.2020", "movie.mkv")

	rerun := fixture.mustRunStage(StageLink)
	if rerun.Reasons[ReasonAlreadyLinked] != 1 || rerun.Linked != 0 {
		t.Fatalf("gotValue %+v, expectedValue already_linked", rerun)
	}

	// Un reflink ne se distingue pas d'une copie : une copie à la place du lien enregistré, et l'étape linked oubliée
	// (arrêt juste avant son enregistrement), ne doivent pas mener à un nouvel Apply, qui refuserait la destination.
	copyContent, err := os.ReadFile(fixture.libraryPath("movie.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(linkPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(linkPath, copyContent, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	fixture.stageStore = &tamperedStore{Store: fixture.store, forgetProgress: true}

	resumed := fixture.mustRunStage(StageLink)

	after, err := os.Lstat(linkPath)
	if err != nil || resumed.Linked != 1 || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("gotValue %+v %v, expectedValue the recorded link kept untouched", resumed, err)
	}
}

func TestLinkStage_fallsBackOnlyToListedKinds(t *testing.T) {
	fixture := newApplyFixture(t)
	fixture.probe = func(directory string) (linker.Capabilities, error) {
		return linker.Capabilities{Hardlink: false, Symlink: true, Reasons: map[linker.Kind]error{linker.Hardlink: linker.ErrLinkRefused}}, nil
	}
	candidate := fixture.addCandidate("Movie.2020", "", movieOnly())
	proposal := fixture.verifiedProposal(candidate, store.ProposalFull)
	fixture.accept(proposal.ID)

	refused := fixture.mustRunStage(StageLink)

	if refused.Reasons[ReasonLinkKindRefused] != 1 || fixture.decisionOf(proposal.ID) != store.ProposalFailed || len(linkDirEntries(t, fixture.linkDir)) != 0 {
		t.Fatalf("gotValue %+v, expectedValue link_kind_refused, a failed proposal and no symlink fallback", refused)
	}

	if err := fixture.store.RetryProposal(context.Background(), proposal.ID, fixedNow); err != nil {
		t.Fatal(err)
	}
	fixture.accept(proposal.ID)
	fixture.linkKinds = []linker.Kind{linker.Hardlink, linker.Symlink}

	fixture.mustRunStage(StageLink)

	linkPath := filepath.Join(fixture.progressOf(proposal.ID).LinkRoot, "Movie.2020", "movie.mkv")
	info, err := os.Lstat(linkPath)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("gotValue %v %v, expectedValue the listed symlink fallback", info, err)
	}
	links, err := fixture.store.ListLinks(context.Background(), "")
	if err != nil || len(links) != 1 || links[0].Kind != "symlink" || links[0].Fingerprint == "" {
		t.Fatalf("gotValue %+v %v, expectedValue one recorded symlink with its fingerprint", links, err)
	}
}

func TestLink_remoteLinkDirLinksThroughTheRegistryAndRecordsTheVolume(t *testing.T) {
	fixture, volume := newRemoteApplyFixture(t)
	candidate, proposal := fixture.linkedProposal(movieOnly(), store.ProposalFull)

	linkRoot := fixture.progressOf(proposal.ID).LinkRoot
	valeurAttendueRacine := "sftp://seedbox/srv/links/Movie.2020 [" + candidate.Infohash + "]"
	if linkRoot != valeurAttendueRacine {
		t.Fatalf("valeurAttendue the link root %s, valeurObtenue %s", valeurAttendueRacine, linkRoot)
	}
	assertSameFile(t, volume.LocalPath("/srv/links/Movie.2020 ["+candidate.Infohash+"]/Movie.2020/movie.mkv"),
		fixture.libraryPath("movie.mkv"))
	links := fixture.linksOf()
	if len(links) != 1 {
		t.Fatalf("valeurObtenue %+v, valeurAttendue one recorded link", links)
	}
	valeurObtenue := links[0]
	isRemoteRow := valeurObtenue.Volume == remoteVolumeName && valeurObtenue.IdentityUnproven() && !valeurObtenue.InodeReliable &&
		valeurObtenue.LinkPath == valeurAttendueRacine+"/Movie.2020/movie.mkv" &&
		valeurObtenue.TargetPath == "sftp://seedbox/srv/media/movie.mkv"
	if !isRemoteRow {
		t.Fatalf("valeurObtenue %+v, valeurAttendue the remote link recorded with its volume and an unproven identity", valeurObtenue)
	}
}

func TestLink_remoteUnavailableIsRetriedOnTheNextRun(t *testing.T) {
	fixture, volume := newRemoteApplyFixture(t)
	candidate := fixture.addCandidate("Movie.2020", "", movieOnly())
	proposal := fixture.verifiedProposal(candidate, store.ProposalFull)
	fixture.accept(proposal.ID)
	volume.Unavailable.Store(true)

	unavailable := fixture.mustRunStage(StageLink)

	isKept := fixture.decisionOf(proposal.ID) == store.ProposalAccepted && fixture.progressOf(proposal.ID).Step == ""
	if unavailable.Reasons[ReasonRemoteUnavailable] != 1 || unavailable.Failed != 0 || !isKept {
		t.Fatalf("valeurObtenue %+v, valeurAttendue remote_unavailable with the proposal still accepted", unavailable)
	}
	volume.Unavailable.Store(false)

	if healed := fixture.mustRunStage(StageLink); healed.Linked != 1 {
		t.Fatalf("valeurObtenue %+v, valeurAttendue the proposal linked on the next run", healed)
	}
}

func TestLink_fingerprintReadsGoThroughTheRegistryUnderTheReadLimiter(t *testing.T) {
	fixture := newApplyFixture(t)
	recording := &recordingVolume{ReadFS: local.New(), inner: local.New()}
	registry, err := fsys.NewRegistry(map[string]fsys.Volume{"": recording}, []fsys.Location{{Path: fixture.root}})
	if err != nil {
		t.Fatal(err)
	}
	fixture.registry = registry
	limiter := &byteCountingLimiter{}
	fixture.limiter = limiter
	candidate := fixture.addCandidate("Movie.2020", "", movieOnly())
	proposal := fixture.verifiedProposal(candidate, store.ProposalFull)
	fixture.accept(proposal.ID)
	limiter.requested.Store(0)
	recording.bytesRead.Store(0)

	if result := fixture.mustRunStage(StageLink); result.Linked != 1 {
		t.Fatalf("link: %+v", result)
	}

	octetsLus, valeurObtenue := recording.bytesRead.Load(), limiter.requested.Load()
	if octetsLus == 0 || valeurObtenue < octetsLus {
		t.Fatalf("valeurAttendue the limiter charged for the %d bytes read through the registry, valeurObtenue %d", octetsLus, valeurObtenue)
	}
}

// stageFilesThroughRegistry sont les fichiers de Link, Seed et Force : aucun accès disque hors du registre.
var stageFilesThroughRegistry = []string{"link.go", "proven.go", "isolate.go", "seed.go", "write_roots.go"}

func TestLinkSeedAndForce_touchTheDiskOnlyThroughTheRegistry(t *testing.T) {
	forbiddenFilepath := map[string]bool{"WalkDir": true, "Walk": true, "EvalSymlinks": true, "Abs": true, "Glob": true}
	for _, fileName := range stageFilesThroughRegistry {
		parsed, err := parser.ParseFile(token.NewFileSet(), fileName, nil, parser.ImportsOnly|parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, imported := range parsed.Imports {
			if path := strings.Trim(imported.Path.Value, `"`); isBannedDiskImport(path) {
				t.Errorf("%s imports %s, valeurAttendue disk access only through the registry", fileName, path)
			}
		}
		full, err := parser.ParseFile(token.NewFileSet(), fileName, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(full, func(node ast.Node) bool {
			selector, isSelector := node.(*ast.SelectorExpr)
			if !isSelector {
				return true
			}
			qualifier, isIdentifier := selector.X.(*ast.Ident)
			if isIdentifier && qualifier.Name == "filepath" && forbiddenFilepath[selector.Sel.Name] {
				t.Errorf("%s calls filepath.%s, valeurAttendue disk access only through the registry", fileName, selector.Sel.Name)
			}
			return true
		})
	}
}

func TestLink_unverifiedSymlinkOrderFailsTheProposalWithItsOwnReason(t *testing.T) {
	fixture, volume := newRemoteApplyFixture(t)
	fixture.linkKinds = []linker.Kind{linker.Symlink}
	volume.SymlinkOrderUnverified.Store(true)
	candidate := fixture.addCandidate("Movie.2020", "", movieOnly())
	proposal := fixture.verifiedProposal(candidate, store.ProposalFull)
	fixture.accept(proposal.ID)

	result := fixture.mustRunStage(StageLink)

	if valeurObtenue := result.Reasons[ReasonSymlinkOrderUnverified]; valeurObtenue != 1 || result.Linked != 0 {
		t.Fatalf("valeurObtenue %+v, valeurAttendue %s once and nothing linked", result, ReasonSymlinkOrderUnverified)
	}
	if valeurObtenue := fixture.decisionOf(proposal.ID); valeurObtenue != store.ProposalFailed {
		t.Errorf("valeurObtenue %s, valeurAttendue the proposal failed like any refused link kind", valeurObtenue)
	}
}

// isBannedDiskImport : os, os/exec, syscall, io/ioutil et golang.org/x/sys/… ouvriraient un accès disque (ou un
// programme externe) hors du registre ; même liste que la garde du linker (internal/linker/guard_test.go).
func isBannedDiskImport(importPath string) bool {
	bannedImports := []string{"os", "os/exec", "syscall", "io/ioutil"}
	return slices.Contains(bannedImports, importPath) || strings.HasPrefix(importPath, "golang.org/x/sys/")
}
