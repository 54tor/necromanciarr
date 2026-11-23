package linker

import (
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

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
// à fichier unique se place directement sous destRoot). Seuls les fichiers prouvés de l'Assignment donnent une Op :
// un fichier ambigu ou absent laisse le torrent incomplet.
func Plan(torrent *torrentcheck.Torrent, assignment matcher.Assignment, destRoot string) ([]Op, error) {
	fileIndexes := slices.Sorted(maps.Keys(assignment.ByTorrentFile))
	ops := make([]Op, 0, len(fileIndexes))
	for _, fileIndex := range fileIndexes {
		if fileIndex < 0 || fileIndex >= len(torrent.Files) {
			return nil, fmt.Errorf("linker: torrent has no file %d", fileIndex)
		}
		torrentFile := torrent.Files[fileIndex]
		destination, err := destinationOf(torrent, torrentFile, destRoot)
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

func destinationOf(torrent *torrentcheck.Torrent, torrentFile torrentcheck.File, destRoot string) (string, error) {
	segments := []string{torrent.Root}
	if !isSingleFile(torrent) {
		segments = append(segments, strings.Split(torrentFile.Path, torrentPathSeparator)...)
	}
	for _, segment := range segments {
		if !isSafeSegment(segment) {
			return "", fmt.Errorf("%w: %q in %q", ErrUnsafePath, segment, torrentFile.Path)
		}
	}
	destination := filepath.Join(append([]string{destRoot}, segments...)...)
	// Seconde barrière, indépendante de la validation des segments : le chemin joint doit rester sous destRoot.
	relative, err := filepath.Rel(destRoot, destination)
	if err != nil || !filepath.IsLocal(relative) {
		return "", fmt.Errorf("%w: %q", ErrUnsafePath, destination)
	}
	return destination, nil
}

func isSingleFile(torrent *torrentcheck.Torrent) bool {
	return len(torrent.Files) == 1 && torrent.Files[0].Path == torrent.Root
}

// isSafeSegment refuse les segments vides, « . », « .. », les séparateurs et ce que l'OS jugerait non local (lecteur,
// nom réservé Windows).
func isSafeSegment(segment string) bool {
	isTraversal := segment == "" || segment == "." || segment == ".."
	hasSeparator := strings.ContainsAny(segment, `/\`)
	return !isTraversal && !hasSeparator && filepath.IsLocal(segment)
}
