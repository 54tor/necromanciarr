package sftptest_test

import (
	"slices"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"necromanciarr/internal/fsys/sftpfs/sftptest"
)

// waitForExecSession attend que la première session exec du journal vérifie condition.
func waitForExecSession(t *testing.T, server *sftptest.Server, condition func(sftptest.ExecSession) bool) sftptest.ExecSession {
	t.Helper()
	deadline := time.Now().Add(settleTimeout)
	for time.Now().Before(deadline) {
		if sessions := server.ExecSessions(); len(sessions) > 0 && condition(sessions[0]) {
			return sessions[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("valeurObtenue %+v : condition jamais remplie", server.ExecSessions())
	return sftptest.ExecSession{}
}

func TestServer_execSessionsRecordStdinAndEnd(t *testing.T) {
	server := sftptest.Start(t, sftptest.WithExec(func(string, []byte) ([]byte, []byte, int) { return []byte("out"), nil, 0 }))

	accepted, stdout, _ := execOutput(t, connectSSH(t, server), "stat --version", []byte("a\x00b\x00"))

	if !accepted || string(stdout) != "out" {
		t.Fatalf("valeurObtenue accepted %v, stdout %q", accepted, stdout)
	}
	gotValue := waitForExecSession(t, server, func(recorded sftptest.ExecSession) bool { return recorded.Ended })
	if gotValue.Command != "stat --version" || string(gotValue.Stdin) != "a\x00b\x00" || !gotValue.StdinReceived {
		t.Fatalf("valeurObtenue %+v", gotValue)
	}
}

func TestServer_stallExecWaitsForTheClientAndRecordsSignals(t *testing.T) {
	handlerCalled := make(chan struct{}, 1)
	server := sftptest.Start(t, sftptest.StallExec("frozen"), sftptest.WithExec(func(string, []byte) ([]byte, []byte, int) {
		handlerCalled <- struct{}{}
		return nil, nil, 0
	}))
	channel, requests, err := connectSSH(t, server).OpenChannel("session", nil)
	if err != nil {
		t.Fatal(err)
	}
	go ssh.DiscardRequests(requests)
	if accepted, err := channel.SendRequest("exec", true, ssh.Marshal(struct{ Value string }{"frozen"})); err != nil || !accepted {
		t.Fatalf("exec: accepted %v, err %v", accepted, err)
	}
	if _, err := channel.Write([]byte("input")); err != nil {
		t.Fatal(err)
	}
	if err := channel.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	waitForExecSession(t, server, func(recorded sftptest.ExecSession) bool { return recorded.StdinReceived })
	if _, err := channel.SendRequest("signal", false, ssh.Marshal(struct{ Value string }{"KILL"})); err != nil {
		t.Fatal(err)
	}

	signaled := waitForExecSession(t, server, func(recorded sftptest.ExecSession) bool {
		return slices.Contains(recorded.Signals, "KILL")
	})
	if signaled.Ended {
		t.Fatalf("valeurObtenue %+v, valeurAttendue une session encore ouverte après le signal", signaled)
	}
	_ = channel.Close()
	waitForExecSession(t, server, func(recorded sftptest.ExecSession) bool { return recorded.Ended })
	select {
	case <-handlerCalled:
		t.Fatal("valeurAttendue aucun appel du handler pour une commande figée")
	default:
	}
}
