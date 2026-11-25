package fsystest_test

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/fsystest"
)

// osVolume est un volume local de contrôle, propre à ce test : il prouve que la suite tourne et attrape un volume
// non confiné. Ce n'est pas le volume local de production (internal/fsys/local).
type osVolume struct{}

// readOnlyFile cache *os.File : seules les méthodes de lecture restent atteignables.
type readOnlyFile struct{ file *os.File }

func (readOnly readOnlyFile) ReadAt(buffer []byte, offset int64) (int, error) {
	return readOnly.file.ReadAt(buffer, offset)
}
func (readOnly readOnlyFile) Close() error               { return readOnly.file.Close() }
func (readOnly readOnlyFile) Stat() (fs.FileInfo, error) { return readOnly.file.Stat() }

func (osVolume) Volume() string { return "" }

func (osVolume) Open(name string) (fsys.File, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	return readOnlyFile{file: file}, nil
}

func (osVolume) Stat(name string) (fs.FileInfo, error)      { return os.Stat(name) }
func (osVolume) Lstat(name string) (fs.FileInfo, error)     { return os.Lstat(name) }
func (osVolume) ReadDir(name string) ([]fs.DirEntry, error) { return os.ReadDir(name) }
func (osVolume) Readlink(name string) (string, error)       { return os.Readlink(name) }
func (osVolume) VolumeID(string) (string, bool, error)      { return "", false, nil }
func (osVolume) FileID(fs.FileInfo) (fsys.FileID, bool)     { return fsys.FileID{}, false }
func (osVolume) FreeSpace(string) (uint64, error)           { return 0, nil }
func (osVolume) Paths() fsys.PathSyntax                     { return fsys.LocalPaths() }

func (osVolume) Canonical(name string) (string, error) {
	resolved, err := filepath.EvalSymlinks(name)
	if err != nil {
		return "", err
	}
	return filepath.Abs(resolved)
}

func (osVolume) Capabilities(context.Context) (fsys.Capabilities, error) {
	return fsys.Capabilities{Symlink: true, Hardlink: true, AtomicReplace: true}, nil
}

func (osVolume) OpenWriteRoot(dir string) (fsys.WriteRoot, error) {
	return &osWriteRoot{dir: dir}, nil
}

type osWriteRoot struct{ dir string }

func (root *osWriteRoot) Root() fsys.Location { return fsys.Location{Path: root.dir} }
func (root *osWriteRoot) Close() error        { return nil }

// resolve confine relative à la racine : ni vide, ni absolu, ni "..", ni parent symbolique (vérifié par Lstat).
func (root *osWriteRoot) resolve(relative string) (string, error) {
	isEscaping := relative == "" || filepath.IsAbs(relative) || filepath.VolumeName(relative) != ""
	segments := strings.FieldsFunc(relative, func(character rune) bool { return os.IsPathSeparator(uint8(character)) })
	if isEscaping || slices.Contains(segments, "..") {
		return "", fsys.ErrUnconfined
	}
	current := root.dir
	for _, parentSegment := range segments[:len(segments)-1] {
		current = filepath.Join(current, parentSegment)
		info, err := os.Lstat(current)
		if err == nil && info.Mode()&fs.ModeSymlink != 0 {
			return "", fsys.ErrUnconfined
		}
	}
	return filepath.Join(root.dir, relative), nil
}

func (root *osWriteRoot) checkSource(source fsys.Location) error {
	if source.Volume != "" || !filepath.IsAbs(source.Path) {
		return fsys.ErrUnconfined
	}
	return nil
}

func (root *osWriteRoot) MkdirAll(relative string, perm fs.FileMode) error {
	target, err := root.resolve(relative)
	if err != nil {
		return err
	}
	return os.MkdirAll(target, perm)
}

func (root *osWriteRoot) Symlink(linkTarget, relative string) error {
	target, err := root.resolve(relative)
	if err != nil {
		return err
	}
	return os.Symlink(linkTarget, target)
}

func (root *osWriteRoot) Hardlink(source fsys.Location, relative string) error {
	if err := root.checkSource(source); err != nil {
		return err
	}
	target, err := root.resolve(relative)
	if err != nil {
		return err
	}
	return os.Link(source.Path, target)
}

func (root *osWriteRoot) CreateExclusive(relative string, perm fs.FileMode) (io.WriteCloser, error) {
	target, err := root.resolve(relative)
	if err != nil {
		return nil, err
	}
	return os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
}

func (root *osWriteRoot) CopyFrom(_ context.Context, source fsys.Location, relative string) (int64, error) {
	if err := root.checkSource(source); err != nil {
		return 0, err
	}
	if _, err := root.resolve(relative); err != nil {
		return 0, err
	}
	sourceFile, err := os.Open(source.Path)
	if err != nil {
		return 0, err
	}
	defer sourceFile.Close()
	writer, err := root.CreateExclusive(relative, 0o644)
	if err != nil {
		return 0, err
	}
	copied, err := io.Copy(writer, sourceFile)
	return copied, errors.Join(err, writer.Close())
}

func (root *osWriteRoot) Replace(oldRelative, newRelative string) error {
	oldTarget, err := root.resolve(oldRelative)
	if err != nil {
		return err
	}
	newTarget, err := root.resolve(newRelative)
	if err != nil {
		return err
	}
	return os.Rename(oldTarget, newTarget)
}

func (root *osWriteRoot) Remove(relative string) error {
	target, err := root.resolve(relative)
	if err != nil {
		return err
	}
	return os.Remove(target)
}

func (root *osWriteRoot) Lstat(relative string) (fs.FileInfo, error) {
	target, err := root.resolve(relative)
	if err != nil {
		return nil, err
	}
	return os.Lstat(target)
}

func newOSSubject(t *testing.T) fsystest.Subject {
	baseDir := t.TempDir()
	libraryRoot := filepath.Join(baseDir, "library")
	linkDir := filepath.Join(baseDir, "links")
	for _, dir := range []string{libraryRoot, linkDir} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	return fsystest.Subject{
		Volume:            osVolume{},
		LibraryRoot:       libraryRoot,
		LinkDir:           linkDir,
		LibraryHasSymlink: fsystest.WriteLibraryFixture(t, libraryRoot),
		PlantSymlink: func(t *testing.T, linkDirRelative, target string) {
			fsystest.PlantLocalSymlink(t, filepath.Join(linkDir, linkDirRelative), target)
		},
	}
}

func TestRun_osVolumeConforms(t *testing.T) {
	fsystest.Run(t, newOSSubject)
}
