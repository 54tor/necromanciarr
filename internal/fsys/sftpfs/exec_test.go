package sftpfs_test

import (
	"bytes"
	"context"
	"crypto/md5"  //nolint:gosec // empreinte attendue de md5sum, jamais une protection
	"crypto/sha1" //nolint:gosec // empreinte attendue de sha1sum, jamais une protection
	"encoding/hex"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"necromanciarr/internal/config"
	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/sftpfs"
	"necromanciarr/internal/fsys/sftpfs/sftptest"
	"necromanciarr/internal/fsys/sftpfs/shellquote"
)

const (
	execLibrary = "/library"
	// fakeDevice : numéro de périphérique rendu par le faux stat distant.
	fakeDevice = 2049
	// fakeFirstInode : inode du premier chemin d'un lot ; les suivants le suivent.
	fakeFirstInode = 100
	// commandNotFound : code de sortie d'un shell pour un programme absent.
	commandNotFound = 127
)

// hostileNames sont des noms de fichiers de tiers : aucun ne doit jamais atteindre une ligne de commande en lots.
var hostileNames = []string{
	"a'b.mkv", "$(id).mkv", "`id`.mkv", "-rf", "new\nline.mkv", "sp ace.mkv", `back\slash.mkv`, "semi;colon.mkv",
	"tab\tname.mkv", "--", "'\\''.mkv",
}

// fakeRemote imite un hôte GNU distant : sondes de version, stat et empreintes calculés sur les fichiers servis.
// intercept, s'il rend handled, remplace la réponse (sorties hostiles, shell non POSIX).
type fakeRemote struct {
	mutex              sync.Mutex
	root               string
	withoutXargs       bool
	withoutLowPriority bool
	intercept          func(command string, stdin []byte) (stdout, stderr []byte, exit int, handled bool)
}

func (remote *fakeRemote) setRoot(root string) {
	remote.mutex.Lock()
	defer remote.mutex.Unlock()
	remote.root = root
}

func (remote *fakeRemote) setIntercept(intercept func(string, []byte) ([]byte, []byte, int, bool)) {
	remote.mutex.Lock()
	defer remote.mutex.Unlock()
	remote.intercept = intercept
}

func (remote *fakeRemote) snapshot() (string, bool, bool, func(string, []byte) ([]byte, []byte, int, bool)) {
	remote.mutex.Lock()
	defer remote.mutex.Unlock()
	return remote.root, remote.withoutXargs, remote.withoutLowPriority, remote.intercept
}

func (remote *fakeRemote) serve(command string, stdin []byte) ([]byte, []byte, int) {
	root, withoutXargs, withoutLowPriority, intercept := remote.snapshot()
	if intercept != nil {
		if stdout, stderr, exit, handled := intercept(command, stdin); handled {
			return stdout, stderr, exit
		}
	}
	versions := map[string]string{
		sftpfs.ProbeLine("stat"):    "stat (GNU coreutils) 9.5\n",
		sftpfs.ProbeLine("sha1sum"): "sha1sum (GNU coreutils) 9.5\n",
		sftpfs.ProbeLine("md5sum"):  "md5sum (GNU coreutils) 9.5\n",
	}
	if !withoutXargs {
		versions[sftpfs.ProbeLine("xargs")] = "xargs (GNU findutils) 4.10.0\n"
	}
	if !withoutLowPriority {
		versions[sftpfs.ProbeLine("nice")] = "nice (GNU coreutils) 9.5\n"
		versions[sftpfs.ProbeLine("ionice")] = "ionice from util-linux 2.40\n"
	}
	if command == shellquote.ShellSemanticsProbe {
		return []byte(shellquote.ShellSemanticsExpected), nil, 0
	}
	if version, known := versions[command]; known {
		return []byte(version), nil, 0
	}
	if paths, id, known := remote.batchPaths(command, stdin); known {
		return remote.answer(root, id, paths)
	}
	if paths, id, known := remote.fallbackPath(command); known {
		return remote.answer(root, id, paths)
	}
	return nil, []byte("sh: command not found"), commandNotFound
}

// batchPaths reconnaît une ligne de la table (avec ou sans préfixe) et lit les chemins NUL de l'entrée standard.
func (remote *fakeRemote) batchPaths(command string, stdin []byte) ([]string, sftpfs.CommandID, bool) {
	for _, id := range []sftpfs.CommandID{sftpfs.CommandStat, sftpfs.CommandSHA1, sftpfs.CommandMD5} {
		if command == sftpfs.CommandLine(id) || (command != "" && command == sftpfs.LowPriorityLine(id)) {
			records := strings.Split(string(stdin), "\x00")
			return records[:len(records)-1], id, true
		}
	}
	return nil, 0, false
}

// fallbackPath reconnaît une ligne « un fichier par commande » et défait les guillemets simples de son chemin.
func (remote *fakeRemote) fallbackPath(command string) ([]string, sftpfs.CommandID, bool) {
	for _, id := range []sftpfs.CommandID{sftpfs.CommandStat, sftpfs.CommandSHA1, sftpfs.CommandMD5} {
		emptyLine, err := sftpfs.FallbackLine(id, "")
		if err != nil {
			return nil, 0, false
		}
		prefixes := []string{strings.TrimSuffix(emptyLine, "''")}
		if lowPriority, err := shellquote.Join(sftpfs.LowPriorityPrefix()); err == nil && id != sftpfs.CommandStat {
			prefixes = append(prefixes, lowPriority+" "+prefixes[0])
		}
		for _, prefix := range prefixes {
			quoted, found := strings.CutPrefix(command, prefix)
			if found && len(quoted) >= 2 && strings.HasPrefix(quoted, "'") && strings.HasSuffix(quoted, "'") {
				return []string{strings.ReplaceAll(quoted[1:len(quoted)-1], `'\''`, "'")}, id, true
			}
		}
	}
	return nil, 0, false
}

func (remote *fakeRemote) answer(root string, id sftpfs.CommandID, paths []string) ([]byte, []byte, int) {
	var stdout, stderr bytes.Buffer
	exit := 0
	for pathIndex, remotePath := range paths {
		hostPath := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(remotePath, "/")))
		info, err := os.Lstat(hostPath)
		if err != nil {
			fmt.Fprintf(&stderr, "cannot stat '%s'\n", remotePath)
			exit = 123
			continue
		}
		switch id {
		case sftpfs.CommandStat:
			fmt.Fprintf(&stdout, "%d\t%d\t%d\t%d\t%d\x00", fakeDevice, fakeFirstInode+pathIndex, 1, info.Size(), info.ModTime().Unix())
		default:
			content, err := os.ReadFile(hostPath)
			if err != nil {
				exit = 123
				continue
			}
			fmt.Fprintf(&stdout, "%s  %s\x00", hashOf(id, content), remotePath)
		}
	}
	return stdout.Bytes(), stderr.Bytes(), exit
}

func hashOf(id sftpfs.CommandID, content []byte) string {
	if id == sftpfs.CommandMD5 {
		sum := md5.Sum(content) //nolint:gosec // empreinte attendue de md5sum
		return hex.EncodeToString(sum[:])
	}
	sum := sha1.Sum(content) //nolint:gosec // empreinte attendue de sha1sum
	return hex.EncodeToString(sum[:])
}

// execVolume démarre un serveur servi par remote et joint un volume dont la bibliothèque est /library.
func execVolume(t *testing.T, remote *fakeRemote, configure func(*config.FSProvider), options ...sftptest.Option) (*sftptest.Server, *sftpfs.Volume) {
	t.Helper()
	server := sftptest.Start(t, append([]sftptest.Option{sftptest.WithExec(remote.serve)}, options...)...)
	remote.setRoot(server.Root)
	provider := pinnedProvider(t, server)
	if configure != nil {
		configure(&provider)
	}
	deps := testDeps()
	deps.LibraryRoots = []string{execLibrary}
	return server, dialVolume(t, provider, deps)
}

func writeLibraryFile(t *testing.T, server *sftptest.Server, name string, content string) string {
	t.Helper()
	remotePath := execLibrary + "/" + name
	writeServedFile(t, server, remotePath, []byte(content))
	return remotePath
}

func skipWithoutHostileNames(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("noms à saut de ligne, barre oblique inverse et apostrophe impossibles sur le disque hôte Windows")
	}
}

// commandsSent rend les lignes exec reçues par le serveur, sondes comprises.
func commandsSent(server *sftptest.Server) []string {
	sent := []string{}
	for _, recorded := range server.ExecSessions() {
		sent = append(sent, recorded.Command)
	}
	return sent
}

// batchSessions rend les sessions exec qui ne sont pas des sondes.
func batchSessions(server *sftptest.Server) []sftptest.ExecSession {
	probeLines := []string{shellquote.ShellSemanticsProbe}
	for _, program := range []string{"stat", "xargs", "sha1sum", "md5sum", "nice", "ionice"} {
		probeLines = append(probeLines, sftpfs.ProbeLine(program))
	}
	batches := []sftptest.ExecSession{}
	for _, recorded := range server.ExecSessions() {
		if !slices.Contains(probeLines, recorded.Command) {
			batches = append(batches, recorded)
		}
	}
	return batches
}

func nulJoined(paths []string) string { return strings.Join(paths, "\x00") + "\x00" }

func waitUntil(t *testing.T, label string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(settleTimeout)
	for !condition() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !condition() {
		t.Fatalf("%s : condition jamais remplie", label)
	}
}

func disabledReason(err error) string {
	var disabled *sftpfs.ExecDisabledError
	if errors.As(err, &disabled) {
		return disabled.Reason
	}
	return ""
}

func TestExec_onlyAllowListedCommands(t *testing.T) {
	expectedTable := map[sftpfs.CommandID][]string{
		sftpfs.CommandProbe: {"stat", "--version"},
		sftpfs.CommandStat:  {"xargs", "-0", "-r", "stat", "--printf=%d\t%i\t%h\t%s\t%Y\\0", "--"},
		sftpfs.CommandSHA1:  {"xargs", "-0", "-r", "sha1sum", "-z", "--"},
		sftpfs.CommandMD5:   {"xargs", "-0", "-r", "md5sum", "-z", "--"},
	}
	if gotValue := sftpfs.CommandIDs(); !slices.Equal(gotValue, []sftpfs.CommandID{sftpfs.CommandProbe, sftpfs.CommandStat, sftpfs.CommandSHA1, sftpfs.CommandMD5}) {
		t.Fatalf("table : valeurObtenue %v, valeurAttendue exactement les quatre entrées", gotValue)
	}
	for id, expectedArgv := range expectedTable {
		if gotValue := sftpfs.CommandArgv(id); !slices.Equal(gotValue, expectedArgv) {
			t.Errorf("argv %d : valeurObtenue %q, valeurAttendue %q", id, gotValue, expectedArgv)
		}
		expectedLine, err := shellquote.Join(expectedArgv)
		if err != nil || sftpfs.CommandLine(id) != expectedLine {
			t.Errorf("ligne %d : valeurObtenue %q, valeurAttendue %q (%v)", id, sftpfs.CommandLine(id), expectedLine, err)
		}
	}
	for _, id := range []sftpfs.CommandID{sftpfs.CommandSHA1, sftpfs.CommandMD5} {
		expectedLine, err := shellquote.Join(append([]string{"nice", "-n", "10", "ionice", "-c3"}, expectedTable[id]...))
		if err != nil || sftpfs.LowPriorityLine(id) != expectedLine {
			t.Errorf("ligne préfixée %d : valeurObtenue %q, valeurAttendue %q", id, sftpfs.LowPriorityLine(id), expectedLine)
		}
	}
	if sftpfs.LowPriorityLine(sftpfs.CommandStat) != "" {
		t.Errorf("valeurAttendue aucun préfixe pour stat")
	}
	assertNoExportedWayToAddACommand(t)
}

// assertNoExportedWayToAddACommand lit les fichiers de production du paquet : les seuls identifiants exportés qui
// parlent de commande sont le type et les quatre constantes, et toute variable exportée est une erreur sentinelle.
func assertNoExportedWayToAddACommand(t *testing.T) {
	t.Helper()
	allowed := []string{"CommandID", "CommandProbe", "CommandStat", "CommandSHA1", "CommandMD5"}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		fileName := entry.Name()
		if !strings.HasSuffix(fileName, ".go") || strings.HasSuffix(fileName, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), fileName, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range file.Decls {
			for _, name := range exportedNames(declaration) {
				mentionsCommand := strings.Contains(strings.ToLower(name.identifier), "command")
				if mentionsCommand && !slices.Contains(allowed, name.identifier) {
					t.Errorf("%s : identifiant exporté %q hors de la table fermée", fileName, name.identifier)
				}
				if name.isVariable && !strings.HasPrefix(name.identifier, "Err") {
					t.Errorf("%s : variable exportée %q (seules les erreurs sentinelles le sont)", fileName, name.identifier)
				}
			}
		}
	}
}

type exportedName struct {
	identifier string
	isVariable bool
}

func exportedNames(declaration ast.Decl) []exportedName {
	var names []exportedName
	switch typed := declaration.(type) {
	case *ast.FuncDecl:
		if typed.Name.IsExported() {
			names = append(names, exportedName{identifier: typed.Name.Name})
		}
	case *ast.GenDecl:
		for _, spec := range typed.Specs {
			switch specTyped := spec.(type) {
			case *ast.ValueSpec:
				for _, valueName := range specTyped.Names {
					if valueName.IsExported() {
						names = append(names, exportedName{identifier: valueName.Name, isVariable: typed.Tok == token.VAR})
					}
				}
			case *ast.TypeSpec:
				if specTyped.Name.IsExported() {
					names = append(names, exportedName{identifier: specTyped.Name.Name})
				}
			}
		}
	}
	return names
}

func TestExec_refusesUnknownCommandWithoutSendingAnything(t *testing.T) {
	server, volume := execVolume(t, &fakeRemote{}, nil)
	requestsBefore := len(server.Requests())

	_, err := volume.Exec(context.Background(), sftpfs.CommandID(99), []string{execLibrary + "/a"})

	if err == nil || len(server.Requests()) != requestsBefore {
		t.Fatalf("valeurObtenue err %v, requêtes %d -> %d", err, requestsBefore, len(server.Requests()))
	}
}

func TestExec_batchCommandLineIsConstant(t *testing.T) {
	skipWithoutHostileNames(t)
	server, volume := execVolume(t, &fakeRemote{}, nil)
	var hostilePaths []string
	for _, name := range hostileNames {
		hostilePaths = append(hostilePaths, writeLibraryFile(t, server, name, "content of "+name))
	}
	halves := [][]string{hostilePaths[:len(hostilePaths)/2], hostilePaths[len(hostilePaths)/2:]}

	for _, half := range halves {
		if _, err := volume.Exec(context.Background(), sftpfs.CommandStat, half); err != nil {
			t.Fatalf("exec stat : %v", err)
		}
		if _, err := volume.Exec(context.Background(), sftpfs.CommandSHA1, half); err != nil {
			t.Fatalf("exec sha1 : %v", err)
		}
	}

	batches := batchSessions(server)
	expectedLines := []string{sftpfs.CommandLine(sftpfs.CommandStat), sftpfs.LowPriorityLine(sftpfs.CommandSHA1)}
	if len(batches) != 4 {
		t.Fatalf("valeurObtenue %d sessions de lots, valeurAttendue 4", len(batches))
	}
	for batchIndex, recorded := range batches {
		if valeurAttendue := expectedLines[batchIndex%2]; recorded.Command != valeurAttendue {
			t.Errorf("lot %d : valeurObtenue %q, valeurAttendue %q", batchIndex, recorded.Command, valeurAttendue)
		}
		if valeurAttendue := nulJoined(halves[batchIndex/2]); string(recorded.Stdin) != valeurAttendue {
			t.Errorf("lot %d : entrée standard %q, valeurAttendue %q", batchIndex, recorded.Stdin, valeurAttendue)
		}
	}
	for _, command := range commandsSent(server) {
		for _, name := range hostileNames {
			if strings.Contains(command, name) && name != "--" {
				t.Errorf("la ligne %q contient le nom %q", command, name)
			}
		}
	}
}

func TestExec_refusesPathOutsideRoots(t *testing.T) {
	server, volume := execVolume(t, &fakeRemote{}, nil)
	accepted := writeLibraryFile(t, server, "ok.mkv", "x")
	refused := []string{
		"/elsewhere/file.mkv", "relative/file.mkv", "", execLibrary + "/../etc/passwd", execLibrary + "/./ok.mkv",
		execLibrary + "//ok.mkv", execLibrary + "/ok.mkv/", execLibrary + "x/file.mkv", execLibrary + "/a\x00b",
		execLibrary + "/" + strings.Repeat("a", maxNameBytes), "/", "/libr",
	}
	requestsBefore := len(server.Requests())

	for _, refusedPath := range refused {
		for _, id := range []sftpfs.CommandID{sftpfs.CommandStat, sftpfs.CommandSHA1, sftpfs.CommandMD5} {
			results, err := volume.Exec(context.Background(), id, []string{accepted, refusedPath})
			if !errors.Is(err, sftpfs.ErrExecPathOutsideRoots) || results != nil {
				t.Errorf("%q : valeurObtenue %v, %v ; valeurAttendue ErrExecPathOutsideRoots", refusedPath, results, err)
			}
			if strings.Contains(fmt.Sprint(err), "passwd") {
				t.Errorf("l'erreur cite le chemin refusé : %v", err)
			}
		}
	}
	if requestsAfter := len(server.Requests()); requestsAfter != requestsBefore || len(server.ExecSessions()) != 0 {
		t.Fatalf("valeurAttendue rien d'envoyé, valeurObtenue requêtes %d -> %d, exec %v", requestsBefore, requestsAfter, commandsSent(server))
	}
}

func TestExec_admitsWriteRootsAndAllowedRoots(t *testing.T) {
	server, volume := execVolume(t, &fakeRemote{}, nil)
	torrentFile := "/torrents/file.mkv"
	writeServedFile(t, server, torrentFile, []byte("torrent"))
	linkFile := "/links/file.mkv"
	writeServedFile(t, server, linkFile, []byte("link"))

	if _, err := volume.Exec(context.Background(), sftpfs.CommandStat, []string{torrentFile}); !errors.Is(err, sftpfs.ErrExecPathOutsideRoots) {
		t.Fatalf("avant AllowExecRoots : valeurObtenue %v", err)
	}
	if err := volume.AllowExecRoots("/torrents/../etc"); err == nil {
		t.Fatal("valeurAttendue le refus d'une racine non canonique")
	}
	if err := volume.AllowExecRoots("/torrents"); err != nil {
		t.Fatal(err)
	}
	root := openLinksRoot(t, volume)
	defer func() { _ = root.Close() }()

	results, err := volume.Exec(context.Background(), sftpfs.CommandStat, []string{torrentFile, linkFile})
	if err != nil || len(results) != 2 || results[0].Size != int64(len("torrent")) || results[1].Size != int64(len("link")) {
		t.Fatalf("valeurObtenue %+v, %v", results, err)
	}
}

func TestExec_parsesNULRecordsWithNewlineInName(t *testing.T) {
	skipWithoutHostileNames(t)
	server, volume := execVolume(t, &fakeRemote{}, nil)
	newline := writeLibraryFile(t, server, "two\nlines.mkv", "first content")
	plain := writeLibraryFile(t, server, "plain.mkv", "second")
	paths := []string{newline, plain}

	stats, err := volume.Exec(context.Background(), sftpfs.CommandStat, paths)
	if err != nil {
		t.Fatal(err)
	}
	hashes, err := volume.Exec(context.Background(), sftpfs.CommandSHA1, paths)
	if err != nil {
		t.Fatal(err)
	}

	contents := []string{"first content", "second"}
	for pathIndex, remotePath := range paths {
		info, err := os.Stat(filepath.Join(server.Root, filepath.FromSlash(strings.TrimPrefix(remotePath, "/"))))
		if err != nil {
			t.Fatal(err)
		}
		valeurAttendue := sftpfs.ExecResult{
			Path: remotePath, Device: fakeDevice, Inode: uint64(fakeFirstInode + pathIndex), Links: 1,
			Size: int64(len(contents[pathIndex])), ModTime: time.Unix(info.ModTime().Unix(), 0),
		}
		if valeurObtenue := stats[pathIndex]; valeurObtenue != valeurAttendue {
			t.Errorf("stat %d : valeurObtenue %+v, valeurAttendue %+v", pathIndex, valeurObtenue, valeurAttendue)
		}
		expectedHash := hashOf(sftpfs.CommandSHA1, []byte(contents[pathIndex]))
		if hashes[pathIndex].Path != remotePath || hashes[pathIndex].Hash != expectedHash || hashes[pathIndex].Err != nil {
			t.Errorf("sha1 %d : valeurObtenue %+v, valeurAttendue %s", pathIndex, hashes[pathIndex], expectedHash)
		}
	}
}

func TestExec_returnsRemoteHashesWithLowPriorityPrefix(t *testing.T) {
	server, volume := execVolume(t, &fakeRemote{}, nil)
	remotePath := writeLibraryFile(t, server, "movie.mkv", "movie bytes")

	for _, id := range []sftpfs.CommandID{sftpfs.CommandSHA1, sftpfs.CommandMD5} {
		results, err := volume.Exec(context.Background(), id, []string{remotePath})
		if valeurAttendue := hashOf(id, []byte("movie bytes")); err != nil || len(results) != 1 || results[0].Hash != valeurAttendue {
			t.Fatalf("%d : valeurObtenue %+v, %v ; valeurAttendue %s", id, results, err, valeurAttendue)
		}
	}
	batches := batchSessions(server)
	if len(batches) != 2 || batches[0].Command != sftpfs.LowPriorityLine(sftpfs.CommandSHA1) || batches[1].Command != sftpfs.LowPriorityLine(sftpfs.CommandMD5) {
		t.Fatalf("valeurObtenue %+v", batches)
	}
}

func TestExec_withoutNiceAndIoniceSendsThePlainLine(t *testing.T) {
	server, volume := execVolume(t, &fakeRemote{withoutLowPriority: true}, nil)
	remotePath := writeLibraryFile(t, server, "movie.mkv", "movie bytes")

	if _, err := volume.Exec(context.Background(), sftpfs.CommandSHA1, []string{remotePath}); err != nil {
		t.Fatal(err)
	}

	if batches := batchSessions(server); len(batches) != 1 || batches[0].Command != sftpfs.CommandLine(sftpfs.CommandSHA1) {
		t.Fatalf("valeurObtenue %+v", batches)
	}
}

func TestExec_rejectsOversizedOutput(t *testing.T) {
	remote := &fakeRemote{}
	server, volume := execVolume(t, remote, nil)
	remotePath := writeLibraryFile(t, server, "movie.mkv", "movie bytes")
	hashCap := len(remotePath) + 128
	oversized := map[string]int{
		sftpfs.CommandLine(sftpfs.CommandStat):     64<<10 + 1,
		sftpfs.LowPriorityLine(sftpfs.CommandSHA1): hashCap + 1,
	}
	remote.setIntercept(func(command string, _ []byte) ([]byte, []byte, int, bool) {
		size, known := oversized[command]
		return bytes.Repeat([]byte("1"), size), nil, 0, known
	})

	for _, id := range []sftpfs.CommandID{sftpfs.CommandStat, sftpfs.CommandSHA1} {
		results, err := volume.Exec(context.Background(), id, []string{remotePath})
		if !errors.Is(err, sftpfs.ErrExecOutputTooLarge) || results != nil {
			t.Fatalf("%d : valeurObtenue %v, %v ; valeurAttendue ErrExecOutputTooLarge", id, results, err)
		}
	}
	waitUntil(t, "sessions fermées", func() bool {
		for _, recorded := range batchSessions(server) {
			if !recorded.Ended {
				return false
			}
		}
		return true
	})
	if volume.State() != sftpfs.StateOK {
		t.Fatalf("une sortie démesurée ne casse pas la connexion : état %q", volume.State())
	}
}

func TestExec_acceptsOutputAtTheHashCap(t *testing.T) {
	remote := &fakeRemote{}
	server, volume := execVolume(t, remote, nil)
	remotePath := writeLibraryFile(t, server, "movie.mkv", "movie bytes")
	record := hashOf(sftpfs.CommandSHA1, []byte("movie bytes")) + "  " + remotePath + "\x00"
	remote.setIntercept(func(command string, _ []byte) ([]byte, []byte, int, bool) {
		return []byte(record), nil, 0, command == sftpfs.LowPriorityLine(sftpfs.CommandSHA1)
	})

	if _, err := volume.Exec(context.Background(), sftpfs.CommandSHA1, []string{remotePath}); err != nil {
		t.Fatalf("un enregistrement sous le plafond est accepté : %v", err)
	}
}

func TestExec_killsSessionOnTimeout(t *testing.T) {
	sftpfs.SetStatExecTimeout(t, 300*time.Millisecond)
	server, volume := execVolume(t, &fakeRemote{}, func(provider *config.FSProvider) {
		provider.ExecHashTimeout = 300 * time.Millisecond
	}, sftptest.StallExec(sftpfs.CommandLine(sftpfs.CommandStat)), sftptest.StallExec(sftpfs.LowPriorityLine(sftpfs.CommandSHA1)),
		sftptest.StallExec(sftpfs.LowPriorityLine(sftpfs.CommandMD5)))
	remotePath := writeLibraryFile(t, server, "movie.mkv", "movie bytes")
	if _, err := volume.Exec(context.Background(), sftpfs.CommandProbe, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	goroutinesBefore := runtime.NumGoroutine()

	_, statErr := volume.Exec(context.Background(), sftpfs.CommandStat, []string{remotePath})
	_, hashErr := volume.Exec(context.Background(), sftpfs.CommandSHA1, []string{remotePath})
	cancelled, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	_, cancelErr := volume.Exec(cancelled, sftpfs.CommandMD5, []string{remotePath})

	if !errors.Is(statErr, sftpfs.ErrExecTimeout) || !errors.Is(hashErr, sftpfs.ErrExecTimeout) || !errors.Is(cancelErr, context.Canceled) {
		t.Fatalf("valeurObtenue %v / %v / %v", statErr, hashErr, cancelErr)
	}
	batches := batchSessions(server)
	if len(batches) != 3 {
		t.Fatalf("valeurObtenue %+v", batches)
	}
	waitUntil(t, "sessions distantes terminées", func() bool {
		for _, recorded := range batchSessions(server) {
			if !recorded.Ended || !slices.Contains(recorded.Signals, "KILL") {
				return false
			}
		}
		return true
	})
	waitUntil(t, "goroutines revenues", func() bool { return runtime.NumGoroutine() <= goroutinesBefore })
	if _, err := volume.Stat(remotePath); err != nil || volume.State() != sftpfs.StateOK {
		t.Fatalf("la connexion reste vivante : %v, état %q", err, volume.State())
	}
}

func TestExec_neverEchoesStderr(t *testing.T) {
	const secret = "private-name.mkv password=hunter2"
	remote := &fakeRemote{}
	server, volume := execVolume(t, remote, nil)
	remotePath := writeLibraryFile(t, server, "movie.mkv", "movie bytes")
	stdout, _, _ := remote.answer(server.Root, sftpfs.CommandStat, []string{remotePath})
	answerStat := func(output []byte, stderr string, exit int) {
		remote.setIntercept(func(command string, _ []byte) ([]byte, []byte, int, bool) {
			return output, []byte(stderr), exit, command == sftpfs.CommandLine(sftpfs.CommandStat)
		})
	}

	answerStat(nil, "stat: "+secret, 1)
	_, failure := volume.Exec(context.Background(), sftpfs.CommandStat, []string{remotePath})
	answerStat(stdout, "warning: "+secret, 0)
	results, success := volume.Exec(context.Background(), sftpfs.CommandStat, []string{remotePath})

	if !errors.Is(failure, sftpfs.ErrExecFailed) || success != nil || len(results) != 1 {
		t.Fatalf("valeurObtenue %v / %+v, %v", failure, results, success)
	}
	for _, rendered := range []string{fmt.Sprintf("%v %+v", failure, failure), fmt.Sprintf("%v %+v", results, results)} {
		if strings.Contains(rendered, "private-name") || strings.Contains(rendered, "hunter2") {
			t.Fatalf("la sortie d'erreur distante est recopiée : %s", rendered)
		}
	}
}

func TestExec_splitsIntoBatchesOf64(t *testing.T) {
	server, volume := execVolume(t, &fakeRemote{}, nil)
	paths := make([]string, 130)
	for pathIndex := range paths {
		paths[pathIndex] = writeLibraryFile(t, server, fmt.Sprintf("file-%03d.mkv", pathIndex), strings.Repeat("x", pathIndex))
	}

	results, err := volume.Exec(context.Background(), sftpfs.CommandStat, paths)

	if err != nil || len(results) != len(paths) {
		t.Fatalf("valeurObtenue %d résultats, %v", len(results), err)
	}
	for pathIndex, result := range results {
		if result.Path != paths[pathIndex] || result.Size != int64(pathIndex) || result.Err != nil {
			t.Fatalf("résultat %d : valeurObtenue %+v", pathIndex, result)
		}
	}
	batches := batchSessions(server)
	expectedBatches := [][]string{paths[:64], paths[64:128], paths[128:]}
	if len(batches) != len(expectedBatches) {
		t.Fatalf("valeurObtenue %d lots, valeurAttendue 3", len(batches))
	}
	for batchIndex, recorded := range batches {
		if string(recorded.Stdin) != nulJoined(expectedBatches[batchIndex]) {
			t.Errorf("lot %d : entrée standard inattendue (%d octets)", batchIndex, len(recorded.Stdin))
		}
	}
}

func TestExec_rejectsStatSizeMismatch(t *testing.T) {
	remote := &fakeRemote{}
	server, volume := execVolume(t, remote, nil)
	first := writeLibraryFile(t, server, "first.mkv", "12345")
	second := writeLibraryFile(t, server, "second.mkv", "abc")
	remote.setIntercept(func(command string, _ []byte) ([]byte, []byte, int, bool) {
		lying := "1\t2\t1\t5\t1700000000\x001\t3\t1\t4\t1700000000\x00"
		return []byte(lying), nil, 0, command == sftpfs.CommandLine(sftpfs.CommandStat)
	})

	results, err := volume.Exec(context.Background(), sftpfs.CommandStat, []string{first, second})

	if err != nil || len(results) != 2 {
		t.Fatalf("valeurObtenue %+v, %v", results, err)
	}
	if results[0].Err != nil || !errors.Is(results[1].Err, sftpfs.ErrExecSizeMismatch) {
		t.Fatalf("valeurObtenue %v / %v, valeurAttendue nil / ErrExecSizeMismatch", results[0].Err, results[1].Err)
	}
}

func TestExec_rejectsMalformedStatRecords(t *testing.T) {
	remote := &fakeRemote{}
	server, volume := execVolume(t, remote, nil)
	first := writeLibraryFile(t, server, "first.mkv", "12345")
	second := writeLibraryFile(t, server, "second.mkv", "abc")
	valid := "1\t2\t1\t5\t1700000000\x00"
	malformed := map[string]string{
		"enregistrement court":        valid,
		"enregistrement en trop":      valid + "1\t3\t1\t3\t1700000000\x00" + valid,
		"sans NUL final":              valid + "1\t3\t1\t3\t1700000000",
		"quatre champs":               valid + "1\t3\t1\t3\x00",
		"six champs":                  valid + "1\t3\t1\t3\t1700000000\t9\x00",
		"champ non numérique":         valid + "1\t3\t1\t3\t17e8\x00",
		"signe":                       valid + "1\t3\t1\t+3\t1700000000\x00",
		"champ vide":                  valid + "1\t\t1\t3\t1700000000\x00",
		"aucun lien":                  valid + "1\t3\t0\t3\t1700000000\x00",
		"inode hors uint64":           valid + "1\t18446744073709551616\t1\t3\t1700000000\x00",
		"taille hors int64":           valid + "1\t3\t1\t9223372036854775808\t1700000000\x00",
		"date absurde":                valid + "1\t3\t1\t3\t99999999999999\x00",
		"saut de ligne dans un champ": valid + "1\t3\t1\t3\n\t1700000000\x00",
	}
	for label, output := range malformed {
		remote.setIntercept(func(command string, _ []byte) ([]byte, []byte, int, bool) {
			return []byte(output), nil, 0, command == sftpfs.CommandLine(sftpfs.CommandStat)
		})
		results, err := volume.Exec(context.Background(), sftpfs.CommandStat, []string{first, second})
		if !errors.Is(err, sftpfs.ErrExecMalformedOutput) || results != nil {
			t.Errorf("%s : valeurObtenue %+v, %v ; valeurAttendue ErrExecMalformedOutput", label, results, err)
		}
	}
}

func TestExec_rejectsHashPathMismatch(t *testing.T) {
	remote := &fakeRemote{}
	server, volume := execVolume(t, remote, nil)
	// Deux chemins de même longueur : seul le contenu des octets distingue l'ordre et le chemin.
	first := writeLibraryFile(t, server, "aaaa.mkv", "one")
	second := writeLibraryFile(t, server, "bbbb.mkv", "two")
	firstHash, secondHash := hashOf(sftpfs.CommandSHA1, []byte("one")), hashOf(sftpfs.CommandSHA1, []byte("two"))
	sameLengthPath := strings.Replace(second, "bbbb", "bbbc", 1)
	malformed := map[string]string{
		"chemin de même longueur": firstHash + "  " + first + "\x00" + secondHash + "  " + sameLengthPath + "\x00",
		"ordre inversé":           secondHash + "  " + second + "\x00" + firstHash + "  " + first + "\x00",
		"chemin différent":        firstHash + "  " + first + "\x00" + secondHash + "  " + second + "x\x00",
		"un seul espace":          firstHash + "  " + first + "\x00" + secondHash + " " + second + "\x00",
		"empreinte courte":        firstHash + "  " + first + "\x00" + secondHash[:39] + "  " + second + "\x00",
		"majuscules":              firstHash + "  " + first + "\x00" + strings.ToUpper(secondHash) + "  " + second + "\x00",
		"échappement GNU":         firstHash + "  " + first + "\x00\\" + secondHash + "  " + second + "\x00",
		"enregistrement court":    firstHash + "  " + first + "\x00",
		"sans NUL final":          firstHash + "  " + first + "\x00" + secondHash + "  " + second,
		"empreinte md5":           firstHash + "  " + first + "\x00" + secondHash[:32] + "  " + second + "\x00",
	}
	for label, output := range malformed {
		remote.setIntercept(func(command string, _ []byte) ([]byte, []byte, int, bool) {
			return []byte(output), nil, 0, command == sftpfs.LowPriorityLine(sftpfs.CommandSHA1)
		})
		results, err := volume.Exec(context.Background(), sftpfs.CommandSHA1, []string{first, second})
		if !errors.Is(err, sftpfs.ErrExecMalformedOutput) || results != nil {
			t.Errorf("%s : valeurObtenue %+v, %v ; valeurAttendue ErrExecMalformedOutput", label, results, err)
		}
	}
}

func TestExec_perFileFallbackQuotesPath(t *testing.T) {
	skipWithoutHostileNames(t)
	server, volume := execVolume(t, &fakeRemote{withoutXargs: true}, nil)
	var paths []string
	for _, name := range hostileNames {
		paths = append(paths, writeLibraryFile(t, server, name, "content of "+name))
	}

	stats, statErr := volume.Exec(context.Background(), sftpfs.CommandStat, paths)
	hashes, hashErr := volume.Exec(context.Background(), sftpfs.CommandMD5, paths[:2])

	if statErr != nil || hashErr != nil || len(stats) != len(paths) || len(hashes) != 2 {
		t.Fatalf("valeurObtenue %v / %v", statErr, hashErr)
	}
	batches := batchSessions(server)
	if len(batches) != len(paths)+2 {
		t.Fatalf("valeurObtenue %d sessions, valeurAttendue une par fichier", len(batches))
	}
	statArgv := sftpfs.CommandArgv(sftpfs.CommandStat)[3:]
	md5Argv := append(sftpfs.LowPriorityPrefix(), sftpfs.CommandArgv(sftpfs.CommandMD5)[3:]...)
	for sessionIndex, recorded := range batches {
		argv, remotePath := statArgv, ""
		if sessionIndex < len(paths) {
			remotePath = paths[sessionIndex]
		} else {
			argv, remotePath = md5Argv, paths[sessionIndex-len(paths)]
		}
		quotedPath, err := shellquote.Quote(remotePath)
		if err != nil {
			t.Fatal(err)
		}
		prefix, err := shellquote.Join(argv)
		if err != nil {
			t.Fatal(err)
		}
		if valeurAttendue := prefix + " " + quotedPath; recorded.Command != valeurAttendue || len(recorded.Stdin) != 0 {
			t.Errorf("session %d : valeurObtenue %q, valeurAttendue %q", sessionIndex, recorded.Command, valeurAttendue)
		}
		if !strings.HasSuffix(prefix, "'--'") || !strings.HasPrefix(quotedPath, "'/") {
			t.Errorf("session %d : -- et chemin absolu exigés : %q", sessionIndex, recorded.Command)
		}
	}
	for pathIndex, remotePath := range paths {
		if stats[pathIndex].Path != remotePath || stats[pathIndex].Size != int64(len("content of "+hostileNames[pathIndex])) {
			t.Errorf("stat %d : valeurObtenue %+v", pathIndex, stats[pathIndex])
		}
	}
	if hashes[0].Hash != hashOf(sftpfs.CommandMD5, []byte("content of "+hostileNames[0])) {
		t.Errorf("md5 : valeurObtenue %+v", hashes[0])
	}
}

func TestExec_disabledForTheSessionWhenLoginShellIsNotPOSIX(t *testing.T) {
	remote := &fakeRemote{}
	remote.setIntercept(func(command string, _ []byte) ([]byte, []byte, int, bool) {
		// fish lit \' entre apostrophes comme une apostrophe : la sonde ne rend pas la sortie attendue.
		return []byte("a'b'!x"), nil, 0, command == shellquote.ShellSemanticsProbe
	})
	server, volume := execVolume(t, remote, nil)
	remotePath := writeLibraryFile(t, server, "movie.mkv", "movie bytes")

	for _, id := range []sftpfs.CommandID{sftpfs.CommandStat, sftpfs.CommandSHA1, sftpfs.CommandProbe, sftpfs.CommandStat} {
		paths := []string{remotePath}
		if id == sftpfs.CommandProbe {
			paths = nil
		}
		_, err := volume.Exec(context.Background(), id, paths)
		if !errors.Is(err, sftpfs.ErrExecDisabled) || disabledReason(err) != "login_shell_not_posix" {
			t.Fatalf("%d : valeurObtenue %v, valeurAttendue ErrExecDisabled login_shell_not_posix", id, err)
		}
	}
	if sent := commandsSent(server); !slices.Equal(sent, []string{shellquote.ShellSemanticsProbe}) {
		t.Fatalf("valeurObtenue %q, valeurAttendue la seule sonde du shell", sent)
	}
	capabilities, err := volume.Capabilities(context.Background())
	if err != nil || capabilities.Reasons["remoteExec"] != "login_shell_not_posix" || capabilities.RemoteStat {
		t.Fatalf("valeurObtenue %+v, %v", capabilities, err)
	}
}

func TestExec_disabledWhenStatIsNotGNU(t *testing.T) {
	remote := &fakeRemote{}
	remote.setIntercept(func(command string, _ []byte) ([]byte, []byte, int, bool) {
		return []byte("BusyBox v1.36.1 multi-call binary.\n"), nil, 1, command == sftpfs.ProbeLine("stat")
	})
	server, volume := execVolume(t, remote, nil)
	remotePath := writeLibraryFile(t, server, "movie.mkv", "movie bytes")

	_, err := volume.Exec(context.Background(), sftpfs.CommandStat, []string{remotePath})

	if disabledReason(err) != "exec_probe_failed" || len(batchSessions(server)) != 0 {
		t.Fatalf("valeurObtenue %v, lots %+v", err, batchSessions(server))
	}
}

func TestExec_offNeverSendsAnything(t *testing.T) {
	server, volume := execVolume(t, &fakeRemote{}, func(provider *config.FSProvider) { provider.Exec = config.SFTPExecOff })
	remotePath := writeLibraryFile(t, server, "movie.mkv", "movie bytes")

	_, err := volume.Exec(context.Background(), sftpfs.CommandStat, []string{remotePath})
	status, probeErr := volume.ProbeExec(context.Background())

	if disabledReason(err) != "exec_off" || probeErr != nil || status.Enabled || status.Reason != "exec_off" {
		t.Fatalf("valeurObtenue %v / %+v, %v", err, status, probeErr)
	}
	if len(server.ExecSessions()) != 0 || slices.ContainsFunc(server.Requests(), func(request sftptest.Request) bool { return request.Method == "Exec" }) {
		t.Fatalf("valeurAttendue aucun exec, valeurObtenue %v", commandsSent(server))
	}
}

func TestExec_deniedFallsBackToSFTPOnlyWithReason(t *testing.T) {
	server, volume := execVolume(t, &fakeRemote{}, nil, sftptest.DenyExec())
	remotePath := writeLibraryFile(t, server, "movie.mkv", "movie bytes")

	_, first := volume.Exec(context.Background(), sftpfs.CommandStat, []string{remotePath})
	_, second := volume.Exec(context.Background(), sftpfs.CommandSHA1, []string{remotePath})
	capabilities, err := volume.Capabilities(context.Background())

	if disabledReason(first) != "exec_denied" || disabledReason(second) != "exec_denied" {
		t.Fatalf("valeurObtenue %v / %v", first, second)
	}
	if err != nil || capabilities.Reasons["remoteExec"] != "exec_denied" || volume.State() != sftpfs.StateOK {
		t.Fatalf("valeurObtenue %+v, %v, état %q", capabilities, err, volume.State())
	}
	denied := 0
	for _, request := range server.Requests() {
		if request.Method == "Exec" {
			denied++
		}
	}
	if denied != 1 {
		t.Fatalf("valeurObtenue %d requêtes exec, valeurAttendue la seule sonde (gardée pour la session)", denied)
	}
}

func TestExec_requiredReportsUnavailableAtProbeTime(t *testing.T) {
	_, volume := execVolume(t, &fakeRemote{}, func(provider *config.FSProvider) { provider.Exec = config.SFTPExecRequired }, sftptest.DenyExec())

	status, err := volume.ProbeExec(context.Background())

	if !errors.Is(err, sftpfs.ErrExecRequiredUnavailable) || disabledReason(err) != "exec_denied" || status.Enabled {
		t.Fatalf("valeurObtenue %+v, %v", status, err)
	}
}

func TestExec_probeReportsWhatTheHostOffers(t *testing.T) {
	_, volume := execVolume(t, &fakeRemote{}, nil)

	status, err := volume.ProbeExec(context.Background())

	valeurAttendue := sftpfs.ExecStatus{Enabled: true, BatchMode: true, Hashes: []string{"sha1", "md5"}, LowPriority: true}
	if err != nil || status.Enabled != valeurAttendue.Enabled || status.BatchMode != valeurAttendue.BatchMode ||
		!slices.Equal(status.Hashes, valeurAttendue.Hashes) || status.LowPriority != valeurAttendue.LowPriority || status.Reason != "" {
		t.Fatalf("valeurObtenue %+v, %v ; valeurAttendue %+v", status, err, valeurAttendue)
	}
}

func TestExec_brokenConnectionMidExecIsRemoteUnavailable(t *testing.T) {
	server, volume := execVolume(t, &fakeRemote{}, nil, sftptest.StallExec(sftpfs.CommandLine(sftpfs.CommandStat)))
	remotePath := writeLibraryFile(t, server, "movie.mkv", "movie bytes")
	if _, err := volume.Exec(context.Background(), sftpfs.CommandProbe, nil); err != nil {
		t.Fatal(err)
	}
	go func() {
		deadline := time.Now().Add(settleTimeout)
		for time.Now().Before(deadline) {
			if batches := batchSessions(server); len(batches) == 1 && batches[0].StdinReceived {
				server.DropConnections()
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	results, err := volume.Exec(context.Background(), sftpfs.CommandStat, []string{remotePath})

	if !errors.Is(err, fsys.ErrRemoteUnavailable) || results != nil {
		t.Fatalf("valeurObtenue %+v, %v ; valeurAttendue ErrRemoteUnavailable", results, err)
	}
}
