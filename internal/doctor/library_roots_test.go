package doctor

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"necromanciarr/internal/config"
	"necromanciarr/internal/fsys"
	"necromanciarr/internal/i18n"
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

func TestChecks_linkFeasibilityNamesNoLibraryRootConfigured(t *testing.T) {
	settings := config.Config{LinkDirs: []string{t.TempDir()}, LinkType: "hardlink"}
	deps := Deps{SameVolume: func(string, string) (bool, error) { return true, nil }}.withDefaults()

	valeurObtenue := checkLinkFeasibility(settings, deps)

	if valeurObtenue.Code != CodeLibraryRootUnavailable {
		t.Fatalf("valeurAttendue %s, valeurObtenue %+v", CodeLibraryRootUnavailable, valeurObtenue)
	}
	if !hasMessageKey(valeurObtenue.Details, i18n.KeyDoctorLibraryRootNoneConfigured) {
		t.Fatalf("valeurAttendue %s, valeurObtenue %+v", i18n.KeyDoctorLibraryRootNoneConfigured, valeurObtenue.Details)
	}
	if !hasMessageKey(valeurObtenue.Hints, i18n.KeyDoctorLibraryRootNoneConfiguredHint) {
		t.Fatalf("valeurAttendue %s, valeurObtenue %+v", i18n.KeyDoctorLibraryRootNoneConfiguredHint, valeurObtenue.Hints)
	}
}

func TestChecks_linkFeasibilityNamesTheUnresolvedLibraryRootPath(t *testing.T) {
	mountedRoot := t.TempDir()
	unmountedRoot := filepath.Join(t.TempDir(), "unmounted")
	settings := config.Config{LibraryRoots: []string{mountedRoot, unmountedRoot}, LinkDirs: []string{t.TempDir()}, LinkType: "hardlink"}
	deps := Deps{SameVolume: func(string, string) (bool, error) { return true, nil }}.withDefaults()

	valeurObtenue := checkLinkFeasibility(settings, deps)

	unavailable := slices.IndexFunc(valeurObtenue.Details, func(detail i18n.Message) bool {
		return detail.Key == i18n.KeyDoctorLibraryRootUnavailable
	})
	if unavailable < 0 {
		t.Fatalf("valeurAttendue %s, valeurObtenue %+v", i18n.KeyDoctorLibraryRootUnavailable, valeurObtenue.Details)
	}
	if valeurObtenue.Details[unavailable].Params["roots"] != unmountedRoot {
		t.Fatalf("valeurAttendue %q, valeurObtenue %q", unmountedRoot, valeurObtenue.Details[unavailable].Params["roots"])
	}
}

func hasMessageKey(messages []i18n.Message, key i18n.Key) bool {
	return slices.ContainsFunc(messages, func(candidate i18n.Message) bool { return candidate.Key == key })
}
