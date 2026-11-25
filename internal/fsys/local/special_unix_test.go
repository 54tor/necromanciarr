//go:build unix

package local

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"necromanciarr/internal/fsys"
)

// specialFileTimeout : un open bloquant sur une FIFO sans écrivain ne rendrait jamais la main.
const specialFileTimeout = 5 * time.Second

func plantFIFO(t *testing.T, dir string) string {
	t.Helper()
	fifoPath := filepath.Join(dir, "pipe.mkv")
	if err := unix.Mkfifo(fifoPath, 0o644); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	return fifoPath
}

func withinTimeout(t *testing.T, operation func() error) error {
	t.Helper()
	result := make(chan error, 1)
	go func() { result <- operation() }()
	select {
	case err := <-result:
		return err
	case <-time.After(specialFileTimeout):
		t.Fatal("operation on a special file did not return")
		return nil
	}
}

func TestOpen_refusesAFIFOWithoutBlocking(t *testing.T) {
	tree := newTestTree(t, true)
	fifoPath := plantFIFO(t, tree.libraryDir)
	err := withinTimeout(t, func() error {
		file, err := New().Open(fifoPath)
		if err == nil {
			_ = file.Close()
		}
		return err
	})
	if !errors.Is(err, fs.ErrInvalid) {
		t.Errorf("Open(FIFO) error = %v, want fs.ErrInvalid", err)
	}
}

func TestOpen_refusesADirectory(t *testing.T) {
	tree := newTestTree(t, true)
	if _, err := New().Open(tree.libraryDir); !errors.Is(err, fs.ErrInvalid) {
		t.Errorf("Open(directory) error = %v, want fs.ErrInvalid", err)
	}
}

func TestCopyFrom_refusesAFIFOSourceWithoutBlocking(t *testing.T) {
	tree := newTestTree(t, true)
	fifoPath := plantFIFO(t, tree.libraryDir)
	err := withinTimeout(t, func() error {
		_, err := tree.root.CopyFrom(t.Context(), fsys.Location{Path: fifoPath}, "copy.mkv")
		return err
	})
	if !errors.Is(err, fs.ErrInvalid) {
		t.Errorf("CopyFrom(FIFO) error = %v, want fs.ErrInvalid", err)
	}
	if _, err := os.Lstat(filepath.Join(tree.linkDir, "copy.mkv")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("refused copy left an entry: %v", err)
	}
}

func TestCreateExclusive_dropsSetuidSetgidAndSticky(t *testing.T) {
	tree := newTestTree(t, true)
	writer, err := tree.root.CreateExclusive("file", fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky|0o755)
	if err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	info, err := os.Lstat(filepath.Join(tree.linkDir, "file"))
	if err != nil {
		t.Fatal(err)
	}
	if specialBits := info.Mode() & (fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky); specialBits != 0 {
		t.Errorf("created file mode = %v, want no setuid, setgid or sticky bit", info.Mode())
	}
}

// Un fichier ordinaire remplacé par une FIFO entre le Stat et l'ouverture : seul le fstat le voit.
func TestCheckOpenedRegular_refusesAFIFOSwappedInAfterTheStat(t *testing.T) {
	tree := newTestTree(t, true)
	regularInfo, err := os.Stat(tree.moviePath)
	if err != nil {
		t.Fatal(err)
	}
	fifo, err := os.OpenFile(plantFIFO(t, tree.libraryDir), os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fifo.Close() }()
	if _, err := checkOpenedRegular(fifo, regularInfo); !errors.Is(err, fs.ErrInvalid) {
		t.Errorf("checkOpenedRegular error = %v, want fs.ErrInvalid", err)
	}
}
