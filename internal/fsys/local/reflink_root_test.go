package local

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"necromanciarr/internal/fsys"
)

func TestReflink_clonesOrRefusesWithoutLeavingAnEntry(t *testing.T) {
	tree := newTestTree(t, true)
	clonePath := filepath.Join(tree.linkDir, "clone.mkv")

	err := tree.root.Reflink(tree.movieSource(), "clone.mkv")

	if err == nil {
		content, readErr := os.ReadFile(clonePath) // #nosec G304 -- chemin de test sous t.TempDir()
		if readErr != nil || string(content) != testMovieContent {
			t.Fatalf("expectedValue the library content, gotValue %q (%v)", content, readErr)
		}
	} else {
		isLinkRefusal := errors.Is(err, ErrUnsupported) || errors.Is(err, ErrCrossDevice)
		if !isLinkRefusal {
			t.Fatalf("expectedValue ErrUnsupported or ErrCrossDevice, gotValue %v", err)
		}
		if _, statErr := os.Lstat(clonePath); !errors.Is(statErr, fs.ErrNotExist) {
			t.Fatalf("expectedValue no entry after a refused clone, gotValue %v", statErr)
		}
	}
	tree.assertLibraryUntouched(t)
}

func TestReflink_refusesUnconfinedPathsAndSources(t *testing.T) {
	tree := newTestTree(t, true)
	assertUnconfined(t, "Reflink ../escape", tree.root.Reflink(tree.movieSource(), "../escape.mkv"))
	refusedSources := []fsys.Location{{Path: testMovieName}, {Volume: "seedbox", Path: tree.moviePath}}
	for _, source := range refusedSources {
		assertUnconfined(t, "Reflink "+source.String(), tree.root.Reflink(source, "refused.mkv"))
	}
	if _, err := os.Lstat(filepath.Join(tree.linkDir, "refused.mkv")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expectedValue no entry after a refusal, gotValue %v", err)
	}
	tree.assertLibraryUntouched(t)
}

func TestSameFile_seesThroughVolumeInformation(t *testing.T) {
	tree := newTestTree(t, true)
	if err := tree.root.Hardlink(tree.movieSource(), "linked.mkv"); err != nil {
		t.Fatal(err)
	}
	linkedInfo, err := tree.root.Lstat("linked.mkv")
	if err != nil {
		t.Fatal(err)
	}
	movieInfo, err := New().Stat(tree.moviePath)
	if err != nil {
		t.Fatal(err)
	}
	otherInfo, err := os.Stat(tree.linkDir)
	if err != nil {
		t.Fatal(err)
	}

	if !SameFile(linkedInfo, movieInfo) || SameFile(linkedInfo, otherInfo) {
		t.Fatal("expectedValue the hardlink equal to the library file only")
	}
}
