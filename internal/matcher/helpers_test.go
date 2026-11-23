package matcher

import (
	"bytes"
	"crypto/sha1" // #nosec G505 -- SHA-1 imposé par le format BitTorrent v1
	"errors"
	"io"
	"testing"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"necromanciarr/internal/store"
	"necromanciarr/internal/torrentcheck"
)

const testPieceLength = 16

type testFile struct {
	path    string
	content []byte
}

// deterministicBytes donne un contenu distinct par graine, pour que deux épisodes de même taille diffèrent octet par octet.
func deterministicBytes(seed byte, size int) []byte {
	content := make([]byte, size)
	for position := range content {
		content[position] = seed + byte(position*7)
	}
	return content
}

func pieceHashes(data []byte) []byte {
	var hashes []byte
	for start := 0; start < len(data); start += testPieceLength {
		end := min(start+testPieceLength, len(data))
		sum := sha1.Sum(data[start:end]) // #nosec G401 -- SHA-1 imposé par BitTorrent v1
		hashes = append(hashes, sum[:]...)
	}
	return hashes
}

func buildTorrent(t *testing.T, files []testFile) *torrentcheck.Torrent {
	t.Helper()
	var concatenated []byte
	info := metainfo.Info{Name: "Show.S01", PieceLength: testPieceLength}
	for _, file := range files {
		concatenated = append(concatenated, file.content...)
		info.Files = append(info.Files, metainfo.FileInfo{Path: []string{file.path}, Length: int64(len(file.content))})
	}
	info.Pieces = pieceHashes(concatenated)
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatalf("marshal info: %v", err)
	}
	raw, err := bencode.Marshal(metainfo.MetaInfo{InfoBytes: infoBytes, Announce: "http://tracker.example.com/announce"})
	if err != nil {
		t.Fatalf("marshal metainfo: %v", err)
	}
	parsed, err := torrentcheck.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return parsed
}

// fakeLibrary sert des fichiers en mémoire et compte les ouvertures et les octets lus par chemin.
type fakeLibrary struct {
	contents  map[string][]byte
	openCount map[string]int
	bytesRead map[string]int64
}

func newFakeLibrary(contents map[string][]byte) *fakeLibrary {
	return &fakeLibrary{contents: contents, openCount: map[string]int{}, bytesRead: map[string]int64{}}
}

func (library *fakeLibrary) open(path string) (io.ReaderAt, error) {
	content, present := library.contents[path]
	if !present {
		return nil, errors.New("no such file")
	}
	library.openCount[path]++
	return countingReader{reader: bytes.NewReader(content), path: path, library: library}, nil
}

func (library *fakeLibrary) totalBytesRead() int64 {
	var total int64
	for _, byteCount := range library.bytesRead {
		total += byteCount
	}
	return total
}

func (library *fakeLibrary) mediaFiles() []store.MediaFile {
	var files []store.MediaFile
	for path, content := range library.contents {
		files = append(files, store.MediaFile{Path: path, Size: int64(len(content))})
	}
	return files
}

type countingReader struct {
	reader  *bytes.Reader
	path    string
	library *fakeLibrary
}

func (reader countingReader) ReadAt(buffer []byte, offset int64) (int, error) {
	readCount, err := reader.reader.ReadAt(buffer, offset)
	reader.library.bytesRead[reader.path] += int64(readCount)
	return readCount, err
}

func paths(files []store.MediaFile) []string {
	var result []string
	for _, file := range files {
		result = append(result, file.Path)
	}
	return result
}
