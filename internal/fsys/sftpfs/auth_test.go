package sftpfs_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"errors"
	"net"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/sftpfs"
	"necromanciarr/internal/fsys/sftpfs/sftptest"
)

// hangGuard : au-delà, l'appel est considéré bloqué ; bien plus que le timeout configuré des tests.
const hangGuard = 5 * time.Second

// silentAgent accepte les connexions sur une socket unix et ne répond jamais : un agent figé.
func silentAgent(t *testing.T) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "silent.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan net.Conn, 16)
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			accepted <- connection
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		close(accepted)
		for connection := range accepted {
			_ = connection.Close()
		}
	})
	return socket
}

// withinGuard lance call et échoue si elle ne rend pas la main avant hangGuard.
func withinGuard(t *testing.T, label string, call func() error) (time.Duration, error) {
	t.Helper()
	start := time.Now()
	returned := make(chan error, 1)
	go func() { returned <- call() }()
	select {
	case err := <-returned:
		return time.Since(start), err
	case <-time.After(hangGuard):
		t.Fatalf("%s: valeurAttendue a return within the timeout, valeurObtenue a hang", label)
		return 0, nil
	}
}

func TestAgent_silentAgentCannotBlockDialBeyondTheTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("agent over a unix socket")
	}
	server := sftptest.Start(t)
	provider := pinnedProvider(t, server)
	provider.Agent, provider.IdentityFile, provider.Timeout = true, "", 300*time.Millisecond
	deps := testDeps()
	socket := silentAgent(t)
	deps.LookupEnv = func(name string) (string, bool) { return socket, name == agentSocketEnv }

	elapsed, err := withinGuard(t, "dial", func() error {
		_, err := sftpfs.Dial(context.Background(), provider, deps)
		return err
	})

	if err == nil || elapsed > provider.Timeout+2*time.Second {
		t.Fatalf("valeurObtenue %v after %v, valeurAttendue an error within the timeout", err, elapsed)
	}
	if !errors.Is(err, fsys.ErrRemoteUnavailable) && !errors.Is(err, sftpfs.ErrAuthRefused) {
		t.Fatalf("valeurObtenue %v, valeurAttendue a timeout or auth_refused class", err)
	}
}

// L'annulation tombe bien avant timeout : seule la fermeture de l'agent à l'annulation peut libérer Dial à temps.
func TestAgent_cancelledDialClosesASilentAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("agent over a unix socket")
	}
	server := sftptest.Start(t)
	provider := pinnedProvider(t, server)
	provider.Agent, provider.IdentityFile, provider.Timeout = true, "", 3*time.Second
	deps := testDeps()
	socket := silentAgent(t)
	deps.LookupEnv = func(name string) (string, bool) { return socket, name == agentSocketEnv }
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	elapsed, err := withinGuard(t, "dial", func() error {
		_, err := sftpfs.Dial(ctx, provider, deps)
		return err
	})

	if err == nil || elapsed > time.Second {
		t.Fatalf("valeurObtenue %v after %v, valeurAttendue an error within 1 s of the cancellation", err, elapsed)
	}
}

func TestAgent_silentAgentOnReconnectReleasesTheVolume(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("agent over a unix socket")
	}
	server := sftptest.Start(t)
	provider := pinnedProvider(t, server)
	// Bornes larges : sous charge, la reconnexion et le refus immédiat restent loin de hangGuard.
	provider.Agent, provider.Timeout = true, 2*time.Second
	socket := silentAgent(t)
	var agentPresent atomic.Bool
	deps := testDeps()
	deps.LookupEnv = func(name string) (string, bool) { return socket, name == agentSocketEnv && agentPresent.Load() }
	volume := dialVolume(t, provider, deps)
	agentPresent.Store(true)
	breakConnection(t, server, volume)

	elapsed, err := withinGuard(t, "reconnect", func() error { return volume.Probe(context.Background()) })
	if !errors.Is(err, fsys.ErrRemoteUnavailable) || elapsed > provider.Timeout+time.Second {
		t.Fatalf("valeurObtenue %v after %v, valeurAttendue ErrRemoteUnavailable within the timeout", err, elapsed)
	}
	elapsed, err = withinGuard(t, "next operation", func() error { return volume.Probe(context.Background()) })
	if !errors.Is(err, fsys.ErrRemoteUnavailable) || elapsed > 500*time.Millisecond {
		t.Fatalf("valeurObtenue %v after %v, valeurAttendue an immediate refusal", err, elapsed)
	}
}

func TestAgent_unavailableWarningNamesTheVariableNeverTheSocket(t *testing.T) {
	server := sftptest.Start(t)
	provider := pinnedProvider(t, server)
	provider.Agent = true
	missingSocket := filepath.Join(t.TempDir(), "private-agent-0000.sock")
	deps := testDeps()
	deps.LookupEnv = func(name string) (string, bool) { return missingSocket, name == agentSocketEnv }

	volume := dialVolume(t, provider, deps)

	valeurAttendue := []sftpfs.Warning{{Code: "agent_unavailable", Path: "SSH_AUTH_SOCK"}}
	if valeurObtenue := volume.Warnings(); !slices.Equal(valeurObtenue, valeurAttendue) {
		t.Fatalf("valeurObtenue %v, valeurAttendue %v", valeurObtenue, valeurAttendue)
	}
}

func TestAgent_signerEqualToTheIdentityKeyIsOfferedOnce(t *testing.T) {
	identity := generateKey(t, "", 0o600)
	other := generateKey(t, "", 0o600)
	identitySigner, err := ssh.NewSignerFromKey(identity.privateKey)
	if err != nil {
		t.Fatal(err)
	}
	otherSigner, err := ssh.NewSignerFromKey(other.privateKey)
	if err != nil {
		t.Fatal(err)
	}

	signers := sftpfs.OrderedSigners([]ssh.Signer{identitySigner, otherSigner}, identitySigner)

	var valeurObtenue []string
	for _, signer := range signers {
		valeurObtenue = append(valeurObtenue, ssh.FingerprintSHA256(signer.PublicKey()))
	}
	valeurAttendue := []string{other.fingerprint, identity.fingerprint}
	if !slices.Equal(valeurObtenue, valeurAttendue) {
		t.Fatalf("valeurObtenue %v, valeurAttendue %v", valeurObtenue, valeurAttendue)
	}
}

func writeRSAKey(t *testing.T, bits int) string {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(privateKey, "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "id_rsa")
	sftptest.WriteFile(t, path, pem.EncodeToMemory(block), 0o600)
	return path
}

func TestIdentityFile_refusesRSAKeysUnder3072Bits(t *testing.T) {
	server := sftptest.Start(t)
	provider := pinnedProvider(t, server)
	provider.IdentityFile = writeRSAKey(t, 2048)

	_, err := sftpfs.Dial(context.Background(), provider, testDeps())

	if !errors.Is(err, sftpfs.ErrIdentityKeyTooWeak) || sftpfs.Code(err) != "identity_key_too_weak" {
		t.Fatalf("valeurObtenue %v (code %q), valeurAttendue identity_key_too_weak", err, sftpfs.Code(err))
	}
	if valeurObtenue := server.Connections(); valeurObtenue != 0 {
		t.Fatalf("connections: valeurObtenue %d, valeurAttendue 0 (refused before dialing)", valeurObtenue)
	}
	if strings.Contains(err.Error(), "PRIVATE KEY") {
		t.Fatalf("valeurObtenue %q, valeurAttendue no key bytes", err)
	}
}

func TestIdentityFile_acceptsRSA3072AndEd25519Keys(t *testing.T) {
	server := sftptest.Start(t)
	provider := pinnedProvider(t, server)
	provider.IdentityFile = writeRSAKey(t, 3072)

	_, err := sftpfs.Dial(context.Background(), provider, testDeps())
	if !errors.Is(err, sftpfs.ErrAuthRefused) || errors.Is(err, sftpfs.ErrIdentityKeyTooWeak) {
		t.Fatalf("rsa 3072: valeurObtenue %v, valeurAttendue offered to the server (auth_refused: not authorized)", err)
	}
	if volume := dialVolume(t, pinnedProvider(t, server), testDeps()); volume.State() != "ok" {
		t.Fatalf("ed25519: valeurObtenue %q, valeurAttendue ok", volume.State())
	}
}
