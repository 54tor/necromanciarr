//go:build windows

package linker

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

// FilesystemKindOf lit le type du lecteur qui porte path (GetDriveType) : un lecteur réseau ou un partage UNC est
// Network ; un montage rclone (WinFsp) se présente souvent en lecteur fixe et n'est donc pas détecté.
func FilesystemKindOf(path string) (FilesystemKind, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return Unknown, err
	}
	rootPath, err := windows.UTF16PtrFromString(filepath.VolumeName(absolutePath) + `\`)
	if err != nil {
		return Unknown, err
	}
	switch windows.GetDriveType(rootPath) {
	case windows.DRIVE_FIXED, windows.DRIVE_RAMDISK:
		return Local, nil
	case windows.DRIVE_REMOTE:
		return Network, nil
	default:
		return Unknown, nil
	}
}
