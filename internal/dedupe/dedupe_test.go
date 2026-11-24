package dedupe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"necromanciarr/internal/linker"
	"necromanciarr/internal/store"
)

const mebibyte = 1 << 20

var agedModTime = time.Now().Add(-time.Hour).Truncate(time.Second)

func writeAged(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, agedModTime, agedModTime); err != nil {
		t.Fatal(err)
	}
}

func patterned(size int, seed byte) []byte {
	content := make([]byte, size)
	for byteIndex := range content {
		content[byteIndex] = byte(byteIndex%251) ^ seed
	}
	return content
}

// guardLibraryFile vérifie, à la fin du test, que ni le contenu, ni la date, ni l'inode du fichier n'ont changé.
func guardLibraryFile(t *testing.T, path string) {
	t.Helper()
	contentBefore, err := os.ReadFile(path) // #nosec G304 -- chemin de test sous t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	infoBefore, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		contentAfter, err := os.ReadFile(path) // #nosec G304 -- chemin de test sous t.TempDir()
		if err != nil {
			t.Fatalf("library file unreadable after test: %v", err)
		}
		infoAfter, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(contentBefore, contentAfter) {
			t.Fatal("library file content changed")
		}
		if !infoAfter.ModTime().Equal(infoBefore.ModTime()) || !os.SameFile(infoBefore, infoAfter) {
			t.Fatal("library file mtime or inode changed")
		}
	})
}

func assertRegularWith(t *testing.T, path string, expectedContent []byte) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("%s is no longer a regular file", path)
	}
	gotContent, err := os.ReadFile(path) // #nosec G304 -- chemin de test sous t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotContent, expectedContent) {
		t.Fatalf("%s content changed", path)
	}
}

func assertNoTemporaryEntry(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".necromanciarr-") {
			t.Fatalf("temporary entry left behind: %s", entry.Name())
		}
	}
}

type countingLimiter struct {
	totalBytes int64
}

func (limiter *countingLimiter) WaitN(_ context.Context, byteCount int) error {
	limiter.totalBytes += int64(byteCount)
	return nil
}

type fakeRecorder struct {
	links []store.Link
}

func (recorder *fakeRecorder) RecordLink(_ context.Context, link store.Link) error {
	recorder.links = append(recorder.links, link)
	return nil
}

type fakeRechecker struct {
	paths []string
	err   error
}

func (rechecker *fakeRechecker) RecheckPath(_ context.Context, path string) error {
	rechecker.paths = append(rechecker.paths, path)
	return rechecker.err
}

func localVolume(string) (linker.FilesystemKind, error) {
	return linker.Local, nil
}

func baseOptions(torrentRoot string) Options {
	return Options{
		Kinds:            []linker.Kind{linker.Symlink},
		RelativeSymlinks: true,
		MinFileAge:       time.Minute,
		RecheckAfter:     true,
		TorrentRoots:     []string{torrentRoot},
		Recorder:         &fakeRecorder{},
		filesystemKind:   localVolume,
	}
}

// operatorLayout reproduit l'exemple de l'opérateur : une copie côté torrent, le même film côté bibliothèque.
type operatorLayout struct {
	torrentRoot, torrentPath, libraryPath string
	content                               []byte
}

func newOperatorLayout(t *testing.T) operatorLayout {
	t.Helper()
	volume := filepath.Join(t.TempDir(), "volumes", "remote")
	layout := operatorLayout{
		torrentRoot: filepath.Join(volume, "qBittorrent"),
		torrentPath: filepath.Join(volume, "qBittorrent", "yuyu.mkv"),
		libraryPath: filepath.Join(volume, "1080p", "yuyu (1944)", "mon film.mkv"),
		content:     patterned(4096, 7),
	}
	writeAged(t, layout.torrentPath, layout.content)
	writeAged(t, layout.libraryPath, layout.content)
	guardLibraryFile(t, layout.libraryPath)
	return layout
}

func (layout operatorLayout) library() []store.MediaFile {
	return []store.MediaFile{{Path: layout.libraryPath, Size: int64(len(layout.content))}}
}

func planOne(t *testing.T, layout operatorLayout, options Options) Replacement {
	t.Helper()
	replacements, err := Plan(context.Background(), []string{layout.torrentRoot}, layout.library(), options)
	if err != nil {
		t.Fatal(err)
	}
	if len(replacements) != 1 {
		t.Fatalf("got %d replacements, want 1: %+v", len(replacements), replacements)
	}
	return replacements[0]
}

func torrentPaths(replacements []Replacement) []string {
	paths := make([]string, 0, len(replacements))
	for _, replacement := range replacements {
		paths = append(paths, filepath.Base(replacement.TorrentPath))
	}
	slices.Sort(paths)
	return paths
}

// sameSizePair écrit une copie identique et une copie de même taille, même date, mêmes début et fin, mais au milieu
// différent : seule la preuve complète d'Apply peut les distinguer.
func sameSizePair(t *testing.T) (torrentRoot, libraryPath string, middleDiffers []byte) {
	t.Helper()
	workDir := t.TempDir()
	torrentRoot = filepath.Join(workDir, "torrents")
	libraryPath = filepath.Join(workDir, "library", "film.mkv")
	content := patterned(3*mebibyte, 1)
	writeAged(t, libraryPath, content)
	guardLibraryFile(t, libraryPath)
	middleDiffers = slices.Clone(content)
	middleDiffers[len(content)/2] ^= 0xff
	headDiffers := slices.Clone(content)
	headDiffers[0] ^= 0xff
	writeAged(t, filepath.Join(torrentRoot, "middle.mkv"), middleDiffers)
	writeAged(t, filepath.Join(torrentRoot, "head.mkv"), headDiffers)
	writeAged(t, filepath.Join(torrentRoot, "same.mkv"), content)
	return torrentRoot, libraryPath, middleDiffers
}

func TestApply_requiresIdenticalContentNotJustSize(t *testing.T) {
	torrentRoot, libraryPath, middleDiffers := sameSizePair(t)
	options := baseOptions(torrentRoot)

	replacements, err := Plan(context.Background(), []string{torrentRoot}, []store.MediaFile{{Path: libraryPath}}, options)
	if err != nil {
		t.Fatal(err)
	}
	expectedCandidates := []string{"middle.mkv", "same.mkv"}
	if gotValue := torrentPaths(replacements); !slices.Equal(gotValue, expectedCandidates) {
		t.Fatalf("plan candidates %v, expected %v (head differs, so excluded by the fingerprint)", gotValue, expectedCandidates)
	}

	result, err := Apply(context.Background(), replacements, options)

	if !errors.Is(err, ErrNotIdentical) {
		t.Fatalf("expected ErrNotIdentical, got %v", err)
	}
	for _, outcome := range result.Outcomes {
		isSame := filepath.Base(outcome.Replacement.TorrentPath) == "same.mkv"
		if outcome.Done != isSame {
			t.Fatalf("%s: done %v, err %v", outcome.Replacement.TorrentPath, outcome.Done, outcome.Err)
		}
	}
	assertRegularWith(t, filepath.Join(torrentRoot, "middle.mkv"), middleDiffers)
	assertNoTemporaryEntry(t, torrentRoot)
}

func TestApply_rejectsSameSizeSameMtimeDifferentContent(t *testing.T) {
	torrentRoot, libraryPath, middleDiffers := sameSizePair(t)
	options := baseOptions(torrentRoot)
	replacements, err := Plan(context.Background(), []string{torrentRoot}, []store.MediaFile{{Path: libraryPath}}, options)
	if err != nil {
		t.Fatal(err)
	}
	middleIndex := slices.IndexFunc(replacements, func(replacement Replacement) bool {
		return filepath.Base(replacement.TorrentPath) == "middle.mkv"
	})

	for _, verification := range []Verification{VerifyFull, VerifySampled} {
		options.Verify = verification
		_, err := Apply(context.Background(), replacements[middleIndex:middleIndex+1], options)

		if !errors.Is(err, ErrNotIdentical) {
			t.Fatalf("%s: expected ErrNotIdentical, got %v", verification, err)
		}
	}
	assertRegularWith(t, filepath.Join(torrentRoot, "middle.mkv"), middleDiffers)
	assertNoTemporaryEntry(t, torrentRoot)
}

func TestPlan_skipsFilesAlreadyLinkedOrTooYoungOrOutsideRoots(t *testing.T) {
	workDir := t.TempDir()
	torrentRoot := filepath.Join(workDir, "torrents")
	libraryPath := filepath.Join(workDir, "library", "film.mkv")
	content := patterned(4096, 2)
	writeAged(t, libraryPath, content)
	guardLibraryFile(t, libraryPath)
	writeAged(t, filepath.Join(torrentRoot, "copy.mkv"), content)
	writeAged(t, filepath.Join(workDir, "elsewhere", "outside.mkv"), content)
	if err := os.Symlink(libraryPath, filepath.Join(torrentRoot, "symlinked.mkv")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(libraryPath, filepath.Join(torrentRoot, "hardlinked.mkv")); err != nil {
		t.Fatal(err)
	}
	youngPath := filepath.Join(torrentRoot, "young.mkv")
	if err := os.WriteFile(youngPath, content, 0o600); err != nil {
		t.Fatal(err)
	}

	replacements, err := Plan(context.Background(), []string{torrentRoot}, []store.MediaFile{{Path: libraryPath}}, baseOptions(torrentRoot))
	if err != nil {
		t.Fatal(err)
	}

	expectedValue := []string{"copy.mkv"}
	if gotValue := torrentPaths(replacements); !slices.Equal(gotValue, expectedValue) {
		t.Fatalf("gotValue %v, expectedValue %v", gotValue, expectedValue)
	}
}

func TestPlan_reportsAmbiguousWhenSeveralIdenticalLibraryFiles(t *testing.T) {
	layout := newOperatorLayout(t)
	secondLibraryPath := filepath.Join(filepath.Dir(layout.libraryPath), "copie.mkv")
	writeAged(t, secondLibraryPath, layout.content)
	guardLibraryFile(t, secondLibraryPath)
	library := append(layout.library(), store.MediaFile{Path: secondLibraryPath})
	options := baseOptions(layout.torrentRoot)

	replacements, err := Plan(context.Background(), []string{layout.torrentRoot}, library, options)
	if err != nil {
		t.Fatal(err)
	}

	if len(replacements) != 1 {
		t.Fatalf("got %d replacements, want 1", len(replacements))
	}
	gotValue := replacements[0]
	expectedValue := []string{layout.libraryPath, secondLibraryPath}
	slices.Sort(expectedValue)
	if gotValue.LibraryPath != "" || gotValue.Kind != 0 || !slices.Equal(gotValue.Ambiguous, expectedValue) {
		t.Fatalf("gotValue %+v, expected ambiguous %v without choice", gotValue, expectedValue)
	}
	if _, err := Apply(context.Background(), replacements, options); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("expected ErrAmbiguous, got %v", err)
	}
	assertRegularWith(t, layout.torrentPath, layout.content)
}

func fakeProbe(capabilities linker.Capabilities) func(string) (linker.Capabilities, error) {
	return func(string) (linker.Capabilities, error) { return capabilities, nil }
}

func TestPlan_picksFirstKindThatProbeConfirms(t *testing.T) {
	layout := newOperatorLayout(t)
	options := baseOptions(layout.torrentRoot)
	options.Kinds = []linker.Kind{linker.Hardlink, linker.Reflink, linker.Symlink}
	options.probe = fakeProbe(linker.Capabilities{Symlink: true, Reasons: map[linker.Kind]error{
		linker.Hardlink: linker.ErrLinkRefused, linker.Reflink: linker.ErrUnsupported,
	}})

	if gotValue := planOne(t, layout, options).Kind; gotValue != linker.Symlink {
		t.Fatalf("gotValue %v, expectedValue symlink", gotValue)
	}

	options.Kinds = []linker.Kind{linker.Hardlink, linker.Reflink}
	refused := planOne(t, layout, options)
	if refused.Kind != 0 || refused.SavedBytes != 0 || !errors.Is(refused.Reason, linker.ErrLinkRefused) {
		t.Fatalf("expected no kind with the probe reason, got %+v", refused)
	}
}

func largeIdenticalPair(t *testing.T, size int) (torrentRoot string, library []store.MediaFile) {
	t.Helper()
	workDir := t.TempDir()
	torrentRoot = filepath.Join(workDir, "torrents")
	libraryPath := filepath.Join(workDir, "library", "film.mkv")
	content := patterned(size, 3)
	writeAged(t, libraryPath, content)
	writeAged(t, filepath.Join(torrentRoot, "copy.mkv"), content)
	guardLibraryFile(t, libraryPath)
	return torrentRoot, []store.MediaFile{{Path: libraryPath}}
}

func TestPlan_doesNotReadWholeFilesAndEstimatesBytes(t *testing.T) {
	fileSize := int64(12 * mebibyte)
	torrentRoot, library := largeIdenticalPair(t, int(fileSize))
	expectedEstimates := map[Verification]int64{
		VerifyFull:    2 * fileSize,
		VerifySampled: 2 * (2*headTailSize + sampleBlockCount*sampleBlockSize),
	}
	for verification, expectedValue := range expectedEstimates {
		limiter := &countingLimiter{}
		options := baseOptions(torrentRoot)
		options.Verify = verification
		options.Limiter = limiter

		replacements, err := Plan(context.Background(), []string{torrentRoot}, library, options)
		if err != nil || len(replacements) != 1 {
			t.Fatalf("%s: %d replacements, err %v", verification, len(replacements), err)
		}

		if gotValue := replacements[0].BytesToRead; gotValue != expectedValue {
			t.Fatalf("%s: BytesToRead %d, expectedValue %d", verification, gotValue, expectedValue)
		}
		maximumPlanRead := int64(2 * 2 * headTailSize)
		if limiter.totalBytes > maximumPlanRead {
			t.Fatalf("%s: plan read %d bytes, at most %d expected (head and tail only)", verification, limiter.totalBytes, maximumPlanRead)
		}
	}
}

func TestApply_sampledVerifyReadsFewerBytesThanFull(t *testing.T) {
	torrentRoot, library := largeIdenticalPair(t, 12*mebibyte)
	bytesRead := map[Verification]int64{}
	for _, verification := range []Verification{VerifyFull, VerifySampled} {
		options := baseOptions(torrentRoot)
		options.Verify = verification
		options.DryRun = true
		replacements, err := Plan(context.Background(), []string{torrentRoot}, library, options)
		if err != nil || len(replacements) != 1 {
			t.Fatalf("%s: %d replacements, err %v", verification, len(replacements), err)
		}
		limiter := &countingLimiter{}
		options.Limiter = limiter

		result, err := Apply(context.Background(), replacements, options)
		if err != nil {
			t.Fatal(err)
		}

		if gotValue := result.Outcomes[0].BytesRead; gotValue != limiter.totalBytes {
			t.Fatalf("%s: BytesRead %d, limiter charged %d", verification, gotValue, limiter.totalBytes)
		}
		bytesRead[verification] = limiter.totalBytes
	}
	if bytesRead[VerifySampled] >= bytesRead[VerifyFull] {
		t.Fatalf("sampled read %d bytes, full %d", bytesRead[VerifySampled], bytesRead[VerifyFull])
	}
}

func TestPlan_reportsBytesToReadAndUsesFingerprintCache(t *testing.T) {
	layout := newOperatorLayout(t)
	cacheStore, err := store.Open(filepath.Join(t.TempDir(), "necromanciarr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cacheStore.Close() })
	options := baseOptions(layout.torrentRoot)
	options.Fingerprints = cacheStore
	fileSize := int64(len(layout.content))
	planReads := make([]int64, 0, 2)
	replacements := make([]Replacement, 0, 2)

	for range 2 {
		limiter := &countingLimiter{}
		options.Limiter = limiter
		replacements = append(replacements, planOne(t, layout, options))
		planReads = append(planReads, limiter.totalBytes)
	}

	// Petit fichier : l'empreinte lit tout le fichier ; au second Plan celle de la bibliothèque vient du cache.
	expectedReads := []int64{2 * fileSize, fileSize}
	if !slices.Equal(planReads, expectedReads) {
		t.Fatalf("plan reads %v, expected %v", planReads, expectedReads)
	}
	for _, replacement := range replacements {
		if replacement.BytesToRead != 2*fileSize || replacement.Fingerprint == "" || replacement.LibraryFingerprint != replacement.Fingerprint {
			t.Fatalf("unexpected estimate or fingerprints %+v", replacement)
		}
	}
}

func TestPlan_warnsOnNonLocalVolumes(t *testing.T) {
	layout := newOperatorLayout(t)
	options := baseOptions(layout.torrentRoot)
	options.filesystemKind = func(string) (linker.FilesystemKind, error) { return linker.Fuse, nil }
	recorder := &fakeRecorder{}
	options.Recorder = recorder

	replacement := planOne(t, layout, options)

	for _, expectedWarning := range []string{warningUnreliableInode, warningFullNetworkRead, warningRenameNotAtomic} {
		if !slices.Contains(replacement.Warnings, expectedWarning) {
			t.Fatalf("missing warning %q in %v", expectedWarning, replacement.Warnings)
		}
	}
	if _, err := Apply(context.Background(), []Replacement{replacement}, options); err != nil {
		t.Fatal(err)
	}
	if gotLink := recorder.links[0]; gotLink.InodeReliable || gotLink.TargetInode != 0 {
		t.Fatalf("inode recorded on a FUSE volume: %+v", gotLink)
	}
}

func TestApply_replacesTorrentFileWithRelativeSymlinkAtomically(t *testing.T) {
	layout := newOperatorLayout(t)
	options := baseOptions(layout.torrentRoot)
	replacement := planOne(t, layout, options)

	result, err := Apply(context.Background(), []Replacement{replacement}, options)
	if err != nil {
		t.Fatal(err)
	}

	expectedValue := filepath.FromSlash("../1080p/yuyu (1944)/mon film.mkv")
	gotValue, err := os.Readlink(layout.torrentPath)
	if err != nil || gotValue != expectedValue {
		t.Fatalf("gotValue %q (%v), expectedValue %q", gotValue, err, expectedValue)
	}
	gotContent, err := os.ReadFile(layout.torrentPath)
	if err != nil || !bytes.Equal(gotContent, layout.content) {
		t.Fatalf("link does not resolve to the library content: %v", err)
	}
	if outcome := result.Outcomes[0]; !outcome.Done || outcome.Err != nil {
		t.Fatalf("unexpected outcome %+v", outcome)
	}
	assertNoTemporaryEntry(t, layout.torrentRoot)
}

func TestApply_leavesTorrentFileIntactOnFailure(t *testing.T) {
	layout := newOperatorLayout(t)
	wrongTarget := filepath.Join(t.TempDir(), "wrong.mkv")
	writeAged(t, wrongTarget, []byte("short"))
	failures := map[string]func(linker.Kind, string, string, string) error{
		"link refused": func(linker.Kind, string, string, string) error { return linker.ErrLinkRefused },
		"link resolves elsewhere": func(_ linker.Kind, _, destination, _ string) error {
			return os.Symlink(wrongTarget, destination)
		},
	}
	for name, createLink := range failures {
		t.Run(name, func(t *testing.T) {
			options := baseOptions(layout.torrentRoot)
			options.createLink = createLink
			replacement := planOne(t, layout, options)

			_, err := Apply(context.Background(), []Replacement{replacement}, options)

			if err == nil {
				t.Fatal("expected an error")
			}
			assertRegularWith(t, layout.torrentPath, layout.content)
			assertNoTemporaryEntry(t, layout.torrentRoot)
		})
	}
}

func TestApply_neverModifiesLibraryFile(t *testing.T) {
	for _, kind := range []linker.Kind{linker.Symlink, linker.Hardlink} {
		t.Run(kind.String(), func(t *testing.T) {
			layout := newOperatorLayout(t)
			sumBefore := sha256.Sum256(layout.content)
			options := baseOptions(layout.torrentRoot)
			options.Kinds = []linker.Kind{kind}
			replacement := planOne(t, layout, options)

			if _, err := Apply(context.Background(), []Replacement{replacement}, options); err != nil {
				t.Fatal(err)
			}

			libraryContent, err := os.ReadFile(layout.libraryPath)
			if err != nil || sha256.Sum256(libraryContent) != sumBefore {
				t.Fatalf("library hash changed (%v)", err)
			}
			libraryInfo, _ := os.Lstat(layout.libraryPath)
			if !libraryInfo.ModTime().Equal(agedModTime) || !libraryInfo.Mode().IsRegular() {
				t.Fatalf("library mtime or type changed: %v %v", libraryInfo.ModTime(), libraryInfo.Mode())
			}
		})
	}
}

func TestApply_refusesWhenFileChangedSincePlan(t *testing.T) {
	layout := newOperatorLayout(t)
	options := baseOptions(layout.torrentRoot)
	changes := map[string]func() error{
		"torrent touched": func() error {
			return os.Chtimes(layout.torrentPath, agedModTime, agedModTime.Add(time.Second))
		},
		"library touched": func() error {
			return os.Chtimes(layout.libraryPath, agedModTime, agedModTime.Add(time.Second))
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			replacement := planOne(t, layout, options)
			if err := change(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = os.Chtimes(layout.torrentPath, agedModTime, agedModTime)
				_ = os.Chtimes(layout.libraryPath, agedModTime, agedModTime)
			})

			_, err := Apply(context.Background(), []Replacement{replacement}, options)

			if !errors.Is(err, ErrChangedSincePlan) {
				t.Fatalf("expected ErrChangedSincePlan, got %v", err)
			}
			assertRegularWith(t, layout.torrentPath, layout.content)
			assertNoTemporaryEntry(t, layout.torrentRoot)
		})
	}
}

func TestApply_recordsLinkWithInodeSizeAndFingerprint(t *testing.T) {
	layout := newOperatorLayout(t)
	options := baseOptions(layout.torrentRoot)
	recorder := &fakeRecorder{}
	rechecker := &fakeRechecker{}
	options.Recorder = recorder
	options.Rechecker = rechecker
	replacement := planOne(t, layout, options)
	libraryInfo, err := os.Lstat(layout.libraryPath)
	if err != nil {
		t.Fatal(err)
	}
	expectedDevice, expectedInode, identityKnown := fileIdentity(libraryInfo)
	headTailSum := sha256.Sum256(layout.content)

	if _, err := Apply(context.Background(), []Replacement{replacement}, options); err != nil {
		t.Fatal(err)
	}

	if len(recorder.links) != 1 {
		t.Fatalf("recorded %d links, want 1", len(recorder.links))
	}
	gotValue := recorder.links[0]
	if gotValue.Kind != "symlink" || gotValue.LinkPath != layout.torrentPath || gotValue.TargetPath != layout.libraryPath ||
		!gotValue.Relative || gotValue.Size != int64(len(layout.content)) || gotValue.State != store.LinkOK ||
		gotValue.Fingerprint != hex.EncodeToString(headTailSum[:]) || gotValue.InodeReliable != identityKnown ||
		!gotValue.ModTime.Equal(replacement.LibraryModTime) || gotValue.TargetDevice != expectedDevice || gotValue.TargetInode != expectedInode {
		t.Fatalf("unexpected recorded link %+v", gotValue)
	}
	if !slices.Equal(rechecker.paths, []string{layout.torrentPath}) {
		t.Fatalf("recheck paths %v", rechecker.paths)
	}
}

func TestApply_reportsRecheckErrorWithoutFailing(t *testing.T) {
	layout := newOperatorLayout(t)
	options := baseOptions(layout.torrentRoot)
	options.Rechecker = &fakeRechecker{err: errors.New("client offline")}
	replacement := planOne(t, layout, options)

	result, err := Apply(context.Background(), []Replacement{replacement}, options)
	if err != nil {
		t.Fatalf("recheck failure must not fail Apply: %v", err)
	}
	if outcome := result.Outcomes[0]; !outcome.Done || outcome.RecheckErr == nil {
		t.Fatalf("expected a done outcome with a recheck error, got %+v", outcome)
	}
}

func TestApply_dryRunRunsTheProofWithoutCreatingAnything(t *testing.T) {
	layout := newOperatorLayout(t)
	options := baseOptions(layout.torrentRoot)
	recorder := &fakeRecorder{}
	options.Recorder = recorder
	options.DryRun = true
	replacement := planOne(t, layout, options)
	fileSize := int64(len(layout.content))

	result, err := Apply(context.Background(), []Replacement{replacement}, options)
	if err != nil {
		t.Fatal(err)
	}

	outcome := result.Outcomes[0]
	expectedBytesRead := 2*fileSize + 2*fileSize
	if !outcome.DryRun || outcome.Done || outcome.LinkTarget == "" || outcome.BytesRead != expectedBytesRead {
		t.Fatalf("unexpected dry-run outcome %+v, expected %d bytes read", outcome, expectedBytesRead)
	}
	assertRegularWith(t, layout.torrentPath, layout.content)
	assertNoTemporaryEntry(t, layout.torrentRoot)
	if len(recorder.links) != 0 {
		t.Fatal("dry run recorded a link")
	}

	differentContent := slices.Clone(layout.content)
	differentContent[len(differentContent)/2] ^= 0xff
	writeAged(t, layout.torrentPath, differentContent)
	if _, err := Apply(context.Background(), []Replacement{replacement}, options); !errors.Is(err, ErrChangedSincePlan) {
		t.Fatalf("dry run must refuse content changed under the same size and mtime, got %v", err)
	}
	assertRegularWith(t, layout.torrentPath, differentContent)
}

func TestApply_refusesTorrentPathOutsideTorrentRoots(t *testing.T) {
	layout := newOperatorLayout(t)
	options := baseOptions(layout.torrentRoot)
	replacement := planOne(t, layout, options)
	options.TorrentRoots = []string{t.TempDir()}

	_, err := Apply(context.Background(), []Replacement{replacement}, options)

	if !errors.Is(err, ErrOutsideTorrentRoots) {
		t.Fatalf("expected ErrOutsideTorrentRoots, got %v", err)
	}
	assertRegularWith(t, layout.torrentPath, layout.content)
}

func TestPlan_stopsOnCancelledContext(t *testing.T) {
	layout := newOperatorLayout(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Plan(ctx, []string{layout.torrentRoot}, layout.library(), baseOptions(layout.torrentRoot))

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestPlan_ignoresTemporaryEntriesOfThisTool(t *testing.T) {
	layout := newOperatorLayout(t)
	leftover := layout.torrentPath + ".necromanciarr-tmp-abc"
	writeAged(t, leftover, layout.content)

	replacement := planOne(t, layout, baseOptions(layout.torrentRoot))

	if replacement.TorrentPath != layout.torrentPath {
		t.Fatalf("unexpected replacement %s", replacement.TorrentPath)
	}
	if _, err := os.Lstat(leftover); errors.Is(err, fs.ErrNotExist) {
		t.Fatal("leftover removed")
	}
}

func TestPlan_warnsWhenRenameOverwriteIsNotConfirmed(t *testing.T) {
	layout := newOperatorLayout(t)
	options := baseOptions(layout.torrentRoot)
	for _, renameOverwrite := range []bool{false, true} {
		options.probe = fakeProbe(linker.Capabilities{Symlink: true, RenameOverwrite: renameOverwrite})

		replacement := planOne(t, layout, options)

		expectedValue := !renameOverwrite
		if gotValue := slices.Contains(replacement.Warnings, warningRenameNotAtomic); gotValue != expectedValue {
			t.Fatalf("renameOverwrite %v: warning present %v, expected %v", renameOverwrite, gotValue, expectedValue)
		}
	}
}

// interruptedLayout simule un Apply interrompu : un lien temporaire vers le fichier de la bibliothèque à côté du
// fichier torrent, que celui-ci soit encore là ou non.
func newInterruptedLayout(t *testing.T, torrentPresent bool) (operatorLayout, string) {
	t.Helper()
	layout := newOperatorLayout(t)
	temporaryPath := temporaryPathFor(layout.torrentPath, int64(len(layout.content)))
	if err := os.Symlink(layout.libraryPath, temporaryPath); err != nil {
		t.Fatal(err)
	}
	if !torrentPresent {
		if err := os.Remove(layout.torrentPath); err != nil {
			t.Fatal(err)
		}
	}
	return layout, temporaryPath
}

func recoverOne(t *testing.T, torrentRoot string) RecoverAction {
	t.Helper()
	result, err := Recover(context.Background(), []string{torrentRoot})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Actions) != 1 {
		t.Fatalf("got %d actions, want 1: %+v", len(result.Actions), result.Actions)
	}
	return result.Actions[0]
}

func TestRecover_removesTemporaryWhenTorrentFileIsPresent(t *testing.T) {
	layout, temporaryPath := newInterruptedLayout(t, true)

	action := recoverOne(t, layout.torrentRoot)

	if action.Completed || !errors.Is(action.Reason, ErrTorrentFilePresent) || action.TemporaryPath != temporaryPath {
		t.Fatalf("unexpected action %+v", action)
	}
	assertRegularWith(t, layout.torrentPath, layout.content)
	assertNoTemporaryEntry(t, layout.torrentRoot)
}

func TestRecover_completesRenameWhenTorrentFileIsAbsent(t *testing.T) {
	layout, _ := newInterruptedLayout(t, false)

	action := recoverOne(t, layout.torrentRoot)

	if !action.Completed || action.Err != nil || action.TorrentPath != layout.torrentPath {
		t.Fatalf("unexpected action %+v", action)
	}
	gotValue, err := os.Readlink(layout.torrentPath)
	if err != nil || gotValue != layout.libraryPath {
		t.Fatalf("torrent path links to %q (%v), want %q", gotValue, err, layout.libraryPath)
	}
	assertNoTemporaryEntry(t, layout.torrentRoot)
}

func TestRecover_removesTemporaryWithDanglingTarget(t *testing.T) {
	layout := newOperatorLayout(t)
	if err := os.Remove(layout.torrentPath); err != nil {
		t.Fatal(err)
	}
	temporaryPath := temporaryPathFor(layout.torrentPath, int64(len(layout.content)))
	if err := os.Symlink(filepath.Join(t.TempDir(), "gone.mkv"), temporaryPath); err != nil {
		t.Fatal(err)
	}

	action := recoverOne(t, layout.torrentRoot)

	if action.Completed || !errors.Is(action.Reason, ErrNotIdentical) || action.Err != nil {
		t.Fatalf("unexpected action %+v", action)
	}
	if _, err := os.Lstat(layout.torrentPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("torrent path created from a dangling link: %v", err)
	}
	assertNoTemporaryEntry(t, layout.torrentRoot)
}

func TestRecover_doesNotFollowSymlinkedDirectories(t *testing.T) {
	layout := newOperatorLayout(t)
	outside := t.TempDir()
	outsideTorrent := filepath.Join(outside, "other.mkv")
	outsideTemporary := temporaryPathFor(outsideTorrent, int64(len(layout.content)))
	if err := os.Symlink(layout.libraryPath, outsideTemporary); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(layout.torrentRoot, "linked")); err != nil {
		t.Fatal(err)
	}

	result, err := Recover(context.Background(), []string{layout.torrentRoot})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Actions) != 0 {
		t.Fatalf("expected no action, got %+v", result.Actions)
	}
	if _, err := os.Lstat(outsideTemporary); err != nil {
		t.Fatalf("entry behind a symlinked directory was touched: %v", err)
	}
}

func TestPlan_refusesTorrentRootThatContainsLibraryFiles(t *testing.T) {
	layout := newOperatorLayout(t)
	volume := filepath.Dir(layout.torrentRoot)
	libraryDir := filepath.Dir(layout.libraryPath)
	overlappingRoots := map[string]string{
		"ancestor of the library": volume,
		"equal to a library dir":  libraryDir,
		"inside a library dir":    filepath.Join(libraryDir, "extras"),
	}
	if err := os.MkdirAll(overlappingRoots["inside a library dir"], 0o750); err != nil {
		t.Fatal(err)
	}
	for name, root := range overlappingRoots {
		t.Run(name, func(t *testing.T) {
			_, err := Plan(context.Background(), []string{root}, layout.library(), baseOptions(root))

			if !errors.Is(err, ErrTorrentRootOverlapsLibrary) {
				t.Fatalf("expected ErrTorrentRootOverlapsLibrary, got %v", err)
			}
		})
	}
}

func TestApply_refusesTorrentRootThatContainsTheLibraryFile(t *testing.T) {
	layout := newOperatorLayout(t)
	options := baseOptions(layout.torrentRoot)
	replacement := planOne(t, layout, options)
	options.TorrentRoots = []string{filepath.Dir(layout.torrentRoot)}

	_, err := Apply(context.Background(), []Replacement{replacement}, options)

	if !errors.Is(err, ErrTorrentRootOverlapsLibrary) {
		t.Fatalf("expected ErrTorrentRootOverlapsLibrary, got %v", err)
	}
	assertRegularWith(t, layout.torrentPath, layout.content)
}

func TestApply_rejectsKindNotAllowedByOptions(t *testing.T) {
	layout := newOperatorLayout(t)
	options := baseOptions(layout.torrentRoot)
	replacement := planOne(t, layout, options)
	replacement.Kind = linker.Hardlink

	_, err := Apply(context.Background(), []Replacement{replacement}, options)

	if !errors.Is(err, ErrKindNotAllowed) {
		t.Fatalf("expected ErrKindNotAllowed, got %v", err)
	}
	assertRegularWith(t, layout.torrentPath, layout.content)
	assertNoTemporaryEntry(t, layout.torrentRoot)
}
