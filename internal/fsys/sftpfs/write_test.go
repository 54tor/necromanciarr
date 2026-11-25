package sftpfs_test

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/pkg/sftp"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/fsystest"
	"necromanciarr/internal/fsys/local"
	"necromanciarr/internal/fsys/sftpfs"
	"necromanciarr/internal/fsys/sftpfs/sftptest"
)

// Arbre servi par les tests d'écriture : bibliothèque, dossier de liens (la racine d'écriture) et dossier extérieur
// où ne doit jamais rien atterrir.
const (
	libraryDir = "/library"
	linksDir   = "/links"
	outsideDir = "/outside"
)

// writeMethods : requêtes qui modifient l'arbre du serveur (contrat C8), Setstat compris.
var writeMethods = []string{"Put", "Open", "Setstat", "Mkdir", "Rmdir", "Remove", "Rename", "PosixRename", "Symlink", "Link"}

func localPathOf(server *sftptest.Server, remotePath string) string {
	return filepath.Join(server.Root, filepath.FromSlash(strings.TrimPrefix(remotePath, "/")))
}

func skipWithoutSymlinks(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlinks planted on the served tree need a Unix host")
	}
}

// writeVolume démarre un serveur qui porte la bibliothèque de conformité, un dossier de liens vide et un dossier
// extérieur vide.
func writeVolume(t *testing.T, options ...sftptest.Option) (*sftptest.Server, *sftpfs.Volume) {
	t.Helper()
	server := sftptest.Start(t, options...)
	sftptest.Mkdir(t, localPathOf(server, libraryDir))
	fsystest.WriteLibraryFixture(t, localPathOf(server, libraryDir))
	sftptest.Mkdir(t, localPathOf(server, linksDir))
	sftptest.Mkdir(t, localPathOf(server, outsideDir))
	return server, dialVolume(t, pinnedProvider(t, server), testDeps())
}

func openLinksRoot(t *testing.T, volume *sftpfs.Volume) fsys.WriteRoot {
	t.Helper()
	root, err := volume.OpenWriteRoot(linksDir)
	if err != nil {
		t.Fatalf("OpenWriteRoot: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

// writesSince rend les requêtes d'écriture reçues après les premières skipped requêtes du journal.
func writesSince(server *sftptest.Server, skipped int) []sftptest.Request {
	var writes []sftptest.Request
	for _, request := range server.Requests()[skipped:] {
		if slices.Contains(writeMethods, request.Method) {
			writes = append(writes, request)
		}
	}
	return writes
}

func methodsSince(server *sftptest.Server, skipped int, method string) []sftptest.Request {
	var matching []sftptest.Request
	for _, request := range server.Requests()[skipped:] {
		if request.Method == method {
			matching = append(matching, request)
		}
	}
	return matching
}

func createThrough(t *testing.T, root fsys.WriteRoot, relative, content string) {
	t.Helper()
	writer, err := root.CreateExclusive(relative, 0o644)
	if err != nil {
		t.Fatalf("CreateExclusive(%q): %v", relative, err)
	}
	if _, err := io.WriteString(writer, content); err != nil {
		t.Fatalf("write %q: %v", relative, err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close %q: %v", relative, err)
	}
}

func servedContent(t *testing.T, server *sftptest.Server, remotePath string) string {
	t.Helper()
	content, err := os.ReadFile(localPathOf(server, remotePath))
	if err != nil {
		t.Fatalf("read %s: %v", remotePath, err)
	}
	return string(content)
}

func outsideEntries(t *testing.T, server *sftptest.Server) []string {
	t.Helper()
	entries, err := os.ReadDir(localPathOf(server, outsideDir))
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// callEveryMethod appelle chaque méthode de la racine avec relative en destination (et dans les deux positions de
// Replace) ; "kept" existe pour que Replace ne puisse échouer pour une autre raison.
func callEveryMethod(t *testing.T, root fsys.WriteRoot, relative string) map[string]error {
	t.Helper()
	source := fsys.Location{Volume: "seedbox", Path: libraryDir + "/" + fsystest.FixtureMovieName}
	outcomes := map[string]error{
		"MkdirAll":     root.MkdirAll(relative, 0o755),
		"Symlink":      root.Symlink(fsystest.FixtureMovieName, relative),
		"Hardlink":     root.Hardlink(source, relative),
		"Replace old":  root.Replace(relative, "kept"),
		"Replace new":  root.Replace("kept", relative),
		"Remove":       root.Remove(relative),
		"CopyFromDest": nil,
	}
	_, outcomes["CopyFromDest"] = root.CopyFrom(t.Context(), source, relative)
	writer, err := root.CreateExclusive(relative, 0o644)
	if writer != nil {
		_ = writer.Close()
	}
	outcomes["CreateExclusive"] = err
	_, outcomes["Lstat"] = root.Lstat(relative)
	return outcomes
}

func assertAllUnconfined(t *testing.T, relative string, outcomes map[string]error) {
	t.Helper()
	for method, err := range outcomes {
		if !errors.Is(err, fsys.ErrUnconfined) {
			t.Errorf("%s(%q): valeurObtenue %v, valeurAttendue ErrUnconfined", method, relative, err)
		}
	}
}

// assertRefusedWithoutWrite : chaque méthode refuse relative, et aucune requête d'écriture ne part.
func assertRefusedWithoutWrite(t *testing.T, server *sftptest.Server, root fsys.WriteRoot, relative string) {
	t.Helper()
	before := len(server.Requests())
	assertAllUnconfined(t, relative, callEveryMethod(t, root, relative))
	if writes := writesSince(server, before); len(writes) != 0 {
		t.Errorf("%q: valeurObtenue write requests %+v, valeurAttendue none", relative, writes)
	}
}

func TestSFTPWriteRoot_refusesSymlinkedParent(t *testing.T) {
	skipWithoutSymlinks(t)
	server, volume := writeVolume(t)
	root := openLinksRoot(t, volume)
	createThrough(t, root, "kept", "kept")
	if err := root.MkdirAll("real", 0o755); err != nil {
		t.Fatal(err)
	}
	fsystest.PlantLocalSymlink(t, localPathOf(server, linksDir+"/escape"), libraryDir)
	fsystest.PlantLocalSymlink(t, localPathOf(server, linksDir+"/real/nested-escape"), "../../library")

	for _, relative := range []string{
		"escape/" + fsystest.FixtureNoteName, "escape/" + fsystest.FixtureSubdirName + "/new",
		"real/nested-escape/new", "real/nested-escape/" + fsystest.FixtureSubdirName + "/new",
	} {
		assertRefusedWithoutWrite(t, server, root, relative)
	}
	if valeurObtenue := servedContent(t, server, libraryDir+"/"+fsystest.FixtureNoteName); valeurObtenue != fsystest.FixtureNoteContent {
		t.Errorf("library note: valeurObtenue %q, valeurAttendue untouched", valeurObtenue)
	}
}

func TestSFTPWriteRoot_refusesDotDot(t *testing.T) {
	server, volume := writeVolume(t)
	root := openLinksRoot(t, volume)
	createThrough(t, root, "kept", "kept")
	before := len(server.Requests())

	for _, relative := range []string{"..", "../outside/x", "a/../b", "a/../../outside", "a/..", "./.."} {
		assertAllUnconfined(t, relative, callEveryMethod(t, root, relative))
	}

	if requests := server.Requests()[before:]; len(requests) != 0 {
		t.Errorf("valeurObtenue requests %+v, valeurAttendue a refusal before any request", requests)
	}
}

func TestSFTPWriteRoot_refusesAbsolute(t *testing.T) {
	server, volume := writeVolume(t)
	root := openLinksRoot(t, volume)
	createThrough(t, root, "kept", "kept")
	before := len(server.Requests())

	for _, relative := range []string{"/x", libraryDir + "/new", outsideDir + "/new", "//links/x"} {
		assertAllUnconfined(t, relative, callEveryMethod(t, root, relative))
	}

	if requests := server.Requests()[before:]; len(requests) != 0 {
		t.Errorf("valeurObtenue requests %+v, valeurAttendue a refusal before any request", requests)
	}
}

func TestSFTPWriteRoot_refusesEmptySegmentNULAndOverlongPaths(t *testing.T) {
	server, volume := writeVolume(t)
	root := openLinksRoot(t, volume)
	createThrough(t, root, "kept", "kept")
	before := len(server.Requests())

	for _, relative := range []string{"", ".", "a//b", "a/", "a/\x00b", strings.Repeat("a", 4097)} {
		assertAllUnconfined(t, relative, callEveryMethod(t, root, relative))
	}

	if requests := server.Requests()[before:]; len(requests) != 0 {
		t.Errorf("valeurObtenue requests %+v, valeurAttendue a refusal before any request", requests)
	}
}

func TestSFTPWriteRoot_acceptsBackslashAndDotSegmentsAsOrdinaryNames(t *testing.T) {
	server, volume := writeVolume(t)
	root := openLinksRoot(t, volume)

	createThrough(t, root, `back\slash`, "b")
	createThrough(t, root, "./dotted", "d")

	if valeurObtenue := servedContent(t, server, linksDir+`/back\slash`); valeurObtenue != "b" {
		t.Errorf("valeurObtenue %q, valeurAttendue a file named with a backslash", valeurObtenue)
	}
	if valeurObtenue := servedContent(t, server, linksDir+"/dotted"); valeurObtenue != "d" {
		t.Errorf("valeurObtenue %q, valeurAttendue the dot segment cleaned", valeurObtenue)
	}
}

// swapOnce rend un crochet qui remplace, à la première requête de chemin watchedPath, le dossier
// swappedRemote par un lien symbolique vers outsideDir.
func swapOnce(t *testing.T, server **sftptest.Server, watchedPath, swappedRemote string) func(sftptest.Request) {
	var once sync.Once
	return func(request sftptest.Request) {
		if request.Path != watchedPath {
			return
		}
		once.Do(func() { sftptest.ReplaceWithSymlink(t, localPathOf(*server, swappedRemote), outsideDir) })
	}
}

func TestSFTPWriteRoot_realPathEscapeRefused(t *testing.T) {
	skipWithoutSymlinks(t)
	var server *sftptest.Server
	// Le parent "a" est remplacé par un lien vers l'extérieur juste après son Lstat, avant toute création dessous.
	hook := swapOnce(t, &server, linksDir+"/a/b", linksDir+"/a")
	server, volume := writeVolume(t, sftptest.OnRequest("Lstat", hook))
	root := openLinksRoot(t, volume)
	if err := root.MkdirAll("a", 0o755); err != nil {
		t.Fatal(err)
	}
	before := len(server.Requests())

	err := root.MkdirAll("a/b/c", 0o755)

	if !errors.Is(err, fsys.ErrUnconfined) {
		t.Errorf("MkdirAll: valeurObtenue %v, valeurAttendue ErrUnconfined", err)
	}
	if writes := writesSince(server, before); len(writes) != 0 {
		t.Errorf("valeurObtenue write requests %+v, valeurAttendue none after the swap", writes)
	}
	if entries := outsideEntries(t, server); len(entries) != 0 {
		t.Errorf("valeurObtenue %q outside the root, valeurAttendue nothing", entries)
	}
}

// swapOnNthRealPath rend un crochet qui remplace swappedRemote par un lien vers outsideDir à la nth requête RealPath
// de watchedPath : le contrôle d'après écriture voit alors le parent détourné.
func swapOnNthRealPath(t *testing.T, server **sftptest.Server, watchedPath string, nth int, swappedRemote string) func(sftptest.Request) {
	var mutex sync.Mutex
	seen := 0
	return func(request sftptest.Request) {
		if request.Path != watchedPath {
			return
		}
		mutex.Lock()
		defer mutex.Unlock()
		if seen++; seen == nth {
			sftptest.ReplaceWithSymlink(t, localPathOf(*server, swappedRemote), outsideDir)
		}
	}
}

// escapeAfterWrite : l'écriture faite, le parent "a" devient un lien vers l'extérieur, où une entrée du même nom
// existait déjà ; l'opération doit être refusée et cette entrée survivre (aucun retrait par chemin).
func escapeAfterWrite(t *testing.T, write func(root fsys.WriteRoot) error) {
	t.Helper()
	skipWithoutSymlinks(t)
	var server *sftptest.Server
	hook := swapOnNthRealPath(t, &server, linksDir+"/a", 2, linksDir+"/a")
	server, volume := writeVolume(t, sftptest.OnRequest("RealPath", hook))
	root := openLinksRoot(t, volume)
	sftptest.Mkdir(t, localPathOf(server, linksDir+"/a"))
	sftptest.WriteFile(t, localPathOf(server, outsideDir+"/entry"), []byte("pre-existing"), 0o644)

	err := write(root)

	if !errors.Is(err, fsys.ErrUnconfined) {
		t.Errorf("valeurObtenue %v, valeurAttendue ErrUnconfined once the parent resolves outside", err)
	}
	if valeurObtenue := servedContent(t, server, outsideDir+"/entry"); valeurObtenue != "pre-existing" {
		t.Errorf("valeurObtenue %q, valeurAttendue the outside entry untouched", valeurObtenue)
	}
}

func TestSFTPCreateExclusive_escapeAfterTheWriteIsReportedAndNeverRemovedByPath(t *testing.T) {
	escapeAfterWrite(t, func(root fsys.WriteRoot) error {
		writer, err := root.CreateExclusive("a/entry", 0o644)
		if writer != nil {
			_ = writer.Close()
		}
		return err
	})
}

func TestSFTPSymlink_escapeAfterTheWriteIsReportedAndNeverRemovedByPath(t *testing.T) {
	escapeAfterWrite(t, func(root fsys.WriteRoot) error {
		return root.Symlink(libraryDir+"/"+fsystest.FixtureMovieName, "a/entry")
	})
}

func TestSFTPHardlink_escapeAfterTheWriteIsReportedAndNeverRemovedByPath(t *testing.T) {
	escapeAfterWrite(t, func(root fsys.WriteRoot) error {
		return root.Hardlink(fsys.Location{Volume: "seedbox", Path: libraryDir + "/" + fsystest.FixtureMovieName}, "a/entry")
	})
}

func TestSFTPRemove_escapeDuringTheRemovalIsReported(t *testing.T) {
	skipWithoutSymlinks(t)
	var server *sftptest.Server
	hook := swapOnce(t, &server, linksDir+"/a/entry", linksDir+"/a")
	server, volume := writeVolume(t, sftptest.OnRequest("Remove", hook))
	root := openLinksRoot(t, volume)
	sftptest.WriteFile(t, localPathOf(server, linksDir+"/a/entry"), []byte("x"), 0o644)
	sftptest.WriteFile(t, localPathOf(server, outsideDir+"/entry"), []byte("pre-existing"), 0o644)

	err := root.Remove("a/entry")

	// La course elle-même peut supprimer l'entrée extérieure (fenêtre acceptée) : elle ne doit jamais être muette.
	if !errors.Is(err, fsys.ErrUnconfined) {
		t.Errorf("valeurObtenue %v, valeurAttendue ErrUnconfined: an escape during a removal is reported", err)
	}
}

func TestSFTPReplace_withoutPosixRenameRefuses(t *testing.T) {
	server, volume := writeVolume(t, sftptest.WithoutExtension("posix-rename@openssh.com"))
	root := openLinksRoot(t, volume)
	createThrough(t, root, "old", "new content")
	createThrough(t, root, "new", "previous content")
	before := len(server.Requests())

	err := root.Replace("old", "new")

	if !errors.Is(err, fsys.ErrNoAtomicReplace) {
		t.Errorf("Replace: valeurObtenue %v, valeurAttendue ErrNoAtomicReplace", err)
	}
	if writes := writesSince(server, before); len(writes) != 0 {
		t.Errorf("valeurObtenue %+v, valeurAttendue no Rename, PosixRename nor Remove", writes)
	}
	for remotePath, valeurAttendue := range map[string]string{linksDir + "/old": "new content", linksDir + "/new": "previous content"} {
		if valeurObtenue := servedContent(t, server, remotePath); valeurObtenue != valeurAttendue {
			t.Errorf("%s: valeurObtenue %q, valeurAttendue %q", remotePath, valeurObtenue, valeurAttendue)
		}
	}
	capabilities, err := volume.Capabilities(t.Context())
	if err != nil || capabilities.AtomicReplace {
		t.Errorf("valeurObtenue %+v (err %v), valeurAttendue AtomicReplace false", capabilities, err)
	}
}

func TestSFTPReplace_replacesThroughPosixRenameOnly(t *testing.T) {
	server, volume := writeVolume(t)
	root := openLinksRoot(t, volume)
	createThrough(t, root, "old", "new content")
	createThrough(t, root, "new", "previous content")
	before := len(server.Requests())

	if err := root.Replace("old", "new"); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	if renames := methodsSince(server, before, "Rename"); len(renames) != 0 {
		t.Errorf("valeurObtenue %+v, valeurAttendue no plain Rename", renames)
	}
	if valeurObtenue := methodsSince(server, before, "PosixRename"); len(valeurObtenue) != 1 || valeurObtenue[0].Path != linksDir+"/old" || valeurObtenue[0].Target != linksDir+"/new" {
		t.Errorf("valeurObtenue %+v, valeurAttendue one PosixRename old -> new", valeurObtenue)
	}
	if valeurObtenue := servedContent(t, server, linksDir+"/new"); valeurObtenue != "new content" {
		t.Errorf("valeurObtenue %q, valeurAttendue the replaced content", valeurObtenue)
	}
}

func TestSFTPHardlink_refusesSourceOnOtherVolume(t *testing.T) {
	server, volume := writeVolume(t)
	root := openLinksRoot(t, volume)
	before := len(server.Requests())

	for _, source := range []fsys.Location{
		{Volume: "other", Path: libraryDir + "/" + fsystest.FixtureMovieName},
		{Volume: "", Path: libraryDir + "/" + fsystest.FixtureMovieName},
		{Volume: "seedbox", Path: fsystest.FixtureMovieName},
		{Volume: "seedbox", Path: libraryDir + "/a\x00b"},
	} {
		if err := root.Hardlink(source, "linked"); !errors.Is(err, fsys.ErrUnconfined) {
			t.Errorf("Hardlink(%+v): valeurObtenue %v, valeurAttendue ErrUnconfined", source, err)
		}
	}

	if requests := server.Requests()[before:]; len(requests) != 0 {
		t.Errorf("valeurObtenue requests %+v, valeurAttendue none", requests)
	}
}

func TestSFTPHardlink_linksTheSourceInsideTheRoot(t *testing.T) {
	server, volume := writeVolume(t)
	root := openLinksRoot(t, volume)
	source := fsys.Location{Volume: "seedbox", Path: libraryDir + "/" + fsystest.FixtureMovieName}
	before := len(server.Requests())

	if err := root.Hardlink(source, "linked.mkv"); err != nil {
		t.Fatalf("Hardlink: %v", err)
	}

	links := methodsSince(server, before, "Link")
	if len(links) != 1 || links[0].Path != source.Path || links[0].Target != linksDir+"/linked.mkv" {
		t.Errorf("valeurObtenue %+v, valeurAttendue one Link from the library file into the root", links)
	}
	if valeurObtenue := servedContent(t, server, linksDir+"/linked.mkv"); valeurObtenue != fsystest.FixtureMovieContent {
		t.Errorf("valeurObtenue %q, valeurAttendue the library content", valeurObtenue)
	}
}

func TestSFTPHardlink_withoutExtensionIsUnsupported(t *testing.T) {
	server, volume := writeVolume(t, sftptest.WithoutExtension("hardlink@openssh.com"))
	root := openLinksRoot(t, volume)
	before := len(server.Requests())

	err := root.Hardlink(fsys.Location{Volume: "seedbox", Path: libraryDir + "/" + fsystest.FixtureMovieName}, "linked")

	if !errors.Is(err, local.ErrUnsupported) || !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("valeurObtenue %v, valeurAttendue the unsupported link kind", err)
	}
	if links := methodsSince(server, before, "Link"); len(links) != 0 {
		t.Errorf("valeurObtenue %+v, valeurAttendue no Link request", links)
	}
	capabilities, err := volume.Capabilities(t.Context())
	if err != nil || capabilities.Hardlink {
		t.Errorf("valeurObtenue %+v (err %v), valeurAttendue Hardlink false", capabilities, err)
	}
}

func TestSFTPCreateExclusive_sendsExclusiveWriteFlagsOnly(t *testing.T) {
	server, volume := writeVolume(t)
	root := openLinksRoot(t, volume)
	before := len(server.Requests())

	createThrough(t, root, "file", "first")

	opens := slices.Concat(methodsSince(server, before, "Put"), methodsSince(server, before, "Open"))
	valeurAttendue := sftptest.FlagWrite | sftptest.FlagCreate | sftptest.FlagExclusive
	if len(opens) != 1 || opens[0].Flags != valeurAttendue {
		t.Errorf("valeurObtenue %+v, valeurAttendue one open with WRITE|CREAT|EXCL only", opens)
	}
}

func TestSFTPCreateExclusive_existingEntryIsErrExistAndUntouched(t *testing.T) {
	skipWithoutSymlinks(t)
	server, volume := writeVolume(t)
	root := openLinksRoot(t, volume)
	createThrough(t, root, "file", "first")
	fsystest.PlantLocalSymlink(t, localPathOf(server, linksDir+"/dangling"), outsideDir+"/target")

	for _, relative := range []string{"file", "dangling"} {
		writer, err := root.CreateExclusive(relative, 0o644)
		if writer != nil {
			_ = writer.Close()
		}
		if !errors.Is(err, fs.ErrExist) {
			t.Errorf("CreateExclusive(%q): valeurObtenue %v, valeurAttendue fs.ErrExist", relative, err)
		}
	}

	if valeurObtenue := servedContent(t, server, linksDir+"/file"); valeurObtenue != "first" {
		t.Errorf("valeurObtenue %q, valeurAttendue the first content", valeurObtenue)
	}
	if entries := outsideEntries(t, server); len(entries) != 0 {
		t.Errorf("valeurObtenue %q, valeurAttendue nothing created through the dangling link", entries)
	}
}

func TestSFTPCreateExclusive_writerExposesOnlyWriteAndClose(t *testing.T) {
	_, volume := writeVolume(t)
	root := openLinksRoot(t, volume)

	writer, err := root.CreateExclusive("file", 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()

	if _, isFile := writer.(*sftp.File); isFile {
		t.Error("valeurObtenue *sftp.File, valeurAttendue a wrapper")
	}
	checks := map[string]bool{}
	_, checks["ReadFrom"] = writer.(io.ReaderFrom)
	_, checks["ReadAt"] = writer.(io.ReaderAt)
	_, checks["Reader"] = writer.(io.Reader)
	_, checks["WriterTo"] = writer.(io.WriterTo)
	_, checks["Truncate"] = writer.(interface{ Truncate(int64) error })
	_, checks["Chmod"] = writer.(interface{ Chmod(os.FileMode) error })
	_, checks["Stat"] = writer.(interface{ Stat() (os.FileInfo, error) })
	for method, isExposed := range checks {
		if isExposed {
			t.Errorf("valeurObtenue %s exposed, valeurAttendue Write and Close only", method)
		}
	}
}

func TestSFTPCreateExclusive_masksPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	server, volume := writeVolume(t)
	root := openLinksRoot(t, volume)

	writer, err := root.CreateExclusive("file", 0o4750|fs.ModeSetgid)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(localPathOf(server, linksDir+"/file"))
	if err != nil {
		t.Fatal(err)
	}
	if valeurObtenue := info.Mode(); valeurObtenue != 0o750 {
		t.Errorf("valeurObtenue %v, valeurAttendue -rwxr-x--- without setuid or setgid", valeurObtenue)
	}
}

func TestSFTPCreateExclusive_missingParentIsNotExist(t *testing.T) {
	_, volume := writeVolume(t)
	root := openLinksRoot(t, volume)

	writer, err := root.CreateExclusive("missing/file", 0o644)
	if writer != nil {
		_ = writer.Close()
	}

	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("valeurObtenue %v, valeurAttendue fs.ErrNotExist (only MkdirAll creates parents)", err)
	}
}

func TestSFTPSymlink_storesTheRelativeTargetAsGiven(t *testing.T) {
	server, volume := writeVolume(t)
	root := openLinksRoot(t, volume)
	createThrough(t, root, "x", "intended")
	if err := root.MkdirAll("sub", 0o755); err != nil {
		t.Fatal(err)
	}
	before := len(server.Requests())

	if err := root.Symlink("../x", "sub/link"); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	symlinks := methodsSince(server, before, "Symlink")
	if len(symlinks) != 1 || symlinks[0].Path != linksDir+"/sub/link" || symlinks[0].Target != "../x" {
		t.Errorf("valeurObtenue %+v, valeurAttendue link %s/sub/link with the raw target ../x", symlinks, linksDir)
	}
	if valeurObtenue, err := volume.Readlink(linksDir + "/sub/link"); err != nil || valeurObtenue != "../x" {
		t.Errorf("Readlink: valeurObtenue %q (err %v), valeurAttendue ../x", valeurObtenue, err)
	}
	file := openRemote(t, volume, linksDir+"/sub/link")
	if valeurObtenue := readAll(t, file); valeurObtenue != "intended" {
		t.Errorf("valeurObtenue %q, valeurAttendue the file resolved from the link's own directory", valeurObtenue)
	}
}

func servedEntries(t *testing.T, server *sftptest.Server, remoteDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(localPathOf(server, remoteDir))
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestSFTPSymlink_reversedServerIsRefusedBeforeAnyRequest(t *testing.T) {
	server, volume := writeVolume(t, sftptest.ReversedSymlinkArgs())
	root := openLinksRoot(t, volume)
	before := len(server.Requests())

	err := root.Symlink("../x", "link")

	if !errors.Is(err, sftpfs.ErrSymlinkOrderUnverified) {
		t.Errorf("valeurObtenue %v, valeurAttendue ErrSymlinkOrderUnverified on a server reading the RFC order", err)
	}
	if requests := server.Requests()[before:]; len(requests) != 0 {
		t.Errorf("valeurObtenue %+v, valeurAttendue no request", requests)
	}
	valeurAttendue := []string{"library", "links", "outside"}
	if valeurObtenue := servedEntries(t, server, "/"); !slices.Equal(valeurObtenue, valeurAttendue) {
		t.Errorf("server root: valeurObtenue %q, valeurAttendue %q", valeurObtenue, valeurAttendue)
	}
	for _, remoteDir := range []string{linksDir, outsideDir} {
		if valeurObtenue := servedEntries(t, server, remoteDir); len(valeurObtenue) != 0 {
			t.Errorf("%s: valeurObtenue %q, valeurAttendue the probe cleaned up", remoteDir, valeurObtenue)
		}
	}
}

func TestSFTPOpenWriteRoot_probesTheSymlinkOrderOncePerSessionAndLeavesNothing(t *testing.T) {
	server, volume := writeVolume(t)
	for range 2 {
		_ = openLinksRoot(t, volume)
	}
	if valeurObtenue := len(methodsSince(server, 0, "Symlink")); valeurObtenue != 1 {
		t.Errorf("valeurObtenue %d probes, valeurAttendue 1 for one session", valeurObtenue)
	}
	if valeurObtenue := servedEntries(t, server, linksDir); len(valeurObtenue) != 0 {
		t.Errorf("valeurObtenue %q, valeurAttendue no probe entry left", valeurObtenue)
	}

	server.DropConnections()
	waitForState(t, volume, sftpfs.StateBroken)
	volume.ResetForStage()
	_ = openLinksRoot(t, volume)

	if valeurObtenue := len(methodsSince(server, 0, "Symlink")); valeurObtenue != 2 {
		t.Errorf("valeurObtenue %d probes, valeurAttendue a new probe on the new session", valeurObtenue)
	}
}

func TestSFTPRemove_neverFollowsNorRecurses(t *testing.T) {
	server, volume := writeVolume(t)
	root := openLinksRoot(t, volume)
	if err := root.Symlink(libraryDir+"/"+fsystest.FixtureMovieName, "movie-link"); err != nil {
		t.Fatal(err)
	}
	if err := root.MkdirAll("full/inner", 0o755); err != nil {
		t.Fatal(err)
	}
	createThrough(t, root, "full/inner/file", "x")
	if err := root.MkdirAll("empty", 0o755); err != nil {
		t.Fatal(err)
	}

	if err := root.Remove("movie-link"); err != nil {
		t.Errorf("Remove(link): %v", err)
	}
	if err := root.Remove("full"); err == nil {
		t.Error("Remove(non-empty dir): valeurObtenue nil, valeurAttendue a refusal")
	}
	if err := root.Remove("empty"); err != nil {
		t.Errorf("Remove(empty dir): %v", err)
	}

	if valeurObtenue := servedContent(t, server, libraryDir+"/"+fsystest.FixtureMovieName); valeurObtenue != fsystest.FixtureMovieContent {
		t.Errorf("valeurObtenue %q, valeurAttendue the link target untouched", valeurObtenue)
	}
	if valeurObtenue := servedContent(t, server, linksDir+"/full/inner/file"); valeurObtenue != "x" {
		t.Errorf("valeurObtenue %q, valeurAttendue no recursive removal", valeurObtenue)
	}
	for _, removed := range []string{"movie-link", "empty"} {
		if _, err := root.Lstat(removed); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Lstat(%q): valeurObtenue %v, valeurAttendue removed", removed, err)
		}
	}
}

func TestSFTPMkdirAll_refusesExistingNonDirectoryAndKeepsExistingDirectories(t *testing.T) {
	skipWithoutSymlinks(t)
	server, volume := writeVolume(t)
	root := openLinksRoot(t, volume)
	createThrough(t, root, "file", "x")
	fsystest.PlantLocalSymlink(t, localPathOf(server, linksDir+"/dir-link"), outsideDir)

	if err := root.MkdirAll("file/child", 0o755); err == nil {
		t.Error("MkdirAll under a file: valeurObtenue nil, valeurAttendue a refusal")
	}
	if err := root.MkdirAll("file", 0o755); err == nil {
		t.Error("MkdirAll on a file: valeurObtenue nil, valeurAttendue a refusal")
	}
	if err := root.MkdirAll("dir-link", 0o755); !errors.Is(err, fsys.ErrUnconfined) {
		t.Errorf("MkdirAll on a symlink: valeurObtenue %v, valeurAttendue ErrUnconfined", err)
	}
	for attempt := range 2 {
		if err := root.MkdirAll("a/b", 0o755); err != nil {
			t.Errorf("MkdirAll attempt %d: %v", attempt, err)
		}
	}
	if entries := outsideEntries(t, server); len(entries) != 0 {
		t.Errorf("valeurObtenue %q, valeurAttendue nothing outside", entries)
	}
}

func TestSFTPMkdirAll_warnsWhenTheServerCreatesWritableDirectories(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	_, volume := writeVolume(t, sftptest.WithDirectoryMode(0o777))
	root := openLinksRoot(t, volume)

	if err := root.MkdirAll("a/b", 0o755); err != nil {
		t.Fatalf("valeurObtenue %v, valeurAttendue a warning, never a failure", err)
	}

	valeurAttendue := []sftpfs.Warning{
		{Code: sftpfs.CodeLinkDirWorldWritable, Path: linksDir + "/a"},
		{Code: sftpfs.CodeLinkDirWorldWritable, Path: linksDir + "/a/b"},
	}
	if valeurObtenue := volume.Warnings(); !slices.Equal(valeurObtenue, valeurAttendue) {
		t.Errorf("valeurObtenue %+v, valeurAttendue %+v", valeurObtenue, valeurAttendue)
	}
}

func TestSFTPMkdirAll_privateDirectoriesRaiseNoWarning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	_, volume := writeVolume(t, sftptest.WithDirectoryMode(0o755))
	root := openLinksRoot(t, volume)

	if err := root.MkdirAll("a", 0o755); err != nil {
		t.Fatal(err)
	}

	if valeurObtenue := volume.Warnings(); len(valeurObtenue) != 0 {
		t.Errorf("valeurObtenue %+v, valeurAttendue none", valeurObtenue)
	}
}

func TestSFTPCopyFrom_isServerCopyUnavailableWithoutReadingTheSource(t *testing.T) {
	server, volume := writeVolume(t)
	root := openLinksRoot(t, volume)
	source := fsys.Location{Volume: "seedbox", Path: libraryDir + "/" + fsystest.FixtureMovieName}
	before := len(server.Requests())

	copied, err := root.CopyFrom(t.Context(), source, "copy.mkv")

	if !errors.Is(err, fsys.ErrServerCopyUnavailable) || copied != 0 {
		t.Errorf("valeurObtenue %d bytes, %v, valeurAttendue 0 and ErrServerCopyUnavailable", copied, err)
	}
	for _, method := range []string{"Get", "Open", "Put"} {
		if requests := methodsSince(server, before, method); len(requests) != 0 {
			t.Errorf("valeurObtenue %+v, valeurAttendue no %s: never a stream copy through the client", requests, method)
		}
	}
	if _, err := root.Lstat("copy.mkv"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("valeurObtenue %v, valeurAttendue no destination", err)
	}
}

func TestSFTPWriteRoot_lostSessionNeverContinuesOnANewSession(t *testing.T) {
	server, volume := writeVolume(t)
	root := openLinksRoot(t, volume)
	server.DropConnections()
	waitForState(t, volume, sftpfs.StateBroken)
	volume.ResetForStage()
	if _, err := volume.Stat(linksDir); err != nil {
		t.Fatalf("the volume itself reconnects: %v", err)
	}
	before := len(server.Requests())

	writer, err := root.CreateExclusive("file", 0o644)
	if writer != nil {
		_ = writer.Close()
	}

	if !errors.Is(err, fsys.ErrRemoteUnavailable) {
		t.Errorf("valeurObtenue %v, valeurAttendue ErrRemoteUnavailable: a new OpenWriteRoot is required", err)
	}
	if requests := server.Requests()[before:]; len(requests) != 0 {
		t.Errorf("valeurObtenue %+v, valeurAttendue nothing sent on the new session", requests)
	}
}

func TestSFTPOpenWriteRoot_refusesWhatIsNotACanonicalDirectory(t *testing.T) {
	skipWithoutSymlinks(t)
	server, volume := writeVolume(t)
	fsystest.PlantLocalSymlink(t, localPathOf(server, "/links-alias"), linksDir)
	sftptest.WriteFile(t, localPathOf(server, "/plain-file"), []byte("x"), 0o644)

	for _, dir := range []string{"/links-alias", "/plain-file", "links", linksDir + "/", "/library/../links", "/missing"} {
		root, err := volume.OpenWriteRoot(dir)
		if err == nil {
			_ = root.Close()
			t.Errorf("OpenWriteRoot(%q): valeurObtenue a root, valeurAttendue a refusal", dir)
		}
	}
}

func TestSFTPWriteRoot_rootIsTheCanonicalLocationAndExposesNoVolume(t *testing.T) {
	_, volume := writeVolume(t)
	root := openLinksRoot(t, volume)

	if valeurObtenue := root.Root(); valeurObtenue != (fsys.Location{Volume: "seedbox", Path: linksDir}) {
		t.Errorf("valeurObtenue %+v, valeurAttendue sftp://seedbox%s", valeurObtenue, linksDir)
	}
	if _, exposesVolume := root.(interface{ Volume() string }); exposesVolume {
		t.Error("valeurObtenue a write root exposing Volume(), valeurAttendue none")
	}
	if _, isReadFS := root.(fsys.ReadFS); isReadFS {
		t.Error("valeurObtenue a write root usable as ReadFS, valeurAttendue none")
	}
}

func TestSFTPWriteRoot_closedRootRefusesEveryOperation(t *testing.T) {
	server, volume := writeVolume(t)
	root := openLinksRoot(t, volume)
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	before := len(server.Requests())

	for method, err := range callEveryMethod(t, root, "file") {
		if !errors.Is(err, fs.ErrClosed) {
			t.Errorf("%s: valeurObtenue %v, valeurAttendue fs.ErrClosed", method, err)
		}
	}
	if requests := server.Requests()[before:]; len(requests) != 0 {
		t.Errorf("valeurObtenue %+v, valeurAttendue none", requests)
	}
}

func TestSFTPCapabilities_followTheAdvertisedExtensions(t *testing.T) {
	_, volume := writeVolume(t)

	capabilities, err := volume.Capabilities(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	isExpected := capabilities.Symlink && capabilities.Hardlink && capabilities.AtomicReplace &&
		!capabilities.ServerCopy && !capabilities.Reflink && !capabilities.FileIdentity && !capabilities.RemoteStat
	if !isExpected {
		t.Errorf("valeurObtenue %+v, valeurAttendue symlink, hardlink and atomic replace only", capabilities)
	}
}

func TestRegistry_readOfTheSFTPVolumeIsTheSealedView(t *testing.T) {
	_, volume := writeVolume(t)
	registry, err := fsys.NewRegistry(map[string]fsys.Volume{"seedbox": volume}, []fsys.Location{{Volume: "seedbox", Path: libraryDir}})
	if err != nil {
		t.Fatal(err)
	}

	view, _, err := registry.Read(fsys.Location{Volume: "seedbox", Path: libraryDir})
	if err != nil {
		t.Fatal(err)
	}
	if _, isVolume := view.(fsys.Volume); isVolume {
		t.Error("valeurObtenue a view usable as fsys.Volume, valeurAttendue the sealed read-only view")
	}
	if _, opensRoots := view.(interface {
		OpenWriteRoot(string) (fsys.WriteRoot, error)
	}); opensRoots {
		t.Error("valeurObtenue a view with OpenWriteRoot, valeurAttendue none")
	}
	if _, err := registry.WriteRoot(fsys.Location{Volume: "seedbox", Path: libraryDir}); !errors.Is(err, fsys.ErrWriteInLibrary) {
		t.Errorf("valeurObtenue %v, valeurAttendue ErrWriteInLibrary for the library itself", err)
	}
}

func TestRecorder_wrapsSFTPVolume(t *testing.T) {
	_, volume := writeVolume(t)
	recorder := fsystest.NewRecorder(volume)
	root, err := recorder.OpenWriteRoot(linksDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	if err := root.MkdirAll("dir", 0o755); err != nil {
		t.Fatal(err)
	}

	recorder.AssertNoWriteUnder(t, libraryDir)
	if writes := recorder.WriteCalls(); len(writes) == 0 {
		t.Error("valeurObtenue no recorded write, valeurAttendue the MkdirAll")
	}
}

func readAll(t *testing.T, file fsys.File) string {
	t.Helper()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(io.NewSectionReader(file, 0, info.Size()))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func TestSFTPWriteRoot_namesContainingTransportTextNeverBreakTheVolume(t *testing.T) {
	skipWithoutSymlinks(t)
	server, volume := writeVolume(t)
	root := openLinksRoot(t, volume)
	fsystest.PlantLocalSymlink(t, localPathOf(server, linksDir+"/x connection lost"), libraryDir)

	if _, err := root.Lstat("x connection lost/entry"); !errors.Is(err, fsys.ErrUnconfined) || errors.Is(err, fsys.ErrRemoteUnavailable) {
		t.Errorf("Lstat: valeurObtenue %v, valeurAttendue ErrUnconfined only", err)
	}
	if err := root.Remove("failed to send packet.mkv"); !errors.Is(err, fs.ErrNotExist) || errors.Is(err, fsys.ErrRemoteUnavailable) {
		t.Errorf("Remove: valeurObtenue %v, valeurAttendue fs.ErrNotExist only", err)
	}

	if valeurObtenue := volume.State(); valeurObtenue != sftpfs.StateOK {
		t.Errorf("valeurObtenue %q, valeurAttendue the volume kept ok", valeurObtenue)
	}
}

func TestSFTPMkdirAll_serverMessageNamingAConnectionLossKeepsTheVolume(t *testing.T) {
	var server *sftptest.Server
	var once sync.Once
	// Juste avant le Mkdir, un fichier prend le nom : le serveur répond SSH_FX_FAILURE avec un texte qui le cite.
	hook := func(request sftptest.Request) {
		once.Do(func() {
			sftptest.WriteFile(t, localPathOf(server, request.Path), []byte("x"), 0o644)
		})
	}
	server, volume := writeVolume(t, sftptest.OnRequest("Mkdir", hook))
	root := openLinksRoot(t, volume)

	err := root.MkdirAll("connection lost", 0o755)

	if err == nil || errors.Is(err, fsys.ErrRemoteUnavailable) {
		t.Errorf("valeurObtenue %v, valeurAttendue a refusal that is not a lost connection", err)
	}
	if valeurObtenue := volume.State(); valeurObtenue != sftpfs.StateOK {
		t.Errorf("valeurObtenue %q, valeurAttendue the volume kept ok", valeurObtenue)
	}
}
