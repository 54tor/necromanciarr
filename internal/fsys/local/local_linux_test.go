//go:build linux

package local

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestFileID_knownOnlyForInformationFromTheVolume(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	kind, err := FilesystemKindOf(path)
	if err != nil || kind != Local {
		t.Skipf("temporary directory is not on a local filesystem: %v, %v", kind, err)
	}
	volumeInfo, err := New().Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	gotValue, known := New().FileID(volumeInfo)
	stat := volumeInfo.Sys().(*syscall.Stat_t)
	expectedDevice, expectedInode := uint64(stat.Dev), uint64(stat.Ino) //nolint:unconvert // types propres à chaque architecture
	if !known || gotValue.Device != expectedDevice || gotValue.Inode != expectedInode {
		t.Errorf("FileID = %+v, %v, want the device and inode of the file", gotValue, known)
	}
	rawInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, known := New().FileID(rawInfo); known {
		t.Error("FileID known for an information not produced by the volume")
	}
}

func TestIsMountRoot_detectsAMountAndNotAPlainDirectory(t *testing.T) {
	cases := map[string]bool{"/proc": true, t.TempDir(): false}
	for dir, expectedValue := range cases {
		root, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatal(err)
		}
		gotValue, err := isMountRoot(root)
		_ = root.Close()
		if err != nil || gotValue != expectedValue {
			t.Errorf("isMountRoot(%s) = %v, %v, want %v", dir, gotValue, err, expectedValue)
		}
	}
}

func TestDeviceOf_readsTheDeviceOfTheFile(t *testing.T) {
	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gotValue, known := DeviceOf(info)
	expectedValue := uint64(info.Sys().(*syscall.Stat_t).Dev) //nolint:unconvert // Dev est un uint32 sur mips
	if !known || gotValue != expectedValue {
		t.Errorf("DeviceOf = %d, %v, want %d, true", gotValue, known, expectedValue)
	}
}
