package repair

import (
	"fmt"
	"testing"

	"necromanciarr/internal/torrentcheck"
)

func TestContainedPieces_keepsOnlyPiecesFullyInsideTheFile(t *testing.T) {
	torrent := &torrentcheck.Torrent{PieceLength: 16, Pieces: make([][20]byte, 4), Files: []torrentcheck.File{
		{Path: "a", Size: 20, Offset: 0}, {Path: "b", Size: 30, Offset: 20}, {Path: "tail", Size: 6, Offset: 50},
	}}
	testCases := map[string]struct {
		fileIndex       int
		expectedIndexes []int
	}{
		"first file":       {fileIndex: 0, expectedIndexes: []int{0}},
		"middle file":      {fileIndex: 1, expectedIndexes: []int{2}},
		"last short piece": {fileIndex: 2, expectedIndexes: nil},
	}
	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			gotIndexes := containedPieces(torrent, testCase.fileIndex)
			if fmt.Sprint(gotIndexes) != fmt.Sprint(testCase.expectedIndexes) {
				t.Fatalf("gotValue %v, expectedValue %v", gotIndexes, testCase.expectedIndexes)
			}
		})
	}
	whole := &torrentcheck.Torrent{PieceLength: 16, Pieces: make([][20]byte, 2), Files: []torrentcheck.File{{Path: "only", Size: 20}}}
	if gotIndexes := containedPieces(whole, 0); fmt.Sprint(gotIndexes) != "[0 1]" {
		t.Fatalf("gotValue %v, expectedValue the short last piece counted for the file ending the torrent", gotIndexes)
	}
}
