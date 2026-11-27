package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"necromanciarr/internal/config"
	"necromanciarr/internal/doctor"
)

func serviceWithLinkDirs(libraryRoot string, linkDirs ...string) Service {
	return New(Deps{Config: config.Config{LibraryRoots: []string{libraryRoot}, LinkDirs: linkDirs}})
}

func TestApplyFix_createLinkDirCreatesAConfiguredLinkDirOnly(t *testing.T) {
	workspace := t.TempDir()
	configuredDir := filepath.Join(workspace, "links")
	unconfiguredDir := filepath.Join(workspace, "elsewhere")
	service := serviceWithLinkDirs(filepath.Join(workspace, "library"), configuredDir)

	refusal := service.ApplyFix(context.Background(), doctor.FixCreateLinkDir, unconfiguredDir)
	success := service.ApplyFix(context.Background(), doctor.FixCreateLinkDir, configuredDir)

	_, unconfiguredStat := os.Stat(unconfiguredDir)
	configuredInfo, configuredStat := os.Stat(configuredDir)
	isExpected := errors.Is(refusal, config.ErrUsage) && errors.Is(unconfiguredStat, os.ErrNotExist) &&
		success == nil && configuredStat == nil && configuredInfo.IsDir()
	if !isExpected {
		t.Fatalf("valeurObtenue refusal=%v success=%v, valeurAttendue ErrUsage then nil with only the configured dir created", refusal, success)
	}
}

func TestApplyFix_createLinkDirRefusesAnyPathUnderALibraryRoot(t *testing.T) {
	libraryRoot := t.TempDir()
	insideLibrary := filepath.Join(libraryRoot, "links")
	viaSymlink := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(libraryRoot, viaSymlink); err != nil {
		t.Fatal(err)
	}
	viaSymlinkTarget := filepath.Join(viaSymlink, "links")
	service := serviceWithLinkDirs(libraryRoot, insideLibrary, viaSymlinkTarget, libraryRoot)

	for _, target := range []string{insideLibrary, viaSymlinkTarget, libraryRoot} {
		valeurObtenue := service.ApplyFix(context.Background(), doctor.FixCreateLinkDir, target)
		if !errors.Is(valeurObtenue, config.ErrUsage) {
			t.Fatalf("target %q: valeurObtenue %v, valeurAttendue ErrUsage", target, valeurObtenue)
		}
	}
	if _, err := os.Stat(insideLibrary); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("valeurObtenue %v, valeurAttendue nothing created in the library", err)
	}
}

func TestApplyFix_createLinkDirIsIdempotent(t *testing.T) {
	configuredDir := filepath.Join(t.TempDir(), "links")
	service := serviceWithLinkDirs(filepath.Join(t.TempDir(), "library"), configuredDir)

	first := service.ApplyFix(context.Background(), doctor.FixCreateLinkDir, configuredDir)
	second := service.ApplyFix(context.Background(), doctor.FixCreateLinkDir, configuredDir)

	if first != nil || second != nil {
		t.Fatalf("valeurObtenue %v and %v, valeurAttendue nil and nil", first, second)
	}
}

func TestApplyFix_refusesAnUnknownFix(t *testing.T) {
	configuredDir := filepath.Join(t.TempDir(), "links")
	service := serviceWithLinkDirs(filepath.Join(t.TempDir(), "library"), configuredDir)

	valeurObtenue := service.ApplyFix(context.Background(), "format_disk", configuredDir)

	if !errors.Is(valeurObtenue, config.ErrUsage) {
		t.Fatalf("valeurObtenue %v, valeurAttendue ErrUsage", valeurObtenue)
	}
}
