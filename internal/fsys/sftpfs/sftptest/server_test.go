package sftptest_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"necromanciarr/internal/fsys/sftpfs/sftptest"
)

const (
	clientTimeout = 5 * time.Second
	settleTimeout = 2 * time.Second
)

// knownHostsFile écrit la ligne known_hosts donnée dans un fichier temporaire, comme le ferait l'opérateur.
func knownHostsFile(t *testing.T, line string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func clientSigner(t *testing.T, keyFile string) ssh.Signer {
	t.Helper()
	keyBytes, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

// dial ouvre une connexion SSH qui épingle la clé d'hôte de hostKeyLine et s'authentifie avec keyFile.
func dial(t *testing.T, address, hostKeyLine, keyFile string) (*ssh.Client, error) {
	t.Helper()
	hostKeyCallback, err := knownhosts.New(knownHostsFile(t, hostKeyLine))
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ClientConfig{
		User: "user", Auth: []ssh.AuthMethod{ssh.PublicKeys(clientSigner(t, keyFile))},
		HostKeyCallback: hostKeyCallback, Timeout: clientTimeout,
	}
	return ssh.Dial("tcp", address, config)
}

func connectSSH(t *testing.T, server *sftptest.Server) *ssh.Client {
	t.Helper()
	sshClient, err := dial(t, server.Address, server.HostKeyLine, server.ClientKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sshClient.Close() })
	return sshClient
}

// connect ouvre un client SFTP ; la connexion SSH se ferme avant le client, comme le fera sftpfs sur un délai
// dépassé : client.Close attendrait sinon la réponse d'une requête figée.
func connect(t *testing.T, server *sftptest.Server) *sftp.Client {
	t.Helper()
	sshClient, err := dial(t, server.Address, server.HostKeyLine, server.ClientKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	client, err := sftp.NewClient(sshClient)
	if err != nil {
		_ = sshClient.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = sshClient.Close()
		_ = client.Close()
	})
	return client
}

// writeRemoteFile ouvre en écriture seule (méthode Put) ; client.Create ouvrirait en lecture et écriture (Open).
func writeRemoteFile(t *testing.T, client *sftp.Client, path, content string) {
	t.Helper()
	file, err := client.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

// exerciseEveryRequest envoie chaque type de requête SFTP ; les symlinks peuvent échouer sous Windows (privilège),
// la requête est enregistrée quand même.
func exerciseEveryRequest(t *testing.T, client *sftp.Client) {
	t.Helper()
	mustSucceed := func(label string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
	}
	mustSucceed("mkdir", client.Mkdir("/dir"))
	writeRemoteFile(t, client, "/dir/file", "content")
	readFile, err := client.Open("/dir/file")
	mustSucceed("open read", err)
	mustSucceed("close read", readFile.Close())
	readWriteFile, err := client.OpenFile("/dir/file", os.O_RDWR)
	mustSucceed("open read-write", err)
	mustSucceed("close read-write", readWriteFile.Close())
	_, err = client.Stat("/dir/file")
	mustSucceed("stat", err)
	_, err = client.Lstat("/dir/file")
	mustSucceed("lstat", err)
	_, err = client.ReadDir("/dir")
	mustSucceed("readdir", err)
	_ = client.Symlink("file", "/dir/link")
	_, _ = client.ReadLink("/dir/link")
	mustSucceed("link", client.Link("/dir/file", "/dir/hard"))
	mustSucceed("rename", client.Rename("/dir/hard", "/dir/hard2"))
	mustSucceed("posix-rename", client.PosixRename("/dir/hard2", "/dir/hard3"))
	mustSucceed("chmod", client.Chmod("/dir/file", 0o600))
	_, err = client.StatVFS("/")
	mustSucceed("statvfs", err)
	_, err = client.RealPath(".")
	mustSucceed("realpath", err)
	mustSucceed("remove", client.Remove("/dir/hard3"))
	_ = client.Remove("/dir/link")
	mustSucceed("remove file", client.Remove("/dir/file"))
	mustSucceed("rmdir", client.RemoveDirectory("/dir"))
}

func TestServer_recordsEveryRequest(t *testing.T) {
	server := sftptest.Start(t)
	client := connect(t, server)

	exerciseEveryRequest(t, client)

	gotValue := server.Requests()
	valeursAttendues := []sftptest.Request{
		{Method: "Mkdir", Path: "/dir"},
		{Method: "Put", Path: "/dir/file", Flags: sftptest.FlagWrite | sftptest.FlagCreate | sftptest.FlagTruncate},
		{Method: "Get", Path: "/dir/file", Flags: sftptest.FlagRead},
		{Method: "Open", Path: "/dir/file", Flags: sftptest.FlagRead | sftptest.FlagWrite},
		{Method: "Stat", Path: "/dir/file"},
		{Method: "Lstat", Path: "/dir/file"},
		{Method: "List", Path: "/dir"},
		{Method: "Symlink", Path: "/dir/link", Target: "file"},
		{Method: "Readlink", Path: "/dir/link"},
		{Method: "Link", Path: "/dir/file", Target: "/dir/hard"},
		{Method: "Rename", Path: "/dir/hard", Target: "/dir/hard2"},
		{Method: "PosixRename", Path: "/dir/hard2", Target: "/dir/hard3"},
		{Method: "Setstat", Path: "/dir/file"},
		{Method: "StatVFS", Path: "/"},
		{Method: "RealPath", Path: "."},
		{Method: "Remove", Path: "/dir/hard3"},
		{Method: "Rmdir", Path: "/dir"},
	}
	for _, valeurAttendue := range valeursAttendues {
		if !slices.Contains(gotValue, valeurAttendue) {
			t.Fatalf("valeurAttendue %+v recorded, valeurObtenue %+v", valeurAttendue, gotValue)
		}
	}
	assertMethodsListed(t, gotValue)
}

// assertMethodsListed exige que chaque méthode enregistrée soit dans sftptest.Methods, et que la liste ne nomme
// rien que le serveur ne journalise (Exec : TestServer_withExecServesTheHandlerAndRecordsTheCommand).
func assertMethodsListed(t *testing.T, requests []sftptest.Request) {
	t.Helper()
	recorded := map[string]bool{"Exec": true}
	for _, request := range requests {
		if !slices.Contains(sftptest.Methods, request.Method) {
			t.Fatalf("valeurObtenue method %q recorded, valeurAttendue listed in sftptest.Methods", request.Method)
		}
		recorded[request.Method] = true
	}
	for _, listed := range sftptest.Methods {
		if !recorded[listed] {
			t.Fatalf("valeurObtenue %q listed but never recorded, valeurAttendue every listed method exercised", listed)
		}
	}
}

func TestServer_servesFilesFromRoot(t *testing.T) {
	server := sftptest.Start(t)
	client := connect(t, server)

	writeRemoteFile(t, client, "/written", "remote content")

	gotValue, err := os.ReadFile(filepath.Join(server.Root, "written"))
	if err != nil || string(gotValue) != "remote content" {
		t.Fatalf("valeurObtenue %q (err %v), valeurAttendue the file under Root", gotValue, err)
	}
	if _, err := client.Stat("/../../outside"); err == nil {
		t.Fatal("valeurAttendue a path that leaves Root refused")
	}
}

func TestServer_withoutExtensionHidesIt(t *testing.T) {
	requests := map[string]func(client *sftp.Client) error{
		"posix-rename@openssh.com": func(client *sftp.Client) error { return client.PosixRename("/a", "/b") },
		"hardlink@openssh.com":     func(client *sftp.Client) error { return client.Link("/a", "/b") },
		"statvfs@openssh.com": func(client *sftp.Client) error {
			_, err := client.StatVFS("/")
			return err
		},
	}
	for extension, send := range requests {
		advertised := connect(t, sftptest.Start(t))
		if _, found := advertised.HasExtension(extension); !found {
			t.Fatalf("%s: valeurAttendue advertised by default", extension)
		}
		server := sftptest.Start(t, sftptest.WithoutExtension(extension))
		client := connect(t, server)
		writeRemoteFile(t, client, "/a", "x")

		_, isAdvertised := client.HasExtension(extension)
		err := send(client)

		if isAdvertised || err == nil {
			t.Fatalf("%s: valeurObtenue advertised %v, err %v; valeurAttendue hidden and refused", extension, isAdvertised, err)
		}
		if _, statErr := os.Lstat(filepath.Join(server.Root, "b")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("%s: valeurAttendue nothing written by the refused request, valeurObtenue %v", extension, statErr)
		}
		if len(server.Requests()) == 0 {
			t.Fatalf("%s: valeurAttendue the refused request recorded", extension)
		}
	}
}

func TestServer_withoutStatVFSRefusesStatVFS(t *testing.T) {
	client := connect(t, sftptest.Start(t, sftptest.WithoutStatVFS()))

	_, isAdvertised := client.HasExtension("statvfs@openssh.com")
	_, err := client.StatVFS("/")

	if isAdvertised || err == nil {
		t.Fatalf("valeurObtenue advertised %v, err %v; valeurAttendue statvfs hidden and refused", isAdvertised, err)
	}
}

func TestServer_listensOnLoopbackWithAPinnedHostKey(t *testing.T) {
	server := sftptest.Start(t)
	other := sftptest.Start(t)

	host, _, err := net.SplitHostPort(server.Address)
	if err != nil || host != "127.0.0.1" {
		t.Fatalf("valeurObtenue %q, valeurAttendue 127.0.0.1", server.Address)
	}
	if server.HostKeyLine == other.HostKeyLine {
		t.Fatal("valeurAttendue a host key generated per server")
	}
	if _, err := dial(t, server.Address, other.HostKeyLine, server.ClientKeyFile); err == nil {
		t.Fatal("wrong host key: valeurAttendue the connection refused")
	}
	if _, err := dial(t, server.Address, server.HostKeyLine, other.ClientKeyFile); err == nil {
		t.Fatal("unknown client key: valeurAttendue the connection refused")
	}
}

func TestServer_clientKeyFileIsPrivateToTheOwner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits")
	}
	server := sftptest.Start(t)

	info, err := os.Stat(server.ClientKeyFile)

	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("valeurObtenue %v (err %v), valeurAttendue 0600", info, err)
	}
}

func TestServer_reversedSymlinkArgsSwapsTargetAndLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege on Windows")
	}
	server := sftptest.Start(t, sftptest.ReversedSymlinkArgs())
	client := connect(t, server)

	if err := client.Symlink("/link-path", "target-text"); err != nil {
		t.Fatal(err)
	}

	gotValue, err := os.Readlink(filepath.Join(server.Root, "link-path"))
	if err != nil || gotValue != "/target-text" {
		t.Fatalf("valeurObtenue %q (err %v), valeurAttendue link-path -> /target-text", gotValue, err)
	}
}

func TestServer_windowsRootAnswersWithADriveLetter(t *testing.T) {
	server := sftptest.Start(t, sftptest.WindowsRoot())
	client := connect(t, server)
	writeRemoteFile(t, client, "/C:/file", "x")

	gotValue, err := client.RealPath(".")
	_, statErr := client.Stat("/C:/file")

	if err != nil || !strings.HasPrefix(gotValue, "/C:/") || statErr != nil {
		t.Fatalf("valeurObtenue %q (err %v, stat %v), valeurAttendue /C:/…", gotValue, err, statErr)
	}
}

func TestServer_stallMethodBlocksUntilTheClientLeaves(t *testing.T) {
	server := sftptest.Start(t, sftptest.StallMethod("Stat"))
	sshClient := connectSSH(t, server)
	client, err := sftp.NewClient(sshClient)
	if err != nil {
		t.Fatal(err)
	}
	returned := make(chan error, 1)

	go func() {
		_, statErr := client.Stat("/")
		returned <- statErr
	}()

	select {
	case statErr := <-returned:
		t.Fatalf("valeurObtenue a stat answered (%v), valeurAttendue a stalled stat", statErr)
	case <-time.After(200 * time.Millisecond):
	}
	_ = sshClient.Close()
	select {
	case statErr := <-returned:
		if statErr == nil {
			t.Fatal("valeurAttendue the stalled stat to fail once the connection is closed")
		}
	case <-time.After(clientTimeout):
		t.Fatal("valeurAttendue the stalled stat released by the closed connection")
	}
}

// execOutput lance command sur un canal brut (aucune méthode de ssh.Session : réservées à sftpfs/exec.go) et rend la
// réponse à la requête exec, la sortie standard et le code de sortie.
func execOutput(t *testing.T, sshClient *ssh.Client, command string, stdin []byte) (bool, []byte, int) {
	t.Helper()
	channel, requests, err := sshClient.OpenChannel("session", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = channel.Close() }()
	exitStatus := make(chan int, 1)
	go func() {
		for request := range requests {
			var status struct{ Value uint32 }
			if request.Type == "exit-status" && ssh.Unmarshal(request.Payload, &status) == nil {
				exitStatus <- int(status.Value)
			}
		}
	}()
	accepted, err := channel.SendRequest("exec", true, ssh.Marshal(struct{ Value string }{command}))
	if err != nil || !accepted {
		return false, nil, -1
	}
	if _, err := channel.Write(stdin); err != nil {
		t.Fatal(err)
	}
	if err := channel.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if _, err := stdout.ReadFrom(channel); err != nil {
		t.Fatal(err)
	}
	select {
	case status := <-exitStatus:
		return true, stdout.Bytes(), status
	case <-time.After(clientTimeout):
		t.Fatal("valeurAttendue an exit-status")
	}
	return true, nil, -1
}

func TestServer_withExecServesTheHandlerAndRecordsTheCommand(t *testing.T) {
	server := sftptest.Start(t, sftptest.WithExec(func(command string, stdin []byte) ([]byte, []byte, int) {
		return append([]byte(command+":"), stdin...), []byte("ignored"), 3
	}))

	accepted, stdout, exitCode := execOutput(t, connectSSH(t, server), "stat --version", []byte("input"))

	if !accepted || string(stdout) != "stat --version:input" || exitCode != 3 {
		t.Fatalf("valeurObtenue accepted %v, stdout %q, exit %d", accepted, stdout, exitCode)
	}
	if !slices.Contains(server.Requests(), sftptest.Request{Method: "Exec", Path: "stat --version"}) {
		t.Fatalf("valeurAttendue the command recorded, valeurObtenue %+v", server.Requests())
	}
}

func TestServer_denyExecRejectsExecButRecordsIt(t *testing.T) {
	for _, options := range [][]sftptest.Option{nil, {sftptest.DenyExec()}} {
		server := sftptest.Start(t, options...)

		accepted, _, _ := execOutput(t, connectSSH(t, server), "sha1sum", nil)

		if accepted || !slices.Contains(server.Requests(), sftptest.Request{Method: "Exec", Path: "sha1sum"}) {
			t.Fatalf("valeurObtenue accepted %v, requests %+v; valeurAttendue rejected and recorded", accepted, server.Requests())
		}
	}
}

// Le décompte des goroutines suppose qu'aucun autre test ne tourne : jamais de t.Parallel dans ce paquet.
func TestServer_cleanupLeavesNoGoroutineOrListener(t *testing.T) {
	goroutinesBefore := runtime.NumGoroutine()
	var address string

	t.Run("serve", func(t *testing.T) {
		server := sftptest.Start(t, sftptest.StallMethod("Lstat"), sftptest.WithExec(func(string, []byte) ([]byte, []byte, int) {
			return nil, nil, 0
		}))
		address = server.Address
		client := connect(t, server)
		writeRemoteFile(t, client, "/file", "x")
		go func() { _, _ = client.Lstat("/file") }()
		_, _, _ = connectSSH(t, server).OpenChannel("session", nil)
	})

	if connection, err := net.DialTimeout("tcp", address, clientTimeout); err == nil {
		_ = connection.Close()
		t.Fatal("valeurAttendue the listener closed after cleanup")
	}
	deadline := time.Now().Add(settleTimeout)
	for runtime.NumGoroutine() > goroutinesBefore && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if gotValue := runtime.NumGoroutine(); gotValue > goroutinesBefore {
		t.Fatalf("valeurObtenue %d goroutines, valeurAttendue at most %d after cleanup", gotValue, goroutinesBefore)
	}
}

func TestServerSources_embedNoPrivateKey(t *testing.T) {
	sources, err := filepath.Glob("*.go")
	if err != nil || len(sources) == 0 {
		t.Fatalf("valeurObtenue %v (err %v), valeurAttendue the package sources", sources, err)
	}
	forbidden := []string{"-----" + "BEGIN", "PRIVATE" + " KEY", "AAAA" + "C3NzaC1"}

	for _, source := range sources {
		content, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		for _, marker := range forbidden {
			if bytes.Contains(content, []byte(marker)) {
				t.Fatalf("%s: valeurObtenue %q, valeurAttendue keys generated per test, never committed", source, marker)
			}
		}
	}
	if entries, _ := filepath.Glob("testdata/*"); len(entries) != 0 {
		t.Fatalf("valeurObtenue fixtures %v, valeurAttendue no committed key file", entries)
	}
}

func skipSymlinksOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege on Windows")
	}
}

// symlinkInRoot pose un lien directement dans le dossier servi, comme un dossier de liens déjà présent sur le serveur.
func symlinkInRoot(t *testing.T, server *sftptest.Server, target, linkPath string) {
	t.Helper()
	if err := os.Symlink(target, filepath.Join(server.Root, filepath.FromSlash(linkPath))); err != nil {
		t.Fatal(err)
	}
}

func mkdirInRoot(t *testing.T, server *sftptest.Server, directory string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(server.Root, filepath.FromSlash(directory)), 0o755); err != nil {
		t.Fatal(err)
	}
}

func readRemote(client *sftp.Client, remotePath string) (string, error) {
	file, err := client.Open(remotePath)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	var content bytes.Buffer
	_, err = content.ReadFrom(file)
	return content.String(), err
}

func TestServer_followsAbsoluteAndRelativeSymlinksInsideRoot(t *testing.T) {
	skipSymlinksOnWindows(t)
	server := sftptest.Start(t)
	mkdirInRoot(t, server, "data")
	mkdirInRoot(t, server, "links")
	if err := os.WriteFile(filepath.Join(server.Root, "data", "file"), []byte("media"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkInRoot(t, server, "/data/file", "links/absolute")
	symlinkInRoot(t, server, "../data/file", "links/relative")
	symlinkInRoot(t, server, "/data", "links/directory")
	client := connect(t, server)

	for _, linkPath := range []string{"/links/absolute", "/links/relative", "/links/directory/file"} {
		info, statErr := client.Stat(linkPath)
		content, readErr := readRemote(client, linkPath)
		if statErr != nil || info.Size() != 5 || readErr != nil || content != "media" {
			t.Fatalf("%s: valeurObtenue stat %v (err %v), content %q (err %v), valeurAttendue the file through the link", linkPath, info, statErr, content, readErr)
		}
	}
	entries, err := client.ReadDir("/links/directory")
	if err != nil || len(entries) != 1 || entries[0].Name() != "file" {
		t.Fatalf("valeurObtenue %v (err %v), valeurAttendue the directory listed through the link", entries, err)
	}
	if info, err := client.Lstat("/links/absolute"); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("valeurObtenue %v (err %v), valeurAttendue Lstat not following the link", info, err)
	}
	if target, err := client.ReadLink("/links/absolute"); err != nil || target != "/data/file" {
		t.Fatalf("valeurObtenue %q (err %v), valeurAttendue the raw target text", target, err)
	}
}

func TestServer_danglingAndLoopingSymlinksFail(t *testing.T) {
	skipSymlinksOnWindows(t)
	server := sftptest.Start(t)
	symlinkInRoot(t, server, "/missing", "dangling")
	symlinkInRoot(t, server, "/second", "first")
	symlinkInRoot(t, server, "first", "second")
	client := connect(t, server)

	if _, err := client.Stat("/dangling"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dangling: valeurObtenue %v, valeurAttendue not exist", err)
	}
	_, err := client.Stat("/first")
	if err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("loop: valeurObtenue %v, valeurAttendue a bounded loop error", err)
	}
}

func TestServer_symlinksNeverLeaveRoot(t *testing.T) {
	skipSymlinksOnWindows(t)
	server := sftptest.Start(t)
	outside := t.TempDir()
	const secret = "outside-the-root"
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.ToSlash(filepath.Join(outside, "secret"))
	climbing := strings.Repeat("../", 32) + strings.TrimPrefix(outsideFile, "/")
	mkdirInRoot(t, server, "deep/inner")
	symlinkInRoot(t, server, outsideFile, "absolute")
	symlinkInRoot(t, server, climbing, "deep/inner/climbing")
	symlinkInRoot(t, server, "/"+climbing, "rooted")
	client := connect(t, server)

	for _, linkPath := range []string{"/absolute", "/deep/inner/climbing", "/rooted"} {
		if content, err := readRemote(client, linkPath); content == secret {
			t.Fatalf("%s: valeurObtenue the file outside Root (err %v), valeurAttendue the link resolved inside Root", linkPath, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(server.Root, filepath.FromSlash(strings.TrimPrefix(filepath.ToSlash(outside), "/"))), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(server.Root, filepath.FromSlash(strings.TrimPrefix(outsideFile, "/"))), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, linkPath := range []string{"/absolute", "/deep/inner/climbing", "/rooted"} {
		if content, err := readRemote(client, linkPath); err != nil || content != "inside" {
			t.Fatalf("%s: valeurObtenue %q (err %v), valeurAttendue the same path under the virtual root", linkPath, content, err)
		}
	}
}

// sftpPacket encode un paquet SFTP : longueur, type, puis la charge.
func sftpPacket(packetType byte, payload []byte) []byte {
	packet := binary.BigEndian.AppendUint32(nil, uint32(len(payload)+1))
	return append(append(packet, packetType), payload...)
}

func sshString(value string) []byte {
	return append(binary.BigEndian.AppendUint32(nil, uint32(len(value))), value...)
}

// pkg/sftp refuse lui-même une requête étendue qu'il ne connaît pas, sans passer par le gestionnaire : le serveur doit
// tout de même la journaliser, sous Extended:<nom>.
func TestServer_recordsAnUnknownExtendedRequest(t *testing.T) {
	server := sftptest.Start(t)
	session, err := connectSSH(t, server).NewSession()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	stdin, err := session.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := session.RequestSubsystem("sftp"); err != nil {
		t.Fatal(err)
	}
	extended := slices.Concat(binary.BigEndian.AppendUint32(nil, 1), sshString("fsync@openssh.com"), sshString("handle"))
	for _, packet := range [][]byte{sftpPacket(1, binary.BigEndian.AppendUint32(nil, 3)), sftpPacket(200, extended)} {
		if _, err := stdin.Write(packet); err != nil {
			t.Fatal(err)
		}
	}
	reply := make([]byte, 4)
	for _, expected := range []string{"version", "status"} {
		if _, err := io.ReadFull(stdout, reply); err != nil {
			t.Fatalf("%s: %v", expected, err)
		}
		if _, err := io.CopyN(io.Discard, stdout, int64(binary.BigEndian.Uint32(reply))); err != nil {
			t.Fatalf("%s: %v", expected, err)
		}
	}

	valeurAttendue := sftptest.Request{Method: sftptest.ExtendedMethodPrefix + "fsync@openssh.com"}
	if valeurObtenue := server.Requests(); !slices.Contains(valeurObtenue, valeurAttendue) {
		t.Fatalf("valeurObtenue %+v, valeurAttendue %+v recorded", valeurObtenue, valeurAttendue)
	}
}
