//go:build windows

package local

import (
	"io/fs"
	"os"
)

// openRegularReadOnly n'ouvre qu'un fichier ordinaire : Stat d'abord, puis le handle ouvert est comparé au Stat
// (Windows n'a pas de FIFO nommée ouvrable par chemin de fichier ordinaire).
func openRegularReadOnly(name string) (*os.File, fs.FileInfo, error) {
	before, err := os.Stat(name)
	if err != nil {
		return nil, nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, nil, errNotRegularFile
	}
	file, err := os.Open(name) // #nosec G304 -- lecture seule d'un chemin choisi par l'appelant
	if err != nil {
		return nil, nil, err
	}
	opened, err := file.Stat()
	if err == nil && (!opened.Mode().IsRegular() || !os.SameFile(before, opened)) {
		err = errNotRegularFile
	}
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	return file, opened, nil
}
