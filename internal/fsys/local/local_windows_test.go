//go:build windows

package local

import (
	"io/fs"
	"syscall"
	"testing"
	"time"
)

// fakeInfo porte un FileAttributes choisi : seuls Mode et Sys servent à isPlainDirectory.
type fakeInfo struct{ attributes uint32 }

func (fakeInfo) Name() string       { return "fake" }
func (fakeInfo) Size() int64        { return 0 }
func (fakeInfo) Mode() fs.FileMode  { return fs.ModeDir }
func (fakeInfo) ModTime() time.Time { return time.Time{} }
func (fakeInfo) IsDir() bool        { return true }
func (info fakeInfo) Sys() any {
	return &syscall.Win32FileAttributeData{FileAttributes: info.attributes}
}

func TestIsPlainDirectory_refusesAReparsePoint(t *testing.T) {
	if isPlainDirectory(fakeInfo{attributes: syscall.FILE_ATTRIBUTE_DIRECTORY | syscall.FILE_ATTRIBUTE_REPARSE_POINT}) {
		t.Error("a junction or mounted folder was taken for a plain directory")
	}
	if !isPlainDirectory(fakeInfo{attributes: syscall.FILE_ATTRIBUTE_DIRECTORY}) {
		t.Error("a plain directory was refused")
	}
}

func TestFileID_unknownOnWindows(t *testing.T) {
	info, err := New().Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, known := New().FileID(info); known {
		t.Error("FileID known on Windows")
	}
}
