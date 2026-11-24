package torrentcheck

import (
	"bytes"
	"crypto/sha1" // #nosec G505 -- SHA-1 imposé par le format BitTorrent v1
	"fmt"
	"io"
	"testing"
	"time"
)

const (
	syntheticFileCount     = 100_000
	syntheticPiecesPerFile = 4
	syntheticPieceLength   = 16
	layoutDeadline         = time.Second
)

// contiguousSource sert chaque fichier depuis un seul tampon contigu.
type contiguousSource struct {
	data  []byte
	files []File
}

func (source contiguousSource) ReaderAt(fileIndex int) (io.ReaderAt, bool) {
	file := source.files[fileIndex]
	return bytes.NewReader(source.data[file.Offset : file.Offset+file.Size]), true
}

// syntheticLayout fabrique 100 000 fichiers de 4 pièces chacun (400 000 pièces) aux SHA1 exacts.
func syntheticLayout() (*Torrent, contiguousSource) {
	fileSize := int64(syntheticPiecesPerFile * syntheticPieceLength)
	data := make([]byte, syntheticFileCount*fileSize)
	for position := range data {
		data[position] = byte(position * 31)
	}
	torrent := &Torrent{Root: "Synthetic", PieceLength: syntheticPieceLength}
	for fileIndex := range syntheticFileCount {
		torrent.Files = append(torrent.Files, File{Path: fmt.Sprintf("file-%06d.bin", fileIndex), Size: fileSize, Offset: int64(fileIndex) * fileSize})
	}
	for start := 0; start < len(data); start += syntheticPieceLength {
		torrent.Pieces = append(torrent.Pieces, sha1.Sum(data[start:start+syntheticPieceLength])) // #nosec G401 -- SHA-1 imposé par BitTorrent v1
	}
	return torrent, contiguousSource{data: data, files: torrent.Files}
}

func finishesWithin(t *testing.T, deadline time.Duration, work func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		work()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(deadline):
		t.Fatalf("expectedValue the work done within %v, gotValue still running", deadline)
	}
}

func TestCoverage_largeLayoutFinishesWithinASecond(t *testing.T) {
	torrent, source := syntheticLayout()
	var report Report
	var err error

	finishesWithin(t, layoutDeadline, func() { report, err = Coverage(torrent, source) })

	if expectedPieces := syntheticFileCount * syntheticPiecesPerFile; err != nil || report.VerifiedPieces != expectedPieces {
		t.Fatalf("gotValue %d verified %v, expectedValue %d", report.VerifiedPieces, err, expectedPieces)
	}
}

func TestFilesOverlapping_findsExactlyTheFilesOfAByteRange(t *testing.T) {
	torrent := &Torrent{PieceLength: 16, Files: []File{
		{Path: "a", Size: 10, Offset: 0}, {Path: "empty", Size: 0, Offset: 10}, {Path: "b", Size: 20, Offset: 10}, {Path: "c", Size: 5, Offset: 30},
	}}
	testCases := map[string]struct {
		start, end      int64
		expectedIndexes []int
	}{
		"first piece":     {start: 0, end: 16, expectedIndexes: []int{0, 2}},
		"second piece":    {start: 16, end: 32, expectedIndexes: []int{2, 3}},
		"inside one file": {start: 12, end: 14, expectedIndexes: []int{2}},
		"past the end":    {start: 35, end: 48, expectedIndexes: nil},
	}
	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			gotIndexes := torrent.FilesOverlapping(testCase.start, testCase.end)
			if fmt.Sprint(gotIndexes) != fmt.Sprint(testCase.expectedIndexes) {
				t.Fatalf("gotValue %v, expectedValue %v", gotIndexes, testCase.expectedIndexes)
			}
		})
	}
}
