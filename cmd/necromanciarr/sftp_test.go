package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"necromanciarr/internal/app"
	"necromanciarr/internal/fsys/sftpfs"
	"necromanciarr/internal/fsys/sftpfs/sftptest"
	"necromanciarr/internal/i18n"
)

// trustConfig écrit une configuration avec un fournisseur sftp vers server, son knownHosts au chemin donné.
func trustConfig(t *testing.T, server *sftptest.Server, knownHosts string) string {
	t.Helper()
	provider := map[string]any{
		"type": "sftp", "name": "seedbox", "address": server.Address, "user": "user",
		"identityFile": server.ClientKeyFile, "knownHosts": knownHosts, "timeout": "5s",
	}
	content, err := json.Marshal(map[string]any{"fsProviders": []any{provider, map[string]any{"type": "rclone", "name": "mounted", "rc": "http://127.0.0.1:5572"}}})
	if err != nil {
		t.Fatal(err)
	}
	return writeDoctorConfig(t, string(content))
}

func TestSFTPTrust_requiresYesToWrite(t *testing.T) {
	server := sftptest.Start(t)
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(knownHosts, []byte("# operator file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(knownHosts, past, past); err != nil {
		t.Fatal(err)
	}
	configDir := trustConfig(t, server, knownHosts)

	english := runCli(t, configDir, "sftp", "trust", "seedbox")
	french := runCli(t, configDir, "sftp", "trust", "seedbox", "--language", "fr")

	content, err := os.ReadFile(knownHosts)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(knownHosts)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "# operator file\n" || !info.ModTime().Equal(past) {
		t.Fatalf("gotValue %q, mtime %v, expectedValue the file untouched", content, info.ModTime())
	}
	if english.exitCode != usageExitCode || !strings.Contains(english.stdout, "Host key fingerprint for seedbox: SHA256:") ||
		!strings.Contains(english.stdout+english.stderr, "another channel") {
		t.Fatalf("gotValue exit %d, stdout %q, stderr %q", english.exitCode, english.stdout, english.stderr)
	}
	if !strings.Contains(french.stdout, "Empreinte de la clé d'hôte de seedbox : SHA256:") || french.exitCode != usageExitCode {
		t.Fatalf("gotValue exit %d, stdout %q", french.exitCode, french.stdout)
	}
	if attempts := server.AuthAttempts(); len(attempts) != 0 {
		t.Fatalf("gotValue %+v, expectedValue no authentication attempt", attempts)
	}
}

func TestSFTPTrust_appendsKnownHostsLine(t *testing.T) {
	server := sftptest.Start(t)
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	configDir := trustConfig(t, server, knownHosts)

	first := runCli(t, configDir, "sftp", "trust", "seedbox", "--yes")
	again := runCli(t, configDir, "sftp", "trust", "seedbox", "--yes")

	content, err := os.ReadFile(knownHosts)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != server.HostKeyLine+"\n" {
		t.Fatalf("gotValue %q, expectedValue the server line once %q", content, server.HostKeyLine)
	}
	if first.exitCode != successExitCode || !strings.Contains(first.stdout, "Added to "+knownHosts+".") {
		t.Fatalf("gotValue exit %d, stdout %q, stderr %q", first.exitCode, first.stdout, first.stderr)
	}
	if again.exitCode != successExitCode || !strings.Contains(again.stdout, "already trusted") {
		t.Fatalf("gotValue exit %d, stdout %q, expectedValue already trusted", again.exitCode, again.stdout)
	}
	if attempts := server.AuthAttempts(); len(attempts) != 0 {
		t.Fatalf("gotValue %+v, expectedValue trust never authenticates", attempts)
	}
	provider := server.Provider(t, "seedbox")
	provider.KnownHosts = knownHosts
	volume, err := sftpfs.Dial(context.Background(), provider, sftpfs.Deps{LookupEnv: func(string) (string, bool) { return "", false }, Now: time.Now})
	if err != nil {
		t.Fatalf("gotValue %v, expectedValue a connection accepted with the added line", err)
	}
	_ = volume.Close()
}

// otherKeyLine épingle pour host une clé générée, qui n'est pas celle du serveur.
func otherKeyLine(t *testing.T, host string) string {
	t.Helper()
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPublicKey, err := ssh.NewPublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	return knownhosts.Line([]string{host}, sshPublicKey)
}

// closedLoopback rend une adresse de boucle locale où plus rien n'écoute.
func closedLoopback(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func TestSFTPTrust_neverReplacesADifferentPinnedKey(t *testing.T) {
	server := sftptest.Start(t)
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	pinned := strings.Replace(server.HostKeyLine, knownhosts.Normalize(server.Address), "unrelated.example.com", 1) + "\n" +
		otherKeyLine(t, knownhosts.Normalize(server.Address)) + "\n"
	if err := os.WriteFile(knownHosts, []byte(pinned), 0o600); err != nil {
		t.Fatal(err)
	}
	configDir := trustConfig(t, server, knownHosts)

	result := runCli(t, configDir, "sftp", "trust", "seedbox", "--yes")

	content, err := os.ReadFile(knownHosts)
	if err != nil {
		t.Fatal(err)
	}
	if result.exitCode != failureExitCode || !strings.Contains(result.stderr, "host_key_mismatch") || string(content) != pinned {
		t.Fatalf("gotValue exit %d, stderr %q, file %q, expectedValue host_key_mismatch and the file unchanged", result.exitCode, result.stderr, content)
	}
}

func TestSFTPTrust_refusesUnknownNonSFTPAndUnreachableProviders(t *testing.T) {
	server := sftptest.Start(t)
	configDir := trustConfig(t, server, filepath.Join(t.TempDir(), "known_hosts"))
	unreachable := trustConfig(t, &sftptest.Server{Address: closedLoopback(t), ClientKeyFile: server.ClientKeyFile}, filepath.Join(t.TempDir(), "known_hosts"))

	unknown := runCli(t, configDir, "sftp", "trust", "elsewhere", "--yes")
	notSFTP := runCli(t, configDir, "sftp", "trust", "mounted", "--yes")
	down := runCli(t, unreachable, "sftp", "trust", "seedbox", "--yes")
	missing := runCli(t, configDir, "sftp", "trust")

	if unknown.exitCode != failureExitCode || !strings.Contains(unknown.stderr, "elsewhere") {
		t.Fatalf("unknown: gotValue exit %d, stderr %q", unknown.exitCode, unknown.stderr)
	}
	if notSFTP.exitCode != failureExitCode || !strings.Contains(notSFTP.stderr, "not of type sftp") {
		t.Fatalf("rclone: gotValue exit %d, stderr %q", notSFTP.exitCode, notSFTP.stderr)
	}
	if down.exitCode != failureExitCode || !strings.Contains(down.stderr, "remote_unavailable") {
		t.Fatalf("unreachable: gotValue exit %d, stderr %q", down.exitCode, down.stderr)
	}
	if missing.exitCode != usageExitCode {
		t.Fatalf("no name: gotValue exit %d, expectedValue %d", missing.exitCode, usageExitCode)
	}
}

// cliOnlyCommands n'ont ni action du registre ni route web, chacune justifiée : sftp trust écrit
// dans le known_hosts de l'opérateur sur une empreinte vérifiée par un autre canal ; depuis un navigateur, ce serait
// un piège à hameçonnage (spec 4.C.5, Q13). Le test web garde la même liste.
var cliOnlyCommands = []string{"sftp trust"}

type routeEntry struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Action string `json:"action"`
}

func goldenRoutes(t *testing.T) []routeEntry {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "..", "internal", "web", "testdata", "routes.json"))
	if err != nil {
		t.Fatal(err)
	}
	var routes []routeEntry
	if err := json.Unmarshal(content, &routes); err != nil {
		t.Fatal(err)
	}
	return routes
}

func TestParity_everyActionHasCommandAndRoute(t *testing.T) {
	routes := goldenRoutes(t)
	actions := app.Actions(i18n.New(i18n.English, nil))
	for _, action := range actions {
		hasCommand := slices.ContainsFunc(commands, func(spec command) bool { return slices.Contains(spec.actions, action.Name) })
		hasRoute := slices.ContainsFunc(routes, func(route routeEntry) bool { return route.Action == action.Name })
		if !hasCommand || !hasRoute {
			t.Fatalf("action %q: command %v, route %v, expectedValue both", action.Name, hasCommand, hasRoute)
		}
	}
	for _, cliOnly := range cliOnlyCommands {
		isAction := slices.ContainsFunc(actions, func(action app.Action) bool { return action.Name == cliOnly })
		words := strings.Fields(cliOnly)
		hasRoute := slices.ContainsFunc(routes, func(route routeEntry) bool {
			return route.Action == cliOnly || strings.Contains(route.Path, words[len(words)-1])
		})
		if isAction || hasRoute {
			t.Fatalf("%q: action %v, route %v, expectedValue a CLI-only command with neither", cliOnly, isAction, hasRoute)
		}
		if !strings.Contains(usageText(i18n.New(i18n.English, nil)), cliOnly) {
			t.Fatalf("%q: expectedValue listed in the usage", cliOnly)
		}
	}
}

func TestSFTPTrust_outputNeverCarriesASecret(t *testing.T) {
	server := sftptest.Start(t)
	configDir := trustConfig(t, server, filepath.Join(t.TempDir(), "known_hosts"))

	result := runCli(t, configDir, "sftp", "trust", "seedbox")

	if bytes.Contains([]byte(result.stdout+result.stderr), []byte("PRIVATE KEY")) || strings.Contains(result.stdout+result.stderr, server.ClientKeyFile) {
		t.Fatalf("gotValue stdout %q, stderr %q, expectedValue no key material nor key path", result.stdout, result.stderr)
	}
}
