package sftpfs_test

import (
	"runtime"
	"testing"
)

// skipOnWindows écarte un test qui suppose une sémantique POSIX (serveur SFTP de test, cible toujours POSIX).
func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("le serveur SFTP simulé reflète le disque Windows ; les hôtes distants Windows sont refusés par doctor")
	}
}
