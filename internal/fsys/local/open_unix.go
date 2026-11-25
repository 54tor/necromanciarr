//go:build unix

package local

import (
	"errors"
	"io/fs"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// openRegularReadOnly n'ouvre qu'un fichier ordinaire : Stat d'abord, puis O_RDONLY|O_NONBLOCK (une FIFO
// substituée entre-temps ne bloque pas l'ouverture), fstat comparé au Stat, puis retour en mode bloquant.
func openRegularReadOnly(name string) (*os.File, fs.FileInfo, error) {
	before, err := os.Stat(name)
	if err != nil {
		return nil, nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, nil, errNotRegularFile
	}
	file, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0) // #nosec G304 -- lecture seule d'un chemin choisi par l'appelant
	if err != nil {
		return nil, nil, err
	}
	opened, err := checkOpenedRegular(file, before)
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	return file, opened, nil
}

func checkOpenedRegular(file *os.File, before fs.FileInfo) (fs.FileInfo, error) {
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, errNotRegularFile
	}
	return opened, clearNonblock(file)
}

func clearNonblock(file *os.File) error {
	rawConn, err := file.SyscallConn()
	if err != nil {
		return err
	}
	var setErr error
	controlErr := rawConn.Control(func(descriptor uintptr) {
		setErr = unix.SetNonblock(int(descriptor), false) // #nosec G115 -- un descripteur tient dans un int
	})
	return errors.Join(controlErr, setErr)
}
