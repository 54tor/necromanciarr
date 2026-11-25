package pipeline

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"necromanciarr/internal/store"
)

// storeInDir ouvre la base du cas dans un dossier connu du test, pour en relire les octets (base et journal WAL).
func storeInDir(t *testing.T, testCase *indexerMatchCase) string {
	t.Helper()
	directory := t.TempDir()
	openedStore, err := store.Open(filepath.Join(directory, "necromanciarr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = openedStore.Close() })
	testCase.store = openedStore
	return directory
}

func filesContaining(t *testing.T, directory string, secret []byte) []string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	var containing []string
	for _, entry := range entries {
		content, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(content, secret) {
			containing = append(containing, entry.Name())
		}
	}
	return containing
}

func TestMatch_indexerTitleCarryingThePasskeyNeverReachesTheDatabase(t *testing.T) {
	testCase := newIndexerMatchCase(t)
	directory := storeInDir(t, testCase)
	size := 4 * gibibyte
	testCase.addMovie("/srv/media/missing.mkv", size, 604, "Missing")
	indexer := testCase.addIndexer(indexerSameTracker, sealedTracker)
	indexer.offer("win", size, indexerTorrent(t, "Movie.Root", size))
	indexer.results.Items[0].Title = "Release " + fallbackPasskey

	testCase.run()

	candidates := testCase.candidates()
	if len(candidates) != 1 || candidates[0].Name != "Movie.Root" {
		t.Fatalf("valeurObtenue %+v, valeurAttendue one candidate named after the torrent", candidates)
	}
	if containing := filesContaining(t, directory, []byte(fallbackPasskey)); len(containing) != 0 {
		t.Fatalf("valeurObtenue passkey in %v, valeurAttendue no database file carrying it", containing)
	}
}

func TestMatch_indexerTorrentNamedAfterThePasskeyIsRefused(t *testing.T) {
	testCase := newIndexerMatchCase(t)
	size := 4 * gibibyte
	testCase.addMovie("/srv/media/missing.mkv", size, 604, "Missing")
	indexer := testCase.addIndexer(indexerSameTracker, sealedTracker)
	indexer.offer("win", size, indexerTorrent(t, "Movie "+fallbackPasskey, size))

	result := testCase.run()

	if result.Reasons[ReasonInvalidTorrent] == 0 || len(testCase.candidates()) != 0 {
		t.Fatalf("valeurObtenue %+v candidates %d, valeurAttendue invalid_torrent and nothing stored", result, len(testCase.candidates()))
	}
}

func TestMatch_indexerTitleCutInsideThePasskeyLeavesNoPrefixInTheDatabase(t *testing.T) {
	testCase := newIndexerMatchCase(t)
	directory := storeInDir(t, testCase)
	size := 4 * gibibyte
	testCase.addMovie("/srv/media/missing.mkv", size, 604, "Missing")
	indexer := testCase.addIndexer(indexerSameTracker, sealedTracker)
	indexer.offer("win", size, indexerTorrent(t, "Movie.Root", size))
	indexer.results.Items[0].Title = strings.Repeat("x", 244) + " " + fallbackPasskey

	testCase.run()

	candidates := testCase.candidates()
	if len(candidates) != 1 || candidates[0].Name != "Movie.Root" {
		t.Fatalf("valeurObtenue %+v, valeurAttendue one candidate named after the torrent", candidates)
	}
	if containing := filesContaining(t, directory, []byte(fallbackPasskey[:8])); len(containing) != 0 {
		t.Fatalf("valeurObtenue passkey prefix in %v, valeurAttendue no database file carrying it", containing)
	}
}
