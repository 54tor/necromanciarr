//go:build linux

package library

import (
	"io/fs"
	"syscall"
)

func deviceOf(info fs.FileInfo) (uint64, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(stat.Dev), true //nolint:unconvert // Dev est un uint32 sur mips
}
