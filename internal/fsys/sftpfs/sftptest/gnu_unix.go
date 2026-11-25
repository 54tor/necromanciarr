//go:build !windows

package sftptest

import (
	"io/fs"
	"syscall"
)

// identityOf rend le périphérique, l'inode et le nombre de liens réels du fichier servi.
func identityOf(info fs.FileInfo, _ string) fileIdentity {
	stat, isUnix := info.Sys().(*syscall.Stat_t)
	if !isUnix {
		return fileIdentity{}
	}
	return fileIdentity{device: uint64(stat.Dev), inode: stat.Ino, links: uint64(stat.Nlink)} //nolint:unconvert // types variables selon l'OS
}
