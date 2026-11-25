package sftptest

import (
	"io"

	"golang.org/x/crypto/ssh"
)

// signalRequest est la requête « signal » d'un canal de session (RFC 4254 §6.9) : le nom du signal sans « SIG ».
const signalRequest = "signal"

// ExecSession est une requête exec acceptée, dans l'ordre d'arrivée : Command la ligne reçue, Stdin l'entrée standard
// complète (StdinReceived une fois sa fin lue), Signals les signaux reçus sur le canal (« KILL »), Ended vrai quand le
// canal est fermé, d'un côté ou de l'autre.
type ExecSession struct {
	Command       string
	Stdin         []byte
	StdinReceived bool
	Signals       []string
	Ended         bool
}

// ExecSessions rend une copie du journal des requêtes exec acceptées.
func (server *Server) ExecSessions() []ExecSession {
	server.mutex.Lock()
	defer server.mutex.Unlock()
	sessions := make([]ExecSession, 0, len(server.execSessions))
	for _, recorded := range server.execSessions {
		recorded.Stdin = append([]byte(nil), recorded.Stdin...)
		recorded.Signals = append([]string(nil), recorded.Signals...)
		sessions = append(sessions, recorded)
	}
	return sessions
}

// openExecSession note une requête exec acceptée et rend son rang dans le journal.
func (server *Server) openExecSession(command string) int {
	server.mutex.Lock()
	defer server.mutex.Unlock()
	server.execSessions = append(server.execSessions, ExecSession{Command: command})
	return len(server.execSessions) - 1
}

func (server *Server) updateExecSession(sessionIndex int, update func(recorded *ExecSession)) {
	server.mutex.Lock()
	defer server.mutex.Unlock()
	update(&server.execSessions[sessionIndex])
}

// watchExecRequests note les signaux reçus jusqu'à la fermeture du canal, puis ferme ended.
func (server *Server) watchExecRequests(sessionIndex int, requests <-chan *ssh.Request, ended chan<- struct{}) {
	defer close(ended)
	for request := range requests {
		if request.Type == signalRequest {
			var payload stringPayload
			_ = ssh.Unmarshal(request.Payload, &payload)
			server.updateExecSession(sessionIndex, func(recorded *ExecSession) {
				recorded.Signals = append(recorded.Signals, payload.Value)
			})
		}
		if request.WantReply {
			_ = request.Reply(false, nil)
		}
	}
	server.updateExecSession(sessionIndex, func(recorded *ExecSession) { recorded.Ended = true })
}

// serveExec lit l'entrée standard jusqu'à sa fin, puis, sauf commande figée (StallExec), appelle handler et renvoie
// les sorties et le code de sortie. Une commande figée attend la fermeture du canal par le client ou la fin du test.
func (server *Server) serveExec(channel ssh.Channel, requests <-chan *ssh.Request, command string, handler ExecHandler) {
	sessionIndex := server.openExecSession(command)
	ended := make(chan struct{})
	server.goTracked(func() { server.watchExecRequests(sessionIndex, requests, ended) })
	stdin, err := io.ReadAll(channel)
	if err != nil {
		return
	}
	server.updateExecSession(sessionIndex, func(recorded *ExecSession) {
		recorded.Stdin, recorded.StdinReceived = stdin, true
	})
	if server.settings.stalledExecs[command] {
		select {
		case <-ended:
		case <-server.stopped:
		}
		return
	}
	stdout, stderr, exit := handler(command, stdin)
	_, _ = channel.Write(stdout)
	_, _ = channel.Stderr().Write(stderr)
	_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{exitStatus(exit)}))
}
