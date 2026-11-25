package sftpfs_test

import (
	"testing"

	"necromanciarr/internal/fsys/fsystest"
	"necromanciarr/internal/fsys/sftpfs/sftptest"
)

// newSFTPReadSubject pose la bibliothèque de test à la racine du dossier servi : le lien symbolique relatif
// movie-link est suivi par le serveur comme sous OpenSSH. La part écriture de la suite arrive avec la WriteRoot
// distante.
func newSFTPReadSubject(t *testing.T) fsystest.ReadSubject {
	server := sftptest.Start(t)
	hasSymlink := fsystest.WriteLibraryFixture(t, server.Root)
	return fsystest.ReadSubject{
		Volume:            dialVolume(t, pinnedProvider(t, server), testDeps()),
		LibraryRoot:       "/",
		LibraryHasSymlink: hasSymlink,
	}
}

func TestSFTP_conformance(t *testing.T) {
	fsystest.RunRead(t, newSFTPReadSubject)
}
