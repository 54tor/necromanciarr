package dedupe

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/fsystest"
	"necromanciarr/internal/fsys/local"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/linker"
	"necromanciarr/internal/store"
)

// remoteLayout : une copie côté torrent et le même film côté bibliothèque, sur un volume distant factice.
type remoteLayout struct {
	volume                                *fsystest.RemoteVolume
	registry                              *fsys.Registry
	torrentRoot, torrentPath, libraryPath string
	content                               []byte
}

const (
	remoteTorrentDir = "/srv/torrents"
	remoteLibraryDir = "/srv/media"
)

func newRemoteLayout(t *testing.T) remoteLayout {
	t.Helper()
	return newRemoteLayoutOfSize(t, 3*mebibyte)
}

func newRemoteLayoutOfSize(t *testing.T, size int) remoteLayout {
	t.Helper()
	volume := fsystest.NewRemoteVolume(t, "seedbox")
	content := patterned(size, 7)
	writeAged(t, volume.LocalPath(remoteTorrentDir+"/movie.mkv"), content)
	writeAged(t, volume.LocalPath(remoteLibraryDir+"/movie.mkv"), content)
	registry, err := fsys.NewRegistry(map[string]fsys.Volume{"": local.New(), "seedbox": volume},
		[]fsys.Location{volume.Location(remoteLibraryDir)})
	if err != nil {
		t.Fatal(err)
	}
	guardLibraryFile(t, volume.LocalPath(remoteLibraryDir+"/movie.mkv"))
	return remoteLayout{
		volume: volume, registry: registry, content: content,
		torrentRoot: volume.Location(remoteTorrentDir).String(),
		torrentPath: volume.Location(remoteTorrentDir + "/movie.mkv").String(),
		libraryPath: volume.Location(remoteLibraryDir + "/movie.mkv").String(),
	}
}

func (layout remoteLayout) library() []store.MediaFile {
	return []store.MediaFile{{Path: layout.libraryPath, Size: int64(len(layout.content)), ModTime: agedModTime}}
}

func (layout remoteLayout) options() Options {
	options := baseOptions(layout.torrentRoot)
	options.Registry = layout.registry
	return options
}

func (layout remoteLayout) planOne(t *testing.T, options Options) Replacement {
	t.Helper()
	replacements, err := Plan(context.Background(), []string{layout.torrentRoot}, layout.library(), options)
	if err != nil {
		t.Fatal(err)
	}
	if len(replacements) != 1 {
		t.Fatalf("gotValue %d replacements, expectedValue 1: %+v", len(replacements), replacements)
	}
	return replacements[0]
}

// assertWritesUnder exige au moins une écriture et aucune hors de directory (chemin distant).
func assertWritesUnder(t *testing.T, writes []fsystest.RecordedWrite, directory string) {
	t.Helper()
	if len(writes) == 0 {
		t.Fatal("expectedValue writes through the remote write root, gotValue none")
	}
	for _, write := range writes {
		if !strings.HasPrefix(write.Path, directory+"/") {
			t.Fatalf("gotValue %s on %s, expectedValue every write under %s", write.Method, write.Path, directory)
		}
	}
}

func TestDedupe_replacesThroughARemoteWriteRootWithAtomicReplace(t *testing.T) {
	layout := newRemoteLayout(t)
	limiter := &countingLimiter{}
	recorder := &fakeRecorder{}
	rechecker := &fakeRechecker{}
	options := layout.options()
	options.Limiter, options.Recorder, options.Rechecker = limiter, recorder, rechecker
	size := int64(len(layout.content))

	replacement := layout.planOne(t, options)

	expectedPlanBytes := int64(2 * 2 * headTailSize)
	hasCostWarning := slices.ContainsFunc(replacement.Warnings, func(warning i18n.Message) bool {
		return warning.Key == i18n.KeyWarningDedupeFullRemoteRead
	})
	isFullReadProposal := replacement.Kind == linker.Symlink && replacement.Method == MethodFull &&
		replacement.BytesToRead == 2*size && hasCostWarning
	if !isFullReadProposal || limiter.totalBytes != expectedPlanBytes {
		t.Fatalf("gotValue %+v (limiter %d), expectedValue a full-read symlink proposal announcing its cost, %d bytes charged",
			replacement, limiter.totalBytes, expectedPlanBytes)
	}
	result, err := Apply(context.Background(), []Replacement{replacement}, options)
	if err != nil || !result.Outcomes[0].Done {
		t.Fatalf("gotValue %v %+v, expectedValue the replacement done", err, result)
	}
	outcome := result.Outcomes[0]
	if chargedDuringApply := limiter.totalBytes - expectedPlanBytes; outcome.BytesRead < 2*size || chargedDuringApply < outcome.BytesRead {
		t.Fatalf("gotValue %d bytes read and %d charged, expectedValue a full read under the limiter", outcome.BytesRead, chargedDuringApply)
	}
	gotTarget, err := os.Readlink(layout.volume.LocalPath(remoteTorrentDir + "/movie.mkv"))
	if err != nil || gotTarget != "../media/movie.mkv" {
		t.Fatalf("gotValue %q (%v), expectedValue the relative POSIX target", gotTarget, err)
	}
	assertWritesUnder(t, layout.volume.Writes(), remoteTorrentDir)
	assertNoTemporaryEntry(t, layout.volume.LocalPath(remoteTorrentDir))
	gotLink := recorder.links[0]
	if gotLink.LinkPath != layout.torrentPath || gotLink.TargetPath != layout.libraryPath || gotLink.Volume != "seedbox" || gotLink.InodeReliable {
		t.Fatalf("gotValue %+v, expectedValue the stored forms, the volume and an unreliable inode", gotLink)
	}
	if !slices.Equal(rechecker.paths, []string{layout.torrentPath}) {
		t.Fatalf("gotValue %v, expectedValue a recheck of the stored torrent path", rechecker.paths)
	}
}

func TestDedupe_noAtomicReplaceMakesProposalNotApplicable(t *testing.T) {
	layout := newRemoteLayout(t)
	layout.volume.NoAtomicReplace.Store(true)
	options := layout.options()

	replacement := layout.planOne(t, options)

	if replacement.Kind != 0 || replacement.SavedBytes != 0 || !errors.Is(replacement.Reason, fsys.ErrNoAtomicReplace) {
		t.Fatalf("gotValue %+v, expectedValue a non-applicable proposal for want of atomic replace", replacement)
	}
	if writes := layout.volume.Writes(); len(writes) != 0 {
		t.Fatalf("gotValue %+v, expectedValue no write while planning", writes)
	}
	replacement.Kind = linker.Symlink

	_, err := Apply(context.Background(), []Replacement{replacement}, options)

	if !errors.Is(err, fsys.ErrNoAtomicReplace) {
		t.Fatalf("gotValue %v, expectedValue fsys.ErrNoAtomicReplace", err)
	}
	if writes := layout.volume.Writes(); len(writes) != 0 {
		t.Fatalf("gotValue %+v, expectedValue nothing written, no non-atomic fallback", writes)
	}
	assertRegularWith(t, layout.volume.LocalPath(remoteTorrentDir+"/movie.mkv"), layout.content)
}

func TestDedupe_replaceRefusedAtTheLastMomentNeverFallsBack(t *testing.T) {
	layout := newRemoteLayout(t)
	options := layout.options()
	replacement := layout.planOne(t, options)
	options.createLink = func(_ linker.Kind, source, destination, _ string) error {
		location, err := fsys.ParseLocation(destination)
		if err != nil {
			return err
		}
		// Le serveur perd posix-rename entre le contrôle de capacité et le remplacement.
		layout.volume.NoAtomicReplace.Store(true)
		return os.Symlink(source, layout.volume.LocalPath(location.Path))
	}

	_, err := Apply(context.Background(), []Replacement{replacement}, options)

	if !errors.Is(err, fsys.ErrNoAtomicReplace) {
		t.Fatalf("gotValue %v, expectedValue fsys.ErrNoAtomicReplace", err)
	}
	assertRegularWith(t, layout.volume.LocalPath(remoteTorrentDir+"/movie.mkv"), layout.content)
	assertNoTemporaryEntry(t, layout.volume.LocalPath(remoteTorrentDir))
}

func TestDedupe_unknownFileIDIsNeverTheSameFile(t *testing.T) {
	layout := newRemoteLayout(t)
	torrentLocal := layout.volume.LocalPath(remoteTorrentDir + "/movie.mkv")
	if err := os.Remove(torrentLocal); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(layout.volume.LocalPath(remoteLibraryDir+"/movie.mkv"), torrentLocal); err != nil {
		t.Skipf("no hardlink in the test directory: %v", err)
	}

	unknown := layout.planOne(t, layout.options())

	if unknown.Method != MethodFull || unknown.BytesToRead == 0 {
		t.Fatalf("gotValue %+v, expectedValue a read proof, never « already linked » on an unknown identity", unknown)
	}
	layout.volume.FileIDKnown.Store(true)
	known, err := Plan(context.Background(), []string{layout.torrentRoot}, layout.library(), layout.options())
	if err != nil || len(known) != 0 {
		t.Fatalf("gotValue %+v %v, expectedValue the same file skipped once its identity is known", known, err)
	}
}

func TestDedupe_unknownFileIDNeverProvesTheTemporaryLink(t *testing.T) {
	layout := newRemoteLayout(t)
	decoy := layout.volume.LocalPath("/srv/other/decoy.mkv")
	writeAged(t, decoy, patterned(len(layout.content), 9))
	options := layout.options()
	options.createLink = func(_ linker.Kind, _, destination, _ string) error {
		location, err := fsys.ParseLocation(destination)
		if err != nil {
			return err
		}
		return os.Symlink(decoy, layout.volume.LocalPath(location.Path))
	}
	replacement := layout.planOne(t, options)

	_, err := Apply(context.Background(), []Replacement{replacement}, options)

	if !errors.Is(err, ErrNotIdentical) {
		t.Fatalf("gotValue %v, expectedValue ErrNotIdentical for a temporary link to another file of the same size", err)
	}
	assertRegularWith(t, layout.volume.LocalPath(remoteTorrentDir+"/movie.mkv"), layout.content)
	assertNoTemporaryEntry(t, layout.volume.LocalPath(remoteTorrentDir))
}

func TestRecover_throughTheRemoteWriteRootTouchesOnlyItsTemporaryEntries(t *testing.T) {
	layout := newRemoteLayout(t)
	size := int64(len(layout.content))
	torrentLocal := layout.volume.LocalPath(remoteTorrentDir + "/movie.mkv")
	if err := os.Remove(torrentLocal); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../media/movie.mkv", temporaryPathFor(torrentLocal, size)); err != nil {
		t.Fatal(err)
	}
	presentLocal := layout.volume.LocalPath(remoteTorrentDir + "/kept.mkv")
	writeAged(t, presentLocal, layout.content)
	if err := os.Symlink("../media/movie.mkv", temporaryPathFor(presentLocal, size)); err != nil {
		t.Fatal(err)
	}
	foreign := layout.volume.LocalPath(remoteTorrentDir + "/notes.txt")
	writeAged(t, foreign, []byte("not ours"))

	result, err := Recover(context.Background(), []string{layout.torrentRoot}, layout.registry)

	if err != nil || len(result.Actions) != 2 {
		t.Fatalf("gotValue %+v %v, expectedValue two recover actions", result, err)
	}
	gotTarget, err := os.Readlink(torrentLocal)
	if err != nil || gotTarget != "../media/movie.mkv" {
		t.Fatalf("gotValue %q (%v), expectedValue the interrupted rename completed", gotTarget, err)
	}
	assertRegularWith(t, presentLocal, layout.content)
	assertRegularWith(t, foreign, []byte("not ours"))
	assertNoTemporaryEntry(t, layout.volume.LocalPath(remoteTorrentDir))
	assertWritesUnder(t, layout.volume.Writes(), remoteTorrentDir)
}

func TestRecover_refusesATorrentRootOverlappingTheLibrary(t *testing.T) {
	layout := newOperatorLayout(t)
	libraryDir := filepath.Dir(layout.libraryPath)
	leftover := temporaryPathFor(filepath.Join(libraryDir, "other.mkv"), int64(len(layout.content)))
	if err := os.Symlink(layout.libraryPath, leftover); err != nil {
		t.Fatal(err)
	}

	_, err := Recover(context.Background(), []string{libraryDir}, fsystest.LocalRegistry(t, libraryDir))

	if !errors.Is(err, fsys.ErrWriteInLibrary) {
		t.Fatalf("gotValue %v, expectedValue fsys.ErrWriteInLibrary", err)
	}
	if _, statErr := os.Lstat(leftover); statErr != nil {
		t.Fatalf("gotValue %v, expectedValue the library entry untouched", statErr)
	}
}

func TestDedupe_touchesTheDiskOnlyThroughTheRegistry(t *testing.T) {
	forbiddenFilepath := map[string]bool{"WalkDir": true, "Walk": true, "EvalSymlinks": true, "Glob": true}
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, fileName := range sources {
		if strings.HasSuffix(fileName, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), fileName, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, imported := range parsed.Imports {
			if importPath := strings.Trim(imported.Path.Value, `"`); isBannedDiskImport(importPath) {
				t.Errorf("%s imports %s, expectedValue disk access only through the registry", fileName, importPath)
			}
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			selector, isSelector := node.(*ast.SelectorExpr)
			if !isSelector {
				return true
			}
			qualifier, isIdentifier := selector.X.(*ast.Ident)
			if isIdentifier && qualifier.Name == "filepath" && forbiddenFilepath[selector.Sel.Name] {
				t.Errorf("%s calls filepath.%s, expectedValue disk access only through the registry", fileName, selector.Sel.Name)
			}
			return true
		})
	}
}

// outageReader rend la moitié de la lecture puis l'erreur d'un volume distant coupé, comme sftpfs sur une lecture
// courte : jamais io.EOF.
type outageReader struct{}

func (outageReader) ReadAt(buffer []byte, _ int64) (int, error) {
	return len(buffer) / 2, fmt.Errorf("%w: read ended before the file size", fsys.ErrRemoteUnavailable)
}

func TestRead_remoteOutageIsReturnedAsItIsNeverAsAShortenedFile(t *testing.T) {
	readMeter := &meter{ctx: context.Background()}

	_, err := fingerprintReader(readMeter, outageReader{}, 3*mebibyte)

	if !errors.Is(err, fsys.ErrRemoteUnavailable) || errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("valeurObtenue %v, valeurAttendue ErrRemoteUnavailable, never a shortened file", err)
	}
}

// isBannedDiskImport : os, os/exec, syscall, io/ioutil et golang.org/x/sys/… ouvriraient un accès disque (ou un
// programme externe) hors du registre ; même liste que la garde du linker (internal/linker/guard_test.go).
func isBannedDiskImport(importPath string) bool {
	bannedImports := []string{"os", "os/exec", "syscall", "io/ioutil"}
	return slices.Contains(bannedImports, importPath) || strings.HasPrefix(importPath, "golang.org/x/sys/")
}
