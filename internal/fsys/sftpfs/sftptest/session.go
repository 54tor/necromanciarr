package sftptest

import (
	"encoding/binary"
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
func (server *Server) serveSession(channel ssh.Channel, requests <-chan *ssh.Request, ended <-chan struct{}) {
	defer func() { _ = channel.Close() }()
	for request := range requests {
		var payload stringPayload
		_ = ssh.Unmarshal(request.Payload, &payload)
		switch {
		case request.Type == "subsystem" && payload.Value == "sftp":
			_ = request.Reply(true, nil)
			server.goTracked(func() { ssh.DiscardRequests(requests) })
			server.serveSFTP(channel, ended)
			return
		case request.Type == "exec":
			server.record(Request{Method: "Exec", Path: payload.Value})
			handler := server.execHandler()
			_ = request.Reply(handler != nil, nil)
			if handler == nil {
				continue
			}
			server.serveExec(channel, requests, payload.Value, handler)
			return
		default:
			_ = request.Reply(false, nil)
		}
	}
}

func (server *Server) serveSFTP(channel ssh.Channel, ended <-chan struct{}) {
	handler := &fileHandler{server: server, ended: ended}
	handlers := sftp.Handlers{FileGet: handler, FilePut: handler, FileCmd: handler, FileList: handler}
	filter := &extensionFilter{Channel: channel, hidden: server.settings.hiddenExtensions, server: server}
	requestServer := sftp.NewRequestServer(filter, handlers)
	_ = requestServer.Serve()
	_ = requestServer.Close()
}

// exitStatus borne un code de sortie à l'intervalle d'un processus POSIX ; hors bornes, il devient maxExitStatus.
func exitStatus(exit int) uint32 {
	if exit < 0 || exit > maxExitStatus {
		return maxExitStatus
	}
	return uint32(exit)
}

// extensionFilter retire les extensions masquées du paquet VERSION, le premier écrit par le serveur : pkg/sftp
// n'offre qu'un réglage global (SetSFTPExtensions), qui changerait tous les serveurs d'un même test. Il lit aussi
// chaque paquet reçu pour journaliser les requêtes étendues que pkg/sftp refuserait sans appeler le gestionnaire.
type extensionFilter struct {
	ssh.Channel
	hidden      map[string]bool
	versionSent bool
	server      *Server
	// inbound garde le début d'un paquet reçu pas encore complet ; inspectionStopped : un paquet démesuré a fait perdre
	// le fil des paquets, plus rien n'est inspecté sur ce canal.
	inbound           []byte
	inspectionStopped bool
}

// ExtendedMethodPrefix préfixe Request.Method d'une requête étendue (SSH_FXP_EXTENDED) que pkg/sftp ne sert pas
// (« Extended:fsync@openssh.com ») ; ces méthodes ne sont pas dans Methods, un classement doit les refuser.
const ExtendedMethodPrefix = "Extended:"

const (
	// sftpExtendedPacket est le type SSH_FXP_EXTENDED ; sa charge commence par l'identifiant (4 octets) et le nom.
	sftpExtendedPacket = 200
	// maxInspectedPacket borne un paquet attendu en entier ; au-delà, l'inspection s'arrête (pkg/sftp le refuse).
	maxInspectedPacket = 1 << 20
)

// servedExtendedRequests sont servies par pkg/sftp et enregistrées sous leur méthode (PosixRename, StatVFS, Link).
var servedExtendedRequests = map[string]bool{extensionPosixRename: true, extensionStatVFS: true, extensionHardlink: true}

func (filter *extensionFilter) Read(buffer []byte) (int, error) {
	readCount, err := filter.Channel.Read(buffer)
	if !filter.inspectionStopped && readCount > 0 {
		filter.inbound = append(filter.inbound, buffer[:readCount]...)
		filter.recordUnknownExtended()
	}
	return readCount, err
}

// recordUnknownExtended journalise chaque requête étendue complète dont pkg/sftp n'a pas de méthode.
func (filter *extensionFilter) recordUnknownExtended() {
	for len(filter.inbound) >= 4 {
		packetLength := int64(binary.BigEndian.Uint32(filter.inbound))
		if packetLength > maxInspectedPacket {
			filter.inbound, filter.inspectionStopped = nil, true
			return
		}
		if int64(len(filter.inbound)-4) < packetLength {
			return
		}
		packet := filter.inbound[4 : 4+packetLength]
		if len(packet) > 5 && packet[0] == sftpExtendedPacket {
			if _, name, _, ok := cutSSHString(packet[5:]); ok && !servedExtendedRequests[string(name)] {
				filter.server.record(Request{Method: ExtendedMethodPrefix + string(name)})
			}
		}
		filter.inbound = append([]byte(nil), filter.inbound[4+packetLength:]...)
	}
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

// execHandler rend le handler d'exec réglé (WithExec, sinon WithGNUExec), précédé de BeforeExec et filtré par
// WithExecOutputFilter ; nil : exec refusé.
func (server *Server) execHandler() ExecHandler {
	handler := server.settings.execHandler
	if handler == nil && server.settings.gnuExec {
		handler = server.gnuExec
	}
	filter, before := server.settings.execOutputFilter, server.settings.beforeExec
	if handler == nil || (filter == nil && before == nil) {
		return handler
	}
	return func(command string, stdin []byte) ([]byte, []byte, int) {
		if before != nil {
			before(command)
		}
		stdout, stderr, exit := handler(command, stdin)
		if filter != nil {
			stdout = filter(command, stdout)
		}
		return stdout, stderr, exit
	}
}
