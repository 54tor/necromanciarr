package repair

import (
	"context"
	"crypto/md5" // #nosec G501 -- faux stockage de test
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"necromanciarr/internal/hashsource"
	"necromanciarr/internal/store"
)

var _ HashConfirmer = (*hashsource.Confirmer)(nil)

// md5Backend fournit le MD5 de chaque fichier sans passer par le limiteur, comme un stockage S3.
type md5Backend struct{ asked []string }

func (backend *md5Backend) Hash(_ context.Context, path string, _ hashsource.Algorithm) (hashsource.Result, error) {
	backend.asked = append(backend.asked, path)
	content, err := os.ReadFile(path)
	if err != nil {
		return hashsource.Result{}, err
	}
	sum := md5.Sum(content)
	return hashsource.Result{Value: hex.EncodeToString(sum[:]), Origin: hashsource.Backend, Available: true}, nil
}

func md5Of(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := md5.Sum(content)
	return hex.EncodeToString(sum[:])
}

func TestRepair_confirmsCandidateByBackendHashBeforeReadingContent(t *testing.T) {
	place := newLayout(t)
	wanted := place.libraryFile(t, "wanted.mkv", patterned(4096, 1), agedModTime.Add(time.Minute))
	other := place.libraryFile(t, "other.mkv", patterned(4096, 2), agedModTime.Add(2*time.Minute))
	oldTarget := filepath.Join(place.libraryDirectory, "old.mkv")
	row := brokenRow(place.symlink(t, "movie.mkv", oldTarget), oldTarget, 4096)
	row.HashAlgo, row.Hash = "MD5", md5Of(t, wanted.Path)
	limiter := &countingLimiter{}
	backend := &md5Backend{}
	options := Options{AutoApply: true, Limiter: limiter, HashConfirmer: hashsource.NewConfirmer(backend)}

	repairs, _ := scanOne(t, options, row, []store.MediaFile{other, wanted})

	if len(repairs) != 1 || repairs[0].NewTarget != wanted.Path || repairs[0].Method != ByFingerprint || !repairs[0].AutoApplicable {
		t.Fatalf("unexpected repairs %+v", repairs)
	}
	if limiter.totalBytes != 0 || len(backend.asked) != 2 {
		t.Fatalf("expected no content read and both candidates asked, read %d bytes, asked %v", limiter.totalBytes, backend.asked)
	}
}

func TestRepair_skipsModTimeMethodWhenMountHasNoModTime(t *testing.T) {
	place := newLayout(t)
	wanted := place.libraryFile(t, "wanted.mkv", patterned(4096, 1), agedModTime)
	oldTarget := filepath.Join(place.libraryDirectory, "old.mkv")
	row := brokenRow(place.symlink(t, "movie.mkv", oldTarget), oldTarget, 4096)
	row.HashAlgo, row.Hash = "MD5", md5Of(t, wanted.Path)
	// Aucune date enregistrée : le montage n'en fournissait pas à la création du lien.
	row.ModTime = time.Time{}

	repairs, _ := scanOne(t, Options{HashConfirmer: hashsource.NewConfirmer(&md5Backend{})}, row, []store.MediaFile{wanted})

	if len(repairs) != 1 || repairs[0].Method != ByFingerprint || repairs[0].NewTarget != wanted.Path {
		t.Fatalf("size and mtime must be skipped without a recorded date: %+v", repairs)
	}
}
