package repair

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/fsystest"
	"necromanciarr/internal/fsys/local"
	"necromanciarr/internal/store"
)

const (
	remoteLibraryDir = "/srv/media"
	remoteLinkDir    = "/srv/links"
	remoteFileSize   = 4096
)

// remotePlace : un lien symbolique cassé dans un dossier de liens distant, le fichier renommé dans la bibliothèque.
type remotePlace struct {
	volume             *fsystest.RemoteVolume
	registry           *fsys.Registry
	linkPath, linkDir  string
	renamed            store.MediaFile
	renamedRemotePath  string
	brokenRelativeText string
}

func newRemotePlace(t *testing.T) remotePlace {
	t.Helper()
	volume := fsystest.NewRemoteVolume(t, "seedbox")
	renamedRemotePath := remoteLibraryDir + "/renamed.mkv"
	writeFileAt(t, volume.LocalPath(renamedRemotePath), patterned(remoteFileSize, 1), agedModTime)
	guardLibraryFile(t, volume.LocalPath(renamedRemotePath))
	volume.MkdirAll(t, remoteLinkDir)
	brokenText := "../media/old.mkv"
	if err := os.Symlink(brokenText, volume.LocalPath(remoteLinkDir+"/movie.mkv")); err != nil {
		t.Fatal(err)
	}
	registry, err := fsys.NewRegistry(map[string]fsys.Volume{"": local.New(), "seedbox": volume},
		[]fsys.Location{volume.Location(remoteLibraryDir)})
	if err != nil {
		t.Fatal(err)
	}
	return remotePlace{
		volume: volume, registry: registry, renamedRemotePath: renamedRemotePath, brokenRelativeText: brokenText,
		linkPath: volume.Location(remoteLinkDir + "/movie.mkv").String(),
		linkDir:  volume.Location(remoteLinkDir).String(),
		renamed:  store.MediaFile{Path: volume.Location(renamedRemotePath).String(), Size: remoteFileSize, ModTime: agedModTime},
	}
}

func (place remotePlace) repair() Repair {
	return Repair{
		LinkPath: place.linkPath, OldTarget: place.brokenRelativeText, NewTarget: place.renamed.Path, Size: remoteFileSize,
		Method: ByFingerprint, Relative: true,
	}
}

func TestRepair_inodeMethodRequiresFileIdentity(t *testing.T) {
	place := newRemotePlace(t)
	place.volume.FileIDKnown.Store(true)
	info, err := place.volume.Stat(place.renamedRemotePath)
	if err != nil {
		t.Fatal(err)
	}
	identity, known := place.volume.FileID(info)
	if !known {
		t.Skip("no inode on this platform")
	}
	row := brokenRow(place.linkPath, place.volume.Location(remoteLibraryDir+"/old.mkv").String(), remoteFileSize)
	row.InodeReliable, row.TargetDevice, row.TargetInode = true, identity.Device, identity.Inode
	options := Options{AutoApply: true, Registry: place.registry}

	withoutIdentity, _ := scanOne(t, options, row, []store.MediaFile{place.renamed})
	place.volume.RemoteStat.Store(true)
	withRemoteStat, _ := scanOne(t, options, row, []store.MediaFile{place.renamed})

	for _, repair := range withoutIdentity {
		if repair.Method == ByInode {
			t.Fatalf("gotValue %+v, expectedValue no inode method on a volume without file identity", withoutIdentity)
		}
	}
	if len(withRemoteStat) != 1 || withRemoteStat[0].Method != ByInode || withRemoteStat[0].NewTarget != place.renamed.Path {
		t.Fatalf("gotValue %+v, expectedValue the inode method once the volume announces remote stat", withRemoteStat)
	}
}

func TestRepair_remoteFingerprintReadsGoThroughTheReadLimiter(t *testing.T) {
	place := newRemotePlace(t)
	row := brokenRow(place.linkPath, place.volume.Location(remoteLibraryDir+"/old.mkv").String(), remoteFileSize)
	row.Fingerprint = fingerprintOf(t, place.volume.LocalPath(place.renamedRemotePath), remoteFileSize)
	limiter := &countingLimiter{}

	repairs, _ := scanOne(t, Options{Limiter: limiter, Registry: place.registry}, row, []store.MediaFile{place.renamed})

	if len(repairs) != 1 || repairs[0].Method != ByFingerprint || limiter.totalBytes != remoteFileSize {
		t.Fatalf("gotValue %+v (limiter %d), expectedValue a fingerprint repair read under the limiter", repairs, limiter.totalBytes)
	}
}

func TestRepair_writesOnlyInsideTheLinkDirWriteRoot(t *testing.T) {
	place := newRemotePlace(t)
	options := ApplyOptions{Roots: []string{place.linkDir}, Registry: place.registry}

	result, err := Apply(t.Context(), []Repair{place.repair()}, options)

	if err != nil || !result.Outcomes[0].Done {
		t.Fatalf("gotValue %v %+v, expectedValue the link replaced", err, result)
	}
	gotText, err := os.Readlink(place.volume.LocalPath(remoteLinkDir + "/movie.mkv"))
	if err != nil || gotText != "../media/renamed.mkv" {
		t.Fatalf("gotValue %q (%v), expectedValue the relative POSIX target", gotText, err)
	}
	writes := place.volume.Writes()
	if len(writes) == 0 {
		t.Fatal("expectedValue writes through the link dir write root, gotValue none")
	}
	for _, write := range writes {
		if !strings.HasPrefix(write.Path, remoteLinkDir+"/") {
			t.Fatalf("gotValue %s on %s, expectedValue every write inside %s", write.Method, write.Path, remoteLinkDir)
		}
	}
}

func TestRepair_noAtomicReplaceRefusesBeforeAnyWrite(t *testing.T) {
	place := newRemotePlace(t)
	place.volume.NoAtomicReplace.Store(true)
	options := ApplyOptions{Roots: []string{place.linkDir}, Registry: place.registry}

	_, err := Apply(t.Context(), []Repair{place.repair()}, options)

	if !errors.Is(err, fsys.ErrNoAtomicReplace) {
		t.Fatalf("gotValue %v, expectedValue fsys.ErrNoAtomicReplace", err)
	}
	if writes := place.volume.Writes(); len(writes) != 0 {
		t.Fatalf("gotValue %+v, expectedValue nothing written, no non-atomic fallback", writes)
	}
	gotText, _ := os.Readlink(place.volume.LocalPath(remoteLinkDir + "/movie.mkv"))
	if gotText != place.brokenRelativeText {
		t.Fatalf("gotValue %q, expectedValue the old link untouched", gotText)
	}
}

func TestRepair_touchesTheDiskOnlyThroughTheRegistry(t *testing.T) {
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
			if importPath := strings.Trim(imported.Path.Value, `"`); importPath == "os" || importPath == "syscall" {
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
