package sftptest_test

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"necromanciarr/internal/fsys/sftpfs/sftptest"
)

const keepaliveRequest = "keepalive@openssh.com"

func dialWithPassword(t *testing.T, server *sftptest.Server, password string) (*ssh.Client, error) {
	t.Helper()
	hostKeyCallback, err := knownhosts.New(knownHostsFile(t, server.HostKeyLine))
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ClientConfig{
		User: "user", Auth: []ssh.AuthMethod{ssh.Password(password)},
		HostKeyCallback: hostKeyCallback, Timeout: clientTimeout,
	}
	return ssh.Dial("tcp", server.Address, config)
}

func TestServer_withPasswordAcceptsOnlyThatPasswordAndRecordsAttempts(t *testing.T) {
	server := sftptest.Start(t, sftptest.WithPassword("fake-password-0000"))

	if _, err := dialWithPassword(t, server, "wrong-password-0000"); err == nil {
		t.Fatal("wrong password: valeurAttendue the connection refused")
	}
	sshClient, err := dialWithPassword(t, server, "fake-password-0000")
	if err != nil {
		t.Fatal(err)
	}
	_ = sshClient.Close()
	connectSSH(t, server)

	valeurAttendue := []sftptest.AuthAttempt{
		{Method: "password"},
		{Method: "password"},
		{Method: "publickey", KeyFingerprint: ssh.FingerprintSHA256(clientSigner(t, server.ClientKeyFile).PublicKey())},
	}
	if valeurObtenue := server.AuthAttempts(); !slices.Equal(valeurObtenue, valeurAttendue) {
		t.Fatalf("valeurObtenue %v, valeurAttendue %v", valeurObtenue, valeurAttendue)
	}
}

func TestServer_withoutWithPasswordRefusesEveryPassword(t *testing.T) {
	server := sftptest.Start(t)

	if _, err := dialWithPassword(t, server, ""); err == nil {
		t.Fatal("valeurAttendue the connection refused")
	}
}

func TestServer_repliesToKeepaliveUnlessIgnored(t *testing.T) {
	replied := connectSSH(t, sftptest.Start(t))
	if _, _, err := replied.SendRequest(keepaliveRequest, true, nil); err != nil {
		t.Fatalf("valeurObtenue %v, valeurAttendue a reply", err)
	}

	silent := connectSSH(t, sftptest.Start(t, sftptest.IgnoreKeepalive()))
	reply := make(chan error, 1)
	go func() {
		_, _, err := silent.SendRequest(keepaliveRequest, true, nil)
		reply <- err
	}()
	select {
	case err := <-reply:
		t.Fatalf("valeurObtenue a reply (%v), valeurAttendue none", err)
	case <-time.After(200 * time.Millisecond):
	}
	_ = silent.Close()
	if err := <-reply; err == nil {
		t.Fatal("valeurAttendue the pending keepalive to fail once the connection is closed")
	}
}

func TestServer_dropConnectionsClosesClientsAndCountsConnections(t *testing.T) {
	server := sftptest.Start(t)
	first := connectSSH(t, server)
	connectSSH(t, server)

	server.DropConnections()

	closed := make(chan struct{})
	go func() {
		_ = first.Wait()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(settleTimeout):
		t.Fatal("valeurAttendue the client connection closed by the server")
	}
	connectSSH(t, server)
	if valeurObtenue := server.Connections(); valeurObtenue != 3 {
		t.Fatalf("valeurObtenue %d, valeurAttendue 3", valeurObtenue)
	}
}

func TestServer_withECDSAHostKeyPresentsBothHostKeys(t *testing.T) {
	server := sftptest.Start(t, sftptest.WithECDSAHostKey())

	if server.ECDSAHostKeyLine == "" || server.ECDSAHostKeyLine == server.HostKeyLine {
		t.Fatalf("valeurObtenue %q, valeurAttendue a distinct ECDSA known_hosts line", server.ECDSAHostKeyLine)
	}
	for _, hostKey := range []struct{ line, algorithm string }{
		{server.HostKeyLine, ssh.KeyAlgoED25519}, {server.ECDSAHostKeyLine, ssh.KeyAlgoECDSA256},
	} {
		hostKeyCallback, err := knownhosts.New(knownHostsFile(t, hostKey.line))
		if err != nil {
			t.Fatal(err)
		}
		config := &ssh.ClientConfig{
			User: "user", Auth: []ssh.AuthMethod{ssh.PublicKeys(clientSigner(t, server.ClientKeyFile))},
			HostKeyCallback: hostKeyCallback, HostKeyAlgorithms: []string{hostKey.algorithm}, Timeout: clientTimeout,
		}
		sshClient, err := ssh.Dial("tcp", server.Address, config)
		if err != nil {
			t.Fatalf("%s: %v", hostKey.algorithm, err)
		}
		_ = sshClient.Close()
	}
}

func TestWriteFile_writesTheContentWithTheExactModeAndCreatesParents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "known_hosts")

	sftptest.WriteFile(t, path, []byte("content"), 0o644)

	content, err := os.ReadFile(path)
	if err != nil || string(content) != "content" {
		t.Fatalf("valeurObtenue %q (err %v), valeurAttendue content", content, err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("valeurObtenue %v (err %v), valeurAttendue 0644", info, err)
	}
}
