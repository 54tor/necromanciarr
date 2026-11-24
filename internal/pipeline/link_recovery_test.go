package pipeline

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"necromanciarr/internal/libraryprovider"
	"necromanciarr/internal/linker"
	"necromanciarr/internal/store"
)

func TestLinkStage_refusesALinkDirResolvingIntoTheLibrary(t *testing.T) {
	fixture := newApplyFixture(t)
	candidate := fixture.addCandidate("Movie.2020", "", movieOnly())
	fixture.accept(fixture.verifiedProposal(candidate, store.ProposalFull).ID)
	fixture.linkDir = filepath.Join(t.TempDir(), "links")
	if err := os.Symlink(fixture.root, fixture.linkDir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	fixture.probe = func(string) (linker.Capabilities, error) {
		t.Fatal("nothing may be probed or created in a link directory that overlaps the library")
		return linker.Capabilities{}, nil
	}

	result, err := fixture.runStage(StageLink)

	if !errors.Is(err, ErrLinkDirOverlapsLibrary) || result.Reasons[ReasonLinkDirOverlapsLibrary] != 1 {
		t.Fatalf("gotValue %v %+v, expectedValue ErrLinkDirOverlapsLibrary", err, result)
	}
}

// replaceWithCopy remplace l'entrée par une copie ordinaire du contenu donné, comme un reflink.
func replaceWithCopy(t *testing.T, entryPath string, content []byte) os.FileInfo {
	t.Helper()
	if err := os.Remove(entryPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entryPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(entryPath)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func TestLinkStage_reflinkStoppedBeforeItsRecordIsAdoptedOnRerun(t *testing.T) {
	fixture := newApplyFixture(t)
	fixture.linkKinds = []linker.Kind{linker.Symlink}
	candidate := fixture.addCandidate("Movie.2020", "", movieOnly())
	proposal := fixture.verifiedProposal(candidate, store.ProposalFull)
	fixture.accept(proposal.ID)
	fixture.mustRunStage(StageLink)
	linkPath := filepath.Join(fixture.progressOf(proposal.ID).LinkRoot, "Movie.2020", "movie.mkv")
	before := replaceWithCopy(t, linkPath, patternBytes(1, movieSize))
	fixture.stageStore = &tamperedStore{Store: fixture.store, forgetProgress: true, forgetLinks: true}

	result := fixture.mustRunStage(StageLink)

	after, err := os.Lstat(linkPath)
	if err != nil || result.Linked != 1 || !os.SameFile(before, after) {
		t.Fatalf("gotValue %+v %v, expectedValue the identical copy adopted untouched", result, err)
	}
	links, err := fixture.store.ListLinks(context.Background(), "")
	if err != nil || len(links) != 1 || links[0].Kind != linker.Reflink.String() {
		t.Fatalf("gotValue %+v %v, expectedValue the adopted entry recorded as a reflink", links, err)
	}
}

func (fixture *applyFixture) linkedNfoTorrent(torrentNfo []byte) (store.Proposal, string) {
	fixture.t.Helper()
	candidate := fixture.addCandidate("Movie.2020", "", movieWithAnnex(torrentNfo))
	fixture.fetcher.nfos[candidate.Infohash] = []libraryprovider.Nfo{{Label: "canonical", Data: []byte("ab\ncd")}}
	proposal := fixture.verifiedProposal(candidate, store.ProposalFull)
	fixture.accept(proposal.ID)
	fixture.mustRunStage(StageLink)
	return proposal, filepath.Join(fixture.progressOf(proposal.ID).LinkRoot, "Movie.2020", "movie.nfo")
}

func TestLinkStage_truncatedAnnexLeftoverIsRewrittenOnlyInAnOwnedRoot(t *testing.T) {
	torrentNfo := []byte("ab\r\ncd")
	for _, foreignEntry := range []bool{false, true} {
		fixture := newApplyFixture(t)
		proposal, nfoPath := fixture.linkedNfoTorrent(torrentNfo)
		replaceWithCopy(t, nfoPath, torrentNfo[:2])
		if foreignEntry {
			writeFileForTest(t, filepath.Join(filepath.Dir(nfoPath), "foreign.txt"), []byte("not ours"))
		}
		fixture.stageStore = &tamperedStore{Store: fixture.store, forgetProgress: true}

		result := fixture.mustRunStage(StageLink)

		gotValue, err := os.ReadFile(nfoPath) // #nosec G304 -- chemin de test
		if err != nil {
			t.Fatal(err)
		}
		if foreignEntry {
			if result.Reasons[ReasonDestinationConflict] != 1 || fixture.decisionOf(proposal.ID) != store.ProposalFailed || !bytes.Equal(gotValue, torrentNfo[:2]) {
				t.Fatalf("foreign entry: gotValue %+v %q, expectedValue destination_conflict and nothing touched", result, gotValue)
			}
			continue
		}
		if result.Linked != 1 || !bytes.Equal(gotValue, torrentNfo) {
			t.Fatalf("owned root: gotValue %+v %q, expectedValue the verified annex rewritten", result, gotValue)
		}
	}
}

func writeFileForTest(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLinkStage_leftoverLinkToAnotherSourceIsReplacedOnlyWhenRecorded(t *testing.T) {
	for _, recorded := range []bool{true, false} {
		fixture := newApplyFixture(t)
		candidate := fixture.addCandidate("Movie.2020", "", movieOnly())
		proposal := fixture.verifiedProposal(candidate, store.ProposalFull)
		fixture.accept(proposal.ID)
		fixture.mustRunStage(StageLink)
		other := fixture.writeLocal("other.mkv", patternBytes(9, movieSize))
		// La revérification associe désormais le fichier du torrent à une autre source de même taille.
		fixture.stageStore = &tamperedStore{Store: fixture.store, forgetProgress: true, forgetLinks: !recorded,
			assignment: func(assignment store.VerifiedAssignment) store.VerifiedAssignment {
				assignment.Files[0].MediaFileID, assignment.Files[0].Path = other.ID, other.Path
				assignment.Files[0].Size, assignment.Files[0].ModTime = other.Size, other.ModTime
				return assignment
			}}

		result := fixture.mustRunStage(StageLink)

		linkPath := filepath.Join(fixture.progressOf(proposal.ID).LinkRoot, "Movie.2020", "movie.mkv")
		if !recorded {
			if result.Reasons[ReasonDestinationConflict] != 1 {
				t.Fatalf("unrecorded: gotValue %+v, expectedValue destination_conflict", result)
			}
			assertSameFile(t, linkPath, fixture.libraryPath("movie.mkv"))
			continue
		}
		if result.Linked != 1 {
			t.Fatalf("recorded: gotValue %+v, expectedValue the stale recorded link replaced", result)
		}
		assertSameFile(t, linkPath, other.Path)
	}
}
