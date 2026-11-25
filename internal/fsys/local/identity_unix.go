//go:build unix

package local

import (
	"io/fs"
	"os"
	"syscall"

	"necromanciarr/internal/fsys"
)

// fileIdentity lit périphérique et inode ; false si le système ne les fournit pas ou si l'inode est nul (certains
// FUSE), qui ne distinguerait rien.
func fileIdentity(info fs.FileInfo) (fsys.FileID, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil || stat.Ino == 0 {
		return fsys.FileID{}, false
	}
	return fsys.FileID{Device: uint64(stat.Dev), Inode: uint64(stat.Ino)}, true //nolint:unconvert // #nosec G115 -- types propres à chaque OS
}

// isPlainDirectory : un dossier réel, jamais un lien symbolique ni une entrée spéciale.
func isPlainDirectory(info fs.FileInfo) bool {
	return info.Mode().Type() == fs.ModeDir
}

// crossesMount dit si child est un point de montage sous la racine : autre périphérique, ou racine d'un montage
// lié du même système de fichiers (Linux, statx). Une identité illisible compte comme une frontière.
func crossesMount(rootInfo, childInfo fs.FileInfo, child *os.Root) (bool, error) {
	rootIdentity, rootKnown := fileIdentity(rootInfo)
	childIdentity, childKnown := fileIdentity(childInfo)
	if !rootKnown || !childKnown || rootIdentity.Device != childIdentity.Device {
		return true, nil
	}
	return isMountRoot(child)
}
