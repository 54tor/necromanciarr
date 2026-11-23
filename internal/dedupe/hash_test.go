package dedupe

import (
	"context"
	"crypto/md5" // #nosec G501 -- faux stockage de test
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"necromanciarr/internal/hashsource"
	"necromanciarr/internal/store"
)

// fakeBackend se comporte comme un stockage S3 : il fournit le MD5 des seuls chemins déclarés, sans que le limiteur
// ne voie de lecture.
type fakeBackend struct {
	backendPaths map[string]bool
	err          error
	calls        []string
}

func backendFor(paths ...string) *fakeBackend {
	backend := &fakeBackend{backendPaths: map[string]bool{}}
	for _, path := range paths {
		backend.backendPaths[path] = true
	}
	return backend
}

func (backend *fakeBackend) Hash(_ context.Context, path string, algorithm hashsource.Algorithm) (hashsource.Result, error) {
	backend.calls = append(backend.calls, path)
	if backend.err != nil {
		return hashsource.Result{}, backend.err
	}
	if algorithm != hashsource.MD5 || !backend.backendPaths[path] {
		return hashsource.Result{}, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return hashsource.Result{}, err
	}
	sum := md5.Sum(content)
	return hashsource.Result{Value: hex.EncodeToString(sum[:]), Origin: hashsource.Backend, Available: true}, nil
}

func hashOptions(torrentRoot string, verification Verification, backend hashsource.HashSource) Options {
	options := baseOptions(torrentRoot)
	options.Verify = verification
	options.Backend = backend
	return options
}

func applyOnce(t *testing.T, replacement Replacement, options Options, limiter Limiter) Outcome {
	t.Helper()
	options.Limiter = limiter
	result, err := Apply(context.Background(), []Replacement{replacement}, options)
	if err != nil {
		t.Fatal(err)
	}
	return result.Outcomes[0]
}

func TestDedupe_autoUsesBackendHashWhenBothSidesProvideIt(t *testing.T) {
	fileSize := int64(12 * mebibyte)
	torrentRoot, library := largeIdenticalPair(t, int(fileSize))
	torrentPath := filepath.Join(torrentRoot, "copy.mkv")
	options := hashOptions(torrentRoot, VerifyAuto, backendFor(torrentPath, library[0].Path))
	replacements, err := Plan(context.Background(), []string{torrentRoot}, library, options)
	if err != nil || len(replacements) != 1 {
		t.Fatalf("%d replacements, err %v", len(replacements), err)
	}
	limiter := &countingLimiter{}

	outcome := applyOnce(t, replacements[0], options, limiter)

	if gotValue := replacements[0].BytesToRead; gotValue != 0 || replacements[0].Method != MethodHash {
		t.Fatalf("plan: BytesToRead %d method %q, expected 0 and hash", gotValue, replacements[0].Method)
	}
	maximumFingerprintReads := int64(2 * 2 * headTailSize)
	if !outcome.Done || outcome.Replacement.Method != MethodHash || limiter.totalBytes > maximumFingerprintReads || outcome.BytesRead != limiter.totalBytes {
		t.Fatalf("apply read %d bytes (at most %d expected): %+v", limiter.totalBytes, maximumFingerprintReads, outcome)
	}
	recorded := options.Recorder.(*fakeRecorder).links
	if len(recorded) != 1 || recorded[0].HashAlgo != "MD5" || len(recorded[0].Hash) != 32 {
		t.Fatalf("the backend hash must be recorded with the link: %+v", recorded)
	}
}

func TestDedupe_autoUsesOneLocalReadWhenOnlyOneSideHasBackendHash(t *testing.T) {
	fileSize := int64(12 * mebibyte)
	torrentRoot, library := largeIdenticalPair(t, int(fileSize))
	options := hashOptions(torrentRoot, VerifyAuto, backendFor(library[0].Path))
	replacements, err := Plan(context.Background(), []string{torrentRoot}, library, options)
	if err != nil || len(replacements) != 1 {
		t.Fatalf("%d replacements, err %v", len(replacements), err)
	}
	limiter := &countingLimiter{}

	outcome := applyOnce(t, replacements[0], options, limiter)

	if replacements[0].BytesToRead != fileSize || replacements[0].Method != MethodHash {
		t.Fatalf("plan: BytesToRead %d method %q, expected %d and hash", replacements[0].BytesToRead, replacements[0].Method, fileSize)
	}
	expectedReads := int64(2*2*headTailSize) + fileSize
	if !outcome.Done || limiter.totalBytes != expectedReads {
		t.Fatalf("apply read %d bytes, expected %d (fingerprints plus one local read): %+v", limiter.totalBytes, expectedReads, outcome)
	}
}

func TestDedupe_autoFallsBackToFullWhenNeitherSideHasBackendHash(t *testing.T) {
	fileSize := int64(12 * mebibyte)
	torrentRoot, library := largeIdenticalPair(t, int(fileSize))
	backend := backendFor()
	options := hashOptions(torrentRoot, VerifyAuto, backend)
	replacements, err := Plan(context.Background(), []string{torrentRoot}, library, options)
	if err != nil || len(replacements) != 1 {
		t.Fatalf("%d replacements, err %v", len(replacements), err)
	}

	outcome := applyOnce(t, replacements[0], options, nil)

	if replacements[0].Method != MethodFull || replacements[0].BytesToRead != 2*fileSize || !outcome.Done || outcome.Replacement.Method != MethodFull {
		t.Fatalf("expected a full read estimate of %d: %+v", 2*fileSize, outcome)
	}
	if len(backend.calls) == 0 {
		t.Fatal("the backend must have been asked first")
	}
}

func TestDedupe_hashModeRequiresEqualSizeAndEqualHash(t *testing.T) {
	torrentRoot, libraryPath, _ := sameSizePair(t)
	library := []store.MediaFile{{Path: libraryPath}}
	backend := backendFor(libraryPath, filepath.Join(torrentRoot, "middle.mkv"), filepath.Join(torrentRoot, "same.mkv"))
	options := hashOptions(torrentRoot, VerifyHash, backend)
	options.DryRun = true
	replacements, err := Plan(context.Background(), []string{torrentRoot}, library, options)
	if err != nil {
		t.Fatal(err)
	}
	if gotValue := torrentPaths(replacements); len(gotValue) != 2 || gotValue[0] != "middle.mkv" || gotValue[1] != "same.mkv" {
		t.Fatalf("head and tail fingerprints must select middle.mkv and same.mkv, got %v", gotValue)
	}

	result, applyErr := Apply(context.Background(), replacements, options)

	if applyErr == nil {
		t.Fatal("middle.mkv has another hash and must be refused")
	}
	for _, outcome := range result.Outcomes {
		isMiddle := filepath.Base(outcome.Replacement.TorrentPath) == "middle.mkv"
		if isMiddle && !errors.Is(outcome.Err, ErrNotIdentical) {
			t.Fatalf("middle.mkv: got %v, expected ErrNotIdentical", outcome.Err)
		}
		if !isMiddle && outcome.Err != nil {
			t.Fatalf("same.mkv must pass: %v", outcome.Err)
		}
	}
}

func TestDedupe_hashModeReportsUnavailableInsteadOfFallingBack(t *testing.T) {
	layout := newOperatorLayout(t)
	options := hashOptions(layout.torrentRoot, VerifyHash, backendFor())

	replacement := planOne(t, layout, options)

	if replacement.Kind != 0 || replacement.SavedBytes != 0 || !errors.Is(replacement.Reason, ErrHashUnavailable) {
		t.Fatalf("expected ErrHashUnavailable and no kind: %+v", replacement)
	}
	if _, err := Apply(context.Background(), []Replacement{replacement}, options); err == nil {
		t.Fatal("apply must refuse a replacement without kind")
	}
	withoutBackend := hashOptions(layout.torrentRoot, VerifyHash, nil)
	if reason := planOne(t, layout, withoutBackend).Reason; !errors.Is(reason, ErrHashUnavailable) {
		t.Fatalf("no backend at all: got %v", reason)
	}
}

func TestDedupe_hashApplyRefusesAHandBuiltReplacementWithoutBackend(t *testing.T) {
	layout := newOperatorLayout(t)
	planned := planOne(t, layout, baseOptions(layout.torrentRoot))
	options := hashOptions(layout.torrentRoot, VerifyHash, nil)
	options.DryRun = true

	result, _ := Apply(context.Background(), []Replacement{planned}, options)

	if !errors.Is(result.Outcomes[0].Err, ErrHashUnavailable) {
		t.Fatalf("got %v", result.Outcomes[0].Err)
	}
}

func TestDedupe_backendFailureAbortsInsteadOfSilentlyReadingEverything(t *testing.T) {
	layout := newOperatorLayout(t)
	backend := &fakeBackend{err: hashsource.ErrRcloneFailed}

	_, err := Plan(context.Background(), []string{layout.torrentRoot}, layout.library(), hashOptions(layout.torrentRoot, VerifyAuto, backend))

	if !errors.Is(err, hashsource.ErrRcloneFailed) {
		t.Fatalf("got %v", err)
	}
}

func TestDedupe_pathsOutsideAnyMountCountAsNoBackendHash(t *testing.T) {
	layout := newOperatorLayout(t)
	backend := &fakeBackend{err: hashsource.ErrNoMountForPath}

	replacement := planOne(t, layout, hashOptions(layout.torrentRoot, VerifyAuto, backend))

	if replacement.Method != MethodFull {
		t.Fatalf("got method %q", replacement.Method)
	}
}

func TestDedupe_replacementRecordsTheMethodUsed(t *testing.T) {
	torrentRoot, library := largeIdenticalPair(t, 12*mebibyte)
	expectedMethods := map[Verification]Method{VerifyFull: MethodFull, VerifySampled: MethodSampled, "": MethodFull}
	for verification, expectedValue := range expectedMethods {
		options := baseOptions(torrentRoot)
		options.Verify = verification
		options.DryRun = true
		replacements, err := Plan(context.Background(), []string{torrentRoot}, library, options)
		if err != nil || len(replacements) != 1 {
			t.Fatalf("%q: %d replacements, err %v", verification, len(replacements), err)
		}

		outcome := applyOnce(t, replacements[0], options, nil)

		if replacements[0].Method != expectedValue || outcome.Replacement.Method != expectedValue {
			t.Fatalf("%q: plan %q apply %q, expected %q", verification, replacements[0].Method, outcome.Replacement.Method, expectedValue)
		}
	}
}

func TestDedupe_autoFullReadsOnceThenReusesFingerprintCache(t *testing.T) {
	layout := newOperatorLayout(t)
	cacheStore, err := store.Open(filepath.Join(t.TempDir(), "necromanciarr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cacheStore.Close() })
	options := hashOptions(layout.torrentRoot, VerifyAuto, backendFor())
	options.Fingerprints = cacheStore
	fileSize := int64(len(layout.content))
	var planReads []int64

	for range 2 {
		limiter := &countingLimiter{}
		options.Limiter = limiter
		if replacement := planOne(t, layout, options); replacement.Method != MethodFull {
			t.Fatalf("got method %q", replacement.Method)
		}
		planReads = append(planReads, limiter.totalBytes)
	}

	if planReads[0] != 2*fileSize || planReads[1] != fileSize {
		t.Fatalf("plan reads %v, expected [%d %d]", planReads, 2*fileSize, fileSize)
	}
}

func TestDedupe_rejectsBackendValuesWithTheWrongLength(t *testing.T) {
	layout := newOperatorLayout(t)
	for _, value := range []string{"", "abc"} {
		backend := &fixedBackend{value: value}

		replacement := planOne(t, layout, hashOptions(layout.torrentRoot, VerifyAuto, backend))

		if replacement.Method != MethodFull {
			t.Fatalf("value %q accepted: method %q", value, replacement.Method)
		}
	}
}

type fixedBackend struct{ value string }

func (backend *fixedBackend) Hash(context.Context, string, hashsource.Algorithm) (hashsource.Result, error) {
	return hashsource.Result{Value: backend.value, Origin: hashsource.Backend, Available: true}, nil
}
