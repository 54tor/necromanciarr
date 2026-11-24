package pipeline

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"necromanciarr/internal/store"
	"necromanciarr/internal/torrentcheck"
)


func TestLinkStage_namesTheTorrentFolderWithTheFullInfohash(t *testing.T) {
	fixture := newApplyFixture(t)
	candidate, proposal := fixture.linkedProposal(movieOnly(), store.ProposalFull)

	gotName := filepath.Base(fixture.progressOf(proposal.ID).LinkRoot)

	if expectedName := "Movie.2020 [" + candidate.Infohash + "]"; gotName != expectedName {
		t.Fatalf("gotValue %q, expectedValue %q", gotName, expectedName)
	}
}

func TestTorrentLinkRoot_truncatesTheRootNameToKeepTheFullInfohash(t *testing.T) {
	infohash := strings.Repeat("ab", 20)
	longRoot := strings.Repeat("é", 200)

	gotRoot, err := torrentLinkRoot("/home/u/links", &torrentcheck.Torrent{Root: longRoot}, infohash)

	gotName := filepath.Base(gotRoot)
	isExpected := err == nil && len(gotName) <= maxFolderNameBytes && strings.HasSuffix(gotName, " ["+infohash+"]") &&
		strings.HasPrefix(longRoot, strings.TrimSuffix(gotName, " ["+infohash+"]"))
	if !isExpected {
		t.Fatalf("gotValue %q (%d bytes) %v, expectedValue a truncated root and the full infohash", gotName, len(gotName), err)
	}
}

// moveToLegacyFolder déplace le dossier lié vers l'ancien nom (8 caractères d'infohash), réenregistre ses liens et
// l'étape comme l'aurait fait une version antérieure.
func (fixture *applyFixture) moveToLegacyFolder(candidate store.Candidate, proposal store.Proposal) string {
	fixture.t.Helper()
	ctx := context.Background()
	currentRoot := fixture.progressOf(proposal.ID).LinkRoot
	legacyRoot := filepath.Join(fixture.linkDir, "Movie.2020 ["+candidate.Infohash[:8]+"]")
	if err := os.Rename(currentRoot, legacyRoot); err != nil {
		fixture.t.Fatal(err)
	}
	links, err := fixture.store.ListLinks(ctx, "")
	if err != nil {
		fixture.t.Fatal(err)
	}
	for _, link := range links {
		link.LinkPath = legacyRoot + strings.TrimPrefix(link.LinkPath, currentRoot)
		if err := fixture.store.RecordLink(ctx, link); err != nil {
			fixture.t.Fatal(err)
		}
	}
	progress := store.ProposalProgress{ProposalID: proposal.ID, Step: store.StepLinked, LinkRoot: legacyRoot}
	if err := fixture.store.SaveProposalProgress(ctx, progress, fixedNow); err != nil {
		fixture.t.Fatal(err)
	}
	return legacyRoot
}

func TestLinkAndSeed_keepUsingARecordedLegacyFolder(t *testing.T) {
	fixture := newApplyFixture(t)
	fixture.skipRecheck = true
	candidate, proposal := fixture.linkedProposal(movieOnly(), store.ProposalFull)
	fullHashRoot := fixture.progressOf(proposal.ID).LinkRoot
	legacyRoot := fixture.moveToLegacyFolder(candidate, proposal)
	if err := os.Remove(filepath.Join(legacyRoot, "Movie.2020", "movie.mkv")); err != nil {
		t.Fatal(err)
	}
	fixture.mustRunStage(StageSeed)
	if err := fixture.store.AcceptProposals(context.Background(), []int64{proposal.ID}, fixedNow); err != nil {
		t.Fatal(err)
	}

	fixture.mustRunStage(StageLink)
	seeded := fixture.mustRunStage(StageSeed)

	if gotRoot := fixture.progressOf(proposal.ID).LinkRoot; gotRoot != legacyRoot || seeded.Seeded != 1 {
		t.Fatalf("gotValue root %q result %+v, expectedValue the legacy folder relinked and seeded", gotRoot, seeded)
	}
	assertSameFile(t, filepath.Join(legacyRoot, "Movie.2020", "movie.mkv"), fixture.libraryPath("movie.mkv"))
	if _, err := os.Lstat(fullHashRoot); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("gotValue %v, expectedValue no second folder for the same torrent", err)
	}
	if gotSavePath := fixture.client.added[candidate.Infohash].savePath; gotSavePath != legacyRoot {
		t.Fatalf("gotValue %q, expectedValue the client pointed at the legacy folder", gotSavePath)
	}
}
