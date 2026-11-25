package sftptest

import (
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
