package sftpfs_test

import (
	"errors"
	"net"
	"testing"

	"golang.org/x/crypto/ssh/knownhosts"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/sftpfs"
	"necromanciarr/internal/fsys/sftpfs/sftptest"
)

func TestNewLazy_dialsOnlyOnFirstUse(t *testing.T) {
	server := sftptest.Start(t)
	volume := sftpfs.NewLazy(pinnedProvider(t, server), testDeps())
	t.Cleanup(func() { _ = volume.Close() })

	connectionsBeforeUse, stateBeforeUse := server.Connections(), volume.State()
	_, err := volume.Stat("/")

	if connectionsBeforeUse != 0 || stateBeforeUse != sftpfs.StateUnknown {
		t.Fatalf("valeurObtenue %d connections, state %q before any use; valeurAttendue 0, %q", connectionsBeforeUse, stateBeforeUse, sftpfs.StateUnknown)
	}
	if valeurObtenue := server.Connections(); err != nil || valeurObtenue != 1 || volume.State() != sftpfs.StateOK {
		t.Fatalf("valeurObtenue err %v, %d connections, state %q; valeurAttendue one dial and ok", err, valeurObtenue, volume.State())
	}
}

// closedAddress rend une adresse de boucle locale où plus rien n'écoute.
func closedAddress(t *testing.T) string {
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

func TestNewLazy_unreachableServerIsRemoteUnavailableUntilTheNextStage(t *testing.T) {
	server := sftptest.Start(t)
	provider := pinnedProvider(t, server)
	provider.Address = closedAddress(t)
	volume := sftpfs.NewLazy(provider, testDeps())
	t.Cleanup(func() { _ = volume.Close() })

	_, firstErr := volume.Stat("/")
	_, secondErr := volume.Stat("/")

	for attempt, err := range []error{firstErr, secondErr} {
		if !errors.Is(err, fsys.ErrRemoteUnavailable) {
			t.Fatalf("attempt %d: valeurObtenue %v, valeurAttendue fsys.ErrRemoteUnavailable", attempt, err)
		}
	}
	if valeurObtenue := volume.State(); valeurObtenue != sftpfs.StateBroken {
		t.Fatalf("state: valeurObtenue %q, valeurAttendue %q", valeurObtenue, sftpfs.StateBroken)
	}
}

func TestNewLazy_hostKeyMismatchKeepsItsStableCode(t *testing.T) {
	server := sftptest.Start(t)
	provider := providerFor(server, knownHostsFile(t, otherHostKeyLine(t, knownhosts.Normalize(server.Address))))
	volume := sftpfs.NewLazy(provider, testDeps())
	t.Cleanup(func() { _ = volume.Close() })

	_, err := volume.Stat("/")

	if valeurObtenue := sftpfs.Code(err); valeurObtenue != sftpfs.CodeHostKeyMismatch || !errors.Is(err, fsys.ErrRemoteUnavailable) {
		t.Fatalf("valeurObtenue code %q (err %v), valeurAttendue %q under fsys.ErrRemoteUnavailable", valeurObtenue, err, sftpfs.CodeHostKeyMismatch)
	}
	if requests := server.Requests(); len(requests) != 0 {
		t.Fatalf("valeurObtenue %+v, valeurAttendue no SFTP request on a refused host key", requests)
	}
}
