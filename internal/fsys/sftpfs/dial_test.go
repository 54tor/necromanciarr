package sftpfs_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"

	"necromanciarr/internal/config"
	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/sftpfs"
	"necromanciarr/internal/fsys/sftpfs/sftptest"
)

const (
	testTimeout    = 5 * time.Second
	settleTimeout  = 2 * time.Second
	agentSocketEnv = "SSH_AUTH_SOCK"
	fakePassword   = "fake-pass&word/0000"
	fakePassphrase = "0000bbbb0000bbbb"
)

// generatedKey est une clé de client générée pour un test : fichier OpenSSH, empreinte et clé brute.
type generatedKey struct {
	path, fingerprint string
	privateKey        ed25519.PrivateKey
	pemBytes          []byte
}

func generateKey(t *testing.T, passphrase string, mode os.FileMode) generatedKey {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return writeKey(t, privateKey, publicKey, passphrase, mode)
}

func writeKey(t *testing.T, privateKey ed25519.PrivateKey, publicKey ed25519.PublicKey, passphrase string, mode os.FileMode) generatedKey {
	t.Helper()
	block, err := ssh.MarshalPrivateKey(privateKey, "")
	if passphrase != "" {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(privateKey, "", []byte(passphrase))
	}
	if err != nil {
		t.Fatal(err)
	}
	sshPublicKey, err := ssh.NewPublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	key := generatedKey{
		path: filepath.Join(t.TempDir(), "id_ed25519"), fingerprint: ssh.FingerprintSHA256(sshPublicKey),
		privateKey: privateKey, pemBytes: pem.EncodeToMemory(block),
	}
	sftptest.WriteFile(t, key.path, key.pemBytes, mode)
	return key
}

// serverClientKey relit la clé que le serveur autorise, pour la réécrire autrement (mode, passphrase).
func serverClientKey(t *testing.T, server *sftptest.Server, passphrase string, mode os.FileMode) generatedKey {
	t.Helper()
	keyBytes, err := os.ReadFile(server.ClientKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	rawKey, err := ssh.ParseRawPrivateKey(keyBytes)
	if err != nil {
		t.Fatal(err)
	}
	privateKey := *rawKey.(*ed25519.PrivateKey)
	return writeKey(t, privateKey, privateKey.Public().(ed25519.PublicKey), passphrase, mode)
}

func knownHostsFile(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "known_hosts")
	sftptest.WriteFile(t, path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
	return path
}

// otherHostKeyLine pin une clé générée, qui n'est pas celle du serveur, pour ces hôtes.
func otherHostKeyLine(t *testing.T, hosts ...string) string {
	t.Helper()
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPublicKey, err := ssh.NewPublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	return knownhosts.Line(hosts, sshPublicKey)
}

func providerFor(server *sftptest.Server, knownHosts string) config.FSProvider {
	return config.FSProvider{
		Type: config.FSProviderTypeSFTP, Name: "seedbox", Address: server.Address, User: "user",
		KnownHosts: knownHosts, IdentityFile: server.ClientKeyFile, MaxConcurrentRequests: 16,
		Timeout: testTimeout, Keepalive: 15 * time.Second, Exec: config.SFTPExecAuto, ExecHashTimeout: 30 * time.Minute,
	}
}

func pinnedProvider(t *testing.T, server *sftptest.Server) config.FSProvider {
	t.Helper()
	return providerFor(server, knownHostsFile(t, server.HostKeyLine))
}

func noEnvironment(string) (string, bool) { return "", false }

func testDeps() sftpfs.Deps { return sftpfs.Deps{LookupEnv: noEnvironment, Now: time.Now} }

func dialVolume(t *testing.T, provider config.FSProvider, deps sftpfs.Deps) *sftpfs.Volume {
	t.Helper()
	volume, err := sftpfs.Dial(context.Background(), provider, deps)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = volume.Close() })
	return volume
}

func waitForState(t *testing.T, volume *sftpfs.Volume, valeurAttendue string) {
	t.Helper()
	deadline := time.Now().Add(settleTimeout)
	for volume.State() != valeurAttendue && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if valeurObtenue := volume.State(); valeurObtenue != valeurAttendue {
		t.Fatalf("state: valeurObtenue %q, valeurAttendue %q", valeurObtenue, valeurAttendue)
	}
}

func warningCodes(volume *sftpfs.Volume) []string {
	codes := []string{}
	for _, warning := range volume.Warnings() {
		codes = append(codes, warning.Code)
	}
	return codes
}

func TestConnect_refusesUnknownHostKey(t *testing.T) {
	server := sftptest.Start(t)
	knownHosts := knownHostsFile(t, otherHostKeyLine(t, "unrelated.example.com"))
	before, err := os.ReadFile(knownHosts)
	if err != nil {
		t.Fatal(err)
	}

	for attempt := range 2 {
		_, err := sftpfs.Dial(context.Background(), providerFor(server, knownHosts), testDeps())

		if !errors.Is(err, sftpfs.ErrHostKeyUnknown) || errors.Is(err, sftpfs.ErrHostKeyMismatch) {
			t.Fatalf("attempt %d: valeurObtenue %v, valeurAttendue ErrHostKeyUnknown", attempt, err)
		}
		if valeurObtenue := sftpfs.Code(err); valeurObtenue != "host_key_unknown" {
			t.Fatalf("code: valeurObtenue %q, valeurAttendue host_key_unknown", valeurObtenue)
		}
		if !strings.Contains(err.Error(), server.Address) {
			t.Fatalf("valeurObtenue %q, valeurAttendue the address named", err)
		}
	}
	after, err := os.ReadFile(knownHosts)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("known_hosts changed (err %v): valeurAttendue never appended", err)
	}
	if requests := server.Requests(); len(requests) != 0 {
		t.Fatalf("valeurObtenue %v, valeurAttendue no SFTP request", requests)
	}
}

func TestConnect_refusesMismatchedHostKey(t *testing.T) {
	server := sftptest.Start(t)
	knownHosts := knownHostsFile(t, otherHostKeyLine(t, knownhosts.Normalize(server.Address)))

	_, err := sftpfs.Dial(context.Background(), providerFor(server, knownHosts), testDeps())

	if !errors.Is(err, sftpfs.ErrHostKeyMismatch) || errors.Is(err, sftpfs.ErrHostKeyUnknown) {
		t.Fatalf("valeurObtenue %v, valeurAttendue ErrHostKeyMismatch", err)
	}
	if valeurObtenue := sftpfs.Code(err); valeurObtenue != "host_key_mismatch" {
		t.Fatalf("code: valeurObtenue %q, valeurAttendue host_key_mismatch", valeurObtenue)
	}
}

func TestConnect_acceptsAHashedKnownHostsEntry(t *testing.T) {
	server := sftptest.Start(t)
	_, keyPart, _ := strings.Cut(server.HostKeyLine, " ")
	hashedLine := knownhosts.HashHostname(knownhosts.Normalize(server.Address)) + " " + keyPart

	volume := dialVolume(t, providerFor(server, knownHostsFile(t, hashedLine)), testDeps())

	if valeurObtenue := volume.State(); valeurObtenue != "ok" {
		t.Fatalf("valeurObtenue %q, valeurAttendue ok", valeurObtenue)
	}
}

// Le client préfère ECDSA à ed25519 par défaut : sans préférence tirée du known_hosts, la clé ECDSA présentée par le
// serveur ne serait pas la clé épinglée, et la connexion échouerait à tort.
func TestConnect_prefersThePinnedHostKeyType(t *testing.T) {
	server := sftptest.Start(t, sftptest.WithECDSAHostKey())

	volume := dialVolume(t, pinnedProvider(t, server), testDeps())

	if valeurObtenue := volume.State(); valeurObtenue != "ok" {
		t.Fatalf("valeurObtenue %q, valeurAttendue ok", valeurObtenue)
	}
}

// startAgent sert un agent SSH en mémoire sur une socket unix ; served se ferme quand le client a raccroché.
func startAgent(t *testing.T, key generatedKey) (socket string, served <-chan struct{}) {
	t.Helper()
	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: key.privateKey}); err != nil {
		t.Fatal(err)
	}
	socket = filepath.Join(t.TempDir(), "agent.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	done := make(chan struct{})
	go func() {
		defer close(done)
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		_ = agent.ServeAgent(keyring, connection)
		_ = connection.Close()
	}()
	return socket, done
}

func TestConnect_triesConfiguredMethodsInOrder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("agent over a unix socket")
	}
	server := sftptest.Start(t, sftptest.WithPassword(fakePassword))
	agentKey := generateKey(t, "", 0o600)
	identityKey := generateKey(t, "", 0o600)
	socket, agentServed := startAgent(t, agentKey)
	provider := pinnedProvider(t, server)
	provider.Agent, provider.IdentityFile, provider.Password = true, identityKey.path, fakePassword
	deps := testDeps()
	deps.LookupEnv = func(name string) (string, bool) { return socket, name == agentSocketEnv }

	volume := dialVolume(t, provider, deps)

	valeurAttendue := []sftptest.AuthAttempt{
		{Method: "publickey", KeyFingerprint: agentKey.fingerprint},
		{Method: "publickey", KeyFingerprint: identityKey.fingerprint},
		{Method: "password"},
	}
	if valeurObtenue := server.AuthAttempts(); !slices.Equal(valeurObtenue, valeurAttendue) {
		t.Fatalf("valeurObtenue %v, valeurAttendue %v", valeurObtenue, valeurAttendue)
	}
	if volume.State() != "ok" {
		t.Fatalf("valeurObtenue %q, valeurAttendue ok", volume.State())
	}
	select {
	case <-agentServed:
	case <-time.After(settleTimeout):
		t.Fatal("valeurAttendue the agent connection closed at the end of Dial")
	}
}

func TestConnect_refusedKeyIsAuthRefusedAndUnconfiguredMethodsAreNotTried(t *testing.T) {
	server := sftptest.Start(t, sftptest.WithPassword(fakePassword))
	provider := pinnedProvider(t, server)
	provider.IdentityFile = generateKey(t, "", 0o600).path

	_, err := sftpfs.Dial(context.Background(), provider, testDeps())

	if !errors.Is(err, sftpfs.ErrAuthRefused) || sftpfs.Code(err) != "auth_refused" {
		t.Fatalf("valeurObtenue %v (code %q), valeurAttendue ErrAuthRefused", err, sftpfs.Code(err))
	}
	for _, attempt := range server.AuthAttempts() {
		if attempt.Method != "publickey" {
			t.Fatalf("valeurObtenue %v, valeurAttendue only the configured key tried", server.AuthAttempts())
		}
	}
}

func TestConnect_skipsAnUnavailableAgentWithAWarning(t *testing.T) {
	server := sftptest.Start(t)
	provider := pinnedProvider(t, server)
	provider.Agent = true

	volume := dialVolume(t, provider, testDeps())

	if valeurObtenue := warningCodes(volume); !slices.Equal(valeurObtenue, []string{"agent_unavailable"}) {
		t.Fatalf("valeurObtenue %v, valeurAttendue [agent_unavailable]", valeurObtenue)
	}

	provider.IdentityFile = ""
	_, err := sftpfs.Dial(context.Background(), provider, testDeps())

	if !errors.Is(err, sftpfs.ErrAuthRefused) {
		t.Fatalf("agent alone: valeurObtenue %v, valeurAttendue ErrAuthRefused", err)
	}
	if valeurObtenue := server.Connections(); valeurObtenue != 1 {
		t.Fatalf("connections: valeurObtenue %d, valeurAttendue 1 (no dial without a method)", valeurObtenue)
	}
}

// trapHome pose un HOME piège : known_hosts qui épingle le serveur, clé autorisée et config SSH. Lire l'un d'eux
// ferait réussir une connexion qui doit échouer.
func trapHome(t *testing.T, server *sftptest.Server) {
	t.Helper()
	home := t.TempDir()
	keyBytes, err := os.ReadFile(server.ClientKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	sshDirectory := filepath.Join(home, ".ssh")
	sftptest.WriteFile(t, filepath.Join(sshDirectory, "known_hosts"), []byte(server.HostKeyLine+"\n"), 0o600)
	sftptest.WriteFile(t, filepath.Join(sshDirectory, "id_ed25519"), keyBytes, 0o600)
	sftptest.WriteFile(t, filepath.Join(sshDirectory, "config"), []byte("Host *\n  StrictHostKeyChecking no\n"), 0o600)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func TestConnect_neverReadsUserSSHDirectory(t *testing.T) {
	server := sftptest.Start(t, sftptest.WithPassword(fakePassword))
	trapHome(t, server)

	t.Run("known_hosts", func(t *testing.T) {
		provider := providerFor(server, knownHostsFile(t, otherHostKeyLine(t, "unrelated.example.com")))

		_, err := sftpfs.Dial(context.Background(), provider, testDeps())

		if !errors.Is(err, sftpfs.ErrHostKeyUnknown) {
			t.Fatalf("valeurObtenue %v, valeurAttendue ErrHostKeyUnknown (the trap known_hosts ignored)", err)
		}
	})
	t.Run("identity", func(t *testing.T) {
		provider := pinnedProvider(t, server)
		provider.IdentityFile, provider.Password = "", "wrong-password-0000"
		attemptsBefore := len(server.AuthAttempts())

		_, err := sftpfs.Dial(context.Background(), provider, testDeps())

		if !errors.Is(err, sftpfs.ErrAuthRefused) {
			t.Fatalf("valeurObtenue %v, valeurAttendue ErrAuthRefused (the trap key ignored)", err)
		}
		for _, attempt := range server.AuthAttempts()[attemptsBefore:] {
			if attempt.Method == "publickey" {
				t.Fatal("valeurObtenue a public key offered, valeurAttendue none")
			}
		}
	})
}

// forbiddenSourceReference : rien, hors tests, ne doit approcher la configuration SSH de l'utilisateur. « .ssh »
// seul, pas un champ comme current.sshClient.
var forbiddenSourceReference = regexp.MustCompile(`(^|[^\w])\.ssh\b|UserHomeDir|user\.Current|"HOME"|USERPROFILE|SSH_ASKPASS|ssh_config`)

func TestSources_neverReferenceUserSSHConfiguration(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if reference := forbiddenSourceReference.Find(source); reference != nil {
			t.Errorf("%s references %q", path, reference)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestIdentityFile_warnsWhenNotMode0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits")
	}
	server := sftptest.Start(t)
	provider := pinnedProvider(t, server)
	provider.IdentityFile = serverClientKey(t, server, "", 0o644).path

	volume := dialVolume(t, provider, testDeps())

	warnings := volume.Warnings()
	if len(warnings) != 1 || warnings[0].Code != "identity_file_permissions" || warnings[0].Path != provider.IdentityFile {
		t.Fatalf("valeurObtenue %v, valeurAttendue one identity_file_permissions warning", warnings)
	}
	if volume.State() != "ok" {
		t.Fatalf("valeurObtenue %q, valeurAttendue ok (a warning, not a failure)", volume.State())
	}

	privateVolume := dialVolume(t, pinnedProvider(t, server), testDeps())
	if valeurObtenue := privateVolume.Warnings(); len(valeurObtenue) != 0 {
		t.Fatalf("mode 0600: valeurObtenue %v, valeurAttendue no warning", valeurObtenue)
	}
}

func TestIdentityFile_passphraseProtectedKey(t *testing.T) {
	server := sftptest.Start(t)
	encryptedKey := serverClientKey(t, server, fakePassphrase, 0o600)
	provider := pinnedProvider(t, server)
	provider.IdentityFile = encryptedKey.path

	_, err := sftpfs.Dial(context.Background(), provider, testDeps())
	if !errors.Is(err, sftpfs.ErrIdentityPassphraseMissing) {
		t.Fatalf("no passphrase: valeurObtenue %v, valeurAttendue ErrIdentityPassphraseMissing", err)
	}

	provider.IdentityPassphrase = fakePassphrase
	if volume := dialVolume(t, provider, testDeps()); volume.State() != "ok" {
		t.Fatalf("valeurObtenue %q, valeurAttendue ok", volume.State())
	}
}

func TestErrors_neverContainSecrets(t *testing.T) {
	server := sftptest.Start(t, sftptest.WithPassword("other-password-0000"))
	encryptedKey := serverClientKey(t, server, fakePassphrase, 0o600)
	secrets := []string{fakePassword, "pass&word", fakePassphrase, "PRIVATE KEY", string(encryptedKey.pemBytes[40:80])}

	wrongPassword := pinnedProvider(t, server)
	wrongPassword.IdentityFile, wrongPassword.Password = "", fakePassword
	wrongPassphrase := pinnedProvider(t, server)
	wrongPassphrase.IdentityFile, wrongPassphrase.IdentityPassphrase = encryptedKey.path, fakePassword
	missingPassphrase := pinnedProvider(t, server)
	missingPassphrase.IdentityFile = encryptedKey.path
	notAKey := pinnedProvider(t, server)
	notAKey.IdentityFile = filepath.Join(t.TempDir(), "id_broken")
	sftptest.WriteFile(t, notAKey.IdentityFile, []byte(fakePassphrase+"\n"), 0o600)

	for label, provider := range map[string]config.FSProvider{
		"wrong password": wrongPassword, "wrong passphrase": wrongPassphrase,
		"missing passphrase": missingPassphrase, "not a key": notAKey,
	} {
		_, err := sftpfs.Dial(context.Background(), provider, testDeps())
		if err == nil {
			t.Fatalf("%s: valeurAttendue an error", label)
		}
		for _, secret := range secrets {
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("%s: valeurObtenue %q, valeurAttendue no secret in the error", label, err)
			}
		}
	}
}

// blackhole accepte les connexions TCP sans jamais parler : une poignée de main SSH qui ne finit pas.
func blackhole(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
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
	return listener.Addr().String()
}

func TestDial_cancelledContextOrTimeoutAbortsPromptly(t *testing.T) {
	server := sftptest.Start(t)
	provider := pinnedProvider(t, server)
	provider.Address = blackhole(t)
	provider.Timeout = time.Minute

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := sftpfs.Dial(ctx, provider, testDeps())
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("cancelled: valeurObtenue %v after %v, valeurAttendue an error within 1 s", err, time.Since(start))
	}

	provider.Timeout = 100 * time.Millisecond
	start = time.Now()
	_, err = sftpfs.Dial(context.Background(), provider, testDeps())
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("timeout: valeurObtenue %v after %v, valeurAttendue an error within 1 s", err, time.Since(start))
	}
}

// Le décompte des goroutines suppose qu'aucun autre test ne tourne : jamais de t.Parallel dans ce paquet.
func TestSFTP_stalledOperationTimesOutAndMarksVolumeBroken(t *testing.T) {
	server := sftptest.Start(t, sftptest.StallMethod("Stat"))
	provider := pinnedProvider(t, server)
	provider.Timeout = 200 * time.Millisecond
	goroutinesBefore := runtime.NumGoroutine()
	volume := dialVolume(t, provider, testDeps())

	start := time.Now()
	err := volume.Probe(context.Background())
	elapsed := time.Since(start)

	if !errors.Is(err, fsys.ErrRemoteUnavailable) || !errors.Is(err, sftpfs.ErrOperationTimeout) {
		t.Fatalf("valeurObtenue %v, valeurAttendue ErrRemoteUnavailable from a timeout", err)
	}
	if elapsed > time.Second {
		t.Fatalf("valeurObtenue %v, valeurAttendue within 1 s", elapsed)
	}
	if valeurObtenue := volume.State(); valeurObtenue != "broken" {
		t.Fatalf("valeurObtenue %q, valeurAttendue broken", valeurObtenue)
	}
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > goroutinesBefore && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if valeurObtenue := runtime.NumGoroutine(); valeurObtenue > goroutinesBefore {
		t.Fatalf("valeurObtenue %d goroutines, valeurAttendue at most %d: the stalled call must be released", valeurObtenue, goroutinesBefore)
	}
}

func TestSFTP_missedKeepalivesMarkVolumeBroken(t *testing.T) {
	server := sftptest.Start(t, sftptest.IgnoreKeepalive())
	provider := pinnedProvider(t, server)
	provider.Keepalive = 30 * time.Millisecond
	volume := dialVolume(t, provider, testDeps())

	waitForState(t, volume, "broken")
}

func TestSFTP_answeredKeepalivesKeepVolumeOk(t *testing.T) {
	server := sftptest.Start(t)
	provider := pinnedProvider(t, server)
	provider.Keepalive = 20 * time.Millisecond
	volume := dialVolume(t, provider, testDeps())

	time.Sleep(300 * time.Millisecond)

	if valeurObtenue := volume.State(); valeurObtenue != "ok" {
		t.Fatalf("valeurObtenue %q, valeurAttendue ok", valeurObtenue)
	}
}

func breakConnection(t *testing.T, server *sftptest.Server, volume *sftpfs.Volume) {
	t.Helper()
	server.DropConnections()
	waitForState(t, volume, "broken")
}

func TestSFTP_reconnectsAtMostOncePerStage(t *testing.T) {
	server := sftptest.Start(t)
	volume := dialVolume(t, pinnedProvider(t, server), testDeps())
	ctx := context.Background()

	breakConnection(t, server, volume)
	if err := volume.Probe(ctx); err != nil {
		t.Fatalf("first break: valeurObtenue %v, valeurAttendue one reconnect", err)
	}
	breakConnection(t, server, volume)
	for range 3 {
		if err := volume.Probe(ctx); !errors.Is(err, fsys.ErrRemoteUnavailable) {
			t.Fatalf("second break: valeurObtenue %v, valeurAttendue ErrRemoteUnavailable", err)
		}
	}
	if valeurObtenue := server.Connections(); valeurObtenue != 2 {
		t.Fatalf("connections: valeurObtenue %d, valeurAttendue 2 (one reconnect in the stage)", valeurObtenue)
	}
	if valeurObtenue := volume.State(); valeurObtenue != "broken" {
		t.Fatalf("valeurObtenue %q, valeurAttendue broken", valeurObtenue)
	}

	volume.ResetForStage()
	if valeurObtenue := volume.State(); valeurObtenue != "unknown" {
		t.Fatalf("after reset: valeurObtenue %q, valeurAttendue unknown", valeurObtenue)
	}
	if err := volume.Probe(ctx); err != nil {
		t.Fatalf("next stage: valeurObtenue %v, valeurAttendue a reconnect", err)
	}
	if valeurObtenue := server.Connections(); valeurObtenue != 3 || volume.State() != "ok" {
		t.Fatalf("valeurObtenue %d connections, state %q, valeurAttendue 3, ok", valeurObtenue, volume.State())
	}
}

func TestSFTP_failedReconnectKeepsVolumeBrokenForTheStage(t *testing.T) {
	server := sftptest.Start(t)
	provider := pinnedProvider(t, server)
	volume := dialVolume(t, provider, testDeps())
	sftptest.WriteFile(t, provider.KnownHosts, []byte(otherHostKeyLine(t, knownhosts.Normalize(server.Address))+"\n"), 0o600)

	breakConnection(t, server, volume)
	err := volume.Probe(context.Background())

	if !errors.Is(err, fsys.ErrRemoteUnavailable) || !errors.Is(err, sftpfs.ErrHostKeyMismatch) {
		t.Fatalf("valeurObtenue %v, valeurAttendue the reread known_hosts refusing the key", err)
	}
	if err := volume.Probe(context.Background()); !errors.Is(err, fsys.ErrRemoteUnavailable) || server.Connections() != 2 {
		t.Fatalf("valeurObtenue %v after %d connections, valeurAttendue no further dial", err, server.Connections())
	}
	if volume.State() != "broken" {
		t.Fatalf("valeurObtenue %q, valeurAttendue broken", volume.State())
	}
}

func TestVolume_isSafeForConcurrentCallers(t *testing.T) {
	server := sftptest.Start(t)
	volume := dialVolume(t, pinnedProvider(t, server), testDeps())
	done := make(chan struct{})

	for range 8 {
		go func() {
			defer func() { done <- struct{}{} }()
			for range 20 {
				_ = volume.Probe(context.Background())
				_ = volume.State()
				_ = volume.Warnings()
			}
		}()
	}
	go func() {
		defer func() { done <- struct{}{} }()
		for range 5 {
			server.DropConnections()
			volume.ResetForStage()
			time.Sleep(5 * time.Millisecond)
		}
	}()
	for range 9 {
		<-done
	}
}

func TestVolume_closeStopsEveryOperation(t *testing.T) {
	server := sftptest.Start(t)
	volume := dialVolume(t, pinnedProvider(t, server), testDeps())

	if err := volume.Close(); err != nil {
		t.Fatal(err)
	}
	volume.ResetForStage()

	if err := volume.Probe(context.Background()); !errors.Is(err, fsys.ErrRemoteUnavailable) || server.Connections() != 1 {
		t.Fatalf("valeurObtenue %v after %d connections, valeurAttendue ErrRemoteUnavailable without dialing", err, server.Connections())
	}
}

func TestCode_namesEachConnectionError(t *testing.T) {
	for valeurAttendue, err := range map[string]error{
		"host_key_unknown": sftpfs.ErrHostKeyUnknown, "host_key_mismatch": sftpfs.ErrHostKeyMismatch,
		"auth_refused": sftpfs.ErrAuthRefused, "": errors.New("other"),
	} {
		if valeurObtenue := sftpfs.Code(errors.Join(fsys.ErrRemoteUnavailable, err)); valeurObtenue != valeurAttendue {
			t.Fatalf("valeurObtenue %q, valeurAttendue %q", valeurObtenue, valeurAttendue)
		}
	}
}

func TestConnect_unreadableKnownHostsHasAStableCode(t *testing.T) {
	server := sftptest.Start(t)
	invalid := filepath.Join(t.TempDir(), "known_hosts")
	sftptest.WriteFile(t, invalid, []byte("not a known_hosts line\n"), 0o600)

	for label, knownHosts := range map[string]string{
		"missing": filepath.Join(t.TempDir(), "absent"), "invalid": invalid,
	} {
		_, err := sftpfs.Dial(context.Background(), providerFor(server, knownHosts), testDeps())

		if !errors.Is(err, sftpfs.ErrKnownHostsUnreadable) || sftpfs.Code(err) != "known_hosts_unreadable" {
			t.Fatalf("%s: valeurObtenue %v (code %q), valeurAttendue known_hosts_unreadable", label, err, sftpfs.Code(err))
		}
		if errors.Is(err, fsys.ErrRemoteUnavailable) {
			t.Fatalf("%s: valeurObtenue %v, valeurAttendue a configuration refusal, not an unavailable volume", label, err)
		}
	}
	if valeurObtenue := server.Connections(); valeurObtenue != 0 {
		t.Fatalf("connections: valeurObtenue %d, valeurAttendue 0", valeurObtenue)
	}
}

func TestVolume_operationCutOffByCloseIsRemoteUnavailable(t *testing.T) {
	server := sftptest.Start(t, sftptest.StallMethod("Stat"))
	volume := dialVolume(t, pinnedProvider(t, server), testDeps())
	returned := make(chan error, 1)
	go func() { returned <- volume.Probe(context.Background()) }()
	time.Sleep(100 * time.Millisecond)

	if err := volume.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-returned:
		if !errors.Is(err, fsys.ErrRemoteUnavailable) {
			t.Fatalf("valeurObtenue %v, valeurAttendue ErrRemoteUnavailable", err)
		}
	case <-time.After(settleTimeout):
		t.Fatal("valeurAttendue the operation released by Close")
	}
}
