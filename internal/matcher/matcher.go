// Package matcher associe les fichiers de la bibliothèque aux fichiers d'un torrent, par taille puis par hash de pièces.
package matcher

import (
	"cmp"
	"context"
	"errors"
	"io"
	"slices"
	"strings"

	"necromanciarr/internal/libraryprovider"
	"necromanciarr/internal/store"
	"necromanciarr/internal/torrentcheck"
)

// ErrReadBudgetExceeded signale que le départage a atteint Options.Budget ; l'Assignment renvoyé reste exploitable.
var ErrReadBudgetExceeded = errors.New("matcher: read budget exceeded")

// FileMatch relie un fichier de release aux fichiers locaux de même taille, triés par chemin.
type FileMatch struct {
	ReleaseFileIndex int
	LocalFiles       []store.MediaFile
}

// Assignment donne, par index de fichier du torrent, le fichier local prouvé (ByTorrentFile) ou les fichiers locaux
// que le hash n'a pas pu départager (Ambiguous) ; un fichier du torrent sans candidat, ou dont tous les fichiers locaux
// sont réfutés par le hash, n'apparaît dans aucune des deux. Ambiguous ne liste que les fichiers locaux non réfutés.
type Assignment struct {
	ByTorrentFile map[int]store.MediaFile
	Ambiguous     map[int][]store.MediaFile
}

// OpenFunc ouvre un fichier local par son chemin.
type OpenFunc func(path string) (io.ReaderAt, error)

// Options règle le départage : Budget plafonne les octets lus (0 = sans limite), Limiter plafonne le débit (nil = sans plafond).
type Options struct {
	Budget  int64
	Limiter torrentcheck.Limiter
}

// Candidates garde, pour chaque fichier de la release, les fichiers locaux de taille exactement égale.
func Candidates(release libraryprovider.Release, files []store.MediaFile) []FileMatch {
	localBySize := filesBySize(files)
	var matches []FileMatch
	for releaseFileIndex, releaseFile := range release.Files {
		sameSize := localBySize[releaseFile.Size]
		if releaseFile.Size <= 0 || len(sameSize) == 0 {
			continue
		}
		matches = append(matches, FileMatch{ReleaseFileIndex: releaseFileIndex, LocalFiles: slices.Clone(sameSize)})
	}
	return matches
}

// Assign associe les fichiers locaux aux fichiers du torrent sans plafond de lecture ni de débit.
func Assign(torrent *torrentcheck.Torrent, local []store.MediaFile, open OpenFunc) (Assignment, error) {
	return Options{}.Assign(context.Background(), torrent, local, open)
}

// Assign associe les fichiers locaux aux fichiers du torrent ; en cas d'erreur (budget, annulation, lecture),
// l'Assignment contient ce qui est déjà prouvé et range le reste dans Ambiguous.
func (options Options) Assign(ctx context.Context, torrent *torrentcheck.Torrent, local []store.MediaFile, open OpenFunc) (Assignment, error) {
	resolver := newResolver(ctx, options, torrent, filesBySize(local), open)
	err := resolver.run()
	return Assignment{ByTorrentFile: resolver.assigned, Ambiguous: resolver.ambiguous}, err
}

// filesBySize trie par chemin pour qu'aucun résultat ne dépende de l'ordre d'entrée ; un fichier vide ne prouve rien.
func filesBySize(files []store.MediaFile) map[int64][]store.MediaFile {
	sorted := slices.Clone(files)
	slices.SortFunc(sorted, compareMediaFiles)
	bySize := map[int64][]store.MediaFile{}
	for _, file := range sorted {
		if file.Size > 0 {
			bySize[file.Size] = append(bySize[file.Size], file)
		}
	}
	return bySize
}

func compareMediaFiles(left, right store.MediaFile) int {
	return cmp.Or(strings.Compare(left.Path, right.Path), cmp.Compare(left.ID, right.ID))
}
