//go:build !unix

package dedupe

import "io/fs"

// fileIdentity : hors Unix, os.FileInfo ne donne pas l'inode ; il n'est alors ni comparé ni enregistré.
func fileIdentity(_ fs.FileInfo) (device, inode uint64, known bool) {
	return 0, 0, false
}
