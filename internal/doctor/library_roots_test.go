package doctor

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
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

func TestChecks_libraryRootThatCannotBeListedIsUnreadable(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission bits do not lock a directory here")
	}
	lockedRoot := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(lockedRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lockedRoot, 0o100); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(lockedRoot, 0o750) })

	got := checkLibraryRoots(t.Context(), config.Config{LibraryRoots: []string{lockedRoot}}, Deps{})

	if got.Status != Fail || got.Code != CodeRootUnreadable {
		t.Fatalf("expectedValue fail %s, gotValue %v %s", CodeRootUnreadable, got.Status, got.Code)
	}
}
