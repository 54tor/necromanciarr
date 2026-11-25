package sftptest

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// fixtureDirectoryMode : dossiers parents des fichiers de test, privés à leur propriétaire.
const fixtureDirectoryMode = 0o700

// WriteFile écrit un fichier de test (known_hosts, clé, piège) avec exactement ce mode, umask ignoré, en créant ses
// dossiers parents. Les tests de sftpfs passent par lui : l'interdiction des écritures disque ne lève que ce paquet.
func WriteFile(t testing.TB, path string, content []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), fixtureDirectoryMode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// Link pose un lien dur du fichier servi oldPath en newPath (chemins du disque hôte).
func Link(t testing.TB, oldPath, newPath string) {
	t.Helper()
	if err := os.Link(oldPath, newPath); err != nil {
		t.Fatal(err)
	}
}

// RemoveFile retire un fichier servi (chemin du disque hôte), ainsi depuis BeforeExec pour qu'il disparaisse
// en plein lot ; une absence n'est pas une erreur.
func RemoveFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
