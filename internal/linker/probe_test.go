package linker

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"necromanciarr/internal/fsys"
)

func directoryEntries(t *testing.T, directory string) []string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestRelativeSymlinkTarget_buildsRelativePathBetweenSiblingTrees(t *testing.T) {
	volume := filepath.Join(t.TempDir(), "volumes", "remote")
	linkPath := filepath.Join(volume, "qBittorrent", "yuyu.mkv")
	targetPath := filepath.Join(volume, "1080p", "yuyu (1944)", "mon film.mkv")
	writeFile(t, linkPath, []byte("x"))
	writeFile(t, targetPath, []byte("x"))
	expectedValue := filepath.FromSlash("../1080p/yuyu (1944)/mon film.mkv")

	gotValue, err := RelativeSymlinkTarget(linkPath, targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if gotValue != expectedValue {
		t.Fatalf("gotValue %q, expectedValue %q", gotValue, expectedValue)
	}
}

func TestRelativeSymlinkTarget_resolvesSymlinkedDirectories(t *testing.T) {
	workDir := t.TempDir()
	realTorrentDir := filepath.Join(workDir, "real", "torrents")
	targetPath := filepath.Join(workDir, "real", "library", "film.mkv")
	writeFile(t, filepath.Join(realTorrentDir, "copy.mkv"), []byte("x"))
	writeFile(t, targetPath, []byte("x"))
	aliasDir := filepath.Join(workDir, "alias")
	if err := os.Symlink(realTorrentDir, aliasDir); err != nil {
		t.Fatal(err)
	}
	expectedValue := filepath.FromSlash("../library/film.mkv")

	gotValue, err := RelativeSymlinkTarget(filepath.Join(aliasDir, "copy.mkv"), targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if gotValue != expectedValue {
		t.Fatalf("gotValue %q, expectedValue %q", gotValue, expectedValue)
	}
}

func TestRelativeSymlinkTarget_failsAcrossWindowsVolumes(t *testing.T) {
	linkDir, targetDir := `C:\torrents`, `D:\library`
	if runtime.GOOS != "windows" {
		// Hors Windows, filepath.Rel n'échoue que sur un chemin absolu face à un relatif : même branche d'erreur.
		linkDir, targetDir = "/torrents", "library"
	}

	_, err := relativeBetween(linkDir, targetDir, "film.mkv")

	if !errors.Is(err, ErrNoRelativePath) {
		t.Fatalf("expected ErrNoRelativePath, got %v", err)
	}
}

func TestProbe_reportsUnsupportedSymlinkWithReason(t *testing.T) {
	directory := t.TempDir()
	refusingApplier := applier{linkers: map[Kind]linkFunc{
		Hardlink: hardlink,
		Reflink:  func(fsys.WriteRoot, fsys.Location, string) error { return ErrUnsupported },
		Symlink:  func(fsys.WriteRoot, fsys.Location, string) error { return ErrLinkRefused },
	}}

	capabilities, err := refusingApplier.probe(openRoot(t, directory))
	if err != nil {
		t.Fatal(err)
	}
	if capabilities.Symlink || !errors.Is(capabilities.Reasons[Symlink], ErrLinkRefused) {
		t.Fatalf("symlink = %v reason %v, want refused with ErrLinkRefused", capabilities.Symlink, capabilities.Reasons[Symlink])
	}
	if !capabilities.Hardlink || capabilities.Reasons[Hardlink] != nil {
		t.Fatalf("hardlink = %v reason %v, want supported", capabilities.Hardlink, capabilities.Reasons[Hardlink])
	}
	if capabilities.Reflink || !errors.Is(capabilities.Reasons[Reflink], ErrUnsupported) {
		t.Fatalf("reflink = %v reason %v, want ErrUnsupported", capabilities.Reflink, capabilities.Reasons[Reflink])
	}
}

func TestProbe_leavesNoFileBehind(t *testing.T) {
	directory := t.TempDir()
	existingPath := filepath.Join(directory, "existing.mkv")
	writeFile(t, existingPath, []byte("keep"))

	capabilities, err := Probe(openRoot(t, directory))
	if err != nil {
		t.Fatal(err)
	}
	if !capabilities.Symlink || !capabilities.Hardlink {
		t.Fatalf("expected symlink and hardlink on a local temp dir, got %+v", capabilities)
	}
	expectedValue := []string{"existing.mkv"}
	if gotValue := directoryEntries(t, directory); !slices.Equal(gotValue, expectedValue) {
		t.Fatalf("gotValue %v, expectedValue %v", gotValue, expectedValue)
	}
	if gotContent := string(readFile(t, existingPath)); gotContent != "keep" {
		t.Fatalf("existing file changed: %q", gotContent)
	}
}

func TestProbe_cleansUpWhenALinkFails(t *testing.T) {
	directory := t.TempDir()
	failingApplier := applier{linkers: map[Kind]linkFunc{
		Hardlink: func(fsys.WriteRoot, fsys.Location, string) error { return errors.New("boom") },
		Symlink:  symlink,
	}}

	if _, err := failingApplier.probe(openRoot(t, directory)); err != nil {
		t.Fatal(err)
	}

	if gotValue := directoryEntries(t, directory); len(gotValue) != 0 {
		t.Fatalf("expected an empty directory, got %v", gotValue)
	}
}

func TestProbe_isSafeConcurrently(t *testing.T) {
	directory := t.TempDir()
	root := openRoot(t, directory)
	failures := make(chan error, 8)
	for range 8 {
		go func() {
			_, err := Probe(root)
			failures <- err
		}()
	}
	for range 8 {
		if err := <-failures; err != nil {
			t.Fatal(err)
		}
	}
	if gotValue := directoryEntries(t, directory); len(gotValue) != 0 {
		t.Fatalf("expected an empty directory, got %v", gotValue)
	}
}

func TestProbeBetween_reportsProbedDirectories(t *testing.T) {
	sourceDir, destDir := t.TempDir(), t.TempDir()

	capabilities, err := ProbeBetween(localSource(t), sourceDir, openRoot(t, destDir))
	if err != nil {
		t.Fatal(err)
	}
	resolvedSource, _ := filepath.EvalSymlinks(sourceDir)
	resolvedDest, _ := filepath.EvalSymlinks(destDir)
	if capabilities.SourceDir != resolvedSource || capabilities.DestDir != resolvedDest {
		t.Fatalf("probed %q -> %q, want %q -> %q", capabilities.SourceDir, capabilities.DestDir, resolvedSource, resolvedDest)
	}
	if len(directoryEntries(t, sourceDir))+len(directoryEntries(t, destDir)) != 0 {
		t.Fatal("probe left files behind")
	}
}

func TestProbe_refusesMissingDirectory(t *testing.T) {
	root, err := testRegistry(t).WriteRoot(localLocation(filepath.Join(t.TempDir(), "missing")))
	if err == nil {
		_, err = Probe(root)
	}

	if err == nil {
		t.Fatal("expected an error for a missing directory")
	}
}

func TestCreateLinkInRoot_createsRelativeSymlinkUnderRoot(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "library", "film.mkv"), []byte("abc"))
	destination := filepath.Join(root, "torrents", "film.mkv")
	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		t.Fatal(err)
	}
	expectedValue := filepath.FromSlash("../library/film.mkv")

	if err := CreateLinkInRoot(openRoot(t, root), Symlink, expectedValue, relativeTo(t, root, destination)); err != nil {
		t.Fatal(err)
	}
	gotValue, err := os.Readlink(destination)
	if err != nil || gotValue != expectedValue {
		t.Fatalf("gotValue %q (%v), expectedValue %q", gotValue, err, expectedValue)
	}
}

func TestCreateLinkInRoot_refusesSymlinkedComponent(t *testing.T) {
	fixture := newConfinementFixture(t)
	if err := os.Symlink(fixture.outside, filepath.Join(fixture.operation.Root, "Pack")); err != nil {
		t.Fatal(err)
	}

	err := CreateLinkInRoot(openRoot(t, fixture.operation.Root), Symlink, fixture.operation.Source,
		relativeTo(t, fixture.operation.Root, fixture.operation.Destination))

	if !errors.Is(err, ErrDestinationEscapesRoot) {
		t.Fatalf("expected ErrDestinationEscapesRoot, got %v", err)
	}
	fixture.assertNothingOutside(t)
}

func TestCreateLinkInRoot_neverReplacesAnExistingEntry(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "taken.mkv")
	writeFile(t, destination, []byte("mine"))

	err := CreateLinkInRoot(openRoot(t, root), Symlink, "elsewhere.mkv", relativeTo(t, root, destination))

	if !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("expected ErrDestinationExists, got %v", err)
	}
	if gotContent := string(readFile(t, destination)); gotContent != "mine" {
		t.Fatalf("existing entry changed: %q", gotContent)
	}
}

func TestProbe_detectsRenameOverwriteBehaviour(t *testing.T) {
	deleteDestinationFirst := func(root fsys.WriteRoot, oldPath, newPath string) error {
		if err := root.Remove(newPath); err != nil {
			return err
		}
		return root.Replace(oldPath, newPath)
	}
	deleteDestinationThenFail := func(root fsys.WriteRoot, _, newPath string) error {
		if err := root.Remove(newPath); err != nil {
			return err
		}
		return errors.New("transfer interrupted")
	}
	cases := []struct {
		name          string
		rename        func(root fsys.WriteRoot, oldPath, newPath string) error
		expectedValue bool
	}{
		{"system rename", nil, true},
		// La sonde ne voit que l'état final : un renommage non atomique mais complet passe ; Plan avertit selon le volume.
		{"delete destination first", deleteDestinationFirst, true},
		{"delete destination then fail", deleteDestinationThenFail, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			directory := t.TempDir()
			probingApplier := systemApplier()
			probingApplier.rename = testCase.rename

			capabilities, err := probingApplier.probe(openRoot(t, directory))
			if err != nil {
				t.Fatal(err)
			}

			if gotValue := capabilities.RenameOverwrite; gotValue != testCase.expectedValue {
				t.Fatalf("gotValue %v, expectedValue %v (reason %v)", gotValue, testCase.expectedValue, capabilities.RenameReason)
			}
			if !testCase.expectedValue && !errors.Is(capabilities.RenameReason, ErrRenameOverwrite) {
				t.Fatalf("expected ErrRenameOverwrite, got %v", capabilities.RenameReason)
			}
			if gotEntries := directoryEntries(t, directory); len(gotEntries) != 0 {
				t.Fatalf("probe left %v behind", gotEntries)
			}
		})
	}
}
