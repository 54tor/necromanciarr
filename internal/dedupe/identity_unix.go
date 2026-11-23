//go:build unix

package dedupe

import (
	"io/fs"
	"syscall"
)

// fileIdentity renvoie périphérique et inode ; false si le système ne les fournit pas.
func fileIdentity(info fs.FileInfo) (device, inode uint64, known bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return uint64(stat.Dev), uint64(stat.Ino), true //nolint:unconvert // #nosec G115 -- types propres à chaque OS
}
