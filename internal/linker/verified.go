package linker

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"necromanciarr/internal/torrentcheck"
)

// verifiedFileMode : l'annexe doit rester lisible par le client torrent, qui tourne souvent sous un autre compte.
const verifiedFileMode fs.FileMode = 0o644

type dataWriter func(writer io.Writer, data []byte) error

func writeAll(writer io.Writer, data []byte) error {
	_, err := writer.Write(data)
	return err
}

// WriteVerifiedFile écrit sous root, au chemin relatif relativePath (séparateur « / »), une annexe dont le contenu a été
// prouvé par les pièces. Mêmes garde-fous qu'Apply : aucun composant symbolique, aucune sortie de root, dossiers créés
// un à un. Création exclusive : un fichier déjà présent au contenu identique compte comme fait, tout autre contenu (ou
// une entrée qui n'est pas un fichier ordinaire) donne ErrDestinationExists. En cas d'échec, seul ce que l'appel a créé
// est retiré.
func WriteVerifiedFile(root, relativePath string, data []byte) error {
	return writeVerifiedFile(root, relativePath, data, writeAll)
}

func writeVerifiedFile(root, relativePath string, data []byte, write dataWriter) error {
	localPath := filepath.FromSlash(relativePath)
	if !filepath.IsLocal(localPath) {
		return fmt.Errorf("%w: %q", ErrDestinationEscapesRoot, relativePath)
	}
	run := &transaction{resolvedRoots: map[string]string{}}
	resolvedRoot, relative, err := run.confine(Op{Destination: filepath.Join(root, localPath), Root: root})
	if err != nil {
		return err
	}
	destination := filepath.Join(resolvedRoot, relative)
	err = run.ensureConfinedDir(resolvedRoot, filepath.Dir(relative))
	if err == nil {
		err = checkParentInRoot(resolvedRoot, destination)
	}
	if err == nil {
		err = run.writeExclusive(destination, data, write)
	}
	if err != nil {
		return errors.Join(err, run.rollback())
	}
	return nil
}

// writeExclusive écrit d'abord un fichier temporaire créé avec O_EXCL dans le même dossier, le synchronise puis le
// renomme : un arrêt en pleine écriture ne laisse jamais une annexe tronquée sous son vrai nom.
func (run *transaction) writeExclusive(destination string, data []byte, write dataWriter) error {
	if _, err := os.Lstat(destination); err == nil {
		return checkSameContent(destination, data)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	temporary := filepath.Join(filepath.Dir(destination), "."+filepath.Base(destination)+temporaryMarker+rand.Text())
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, verifiedFileMode) // #nosec G302 G304 -- nom aléatoire confiné sous la racine des liens
	if err != nil {
		return err
	}
	err = errors.Join(write(file, data), file.Sync(), file.Close())
	if err == nil {
		err = os.Rename(temporary, destination)
	}
	if err != nil {
		return errors.Join(err, removeIfPresent(temporary))
	}
	run.created = append(run.created, destination)
	return nil
}

// temporaryMarker distingue les fichiers temporaires de cet outil.
const temporaryMarker = ".necromanciarr-tmp-"

func removeIfPresent(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func checkSameContent(destination string, data []byte) error {
	info, err := os.Lstat(destination)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != int64(len(data)) {
		return fmt.Errorf("%w: %s", ErrDestinationExists, destination)
	}
	existing, err := os.ReadFile(destination) // #nosec G304 -- chemin confiné sous la racine des liens
	if err != nil {
		return err
	}
	if !bytes.Equal(existing, data) {
		return fmt.Errorf("%w: %s", ErrDestinationExists, destination)
	}
	return nil
}

// RelativeDestination renvoie le chemin, relatif à la racine des liens et en séparateurs « / », où Plan place le fichier
// fileIndex du torrent ; il sert à écrire les annexes vérifiées à côté des liens.
func RelativeDestination(torrent *torrentcheck.Torrent, fileIndex int) (string, error) {
	if fileIndex < 0 || fileIndex >= len(torrent.Files) {
		return "", fmt.Errorf("linker: torrent has no file %d", fileIndex)
	}
	destination, err := destinationOf(torrent, torrent.Files[fileIndex], ".")
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(destination), nil
}
