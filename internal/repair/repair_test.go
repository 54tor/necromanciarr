package repair

import (
	"bytes"
	"context"
	"crypto/sha1" // #nosec G505 -- SHA1 imposé par le format .torrent
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"necromanciarr/internal/dedupe"
	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/fsystest"
	"necromanciarr/internal/store"
	"necromanciarr/internal/torrentcheck"
)

var agedModTime = time.Now().Add(-time.Hour).Truncate(time.Second)

func writeFileAt(t *testing.T, path string, content []byte, modTime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
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

type countingLimiter struct{ totalBytes int64 }

func (limiter *countingLimiter) WaitN(_ context.Context, byteCount int) error {
	limiter.totalBytes += int64(byteCount)
	return nil
}

type fakeStore struct {
	states       map[string]store.LinkState
	verifiedAt   map[string]time.Time
	targets      map[string]string
	unknownLinks bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{states: map[string]store.LinkState{}, verifiedAt: map[string]time.Time{}, targets: map[string]string{}}
}

func (fake *fakeStore) ListLinks(context.Context, store.LinkState) ([]store.Link, error) {
	return nil, nil
}

func (fake *fakeStore) SetLinkState(_ context.Context, linkPath string, state store.LinkState, verifiedAt time.Time) error {
	if fake.unknownLinks {
		return store.ErrLinkNotFound
	}
	fake.states[linkPath], fake.verifiedAt[linkPath] = state, verifiedAt
	return nil
}

func (fake *fakeStore) UpdateLinkTarget(_ context.Context, linkPath, targetPath string) error {
	if fake.unknownLinks {
		return store.ErrLinkNotFound
	}
	fake.targets[linkPath] = targetPath
	return nil
}

type fakeCache struct{ entries map[string]string }

func (cache *fakeCache) LookupFingerprint(_ context.Context, path string, _ int64, _ time.Time) (string, bool, error) {
	fingerprint, found := cache.entries[path]
	return fingerprint, found, nil
}

func (cache *fakeCache) RecordFingerprint(_ context.Context, entry store.CachedFingerprint) error {
	cache.entries[entry.Path] = entry.HeadTailHash
	return nil
}

type fakeTorrentSource struct {
	reference TorrentRef
	known     bool
}

func (source fakeTorrentSource) TorrentForPath(context.Context, string) (TorrentRef, bool, error) {
	return source.reference, source.known, nil
}

type layout struct {
	libraryDirectory string
	linkDirectory    string
}

func newLayout(t *testing.T) layout {
	t.Helper()
	root := t.TempDir()
	return layout{libraryDirectory: filepath.Join(root, "library"), linkDirectory: filepath.Join(root, "links")}
}

func (place layout) libraryFile(t *testing.T, name string, content []byte, modTime time.Time) store.MediaFile {
	t.Helper()
	path := filepath.Join(place.libraryDirectory, name)
	writeFileAt(t, path, content, modTime)
	return store.MediaFile{Path: path, Size: int64(len(content)), ModTime: modTime}
}

func (place layout) symlink(t *testing.T, name, targetText string) string {
	t.Helper()
	if err := os.MkdirAll(place.linkDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(place.linkDirectory, name)
	if err := os.Symlink(targetText, linkPath); err != nil {
		t.Fatal(err)
	}
	return linkPath
}

func fingerprintOf(t *testing.T, path string, size int64) string {
	t.Helper()
	fingerprint, _, err := dedupe.HeadTailFingerprint(t.Context(), nil, nil, path, size, time.Time{}, true)
	if err != nil {
		t.Fatal(err)
	}
	return fingerprint
}

func brokenRow(linkPath, oldTarget string, size int64) store.Link {
	return store.Link{Kind: "symlink", LinkPath: linkPath, TargetPath: oldTarget, Size: size, State: store.LinkOK}
}

func scanOne(t *testing.T, options Options, link store.Link, library []store.MediaFile) ([]Repair, Report) {
	t.Helper()
	repairs, report, err := Scan(t.Context(), options, []store.Link{link}, library)
	if err != nil {
		t.Fatal(err)
	}
	return repairs, report
}

func TestScan_findsRenamedTargetByInodeOnLocalVolume(t *testing.T) {
	place := newLayout(t)
	oldFile := place.libraryFile(t, "old.mkv", patterned(4096, 1), agedModTime)
	info, err := os.Stat(oldFile.Path)
	if err != nil {
		t.Fatal(err)
	}
	device, inode, known := dedupe.FileIdentity(info)
	if !known {
		t.Skip("no inode on this platform")
	}
	linkPath := place.symlink(t, "movie.mkv", oldFile.Path)
	newPath := filepath.Join(place.libraryDirectory, "renamed.mkv")
	if err := os.Rename(oldFile.Path, newPath); err != nil {
		t.Fatal(err)
	}
	guardLibraryFile(t, newPath)
	decoy := place.libraryFile(t, "decoy.mkv", patterned(4096, 2), agedModTime)
	row := brokenRow(linkPath, oldFile.Path, 4096)
	row.InodeReliable, row.TargetDevice, row.TargetInode = true, device, inode
	library := []store.MediaFile{decoy, {Path: newPath, Size: 4096, ModTime: agedModTime}}

	repairs, _ := scanOne(t, Options{AutoApply: true}, row, library)

	if len(repairs) != 1 || repairs[0].Method != ByInode || repairs[0].NewTarget != newPath || !repairs[0].AutoApplicable {
		t.Fatalf("expected one automatic ByInode repair to %s, got %+v", newPath, repairs)
	}
}

func TestScan_skipsInodeMethodWhenNotReliable(t *testing.T) {
	place := newLayout(t)
	renamed := place.libraryFile(t, "renamed.mkv", patterned(4096, 1), agedModTime)
	linkPath := place.symlink(t, "movie.mkv", filepath.Join(place.libraryDirectory, "old.mkv"))
	row := brokenRow(linkPath, filepath.Join(place.libraryDirectory, "old.mkv"), 4096)

	repairs, report := scanOne(t, Options{}, row, []store.MediaFile{renamed})

	if len(repairs) != 0 || len(report.Lost) != 1 {
		t.Fatalf("expected no repair and one lost link, got %+v %+v", repairs, report)
	}
}

func TestScan_inodeMethodSkippedWhenRowSaysUnreliable(t *testing.T) {
	place := newLayout(t)
	file := place.libraryFile(t, "renamed.mkv", patterned(4096, 1), agedModTime)
	info, err := os.Stat(file.Path)
	if err != nil {
		t.Fatal(err)
	}
	device, inode, _ := dedupe.FileIdentity(info)
	oldTarget := filepath.Join(place.libraryDirectory, "old.mkv")
	row := brokenRow(place.symlink(t, "movie.mkv", oldTarget), oldTarget, 4096)
	row.TargetDevice, row.TargetInode = device, inode
	options := Options{RecordedModTimes: map[string]time.Time{oldTarget: agedModTime}}

	repairs, _ := scanOne(t, options, row, []store.MediaFile{file})

	if len(repairs) != 1 || repairs[0].Method != BySizeAndModTime {
		t.Fatalf("expected BySizeAndModTime, got %+v", repairs)
	}
}

func TestScan_findsRenamedTargetBySizeAndModTimeOnNonLocalVolume(t *testing.T) {
	place := newLayout(t)
	content := patterned(4096, 1)
	renamed := place.libraryFile(t, "renamed.mkv", content, agedModTime)
	other := place.libraryFile(t, "other.mkv", patterned(4096, 2), agedModTime.Add(time.Minute))
	oldTarget := filepath.Join(place.libraryDirectory, "old.mkv")
	row := brokenRow(place.symlink(t, "movie.mkv", oldTarget), oldTarget, 4096)
	row.Fingerprint = fingerprintOf(t, renamed.Path, 4096)
	row.ModTime = agedModTime
	limiter := &countingLimiter{}
	cache := &fakeCache{entries: map[string]string{renamed.Path: row.Fingerprint}}
	options := Options{AutoApply: true, Limiter: limiter, FingerprintCache: cache}

	repairs, _ := scanOne(t, options, row, []store.MediaFile{other, renamed})

	if len(repairs) != 1 || repairs[0].Method != BySizeAndModTime || repairs[0].NewTarget != renamed.Path || !repairs[0].AutoApplicable {
		t.Fatalf("unexpected repairs %+v", repairs)
	}
	if limiter.totalBytes != 0 {
		t.Fatalf("expected the cached fingerprint to avoid any read, read %d bytes", limiter.totalBytes)
	}
}

func TestScan_readsAtMostMaxCandidatesToConfirmFingerprint(t *testing.T) {
	place := newLayout(t)
	var library []store.MediaFile
	for index := range 4 {
		library = append(library, place.libraryFile(t, string(rune('a'+index))+".mkv", patterned(4096, byte(index+1)), agedModTime.Add(time.Duration(index)*time.Minute)))
	}
	oldTarget := filepath.Join(place.libraryDirectory, "old.mkv")
	row := brokenRow(place.symlink(t, "movie.mkv", oldTarget), oldTarget, 4096)
	row.Fingerprint = fingerprintOf(t, library[3].Path, 4096)
	limiter := &countingLimiter{}

	repairs, report := scanOne(t, Options{MaxCandidatesToRead: 2, Limiter: limiter}, row, library)

	if limiter.totalBytes > 2*4096 {
		t.Fatalf("expected at most two candidates read, read %d bytes", limiter.totalBytes)
	}
	if len(repairs) != 0 || len(report.Lost) != 1 {
		t.Fatalf("a truncated search must not repair: %+v %+v", repairs, report)
	}
}

func TestScan_fallsBackToFingerprintWhenModTimeDiffers(t *testing.T) {
	place := newLayout(t)
	wanted := place.libraryFile(t, "wanted.mkv", patterned(4096, 1), agedModTime.Add(3*time.Minute))
	other := place.libraryFile(t, "other.mkv", patterned(4096, 2), agedModTime.Add(4*time.Minute))
	oldTarget := filepath.Join(place.libraryDirectory, "old.mkv")
	row := brokenRow(place.symlink(t, "movie.mkv", oldTarget), oldTarget, 4096)
	row.Fingerprint = fingerprintOf(t, wanted.Path, 4096)
	options := Options{AutoApply: true, RecordedModTimes: map[string]time.Time{oldTarget: agedModTime}}

	repairs, _ := scanOne(t, options, row, []store.MediaFile{other, wanted})

	if len(repairs) != 1 || repairs[0].Method != ByFingerprint || repairs[0].NewTarget != wanted.Path || !repairs[0].AutoApplicable {
		t.Fatalf("unexpected repairs %+v", repairs)
	}
}

func TestScan_reportsAmbiguousCandidatesWithoutRepairing(t *testing.T) {
	place := newLayout(t)
	content := patterned(4096, 1)
	first := place.libraryFile(t, "a.mkv", content, agedModTime)
	second := place.libraryFile(t, "b.mkv", content, agedModTime.Add(time.Minute))
	oldTarget := filepath.Join(place.libraryDirectory, "old.mkv")
	row := brokenRow(place.symlink(t, "movie.mkv", oldTarget), oldTarget, 4096)
	row.Fingerprint = fingerprintOf(t, first.Path, 4096)

	repairs, report := scanOne(t, Options{}, row, []store.MediaFile{second, first})

	expectedCandidates := []string{first.Path, second.Path}
	if len(repairs) != 0 || len(report.Ambiguous) != 1 {
		t.Fatalf("expected one ambiguous link, got %+v %+v", repairs, report)
	}
	if got := report.Ambiguous[0].Candidates; len(got) != 2 || got[0] != expectedCandidates[0] || got[1] != expectedCandidates[1] {
		t.Fatalf("expected sorted candidates %v, got %v", expectedCandidates, got)
	}
}

func hashPieces(content []byte, pieceLength int) [][20]byte {
	var hashes [][20]byte
	for start := 0; start < len(content); start += pieceLength {
		hashes = append(hashes, sha1.Sum(content[start:min(start+pieceLength, len(content))])) // #nosec G401 -- SHA1 imposé par le format .torrent
	}
	return hashes
}

func TestScan_dedupedDirectoryMatchesBrokenLinkThroughTorrentPieces(t *testing.T) {
	const fileSize, pieceLength = 65536, 16384
	place := newLayout(t)
	firstContent, secondContent := patterned(fileSize, 1), patterned(fileSize, 2)
	torrent := &torrentcheck.Torrent{
		PieceLength: pieceLength,
		Pieces:      hashPieces(append(append([]byte{}, firstContent...), secondContent...), pieceLength),
		Files:       []torrentcheck.File{{Path: "e01.mkv", Size: fileSize}, {Path: "e02.mkv", Size: fileSize, Offset: fileSize}},
	}
	decoy := place.libraryFile(t, "decoy.mkv", patterned(fileSize, 3), agedModTime)
	twin := place.libraryFile(t, "e02-renamed.mkv", secondContent, agedModTime)
	linkPath := place.symlink(t, "e02.mkv", filepath.Join(place.libraryDirectory, "e02.mkv"))
	options := Options{
		ScanDirectories: []string{place.linkDirectory},
		TorrentSource:   fakeTorrentSource{reference: TorrentRef{Torrent: torrent, FileIndex: 1}, known: true},
	}

	repairs, _, err := Scan(t.Context(), options, nil, []store.MediaFile{decoy, twin})
	if err != nil {
		t.Fatal(err)
	}
	if len(repairs) != 1 || repairs[0].Method != ByTorrentPieces || repairs[0].NewTarget != twin.Path || repairs[0].LinkPath != linkPath {
		t.Fatalf("unexpected repairs %+v", repairs)
	}
	if repairs[0].AutoApplicable {
		t.Fatal("torrent-piece repairs are proposals only")
	}
}

func TestScan_flagsDivergedHardlink(t *testing.T) {
	place := newLayout(t)
	target := place.libraryFile(t, "movie.mkv", patterned(4096, 1), agedModTime)
	linkPath := filepath.Join(place.linkDirectory, "movie.mkv")
	writeFileAt(t, linkPath, patterned(4096, 1), agedModTime)
	row := store.Link{Kind: "hardlink", LinkPath: linkPath, TargetPath: target.Path, Size: 4096, State: store.LinkOK}
	fake := newFakeStore()

	repairs, report := scanOne(t, Options{Store: fake}, row, []store.MediaFile{target})

	if len(repairs) != 0 || len(report.Diverged) != 1 || fake.states[linkPath] != store.LinkDiverged {
		t.Fatalf("expected a diverged hardlink, got %+v %+v %v", repairs, report, fake.states)
	}
}

func TestScan_notBrokenSymlinkIsLeftAlone(t *testing.T) {
	place := newLayout(t)
	target := place.libraryFile(t, "movie.mkv", patterned(4096, 1), agedModTime)
	linkPath := place.symlink(t, "movie.mkv", target.Path)
	fake := newFakeStore()

	repairs, report := scanOne(t, Options{Store: fake}, brokenRow(linkPath, target.Path, 4096), []store.MediaFile{target})

	if len(repairs) != 0 || len(report.OK) != 1 || fake.states[linkPath] != store.LinkOK || fake.verifiedAt[linkPath].IsZero() {
		t.Fatalf("expected an ok link with a fresh verification date: %+v %v", report, fake.states)
	}
}

type applyFixture struct {
	place   layout
	repair  Repair
	target  store.MediaFile
	options ApplyOptions
	fake    *fakeStore
}

func newApplyFixture(t *testing.T, relative bool) applyFixture {
	t.Helper()
	place := newLayout(t)
	target := place.libraryFile(t, "renamed.mkv", patterned(4096, 1), agedModTime)
	guardLibraryFile(t, target.Path)
	oldTarget := filepath.Join(place.libraryDirectory, "old.mkv")
	oldText := oldTarget
	if relative {
		oldText = filepath.Join("..", "library", "old.mkv")
	}
	linkPath := place.symlink(t, "movie.mkv", oldText)
	fake := newFakeStore()
	return applyFixture{
		place:   place,
		target:  target,
		fake:    fake,
		options: ApplyOptions{Roots: []string{place.linkDirectory}, Store: fake, Registry: fsystest.LocalRegistry(t, place.libraryDirectory)},
		repair:  Repair{LinkPath: linkPath, OldTarget: oldText, NewTarget: target.Path, Size: 4096, Method: ByFingerprint, Relative: relative},
	}
}

func assertNoTemporary(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".mkv" {
			t.Fatalf("unexpected leftover %s", entry.Name())
		}
	}
}

func TestApply_keepsRelativeFormAndUpdatesRecord(t *testing.T) {
	fixture := newApplyFixture(t, true)

	result, err := Apply(t.Context(), []Repair{fixture.repair}, fixture.options)

	if err != nil || !result.Outcomes[0].Done {
		t.Fatalf("apply failed: %v %+v", err, result)
	}
	expectedText := filepath.Join("..", "library", "renamed.mkv")
	gotText, readErr := os.Readlink(fixture.repair.LinkPath)
	if readErr != nil || gotText != expectedText {
		t.Fatalf("expected relative target %q, got %q (%v)", expectedText, gotText, readErr)
	}
	if fixture.fake.targets[fixture.repair.LinkPath] != fixture.target.Path || fixture.fake.states[fixture.repair.LinkPath] != store.LinkOK {
		t.Fatalf("record not updated: %v %v", fixture.fake.targets, fixture.fake.states)
	}
}

func TestApply_keepsAbsoluteFormForAbsoluteLinks(t *testing.T) {
	fixture := newApplyFixture(t, false)

	if _, err := Apply(t.Context(), []Repair{fixture.repair}, fixture.options); err != nil {
		t.Fatal(err)
	}
	gotText, _ := os.Readlink(fixture.repair.LinkPath)
	if gotText != fixture.target.Path {
		t.Fatalf("expected absolute target %q, got %q", fixture.target.Path, gotText)
	}
}

func TestApply_isAtomicAndNeverTouchesLibraryFile(t *testing.T) {
	fixture := newApplyFixture(t, true)
	fixture.options.renameFile = func(temporaryPath, linkPath string) error {
		if _, err := os.Lstat(linkPath); err != nil {
			t.Fatalf("old link must still exist right before the rename: %v", err)
		}
		if _, err := os.Lstat(temporaryPath); err != nil {
			t.Fatalf("temporary link must exist right before the rename: %v", err)
		}
		return os.Rename(temporaryPath, linkPath)
	}

	if _, err := Apply(t.Context(), []Repair{fixture.repair}, fixture.options); err != nil {
		t.Fatal(err)
	}
	assertNoTemporary(t, fixture.place.libraryDirectory)
	entries, _ := os.ReadDir(fixture.place.linkDirectory)
	if len(entries) != 1 {
		t.Fatalf("expected only the repaired link, got %d entries", len(entries))
	}
}

func TestApply_refusesLinkOutsideRoots(t *testing.T) {
	fixture := newApplyFixture(t, false)
	otherRoot := filepath.Join(fixture.place.libraryDirectory, "elsewhere")
	if err := os.MkdirAll(otherRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	fixture.options.Roots = []string{otherRoot}

	_, err := Apply(t.Context(), []Repair{fixture.repair}, fixture.options)

	if !errors.Is(err, ErrOutsideRoots) {
		t.Fatalf("expected ErrOutsideRoots, got %v", err)
	}
}

func TestApply_refusesWhenLinkChangedSinceScan(t *testing.T) {
	fixture := newApplyFixture(t, false)
	if err := os.Remove(fixture.repair.LinkPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(fixture.place.libraryDirectory, "somewhere-else.mkv"), fixture.repair.LinkPath); err != nil {
		t.Fatal(err)
	}

	_, err := Apply(t.Context(), []Repair{fixture.repair}, fixture.options)

	if !errors.Is(err, ErrChangedSinceScan) {
		t.Fatalf("expected ErrChangedSinceScan, got %v", err)
	}
}

func TestApply_refusesInvalidNewTarget(t *testing.T) {
	fixture := newApplyFixture(t, false)
	fixture.repair.Size++

	_, err := Apply(t.Context(), []Repair{fixture.repair}, fixture.options)

	if !errors.Is(err, ErrTargetInvalid) {
		t.Fatalf("expected ErrTargetInvalid, got %v", err)
	}
}

func TestApply_dryRunCreatesNothing(t *testing.T) {
	fixture := newApplyFixture(t, true)
	fixture.options.DryRun = true

	result, err := Apply(t.Context(), []Repair{fixture.repair}, fixture.options)

	if err != nil || result.Outcomes[0].Done || !result.Outcomes[0].DryRun {
		t.Fatalf("unexpected dry run result: %v %+v", err, result)
	}
	gotText, _ := os.Readlink(fixture.repair.LinkPath)
	entries, _ := os.ReadDir(fixture.place.linkDirectory)
	if gotText != fixture.repair.OldTarget || len(entries) != 1 || len(fixture.fake.targets) != 0 {
		t.Fatalf("dry run changed something: %q %d %v", gotText, len(entries), fixture.fake.targets)
	}
}

func TestApply_removesOnlyItsTempOnFailure(t *testing.T) {
	fixture := newApplyFixture(t, true)
	bystander := filepath.Join(fixture.place.linkDirectory, "movie.mkv"+dedupe.TemporaryMarker+"someone-else")
	if err := os.Symlink(fixture.target.Path, bystander); err != nil {
		t.Fatal(err)
	}
	renameFailure := errors.New("rename refused")
	fixture.options.renameFile = func(string, string) error { return renameFailure }

	_, err := Apply(t.Context(), []Repair{fixture.repair}, fixture.options)

	if !errors.Is(err, renameFailure) {
		t.Fatalf("expected the rename failure, got %v", err)
	}
	if _, statErr := os.Lstat(bystander); statErr != nil {
		t.Fatalf("an entry that is not ours was removed: %v", statErr)
	}
	entries, _ := os.ReadDir(fixture.place.linkDirectory)
	if len(entries) != 2 {
		t.Fatalf("expected the old link and the bystander only, got %d entries", len(entries))
	}
	gotText, _ := os.Readlink(fixture.repair.LinkPath)
	if gotText != fixture.repair.OldTarget {
		t.Fatalf("old link modified: %q", gotText)
	}
}

func TestScan_usesModTimeReadBackFromARealStore(t *testing.T) {
	place := newLayout(t)
	renamed := place.libraryFile(t, "renamed.mkv", patterned(4096, 1), agedModTime)
	oldTarget := filepath.Join(place.libraryDirectory, "old.mkv")
	row := brokenRow(place.symlink(t, "movie.mkv", oldTarget), oldTarget, 4096)
	row.ModTime, row.CreatedAt, row.VerifiedAt = agedModTime, agedModTime, agedModTime
	realStore, err := store.Open(filepath.Join(t.TempDir(), "repair.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = realStore.Close() })
	if err := realStore.RecordLink(t.Context(), row); err != nil {
		t.Fatal(err)
	}

	repairs, _, err := Scan(t.Context(), Options{Store: realStore}, nil, []store.MediaFile{renamed})

	if err != nil || len(repairs) != 1 || repairs[0].Method != BySizeAndModTime {
		t.Fatalf("expected BySizeAndModTime from the stored mtime: %v %+v", err, repairs)
	}
}

func TestScan_recordedModTimesMapOverridesTheStoredOne(t *testing.T) {
	place := newLayout(t)
	renamed := place.libraryFile(t, "renamed.mkv", patterned(4096, 1), agedModTime)
	oldTarget := filepath.Join(place.libraryDirectory, "old.mkv")
	row := brokenRow(place.symlink(t, "movie.mkv", oldTarget), oldTarget, 4096)
	row.ModTime = agedModTime.Add(time.Hour)
	options := Options{RecordedModTimes: map[string]time.Time{oldTarget: agedModTime}}

	repairs, _ := scanOne(t, options, row, []store.MediaFile{renamed})

	if len(repairs) != 1 {
		t.Fatalf("the override should win, got %+v", repairs)
	}
}

func TestScan_reflinkRowStaysOkAndStoreIsNotMutated(t *testing.T) {
	place := newLayout(t)
	target := place.libraryFile(t, "movie.mkv", patterned(4096, 1), agedModTime)
	linkPath := filepath.Join(place.linkDirectory, "movie.mkv")
	writeFileAt(t, linkPath, patterned(4096, 1), agedModTime)
	row := store.Link{Kind: "reflink", LinkPath: linkPath, TargetPath: target.Path, Size: 4096, State: store.LinkOK}
	fake := newFakeStore()

	repairs, report := scanOne(t, Options{Store: fake}, row, nil)

	if len(repairs) != 0 || len(report.OK) != 1 || len(report.Diverged) != 0 || len(fake.states) != 0 {
		t.Fatalf("reflink must be left alone: %+v %v", report, fake.states)
	}
}

func TestScan_renamedHardlinkTargetIsNotDiverged(t *testing.T) {
	place := newLayout(t)
	target := filepath.Join(place.libraryDirectory, "gone.mkv")
	linkPath := filepath.Join(place.linkDirectory, "movie.mkv")
	writeFileAt(t, linkPath, patterned(4096, 1), agedModTime)
	row := store.Link{Kind: "hardlink", LinkPath: linkPath, TargetPath: target, Size: 4096, State: store.LinkOK}
	fake := newFakeStore()

	_, report := scanOne(t, Options{Store: fake}, row, nil)

	if len(report.Diverged) != 0 || len(report.RenamedTarget) != 1 || len(fake.states) != 0 {
		t.Fatalf("a renamed target is not a divergence: %+v %v", report, fake.states)
	}
}

func TestScan_transientStatErrorLeavesLinkUnknown(t *testing.T) {
	place := newLayout(t)
	candidate := place.libraryFile(t, "renamed.mkv", patterned(4096, 1), agedModTime)
	oldTarget := filepath.Join(place.libraryDirectory, "old.mkv")
	linkPath := place.symlink(t, "movie.mkv", oldTarget)
	row := brokenRow(linkPath, oldTarget, 4096)
	row.ModTime = agedModTime
	fake := newFakeStore()
	options := Options{Store: fake, stat: func(string) (fs.FileInfo, error) { return nil, errors.New("input/output error") }}

	repairs, report := scanOne(t, options, row, []store.MediaFile{candidate})

	if len(repairs) != 0 || len(report.Unreadable) != 1 || report.Unreadable[0].Err == "" || len(report.Lost) != 0 || len(fake.states) != 0 {
		t.Fatalf("expected an unreadable link and no mutation: %+v %v", report, fake.states)
	}
}

func TestScan_sharesOneReadBudgetAcrossMethodsOfALink(t *testing.T) {
	place := newLayout(t)
	var library []store.MediaFile
	for index := range 4 {
		library = append(library, place.libraryFile(t, string(rune('a'+index))+".mkv", patterned(4096, byte(index+1)), agedModTime.Add(time.Duration(index)*time.Minute)))
	}
	oldTarget := filepath.Join(place.libraryDirectory, "old.mkv")
	row := brokenRow(place.symlink(t, "movie.mkv", oldTarget), oldTarget, 4096)
	row.ModTime = library[0].ModTime
	row.Fingerprint = fingerprintOf(t, library[3].Path, 4096)
	limiter := &countingLimiter{}

	scanOne(t, Options{MaxCandidatesToRead: 2, Limiter: limiter}, row, library)

	if limiter.totalBytes > 2*4096 {
		t.Fatalf("a link must cost at most two content reads, read %d bytes", limiter.totalBytes)
	}
}

func TestApply_refusesWhenOldTargetReappeared(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		fixture := newApplyFixture(t, false)
		fixture.options.DryRun = dryRun
		writeFileAt(t, fixture.repair.OldTarget, patterned(4096, 9), agedModTime)

		_, err := Apply(t.Context(), []Repair{fixture.repair}, fixture.options)

		if !errors.Is(err, ErrChangedSinceScan) {
			t.Fatalf("dryRun=%v: expected ErrChangedSinceScan, got %v", dryRun, err)
		}
	}
}

func TestScan_flagsReplacedSymlinksAndResizedCopiesAsDiverged(t *testing.T) {
	directory := t.TempDir()
	replaced := filepath.Join(directory, "replaced.mkv")
	resized := filepath.Join(directory, "resized.mkv")
	sameSize := filepath.Join(directory, "same-size.mkv")
	unsized := filepath.Join(directory, "unsized.mkv")
	for _, path := range []string{replaced, resized, sameSize, unsized} {
		writeFileAt(t, path, patterned(64, 1), agedModTime)
	}
	links := []store.Link{
		{Kind: "symlink", LinkPath: replaced, TargetPath: "/home/u/library/replaced.mkv", Size: 64},
		{Kind: "reflink", LinkPath: resized, TargetPath: "/home/u/library/resized.mkv", Size: 65},
		{Kind: "reflink", LinkPath: sameSize, TargetPath: "/home/u/library/same-size.mkv", Size: 64},
		{Kind: "copy", LinkPath: unsized, TargetPath: "/home/u/library/unsized.mkv"},
	}

	_, report, err := Scan(context.Background(), Options{}, links, nil)

	expectedDiverged := []string{replaced, resized}
	if err != nil || !slices.Equal(report.Diverged, expectedDiverged) {
		t.Fatalf("gotValue diverged %v %v, expectedValue %v", report.Diverged, err, expectedDiverged)
	}
	if expectedOK := []string{sameSize, unsized}; !slices.Equal(report.OK, expectedOK) {
		t.Fatalf("gotValue ok %v, expectedValue %v", report.OK, expectedOK)
	}
	conditions := map[string]Condition{}
	for _, link := range links {
		conditions[link.LinkPath] = Inspect(link).Condition
	}
	expectedConditions := map[string]Condition{
		replaced: ConditionReplaced, resized: ConditionResized, sameSize: ConditionSizeMatches, unsized: ConditionUnverified,
	}
	if !maps.Equal(conditions, expectedConditions) {
		t.Fatalf("gotValue %v, expectedValue %v", conditions, expectedConditions)
	}
}

// recordingRechecker retient les chemins recontrôlés et vérifie que le lien est déjà remplacé à l'appel.
type recordingRechecker struct {
	t       *testing.T
	checked []string
	err     error
}

func (rechecker *recordingRechecker) RecheckPath(_ context.Context, path string) error {
	if _, err := os.Stat(path); err != nil {
		rechecker.t.Fatalf("recheck asked before the link was repaired: %v", err)
	}
	rechecker.checked = append(rechecker.checked, path)
	return rechecker.err
}

func TestApply_rechecksTheRepairedLinkAfterTheRenameButNotInDryRun(t *testing.T) {
	fixture := newApplyFixture(t, true)
	rechecker := &recordingRechecker{t: t, err: errors.New("still incomplete")}
	fixture.options.Rechecker = rechecker
	fixture.options.DryRun = true

	if _, err := Apply(t.Context(), []Repair{fixture.repair}, fixture.options); err != nil || len(rechecker.checked) != 0 {
		t.Fatalf("dry run: gotValue %v %v, expectedValue no recheck", err, rechecker.checked)
	}
	fixture.options.DryRun = false
	result, err := Apply(t.Context(), []Repair{fixture.repair}, fixture.options)

	if err != nil || len(rechecker.checked) != 1 || rechecker.checked[0] != fixture.repair.LinkPath {
		t.Fatalf("gotValue %v %v, expectedValue one recheck of the link", err, rechecker.checked)
	}
	if gotErr := result.Outcomes[0].RecheckErr; gotErr == nil || !result.Outcomes[0].Done {
		t.Fatalf("gotValue %+v, expectedValue the recheck failure kept beside a done repair", result.Outcomes[0])
	}
}

func TestApply_refusesWritesWithoutRegistry(t *testing.T) {
	fixture := newApplyFixture(t, false)
	fixture.options.Registry = nil

	_, err := Apply(t.Context(), []Repair{fixture.repair}, fixture.options)

	if !errors.Is(err, fsys.ErrNoLibraryRoots) {
		t.Fatalf("expectedValue fsys.ErrNoLibraryRoots, gotValue %v", err)
	}
	gotText, _ := os.Readlink(fixture.repair.LinkPath)
	if gotText != fixture.repair.OldTarget {
		t.Fatalf("expectedValue the broken link untouched, gotValue %q", gotText)
	}
	assertNoTemporary(t, fixture.place.linkDirectory)
}
