//go:build linux

package local

import "golang.org/x/sys/unix"

// FilesystemKindOf lit le type du système de fichiers qui porte path (statfs).
func FilesystemKindOf(path string) (FilesystemKind, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return Unknown, err
	}
	return kindFromMagic(uint32(stat.Type)), nil // #nosec G115 -- les nombres magiques tiennent sur 32 bits
}
