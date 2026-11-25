package hashsource

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/fsystest"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/store"
)

// scriptedSource répond selon l'algorithme demandé, comme un backend réel.
type scriptedSource struct {
	available map[Algorithm]string
	err       error
	calls     int
}

func (source *scriptedSource) Hash(_ context.Context, _ string, algorithm Algorithm) (Result, error) {
	source.calls++
	if source.err != nil {
		return Result{}, source.err
	}
	value, found := source.available[algorithm]
	return Result{Value: value, Origin: Backend, Available: found}, nil
}

func sampleFile(t *testing.T, modTime time.Time) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sample.mkv")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCapabilities_detectedByProbeNotByBackendName(t *testing.T) {
	// Un remote nommé « mega » mais qui répond SHA1 : seul l'essai compte.
	source := &scriptedSource{available: map[Algorithm]string{SHA1: sha1Value}}
	sample := sampleFile(t, time.Unix(1700000000, 0))

	gotValue, err := ProbeCapabilities(context.Background(), source, sample, t.TempDir(), fsystest.LocalRegistry(t, filepath.Dir(sample)))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gotValue.Hashes, []Algorithm{SHA1}) || !gotValue.ModTime || !gotValue.Supports(SHA1) || gotValue.Supports(MD5) {
		t.Fatalf("got %+v", gotValue)
	}
}

func TestCapabilities_modTimeUnknownWithoutWritableDirectory(t *testing.T) {
	source := &scriptedSource{available: map[Algorithm]string{MD5: md5Value}}

	gotValue, err := ProbeCapabilities(context.Background(), source, sampleFile(t, time.Unix(1700000000, 0)), "", nil)

	if err != nil || gotValue.ModTime || gotValue.Symlinks {
		t.Fatalf("got %+v %v", gotValue, err)
	}
}

func TestProbeModTime_roundTripOnARealDirectoryAndNoLeftover(t *testing.T) {
	directory := t.TempDir()

	gotValue, err := probeModTime(directory)

	entries, _ := os.ReadDir(directory)
	if err != nil || !gotValue || len(entries) != 0 {
		t.Fatalf("got %v %v, leftovers %d", gotValue, err, len(entries))
	}
}

func TestCapabilities_unknownWithoutSampleAndPropagatesSourceErrors(t *testing.T) {
	if _, err := ProbeCapabilities(context.Background(), &scriptedSource{}, "", "", nil); !errors.Is(err, ErrNoSampleFile) {
		t.Fatalf("got %v", err)
	}
	failing := &scriptedSource{err: ErrRcloneFailed}
	if _, err := ProbeCapabilities(context.Background(), failing, sampleFile(t, time.Now()), "", nil); !errors.Is(err, ErrRcloneFailed) {
		t.Fatalf("got %v", err)
	}
}

func TestCachedCapabilities_probesOnceThenReadsTheStore(t *testing.T) {
	testStore, err := store.Open(filepath.Join(t.TempDir(), "necromanciarr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = testStore.Close() })
	source := &scriptedSource{available: map[Algorithm]string{MD5: md5Value}}
	sample := sampleFile(t, time.Unix(1700000000, 0))
	writable := t.TempDir()
	registry := fsystest.LocalRegistry(t, filepath.Dir(sample))
	probe := func(ctx context.Context) (Capabilities, error) {
		return ProbeCapabilities(ctx, source, sample, writable, registry)
	}

	first, err := CachedCapabilities(context.Background(), testStore, "remote", "/home/u/mnt/remote", probe)
	callsAfterFirst := source.calls
	second, secondErr := CachedCapabilities(context.Background(), testStore, "remote", "/home/u/mnt/remote", probe)

	if err != nil || secondErr != nil || !slices.Equal(first.Hashes, second.Hashes) || second.Hashes[0] != MD5 || !second.ModTime {
		t.Fatalf("%+v %+v %v %v", first, second, err, secondErr)
	}
	stored, _, _ := testStore.LoadMountCapabilities(context.Background(), "/home/u/mnt/remote")
	if stored.Provider != "remote" {
		t.Fatalf("stored provider %q, expectedValue remote", stored.Provider)
	}
	if source.calls != callsAfterFirst {
		t.Fatalf("second call probed again: %d then %d calls", callsAfterFirst, source.calls)
	}
}

func TestCachedCapabilities_doesNotCacheAFailedProbe(t *testing.T) {
	testStore, err := store.Open(filepath.Join(t.TempDir(), "necromanciarr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = testStore.Close() })
	failing := func(context.Context) (Capabilities, error) { return Capabilities{}, ErrRcloneFailed }

	if _, err := CachedCapabilities(context.Background(), testStore, "remote", "/home/u/mnt/remote", failing); !errors.Is(err, ErrRcloneFailed) {
		t.Fatalf("got %v", err)
	}
	if _, found, _ := testStore.LoadMountCapabilities(context.Background(), "/home/u/mnt/remote"); found {
		t.Fatal("failed probe was cached")
	}
}

func TestConfirmer_comparesRecordedHashWithTheCandidateBackendHash(t *testing.T) {
	source := &scriptedSource{available: map[Algorithm]string{MD5: md5Value}}
	confirmer := NewConfirmer(source)
	link := store.Link{HashAlgo: "MD5", Hash: md5Value}
	other := store.Link{HashAlgo: "MD5", Hash: strings.Repeat("1", 32)}

	matches, known, err := confirmer.Confirm(context.Background(), link, "/home/u/mnt/remote/a.mkv")
	if err != nil || !matches || !known {
		t.Fatalf("same hash: %v %v %v", matches, known, err)
	}
	matches, known, err = confirmer.Confirm(context.Background(), other, "/home/u/mnt/remote/a.mkv")
	if err != nil || matches || !known {
		t.Fatalf("different hash must be known and not matching: %v %v %v", matches, known, err)
	}
}

func TestConfirmer_isUnknownWhenNothingCanBeCompared(t *testing.T) {
	cases := map[string]struct {
		source HashSource
		link   store.Link
	}{
		"link without hash":        {&scriptedSource{available: map[Algorithm]string{MD5: md5Value}}, store.Link{}},
		"backend has no such hash": {&scriptedSource{}, store.Link{HashAlgo: "MD5", Hash: md5Value}},
		"candidate outside mounts": {&scriptedSource{err: ErrNoMountForPath}, store.Link{HashAlgo: "MD5", Hash: md5Value}},
		"locally read result":      {sourceReturning(Result{Value: md5Value, Origin: ReadLocally, Available: true}), store.Link{HashAlgo: "MD5", Hash: md5Value}},
	}
	for name, testCase := range cases {
		matches, known, err := NewConfirmer(testCase.source).Confirm(context.Background(), testCase.link, "/home/u/x.mkv")

		if err != nil || matches || known {
			t.Fatalf("%s: %v %v %v", name, matches, known, err)
		}
	}
	_, _, err := NewConfirmer(&scriptedSource{err: ErrRcloneFailed}).Confirm(context.Background(), store.Link{HashAlgo: "MD5", Hash: md5Value}, "/home/u/x.mkv")
	if !errors.Is(err, ErrRcloneFailed) {
		t.Fatalf("a real failure must surface: %v", err)
	}
}

type fixedSource struct{ result Result }

func sourceReturning(result Result) HashSource { return fixedSource{result} }

func (source fixedSource) Hash(context.Context, string, Algorithm) (Result, error) {
	return source.result, nil
}

func TestDescribeMount_explainsMissingHash(t *testing.T) {
	mountDirectory := t.TempDir()
	if err := os.WriteFile(filepath.Join(mountDirectory, "film.mkv"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := newFakeRclone(t, "", 0)

	report := DescribeMountWith(context.Background(), Mount{LocalPath: mountDirectory, Remote: "remote:bucket"},
		DescribeOptions{Binary: fake.binary, WritableDir: t.TempDir(), Registry: fsystest.LocalRegistry(t, mountDirectory)})

	if report.Mode != ModeBinary || !report.BinaryFound || !report.Probed || len(report.Capabilities.Hashes) != 0 || !report.SymlinksKnown {
		t.Fatalf("unexpected report %+v", report)
	}
	if gotValue := adviceKeys(report); !slices.Contains(gotValue, i18n.KeyAdviceMountNoHash) || !slices.Contains(gotValue, i18n.KeyAdviceMountStartRC) {
		t.Fatalf("gotValue %v, expectedValue the missing hash and how to start rc", gotValue)
	}
}

// adviceKeys liste les clés des conseils d'un rapport de montage.
func adviceKeys(report MountReport) []i18n.Key {
	keys := make([]i18n.Key, 0, len(report.Advice))
	for _, advice := range report.Advice {
		keys = append(keys, advice.Key)
	}
	return keys
}

func TestHashsource_warningsAreMessages(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	report := DescribeMount(context.Background(), Mount{LocalPath: t.TempDir(), Remote: "remote:bucket"})

	expectedValue := []i18n.Message{{Key: i18n.KeyAdviceMountStartRC}}
	if !reflect.DeepEqual(report.Advice, expectedValue) {
		t.Fatalf("gotValue %+v, expectedValue %+v", report.Advice, expectedValue)
	}
	for _, advice := range report.Advice {
		if strings.Contains(string(advice.Key), " ") || strings.ContainsAny(string(advice.Key), "éèà") {
			t.Fatalf("gotValue %q, expectedValue a stable key and not a sentence", advice.Key)
		}
	}
}

func TestDescribeMount_withoutRcNorBinaryTellsHowToStartRC(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	report := DescribeMount(context.Background(), Mount{LocalPath: t.TempDir(), Remote: "remote:bucket"})

	if report.Mode != ModeNone || report.BinaryFound || report.Probed || !slices.Contains(adviceKeys(report), i18n.KeyAdviceMountStartRC) {
		t.Fatalf("unexpected report %+v", report)
	}
}

func TestDescribeMount_reportsHashesFoundThroughRC(t *testing.T) {
	mountDirectory := t.TempDir()
	if err := os.WriteFile(filepath.Join(mountDirectory, "film.mkv"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := newRCServer(t, respondWith(`{"item":{"Name":"film.mkv","Hashes":{"md5":"`+md5Value+`"}}}`))

	report := DescribeMountWith(context.Background(), Mount{LocalPath: mountDirectory, Remote: "remote:bucket"}, DescribeOptions{RCURL: server.URL})

	if report.Mode != ModeRC || !report.Probed || !report.Capabilities.Supports(MD5) || report.Capabilities.Supports(SHA1) {
		t.Fatalf("unexpected report %+v", report)
	}
}

func TestConfirmer_comparesHashesCaseInsensitively(t *testing.T) {
	source := sourceReturning(Result{Value: strings.ToUpper(md5Value), Origin: Backend, Available: true})

	matches, known, err := NewConfirmer(source).Confirm(context.Background(), store.Link{HashAlgo: "MD5", Hash: md5Value}, "/home/u/x.mkv")

	if err != nil || !matches || !known {
		t.Fatalf("%v %v %v", matches, known, err)
	}
}

func TestProbeModTime_falseWhenTheStorageKeepsUploadTime(t *testing.T) {
	original := setModTime
	t.Cleanup(func() { setModTime = original })
	setModTime = func(string, time.Time, time.Time) error { return nil }

	gotValue, err := probeModTime(t.TempDir())

	if err != nil || gotValue {
		t.Fatalf("got %v %v", gotValue, err)
	}
}

func TestProbeCapabilities_refusesTheWritableProbeWithoutRegistry(t *testing.T) {
	source := &scriptedSource{available: map[Algorithm]string{MD5: md5Value}}

	_, err := ProbeCapabilities(context.Background(), source, sampleFile(t, time.Unix(1700000000, 0)), t.TempDir(), nil)

	if !errors.Is(err, fsys.ErrNoLibraryRoots) {
		t.Fatalf("expectedValue fsys.ErrNoLibraryRoots, gotValue %v", err)
	}
}
