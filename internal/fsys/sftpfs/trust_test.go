package sftpfs_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/crypto/ssh/knownhosts"

	"necromanciarr/internal/config"
	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/sftpfs"
	"necromanciarr/internal/fsys/sftpfs/sftptest"
)

func fetchHostKey(t *testing.T, provider config.FSProvider) sftpfs.HostKey {
	t.Helper()
	hostKey, err := sftpfs.FetchHostKey(context.Background(), provider)
	if err != nil {
		t.Fatal(err)
	}
	return hostKey
}

func readKnownHosts(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func TestFetchHostKey_capturesTheKeyWithoutAnyAuthentication(t *testing.T) {
	server := sftptest.Start(t)
	provider := providerFor(server, filepath.Join(t.TempDir(), "known_hosts"))

	hostKey := fetchHostKey(t, provider)

	if gotValue := knownhosts.Line([]string{server.Address}, hostKey.Key); gotValue != server.HostKeyLine {
		t.Fatalf("gotValue %q, expectedValue %q", gotValue, server.HostKeyLine)
	}
	if attempts := server.AuthAttempts(); len(attempts) != 0 {
		t.Fatalf("gotValue %+v, expectedValue no authentication attempt", attempts)
	}
	if fingerprint := hostKey.Fingerprint(); len(fingerprint) < len("SHA256:") || fingerprint[:7] != "SHA256:" {
		t.Fatalf("gotValue %q, expectedValue a SHA-256 fingerprint", fingerprint)
	}
}

func TestFetchHostKey_unreachableServerIsRemoteUnavailable(t *testing.T) {
	server := sftptest.Start(t)
	provider := providerFor(server, filepath.Join(t.TempDir(), "known_hosts"))
	provider.Address = closedAddress(t)

	_, err := sftpfs.FetchHostKey(context.Background(), provider)

	if !errors.Is(err, fsys.ErrRemoteUnavailable) {
		t.Fatalf("gotValue %v, expectedValue fsys.ErrRemoteUnavailable", err)
	}
}

func TestTrustHostKey_writesNothingWithoutConfirmation(t *testing.T) {
	server := sftptest.Start(t)
	knownHosts := knownHostsFile(t, otherHostKeyLine(t, "unrelated.example.com"))
	provider := providerFor(server, knownHosts)
	before := readKnownHosts(t, knownHosts)

	outcome, err := sftpfs.TrustHostKey(provider, fetchHostKey(t, provider), false)

	if err != nil || outcome != sftpfs.TrustPending {
		t.Fatalf("gotValue %q, %v, expectedValue %q", outcome, err, sftpfs.TrustPending)
	}
	if gotValue := readKnownHosts(t, knownHosts); !bytes.Equal(gotValue, before) {
		t.Fatalf("gotValue %q, expectedValue the file unchanged %q", gotValue, before)
	}
}

func TestTrustHostKey_appendsOneLineAfterTheExistingEntries(t *testing.T) {
	server := sftptest.Start(t)
	existing := otherHostKeyLine(t, "unrelated.example.com")
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	sftptest.WriteFile(t, knownHosts, []byte(existing), 0o600)
	provider := providerFor(server, knownHosts)

	outcome, err := sftpfs.TrustHostKey(provider, fetchHostKey(t, provider), true)

	expectedValue := existing + "\n" + server.HostKeyLine + "\n"
	if gotValue := string(readKnownHosts(t, knownHosts)); err != nil || outcome != sftpfs.TrustAdded || gotValue != expectedValue {
		t.Fatalf("gotValue %q, %q, %v, expectedValue %q added", gotValue, outcome, err, expectedValue)
	}
	if _, err := sftpfs.Dial(context.Background(), provider, testDeps()); err != nil {
		t.Fatalf("gotValue %v, expectedValue a dial accepted with the added line", err)
	}
}

func TestTrustHostKey_identicalKeyIsNeverAddedTwice(t *testing.T) {
	server := sftptest.Start(t)
	provider := pinnedProvider(t, server)
	before := readKnownHosts(t, provider.KnownHosts)

	outcome, err := sftpfs.TrustHostKey(provider, fetchHostKey(t, provider), true)

	if err != nil || outcome != sftpfs.TrustAlreadyPinned {
		t.Fatalf("gotValue %q, %v, expectedValue %q", outcome, err, sftpfs.TrustAlreadyPinned)
	}
	if gotValue := readKnownHosts(t, provider.KnownHosts); !bytes.Equal(gotValue, before) {
		t.Fatalf("gotValue %q, expectedValue no duplicate line", gotValue)
	}
}

func TestTrustHostKey_neverReplacesADifferentPinnedKey(t *testing.T) {
	server := sftptest.Start(t)
	knownHosts := knownHostsFile(t, otherHostKeyLine(t, knownhosts.Normalize(server.Address)))
	provider := providerFor(server, knownHosts)
	before := readKnownHosts(t, knownHosts)

	for _, confirmed := range []bool{false, true} {
		outcome, err := sftpfs.TrustHostKey(provider, fetchHostKey(t, provider), confirmed)

		if !errors.Is(err, sftpfs.ErrHostKeyMismatch) || sftpfs.Code(err) != sftpfs.CodeHostKeyMismatch {
			t.Fatalf("confirmed %v: gotValue %q, %v, expectedValue host_key_mismatch", confirmed, outcome, err)
		}
	}
	if gotValue := readKnownHosts(t, knownHosts); !bytes.Equal(gotValue, before) {
		t.Fatalf("gotValue %q, expectedValue the pinned key kept alone", gotValue)
	}
}

func TestTrustHostKey_createsAMissingFileReadableByItsOwnerOnly(t *testing.T) {
	server := sftptest.Start(t)
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	provider := providerFor(server, knownHosts)

	outcome, err := sftpfs.TrustHostKey(provider, fetchHostKey(t, provider), true)

	if gotValue := string(readKnownHosts(t, knownHosts)); err != nil || outcome != sftpfs.TrustAdded || gotValue != server.HostKeyLine+"\n" {
		t.Fatalf("gotValue %q, %q, %v, expectedValue the host key line", gotValue, outcome, err)
	}
	info, err := os.Stat(knownHosts)
	if err != nil {
		t.Fatal(err)
	}
	if gotValue := info.Mode().Perm(); runtime.GOOS != "windows" && gotValue != 0o600 {
		t.Fatalf("gotValue %v, expectedValue 0600", gotValue)
	}
}

func TestTrustHostKey_neverCreatesTheParentFolder(t *testing.T) {
	server := sftptest.Start(t)
	missingParent := filepath.Join(t.TempDir(), "absent")
	provider := providerFor(server, filepath.Join(missingParent, "known_hosts"))

	_, err := sftpfs.TrustHostKey(provider, fetchHostKey(t, provider), true)

	if !errors.Is(err, sftpfs.ErrKnownHostsNotWritable) || sftpfs.Code(err) != sftpfs.CodeKnownHostsNotWritable {
		t.Fatalf("gotValue %v, expectedValue known_hosts_not_writable", err)
	}
	if _, statErr := os.Stat(missingParent); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("gotValue %v, expectedValue the parent folder still absent", statErr)
	}
}

func TestTrustHostKey_refusesASymlinkedFileAndLeavesItsTargetAlone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	server := sftptest.Start(t)
	directory := t.TempDir()
	target := filepath.Join(directory, "elsewhere")
	sftptest.WriteFile(t, target, []byte("kept\n"), 0o600)
	knownHosts := filepath.Join(directory, "known_hosts")
	sftptest.Symlink(t, target, knownHosts)
	provider := providerFor(server, knownHosts)

	_, err := sftpfs.TrustHostKey(provider, fetchHostKey(t, provider), true)

	if !errors.Is(err, sftpfs.ErrKnownHostsNotWritable) {
		t.Fatalf("gotValue %v, expectedValue known_hosts_not_writable for a symlink", err)
	}
	if gotValue := string(readKnownHosts(t, target)); gotValue != "kept\n" {
		t.Fatalf("gotValue %q, expectedValue the symlink target unchanged", gotValue)
	}
}
