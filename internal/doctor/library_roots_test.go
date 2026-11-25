package doctor

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"necromanciarr/internal/config"
	"necromanciarr/internal/fsys"
)

func TestChecks_linkFeasibilityNamesAMissingLibraryRoot(t *testing.T) {
	linkDirectory := t.TempDir()
	missingLibraryRoot := filepath.Join(t.TempDir(), "unmounted")
	settings := config.Config{LibraryRoots: []string{missingLibraryRoot}, LinkDirs: []string{linkDirectory}, LinkType: "hardlink"}
	deps := Deps{SameVolume: func(string, string) (bool, error) { return true, nil }}.withDefaults()

	got := checkLinkFeasibility(settings, deps)

	if got.Status != Fail || got.Code != CodeLibraryRootUnavailable {
		t.Fatalf("expectedValue fail %s, gotValue %+v", CodeLibraryRootUnavailable, got)
	}
	if entries, err := os.ReadDir(linkDirectory); err != nil || len(entries) != 0 {
		t.Fatalf("expectedValue nothing probed in the link directory, gotValue %v", entries)
	}
}

func TestRegistryFor_refusesEveryWriteRootWhenALibraryRootDoesNotParse(t *testing.T) {
	linkDirectory := t.TempDir()
	settings := config.Config{LibraryRoots: []string{t.TempDir(), "sftp://seedbox"}}

	_, err := Deps{}.registryFor(settings).WriteRoot(fsys.Location{Path: linkDirectory})

	if !errors.Is(err, fsys.ErrNoLibraryRoots) {
		t.Fatalf("expectedValue fsys.ErrNoLibraryRoots, gotValue %v", err)
	}
}
