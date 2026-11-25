//go:build linux

package local

import (
	"io/fs"
	"syscall"
)

// DeviceOf renvoie l'identifiant du périphérique du fichier ; false si le système ne le fournit pas.
func DeviceOf(info fs.FileInfo) (uint64, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(stat.Dev), true //nolint:unconvert // Dev est un uint32 sur mips
}
