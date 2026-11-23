package matcher

import (
	"fmt"
	"io"

	"necromanciarr/internal/store"
)

// readerCache ouvre chaque fichier local une seule fois, comme l'exige torrentcheck.Source.
type readerCache struct {
	open    OpenFunc
	readers map[string]io.ReaderAt
}

func (cache *readerCache) reader(path string) (io.ReaderAt, error) {
	if reader, cached := cache.readers[path]; cached {
		return reader, nil
	}
	reader, err := cache.open(path)
	if err != nil {
		return nil, fmt.Errorf("matcher: open %s: %w", path, err)
	}
	cache.readers[path] = reader
	return reader, nil
}

// trialSource présente au vérificateur le candidat à la place du fichier testé et les voisins déjà attribués ;
// les autres fichiers sont absents, donc leurs pièces ne sont jamais lues.
type trialSource struct {
	cache            *readerCache
	torrentFileIndex int
	candidatePath    string
	assigned         map[int]store.MediaFile
	openErr          error
}

func (source *trialSource) ReaderAt(fileIndex int) (io.ReaderAt, bool) {
	path := source.candidatePath
	if fileIndex != source.torrentFileIndex {
		neighbour, isAssigned := source.assigned[fileIndex]
		if !isAssigned {
			return nil, false
		}
		path = neighbour.Path
	}
	reader, err := source.cache.reader(path)
	if err != nil {
		source.openErr = err
		return nil, false
	}
	return reader, true
}
