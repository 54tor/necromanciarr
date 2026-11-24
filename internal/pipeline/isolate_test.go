package pipeline

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"necromanciarr/internal/linker"
	"necromanciarr/internal/store"
)

// linkedPartialAnnex lie un partial_annex et renvoie le candidat, la proposition et le dossier de liens du torrent.
func (fixture *applyFixture) linkedPartialAnnex(entries []torrentEntry) (store.Candidate, store.Proposal, string) {
	fixture.t.Helper()
	candidate, proposal := fixture.linkedProposal(entries, store.ProposalPartialAnnex)
	return candidate, proposal, fixture.progressOf(proposal.ID).LinkRoot
}

func (fixture *applyFixture) isolate(candidate store.Candidate, proposal store.Proposal, linkRoot string) (IsolationResult, error) {
	fixture.t.Helper()
	before := snapshotLibrary(fixture.t, fixture.root)
	result, err := IsolateStraddlingMedia(context.Background(), fixture.applyDeps(), proposal, candidate, linkRoot)
	assertLibraryUnchanged(fixture.t, before, snapshotLibrary(fixture.t, fixture.root))
	return result, err
}

func (fixture *applyFixture) recordedKind(linkPath string) string {
	fixture.t.Helper()
	links, err := fixture.store.ListLinks(context.Background(), "")
	if err != nil {
		fixture.t.Fatal(err)
	}
	for _, link := range links {
		if link.LinkPath == linkPath {
			return link.Kind
		}
	}
	return ""
}

func assertIndependentCopy(t *testing.T, linkPath, libraryPath string) {
	t.Helper()
	linkInfo, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	libraryInfo, err := os.Stat(libraryPath)
	if err != nil {
		t.Fatal(err)
	}
	if !linkInfo.Mode().IsRegular() || os.SameFile(linkInfo, libraryInfo) {
		t.Fatalf("%s still shares %s", linkPath, libraryPath)
	}
	expectedContent, gotContent := readTestFile(t, libraryPath), readTestFile(t, linkPath)
	if !bytes.Equal(gotContent, expectedContent) {
		t.Fatalf("%s content differs from the library file", linkPath)
	}
}

func readTestFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path) // #nosec G304 -- chemin de test sous t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func TestIsolateStraddlingMedia_replacesLibraryLinksByIndependentCopies(t *testing.T) {
	for _, kind := range []linker.Kind{linker.Hardlink, linker.Symlink} {
		t.Run(kind.String(), func(t *testing.T) {
			fixture := newApplyFixture(t)
			fixture.linkKinds = []linker.Kind{kind}
			candidate, proposal, linkRoot := fixture.linkedPartialAnnex(movieWithAnnex(patternBytes(50, nfoSize)))
			linkPath := filepath.Join(linkRoot, "Movie.2020", "movie.mkv")

			result, err := fixture.isolate(candidate, proposal, linkRoot)

			if err != nil || len(result.Replaced) != 1 {
				t.Fatalf("gotValue %+v %v, expectedValue the straddling media replaced", result, err)
			}
			assertIndependentCopy(t, linkPath, fixture.libraryPath("movie.mkv"))
			gotKind := fixture.recordedKind(linkPath)
			if gotKind != linker.Copy.String() && gotKind != linker.Reflink.String() {
				t.Fatalf("gotValue kind %q, expectedValue the copy recorded with its kind", gotKind)
			}
		})
	}
}

func TestIsolateStraddlingMedia_leavesMediaOutsideMissingPiecesLinked(t *testing.T) {
	fixture := newApplyFixture(t)
	entries := []torrentEntry{
		{path: "a.mkv", content: patternBytes(1, 32), localName: "a.mkv"},
		{path: "b.mkv", content: patternBytes(2, 20), localName: "b.mkv"},
		{path: "b.nfo", content: patternBytes(3, 5)},
	}
	candidate, proposal, linkRoot := fixture.linkedPartialAnnex(entries)

	result, err := fixture.isolate(candidate, proposal, linkRoot)

	if err != nil || len(result.Replaced) != 1 {
		t.Fatalf("gotValue %+v %v, expectedValue only b.mkv replaced", result, err)
	}
	assertSameFile(t, filepath.Join(linkRoot, "Movie.2020", "a.mkv"), fixture.libraryPath("a.mkv"))
	assertIndependentCopy(t, filepath.Join(linkRoot, "Movie.2020", "b.mkv"), fixture.libraryPath("b.mkv"))
}

func TestIsolateStraddlingMedia_refusesAndChangesNothingWhenACopyIsImpossible(t *testing.T) {
	fixture := newApplyFixture(t)
	entries := []torrentEntry{
		{path: "a.mkv", content: patternBytes(1, 30), localName: "a.mkv"},
		{path: "a.nfo", content: patternBytes(3, 4)},
		{path: "b.mkv", content: patternBytes(2, 30), localName: "b.mkv"},
	}
	candidate, proposal, linkRoot := fixture.linkedPartialAnnex(entries)
	prepareCalls := 0
	fixture.prepareCopy = func(source, destination, root string) (linker.PreparedCopy, error) {
		prepareCalls++
		if prepareCalls == 1 {
			return linker.PrepareCopy(source, destination, root)
		}
		return linker.PreparedCopy{}, linker.ErrInsufficientSpace
	}

	_, err := fixture.isolate(candidate, proposal, linkRoot)

	if !errors.Is(err, ErrIsolationImpossible) || !errors.Is(err, linker.ErrInsufficientSpace) || prepareCalls != 2 {
		t.Fatalf("gotValue %v after %d calls, expectedValue a typed refusal", err, prepareCalls)
	}
	for _, name := range []string{"a.mkv", "b.mkv"} {
		assertSameFile(t, filepath.Join(linkRoot, "Movie.2020", name), fixture.libraryPath(name))
	}
	if gotEntries, expectedEntries := len(linkDirEntries(t, linkRoot)), 3; gotEntries != expectedEntries {
		t.Fatalf("gotValue %v, expectedValue no temporary copy left", linkDirEntries(t, linkRoot))
	}
}
