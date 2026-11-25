//go:build windows

package sftptest

import (
	"hash/fnv"
	"io/fs"
)

// identityOf : sans Stat_t, une identité tirée du chemin (un lien dur y paraît un autre fichier).
func identityOf(_ fs.FileInfo, remotePath string) fileIdentity {
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(remotePath))
	return fileIdentity{device: 1, inode: hasher.Sum64() | 1, links: 1}
}
