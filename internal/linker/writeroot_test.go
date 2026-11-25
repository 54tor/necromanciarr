package linker

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/matcher"
	"necromanciarr/internal/store"
	"necromanciarr/internal/torrentcheck"
)

func TestPlan_remoteDestinationUsesPOSIXPaths(t *testing.T) {
	torrent := &torrentcheck.Torrent{Root: "Pack", Files: []torrentcheck.File{
		{Path: `a\b/c.mkv`, Size: 1}, {Path: "d.mkv", Size: 1, Offset: 1},
	}}
	assignment := matcher.Assignment{ByTorrentFile: map[int]store.MediaFile{0: {Path: "/srv/media/c.mkv"}}}

	gotOps, err := Plan(torrent, assignment, "/srv/links", fsys.RemotePaths())
	if err != nil {
		t.Fatal(err)
	}
	expectedValue := `/srv/links/Pack/a\b/c.mkv`
	if len(gotOps) != 1 || gotOps[0].Destination != expectedValue || gotOps[0].Root != "/srv/links" {
		t.Fatalf("expectedValue %s, gotValue %+v", expectedValue, gotOps)
	}
	if _, err := Plan(torrent, assignment, t.TempDir(), fsys.LocalPaths()); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("expectedValue ErrUnsafePath for a backslash on the local disk, gotValue %v", err)
	}
}

func TestChooseLinkDir_skipsLinkDirOnAnotherVolume(t *testing.T) {
	sameDeviceEverywhere := func(fsys.Location) (string, bool, error) { return "dev1", true, nil }
	remoteLinkDir := fsys.Location{Volume: "seedbox", Path: "/l1"}

	gotValue, err := chooseLinkDir(localLocation("/home/u/library/a.mkv"),
		[]fsys.Location{remoteLinkDir, localLocation("/l2")}, sameDeviceEverywhere)

	expectedValue := localLocation("/l2")
	if err != nil || gotValue != expectedValue {
		t.Fatalf("expectedValue %v, gotValue %v (%v)", expectedValue, gotValue, err)
	}
}

func TestChooseLinkDir_unknownVolumeIdentityIsNeverSameDevice(t *testing.T) {
	sourcePath := "/home/u/library/a.mkv"
	unknownIdentity := func(location fsys.Location) (string, bool, error) {
		isSource := location.Path == sourcePath
		return "dev1", isSource, nil
	}

	_, err := chooseLinkDir(localLocation(sourcePath), localLocations("/l1"), unknownIdentity)

	if !errors.Is(err, ErrNoLinkDirOnSameDevice) {
		t.Fatalf("expectedValue ErrNoLinkDirOnSameDevice, gotValue %v", err)
	}
}

// recordingRoot note chaque écriture réussie faite à la racine : tout ce qu'Apply ou Probe crée doit y passer.
type recordingRoot struct {
	inner fsys.WriteRoot
	mutex sync.Mutex
	calls []string
}

func (root *recordingRoot) recordSuccess(err error, method, relative string) {
	if err != nil {
		return
	}
	root.mutex.Lock()
	defer root.mutex.Unlock()
	root.calls = append(root.calls, method+" "+filepath.ToSlash(relative))
}

func (root *recordingRoot) Root() fsys.Location { return root.inner.Root() }

func (root *recordingRoot) MkdirAll(relative string, perm fs.FileMode) error {
	err := root.inner.MkdirAll(relative, perm)
	root.recordSuccess(err, "MkdirAll", relative)
	return err
}

func (root *recordingRoot) Symlink(target, relative string) error {
	err := root.inner.Symlink(target, relative)
	root.recordSuccess(err, "Symlink", relative)
	return err
}

func (root *recordingRoot) Hardlink(source fsys.Location, relative string) error {
	err := root.inner.Hardlink(source, relative)
	root.recordSuccess(err, "Hardlink", relative)
	return err
}

func (root *recordingRoot) Reflink(source fsys.Location, relative string) error {
	reflinker, supported := root.inner.(fsys.Reflinker)
	if !supported {
		return ErrUnsupported
	}
	err := reflinker.Reflink(source, relative)
	root.recordSuccess(err, "Reflink", relative)
	return err
}

func (root *recordingRoot) CreateExclusive(relative string, perm fs.FileMode) (io.WriteCloser, error) {
	writer, err := root.inner.CreateExclusive(relative, perm)
	root.recordSuccess(err, "CreateExclusive", relative)
	return writer, err
}

func (root *recordingRoot) CopyFrom(ctx context.Context, source fsys.Location, relative string) (int64, error) {
	copied, err := root.inner.CopyFrom(ctx, source, relative)
	root.recordSuccess(err, "CopyFrom", relative)
	return copied, err
}

func (root *recordingRoot) Replace(oldRelative, newRelative string) error {
	err := root.inner.Replace(oldRelative, newRelative)
	root.recordSuccess(err, "Replace", oldRelative+" -> "+newRelative)
	return err
}

func (root *recordingRoot) Remove(relative string) error {
	err := root.inner.Remove(relative)
	root.recordSuccess(err, "Remove", relative)
	return err
}

func (root *recordingRoot) Lstat(relative string) (fs.FileInfo, error) {
	return root.inner.Lstat(relative)
}

func (root *recordingRoot) Close() error { return root.inner.Close() }

func (root *recordingRoot) recorded() []string {
	root.mutex.Lock()
	defer root.mutex.Unlock()
	return slices.Clone(root.calls)
}

func TestApply_writesOnlyThroughTheWriteRoot(t *testing.T) {
	rootMethodOf := map[Kind]string{Hardlink: "Hardlink", Symlink: "Symlink"}
	for kind, rootMethod := range rootMethodOf {
		t.Run(kind.String(), func(t *testing.T) {
			operation := singleOp(t)
			root := &recordingRoot{inner: openRoot(t, operation.Root)}

			if _, err := Apply(root, localSource(t), []Op{operation}, []Kind{kind}); err != nil {
				t.Fatal(err)
			}

			expectedValue := []string{"MkdirAll links", "MkdirAll links/Pack", rootMethod + " links/Pack/a.mkv"}
			if gotValue := root.recorded(); !slices.Equal(gotValue, expectedValue) {
				t.Fatalf("expectedValue %v, gotValue %v", expectedValue, gotValue)
			}
			assertExists(t, operation.Destination, true)
		})
	}
}

func TestProbe_removesEveryEntryItCreatesInTheRoot(t *testing.T) {
	root := &recordingRoot{inner: openRoot(t, t.TempDir())}

	if _, err := Probe(root); err != nil {
		t.Fatal(err)
	}

	created, removed := createdAndRemoved(root.recorded())
	if len(created) == 0 || !slices.Equal(created, removed) {
		t.Fatalf("expectedValue every created entry removed, gotValue created %v removed %v", created, removed)
	}
}

func createdAndRemoved(calls []string) ([]string, []string) {
	var created, removed []string
	for _, call := range calls {
		method, relative, _ := strings.Cut(call, " ")
		switch method {
		case "Remove":
			removed = append(removed, relative)
		case "Replace":
			oldRelative, _, _ := strings.Cut(relative, " -> ")
			removed = append(removed, oldRelative)
		default:
			created = append(created, relative)
		}
	}
	slices.Sort(created)
	slices.Sort(removed)
	return created, slices.Compact(removed)
}

// rootWithoutReflink n'expose que les méthodes de fsys.WriteRoot : aucun clonage possible.
type rootWithoutReflink struct{ fsys.WriteRoot }

func TestApply_reflinkNeverFallsBackToACopy(t *testing.T) {
	operation := singleOp(t)
	root := rootWithoutReflink{WriteRoot: openRoot(t, operation.Root)}

	result, err := Apply(root, localSource(t), []Op{operation}, []Kind{Reflink, Hardlink})
	if err != nil {
		t.Fatal(err)
	}

	sourceInfo, sourceErr := os.Stat(operation.Source)
	destinationInfo, destinationErr := os.Stat(operation.Destination)
	isHardlink := sourceErr == nil && destinationErr == nil && os.SameFile(sourceInfo, destinationInfo)
	if !slices.Equal(result.Used, []Kind{Hardlink}) || !isHardlink {
		t.Fatalf("expectedValue the listed hardlink after a refused reflink, gotValue %v (hardlink %v)", result.Used, isHardlink)
	}
}

func TestApply_refusesOpRootOtherThanTheWriteRoot(t *testing.T) {
	operation := singleOp(t)
	writeRootDir := t.TempDir()

	_, err := Apply(openRoot(t, writeRootDir), localSource(t), []Op{operation}, []Kind{Hardlink})

	if !errors.Is(err, ErrDestinationEscapesRoot) {
		t.Fatalf("expectedValue ErrDestinationEscapesRoot, gotValue %v", err)
	}
	if entries := directoryEntries(t, writeRootDir); len(entries) != 0 {
		t.Fatalf("expectedValue nothing created in the write root, gotValue %v", entries)
	}
}

func TestReadViewOf_neverHandsOutAVolume(t *testing.T) {
	view := readViewOf(openRoot(t, t.TempDir()))

	_, isVolume := view.(fsys.Volume)
	_, opensWriteRoots := view.(interface {
		OpenWriteRoot(dir string) (fsys.WriteRoot, error)
	})
	if view == nil || isVolume || opensWriteRoots {
		t.Fatalf("expectedValue a read-only view, gotValue %T", view)
	}
}

// wrappedSyntax emballe la syntaxe POSIX : seule RemotePaths elle-même desserre les règles des segments.
type wrappedSyntax struct{ fsys.PathSyntax }

func TestPlan_onlyTheRemoteSyntaxRelaxesSegmentRules(t *testing.T) {
	torrent := &torrentcheck.Torrent{Root: "Pack", Files: []torrentcheck.File{
		{Path: `a\..\..\escape.mkv`, Size: 1}, {Path: "d.mkv", Size: 1, Offset: 1},
	}}
	assignment := matcher.Assignment{ByTorrentFile: map[int]store.MediaFile{0: {Path: "/srv/media/c.mkv"}}}
	strictSyntaxes := map[string]fsys.PathSyntax{
		"local":          fsys.LocalPaths(),
		"wrapped remote": wrappedSyntax{PathSyntax: fsys.RemotePaths()},
	}
	for name, paths := range strictSyntaxes {
		if _, err := Plan(torrent, assignment, "/srv/links", paths); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("%s: expectedValue ErrUnsafePath, gotValue %v", name, err)
		}
	}
}

func TestPlan_refusesMissingPathSyntax(t *testing.T) {
	assignment := matcher.Assignment{ByTorrentFile: map[int]store.MediaFile{0: {Path: "/srv/media/c.mkv"}}}

	_, err := Plan(packTorrent(), assignment, "/srv/links", nil)

	if !errors.Is(err, ErrNoPathSyntax) {
		t.Fatalf("expectedValue ErrNoPathSyntax, gotValue %v", err)
	}
}
