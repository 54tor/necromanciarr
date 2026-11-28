//go:build windows

package local

import (
	"os"
	"path/filepath"
	"testing"
)

// Un lien posé par os.Root.Symlink avec symlinkTarget doit s'ouvrir comme un lien posé par os.Symlink.
func TestWindowsSymlink_rootAndOsProduceReadableLinks(t *testing.T) {
	directory := t.TempDir()
	targetPath := filepath.Join(directory, "target.mkv")
	if err := os.WriteFile(targetPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := root.Symlink(symlinkTarget(targetPath), "via-root"); err != nil {
		t.Fatalf("Root.Symlink: %v", err)
	}
	if err := os.Symlink(targetPath, filepath.Join(directory, "via-os")); err != nil {
		t.Fatalf("os.Symlink: %v", err)
	}
	for _, name := range []string{"via-root", "via-os"} {
		linkPath := filepath.Join(directory, name)
		readTarget, readErr := os.Readlink(linkPath)
		_, openErr := os.ReadFile(linkPath)
		t.Logf("%s: target=%q readlinkErr=%v openErr=%v", name, readTarget, readErr, openErr)
	}
	if _, err := os.ReadFile(filepath.Join(directory, "via-root")); err != nil {
		t.Errorf("link made by Root.Symlink is unreadable: %v", err)
	}
}
