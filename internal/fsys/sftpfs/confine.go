package sftpfs

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/pkg/sftp"

	"necromanciarr/internal/fsys"
)

// errNotDirectory : une entrée du chemin existe mais est un fichier ordinaire (ce n'est pas une sortie de la racine).
var errNotDirectory = errors.New("sftpfs: an entry of the path is not a directory")

func unconfined(reason string) error {
	return fmt.Errorf("%w: sftpfs: %s", fsys.ErrUnconfined, reason)
}

// confinedTarget est une destination validée par sa seule syntaxe : parents à vérifier sur le serveur, chemin du
// dossier parent et chemin complet, tous deux absolus sous la racine.
type confinedTarget struct {
	parents              []string
	parentPath, fullPath string
}

// splitRelative refuse le chemin vide, absolu, de plus de maxNameBytes, coupé par un NUL, à segment vide (« a//b »,
// « a/ ») ou « .. » ; les segments « . » tombent. « \ » est un caractère ordinaire d'un nom POSIX, jamais interprété.
func splitRelative(relative string) ([]string, error) {
	isMalformed := relative == "" || len(relative) > maxNameBytes || strings.ContainsRune(relative, 0) || path.IsAbs(relative)
	if isMalformed {
		return nil, unconfined(fmt.Sprintf("relative path %q is empty, absolute, too long or contains NUL", relative))
	}
	var segments []string
	for _, segment := range strings.Split(relative, "/") {
		switch segment {
		case "", "..":
			return nil, unconfined(fmt.Sprintf("relative path %q has an empty or \"..\" segment", relative))
		case ".":
			continue
		}
		segments = append(segments, segment)
	}
	if len(segments) == 0 {
		return nil, unconfined(fmt.Sprintf("relative path %q names the write root itself", relative))
	}
	return segments, nil
}

func (root *writeRoot) target(relative string) (confinedTarget, error) {
	segments, err := splitRelative(relative)
	if err != nil {
		return confinedTarget{}, err
	}
	parents := segments[:len(segments)-1]
	parentPath := path.Join(append([]string{root.dir}, parents...)...)
	return confinedTarget{parents: parents, parentPath: parentPath, fullPath: path.Join(parentPath, segments[len(segments)-1])}, nil
}

// checkDirectoryEntry n'accepte qu'un vrai dossier : un lien symbolique ou une entrée spéciale est une sortie
// possible de la racine.
func checkDirectoryEntry(name string, info fs.FileInfo) error {
	switch mode := info.Mode(); {
	case mode.IsDir():
		return nil
	case mode.IsRegular():
		return fmt.Errorf("%w: %q", errNotDirectory, name)
	default:
		return unconfined(fmt.Sprintf("%q is a symlink or a special entry", name))
	}
}

func checkPlainDirectory(client *sftp.Client, name string) error {
	info, err := client.Lstat(name)
	if err != nil {
		return err
	}
	return checkDirectoryEntry(name, info)
}

// checkResolvesTo exige que le serveur résolve expected en lui-même : un parent remplacé par un lien après son Lstat
// se résout ailleurs et la requête suivante n'est pas envoyée.
func checkResolvesTo(client *sftp.Client, expected string) error {
	resolved, err := client.RealPath(expected)
	if err != nil {
		return err
	}
	if path.Clean(resolved) != expected {
		return unconfined(fmt.Sprintf("%q resolves outside the write root", expected))
	}
	return nil
}

// checkParents vérifie par Lstat chaque parent depuis la racine (aucun lien, aucune entrée spéciale ; un parent
// manquant rend fs.ErrNotExist : seul MkdirAll crée), puis le RealPath du dernier.
func (root *writeRoot) checkParents(client *sftp.Client, target confinedTarget) error {
	current := root.dir
	for _, segment := range target.parents {
		current = path.Join(current, segment)
		if err := checkPlainDirectory(client, current); err != nil {
			return err
		}
	}
	return checkResolvesTo(client, target.parentPath)
}

// checkAfterWrite revoit, après l'écriture, que le parent se résout toujours en lui-même ; sinon l'opération est
// refusée (fsys.ErrUnconfined) et l'entrée laissée en place : la retirer par chemin suivrait le même parent détourné
// et pourrait supprimer une entrée hors de la racine.
func checkAfterWrite(client *sftp.Client, target confinedTarget) error {
	return checkResolvesTo(client, target.parentPath)
}

// checkSource exige un chemin source complet de ce volume, sans NUL : la bibliothèque n'est lue que par le serveur.
func (root *writeRoot) checkSource(source fsys.Location) error {
	isSameVolume := source.Volume == root.volume.Volume()
	if !isSameVolume || !path.IsAbs(source.Path) || strings.ContainsRune(source.Path, 0) {
		return unconfined("source is not an absolute path of this sftp volume")
	}
	return nil
}
