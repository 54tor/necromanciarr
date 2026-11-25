//go:build windows

package local

import (
	"io/fs"
	"os"
	"syscall"

	"necromanciarr/internal/fsys"
)

// fileIdentity : Windows ne donne pas d'index de fichier par FileInfo ; l'identité reste inconnue.
func fileIdentity(fs.FileInfo) (fsys.FileID, bool) {
	return fsys.FileID{}, false
}

// isPlainDirectory refuse tout point d'analyse (lien, jonction, dossier monté) : Go ne marque plus les jonctions
// en ModeSymlink, seul l'attribut REPARSE_POINT les distingue.
func isPlainDirectory(info fs.FileInfo) bool {
	attributes, ok := info.Sys().(*syscall.Win32FileAttributeData)
	isReparsePoint := !ok || attributes.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
	return info.Mode().Type() == fs.ModeDir && !isReparsePoint
}

// crossesMount : un dossier monté est un point d'analyse, déjà refusé par isPlainDirectory.
func crossesMount(_, _ fs.FileInfo, _ *os.Root) (bool, error) {
	return false, nil
}
