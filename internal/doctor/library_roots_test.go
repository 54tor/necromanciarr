package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"necromanciarr/internal/config"
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
