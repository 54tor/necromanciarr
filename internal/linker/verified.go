package linker

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/torrentcheck"
)

// verifiedFileMode : l'annexe doit rester lisible par le client torrent, qui tourne souvent sous un autre compte.
const verifiedFileMode fs.FileMode = 0o644

type dataWriter func(writer io.Writer, data []byte) error

func writeAll(writer io.Writer, data []byte) error {
	_, err := writer.Write(data)
	return err
}

// WriteVerifiedFile écrit dans root, au chemin relatif relativePath (séparateur « / »), une annexe dont le contenu a
// été prouvé par les pièces. Mêmes garde-fous qu'Apply : aucun composant symbolique, aucune sortie de root, dossiers
// créés un à un. Création exclusive : un fichier déjà présent au contenu identique compte comme fait, tout autre
// contenu (ou une entrée qui n'est pas un fichier ordinaire) donne ErrDestinationExists. En cas d'échec, seul ce que
// l'appel a créé est retiré.
func WriteVerifiedFile(root fsys.WriteRoot, relativePath string, data []byte) error {
	return writeVerifiedFile(root, relativePath, data, writeAll)
}

func writeVerifiedFile(root fsys.WriteRoot, relativePath string, data []byte, write dataWriter) error {
	if !isPlainRelative(relativePath) {
		return fmt.Errorf("%w: %q", ErrDestinationEscapesRoot, relativePath)
	}
	maker := &directoryMaker{}
	err := maker.ensure(root, path.Dir(relativePath))
	if err == nil {
		err = writeExclusive(root, relativePath, data, write)
	}
	if err != nil {
		return errors.Join(err, rollback(root, maker.created))
	}
	return nil
}

// isPlainRelative refuse le chemin vide, absolu et tout segment vide, « . » ou « .. ».
func isPlainRelative(relativePath string) bool {
	segments := strings.Split(relativePath, relativeSeparator)
	isBadSegment := func(segment string) bool { return segment == "" || segment == "." || segment == ".." }
	return relativePath != "" && !slices.ContainsFunc(segments, isBadSegment) && filepath.IsLocal(filepath.FromSlash(relativePath))
}

// writeExclusive écrit d'abord un fichier temporaire créé en exclusif dans le même dossier, le synchronise puis le
// renomme : un arrêt en pleine écriture ne laisse jamais une annexe tronquée sous son vrai nom.
func writeExclusive(root fsys.WriteRoot, destination string, data []byte, write dataWriter) error {
	if _, err := root.Lstat(destination); err == nil {
		return checkSameContent(root, destination, data)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return confined(err)
	}
	temporary := path.Join(path.Dir(destination), "."+path.Base(destination)+temporaryMarker+rand.Text())
	writer, err := root.CreateExclusive(temporary, verifiedFileMode)
	if err != nil {
		return confined(err)
	}
	err = errors.Join(write(writer, data), syncWriter(writer), writer.Close())
	if err == nil {
		err = root.Replace(temporary, destination)
	}
	if err != nil {
		return errors.Join(err, removeIfPresent(root, temporary))
	}
	return nil
}

// syncWriter force l'écriture sur disque quand le volume la propose (fichier local).
func syncWriter(writer io.Writer) error {
	if syncer, canSync := writer.(interface{ Sync() error }); canSync {
		return syncer.Sync()
	}
	return nil
}

// temporaryMarker distingue les fichiers temporaires de cet outil.
const temporaryMarker = ".necromanciarr-tmp-"

// checkSameContent relit l'entrée par la vue du volume : une lecture impossible rend son erreur brute (comme en v1) ;
// sans vue (volume distant), le contenu ne peut être prouvé identique et l'entrée est refusée.
func checkSameContent(root fsys.WriteRoot, destination string, data []byte) error {
	conflict := fmt.Errorf("%w: %s", ErrDestinationExists, absoluteIn(root, destination))
	info, err := root.Lstat(destination)
	if err != nil {
		return confined(err)
	}
	view := readViewOf(root)
	if view == nil || !info.Mode().IsRegular() || info.Size() != int64(len(data)) {
		return conflict
	}
	existing, err := readWhole(view, absoluteIn(root, destination), info.Size())
	if err != nil {
		return err
	}
	if !bytes.Equal(existing, data) {
		return conflict
	}
	return nil
}

// RelativeDestination renvoie le chemin, relatif à la racine des liens et en séparateurs « / », où Plan place le fichier
// fileIndex du torrent ; il sert à écrire les annexes vérifiées à côté des liens.
func RelativeDestination(torrent *torrentcheck.Torrent, fileIndex int) (string, error) {
	if fileIndex < 0 || fileIndex >= len(torrent.Files) {
		return "", fmt.Errorf("linker: torrent has no file %d", fileIndex)
	}
	destination, err := destinationOf(torrent, torrent.Files[fileIndex], ".", fsys.LocalPaths())
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(destination), nil
}
