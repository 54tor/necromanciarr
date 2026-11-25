package sftptest

import (
	"encoding/binary"
	"io"
	"math"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const (
	extensionPosixRename = "posix-rename@openssh.com"
	extensionHardlink    = "hardlink@openssh.com"
	extensionStatVFS     = "statvfs@openssh.com"
	// sftpVersionPacket est le type SSH_FXP_VERSION : la réponse du serveur à INIT, qui annonce les extensions.
	sftpVersionPacket = 2
	// versionHeaderLength : longueur (4), type (1) et version (4) avant la liste des extensions.
	versionHeaderLength = 9
	// maxExitStatus est le plus grand code de sortie d'un processus POSIX.
	maxExitStatus = 255
)

// stringPayload est la charge d'une requête subsystem ou exec : une seule chaîne SSH.
type stringPayload struct{ Value string }

// serveSession attend la première requête subsystem « sftp » ou exec ; les autres sont refusées.
func (server *Server) serveSession(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer func() { _ = channel.Close() }()
	for request := range requests {
		var payload stringPayload
		_ = ssh.Unmarshal(request.Payload, &payload)
		switch {
		case request.Type == "subsystem" && payload.Value == "sftp":
			_ = request.Reply(true, nil)
			server.goTracked(func() { ssh.DiscardRequests(requests) })
			server.serveSFTP(channel)
			return
		case request.Type == "exec":
			server.record(Request{Method: "Exec", Path: payload.Value})
			handler := server.settings.execHandler
			_ = request.Reply(handler != nil, nil)
			if handler == nil {
				continue
			}
			server.goTracked(func() { ssh.DiscardRequests(requests) })
			runExec(channel, payload.Value, handler)
			return
		default:
			_ = request.Reply(false, nil)
		}
	}
}

func (server *Server) serveSFTP(channel ssh.Channel) {
	handler := &fileHandler{server: server}
	handlers := sftp.Handlers{FileGet: handler, FilePut: handler, FileCmd: handler, FileList: handler}
	requestServer := sftp.NewRequestServer(&extensionFilter{Channel: channel, hidden: server.settings.hiddenExtensions}, handlers)
	_ = requestServer.Serve()
	_ = requestServer.Close()
}

// runExec lit l'entrée standard jusqu'à sa fin, appelle handler, renvoie les sorties et le code de sortie.
func runExec(channel ssh.Channel, command string, handler ExecHandler) {
	stdin, err := io.ReadAll(channel)
	if err != nil {
		return
	}
	stdout, stderr, exit := handler(command, stdin)
	_, _ = channel.Write(stdout)
	_, _ = channel.Stderr().Write(stderr)
	_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{exitStatus(exit)}))
}

// exitStatus borne un code de sortie à l'intervalle d'un processus POSIX ; hors bornes, il devient maxExitStatus.
func exitStatus(exit int) uint32 {
	if exit < 0 || exit > maxExitStatus {
		return maxExitStatus
	}
	return uint32(exit)
}

// extensionFilter retire les extensions masquées du paquet VERSION, le premier écrit par le serveur : pkg/sftp
// n'offre qu'un réglage global (SetSFTPExtensions), qui changerait tous les serveurs d'un même test.
type extensionFilter struct {
	ssh.Channel
	hidden      map[string]bool
	versionSent bool
}

func (filter *extensionFilter) Write(packet []byte) (int, error) {
	if filter.versionSent || len(filter.hidden) == 0 {
		return filter.Channel.Write(packet)
	}
	filter.versionSent = true
	if _, err := filter.Channel.Write(withoutHiddenExtensions(packet, filter.hidden)); err != nil {
		return 0, err
	}
	return len(packet), nil
}

// withoutHiddenExtensions réécrit un paquet VERSION complet sans les extensions masquées, en recopiant les champs
// gardés octet pour octet ; tout autre contenu passe tel quel.
func withoutHiddenExtensions(packet []byte, hidden map[string]bool) []byte {
	if len(packet) < versionHeaderLength || packet[4] != sftpVersionPacket {
		return packet
	}
	rewritten := append([]byte(nil), packet[:versionHeaderLength]...)
	remaining := packet[versionHeaderLength:]
	for len(remaining) > 0 {
		nameField, name, afterName, nameOK := cutSSHString(remaining)
		dataField, _, afterData, dataOK := cutSSHString(afterName)
		if !nameOK || !dataOK {
			return packet
		}
		if !hidden[string(name)] {
			rewritten = append(append(rewritten, nameField...), dataField...)
		}
		remaining = afterData
	}
	packetLength := len(rewritten) - 4
	if packetLength > math.MaxUint32 {
		return packet
	}
	binary.BigEndian.PutUint32(rewritten, uint32(packetLength))
	return rewritten
}

// cutSSHString lit une chaîne SSH (longueur sur 4 octets puis valeur) : le champ entier, la valeur et la suite.
func cutSSHString(buffer []byte) (field, value, rest []byte, ok bool) {
	if len(buffer) < 4 {
		return nil, nil, nil, false
	}
	valueLength := int64(binary.BigEndian.Uint32(buffer))
	if valueLength > int64(len(buffer)-4) {
		return nil, nil, nil, false
	}
	end := 4 + int(valueLength)
	return buffer[:end], buffer[4:end], buffer[end:], true
}
