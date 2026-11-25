package sftptest

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
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

// SetModTime fixe la date de modification d'un fichier servi (chemin du disque hôte).
func SetModTime(t testing.TB, path string, modTime time.Time) {
	t.Helper()
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

// Rename renomme un fichier servi par-dessus newPath (chemins du disque hôte) : un autre inode prend le nom.
func Rename(t testing.TB, oldPath, newPath string) {
	t.Helper()
	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatal(err)
	}
}

// Symlink remplace linkPath par un lien symbolique vers target (chemin du disque hôte).
func Symlink(t testing.TB, target, linkPath string) {
	t.Helper()
	if err := RemoveFile(linkPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, linkPath); err != nil {
		t.Fatal(err)
	}
}
