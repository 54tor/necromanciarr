//go:build linux

package local

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestFileID_knownForAnyInformationWithAnInode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	rawInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	gotValue, known := New().FileID(rawInfo)
	stat := rawInfo.Sys().(*syscall.Stat_t)
	expectedDevice, expectedInode := uint64(stat.Dev), uint64(stat.Ino) //nolint:unconvert // types propres à chaque architecture
	if !known || gotValue.Device != expectedDevice || gotValue.Inode != expectedInode {
		t.Errorf("FileID = %+v, %v, want the device and inode of the file", gotValue, known)
	}
}

func TestReliableFileID_knownOnlyForVolumeInformationOnALocalFilesystem(t *testing.T) {
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
	if _, known := ReliableFileID(volumeInfo); !known {
		t.Error("ReliableFileID unknown for a volume information on a local filesystem")
	}
	rawInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, known := ReliableFileID(rawInfo); known {
		t.Error("ReliableFileID known for an information not produced by the volume")
	}
	unreliableInfo := localInfo{FileInfo: rawInfo, identityReliable: false}
	if _, known := ReliableFileID(unreliableInfo); known {
		t.Error("ReliableFileID known for an information on a network or FUSE filesystem")
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

func TestCapabilities_reportMissingMountRootDetection(t *testing.T) {
	previous := mountRootDetectable
	t.Cleanup(func() { mountRootDetectable = previous })
	cases := map[bool]string{false: reasonStatxMountRootUnsupported, true: ""}
	for detectable, expectedValue := range cases {
		mountRootDetectable = func() bool { return detectable }
		capabilities, err := New().Capabilities(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if gotValue := capabilities.Reasons[reasonKeyMountRoot]; gotValue != expectedValue {
			t.Errorf("detectable=%v: Reasons[%q] = %q, want %q", detectable, reasonKeyMountRoot, gotValue, expectedValue)
		}
	}
}
