package linker

import (
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/matcher"
	"necromanciarr/internal/torrentcheck"
)

// ErrUnsafePath signale un chemin de torrent qui sortirait du répertoire de liens.
var ErrUnsafePath = errors.New("linker: torrent path escapes the link root")

const torrentPathSeparator = "/"

// Op est un lien à créer : Destination pointera sur Source, dont la taille sur disque doit valoir Size ; Root est
// la racine des liens hors de laquelle Apply refuse d'écrire.
type Op struct {
	Source      string
	Destination string
	Root        string
	Size        int64
}

// Plan reconstitue sous destRoot l'arborescence d'origine du torrent (dossier racine Root puis File.Path ; un torrent
// à fichier unique se place directement sous destRoot), joint selon paths, la syntaxe du volume de destRoot (POSIX
// à distance, même sous Windows). Seuls les fichiers prouvés de l'Assignment donnent une Op : un fichier ambigu ou
// absent laisse le torrent incomplet.
func Plan(torrent *torrentcheck.Torrent, assignment matcher.Assignment, destRoot string, paths fsys.PathSyntax) ([]Op, error) {
	fileIndexes := slices.Sorted(maps.Keys(assignment.ByTorrentFile))
	ops := make([]Op, 0, len(fileIndexes))
	for _, fileIndex := range fileIndexes {
		if fileIndex < 0 || fileIndex >= len(torrent.Files) {
			return nil, fmt.Errorf("linker: torrent has no file %d", fileIndex)
		}
		torrentFile := torrent.Files[fileIndex]
		destination, err := destinationOf(torrent, torrentFile, destRoot, paths)
		if err != nil {
			return nil, err
		}
		ops = append(ops, Op{
			Source:      assignment.ByTorrentFile[fileIndex].Path,
			Destination: destination,
			Root:        destRoot,
			Size:        torrentFile.Size,
		})
	}
	return ops, nil
}

func destinationOf(torrent *torrentcheck.Torrent, torrentFile torrentcheck.File, destRoot string, paths fsys.PathSyntax) (string, error) {
	segments := []string{torrent.Root}
	if !isSingleFile(torrent) {
		segments = append(segments, strings.Split(torrentFile.Path, torrentPathSeparator)...)
	}
	isLocal := paths == fsys.LocalPaths()
	for _, segment := range segments {
		if !isSafeSegment(segment, isLocal) {
			return "", fmt.Errorf("%w: %q in %q", ErrUnsafePath, segment, torrentFile.Path)
		}
	}
	destination := paths.Join(append([]string{destRoot}, segments...)...)
	// Seconde barrière, indépendante de la validation des segments : le chemin joint doit rester sous destRoot.
	relative, err := paths.Rel(destRoot, destination)
	if err != nil || !isBelow(relative, isLocal) {
		return "", fmt.Errorf("%w: %q", ErrUnsafePath, destination)
	}
	return destination, nil
}

func isSingleFile(torrent *torrentcheck.Torrent) bool {
	return len(torrent.Files) == 1 && torrent.Files[0].Path == torrent.Root
}

// isSafeSegment refuse les segments vides, « . », « .. » et les séparateurs ; en local aussi « \ » et ce que l'OS
// jugerait non local (lecteur, nom réservé Windows). À distance (POSIX), « \ » est un caractère de nom ordinaire.
func isSafeSegment(segment string, isLocal bool) bool {
	isTraversal := segment == "" || segment == "." || segment == ".."
	if !isLocal {
		return !isTraversal && !strings.Contains(segment, torrentPathSeparator)
	}
	hasSeparator := strings.ContainsAny(segment, `/\`)
	return !isTraversal && !hasSeparator && filepath.IsLocal(segment)
}

// isBelow dit qu'un chemin relatif reste sous sa base : filepath.IsLocal en local, aucun segment « .. » à distance.
func isBelow(relative string, isLocal bool) bool {
	if isLocal {
		return filepath.IsLocal(relative)
	}
	isEscape := relative == "." || relative == ".." || strings.HasPrefix(relative, "../") || strings.HasPrefix(relative, torrentPathSeparator)
	return !isEscape
}
