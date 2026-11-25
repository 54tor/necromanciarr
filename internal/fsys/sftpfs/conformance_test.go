package sftpfs_test

import (
	"testing"

	"necromanciarr/internal/fsys/fsystest"
)

// newSFTPSubject pose la bibliothèque de test sous /library du dossier servi et un dossier de liens vide /links ; le
// lien symbolique relatif movie-link est suivi par le serveur comme sous OpenSSH. PlantSymlink pose le piège sur le
// disque servi, hors du volume ; une cible commençant par « / » y est lue depuis la racine servie, comme dans un chroot.
func newSFTPSubject(t *testing.T) fsystest.Subject {
	server, volume := writeVolume(t)
	return fsystest.Subject{
		Volume:            volume,
		LibraryRoot:       libraryDir,
		LinkDir:           linksDir,
		LibraryHasSymlink: true,
		PlantSymlink: func(t *testing.T, linkDirRelative, target string) {
			skipWithoutSymlinks(t)
			fsystest.PlantLocalSymlink(t, localPathOf(server, linksDir+"/"+linkDirRelative), target)
		},
		CopyNeedsServerCopy: true,
	}
}

func TestSFTP_conformance(t *testing.T) {
	fsystest.Run(t, newSFTPSubject)
}
