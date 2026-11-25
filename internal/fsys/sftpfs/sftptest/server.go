// Package sftptest démarre, pour les tests seulement, un serveur SSH + SFTP en processus sur la boucle locale : clés
// d'hôte et de client générées à chaque test, dossier servi temporaire, journal de chaque requête. Jamais importé
// par du code de production (garde dans fsystest).
package sftptest

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// loopbackAddress : le serveur n'écoute jamais ailleurs que sur la boucle locale, port choisi par le système.
const loopbackAddress = "127.0.0.1:0"

// Request est une requête reçue, dans l'ordre d'arrivée. Method reprend le nom de pkg/sftp (Get, Put, Open, Stat,
// Lstat, List, Readlink, Setstat, Mkdir, Rmdir, Remove, Rename, PosixRename, Link, Symlink, StatVFS), plus RealPath
// et Exec (Path est alors la commande). Path est le chemin visé, Target le second chemin : nouveau nom de Rename,
// PosixRename et Link, texte de la cible pour Symlink (Path est alors le lien créé), tels que le serveur les applique.
type Request struct {
	Method, Path, Target string
}

// ExecHandler sert une requête exec : commande reçue et entrée standard complète, sorties et code de sortie rendus.
type ExecHandler func(command string, stdin []byte) (stdout, stderr []byte, exit int)

// Option règle le comportement du serveur.
type Option func(*settings)

type settings struct {
	hiddenExtensions    map[string]bool
	stalledMethods      map[string]bool
	reversedSymlinkArgs bool
	windowsRoot         bool
	execHandler         ExecHandler
}

// WithoutExtension retire une extension SFTP (posix-rename@openssh.com, hardlink@openssh.com, statvfs@openssh.com)
// de celles annoncées, et refuse la requête qui l'emploie (enregistrée quand même). copy-data n'est jamais servi.
func WithoutExtension(name string) Option {
	return func(current *settings) { current.hiddenExtensions[name] = true }
}

// WithoutStatVFS simule un serveur sans statvfs@openssh.com.
func WithoutStatVFS() Option { return WithoutExtension(extensionStatVFS) }

// StallMethod fige toute requête de cette méthode (nom de Request.Method) jusqu'à la fermeture de la connexion du
// client ou la fin du test : une session bloquée, pour les tests de délai.
func StallMethod(method string) Option {
	return func(current *settings) { current.stalledMethods[method] = true }
}

// ReversedSymlinkArgs lit les deux arguments de SSH_FXP_SYMLINK dans l'ordre inverse de celui d'OpenSSH (que suit le
// client pkg/sftp), comme certains serveurs qui suivent la RFC : la sonde doit le détecter.
func ReversedSymlinkArgs() Option {
	return func(current *settings) { current.reversedSymlinkArgs = true }
}

// WindowsRoot simule un serveur Windows : les chemins vus par le client commencent par « /C: ».
func WindowsRoot() Option {
	return func(current *settings) { current.windowsRoot = true }
}

// WithExec sert les requêtes exec par handler ; sans cette option, exec est refusé.
func WithExec(handler ExecHandler) Option {
	return func(current *settings) { current.execHandler = handler }
}

// DenyExec refuse les requêtes exec (enregistrées quand même), même après WithExec.
func DenyExec() Option {
	return func(current *settings) { current.execHandler = nil }
}

// Server est un serveur en cours. Address est « 127.0.0.1:port » ; HostKeyLine une ligne known_hosts de sa clé
// d'hôte ; Root le dossier servi ; ClientKeyFile la clé privée (0600) que le serveur autorise.
type Server struct {
	Address, HostKeyLine, Root, ClientKeyFile string

	settings settings
	root     *os.Root
	listener net.Listener
	config   *ssh.ServerConfig
	stopped  chan struct{}
	routines sync.WaitGroup

	mutex       sync.Mutex
	closed      bool
	requests    []Request
	connections map[net.Conn]bool
}

// Start démarre un serveur arrêté par t.Cleanup : écoute, connexions, sessions et requêtes figées compris.
func Start(t *testing.T, options ...Option) *Server {
	t.Helper()
	server := &Server{
		Root: t.TempDir(), stopped: make(chan struct{}), connections: map[net.Conn]bool{},
		settings: settings{hiddenExtensions: map[string]bool{}, stalledMethods: map[string]bool{}},
	}
	for _, option := range options {
		option(&server.settings)
	}
	clientKey, err := server.writeClientKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := server.listen(clientKey); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.close)
	server.routines.Add(1)
	go server.acceptConnections()
	return server
}

// writeClientKey génère la clé du client et l'écrit au format OpenSSH ; rend sa clé publique.
func (server *Server) writeClientKey(directory string) (ssh.PublicKey, error) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(privateKey, "")
	if err != nil {
		return nil, err
	}
	server.ClientKeyFile = filepath.Join(directory, "client_ed25519")
	if err := os.WriteFile(server.ClientKeyFile, pem.EncodeToMemory(block), 0o600); err != nil {
		return nil, err
	}
	return ssh.NewPublicKey(publicKey)
}

// listen génère la clé d'hôte, ouvre le dossier servi et la socket de boucle locale.
func (server *Server) listen(clientKey ssh.PublicKey) error {
	_, hostPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPrivateKey)
	if err != nil {
		return err
	}
	server.config = &ssh.ServerConfig{PublicKeyCallback: authorizeOnly(clientKey)}
	server.config.AddHostKey(hostSigner)
	if server.root, err = os.OpenRoot(server.Root); err != nil {
		return err
	}
	if server.listener, err = net.Listen("tcp", loopbackAddress); err != nil {
		return errors.Join(err, server.root.Close())
	}
	server.Address = server.listener.Addr().String()
	server.HostKeyLine = knownhosts.Line([]string{knownhosts.Normalize(server.Address)}, hostSigner.PublicKey())
	return nil
}

var errUnauthorizedKey = errors.New("sftptest: unknown client key")

func authorizeOnly(clientKey ssh.PublicKey) func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
	return func(_ ssh.ConnMetadata, offeredKey ssh.PublicKey) (*ssh.Permissions, error) {
		if !bytes.Equal(offeredKey.Marshal(), clientKey.Marshal()) {
			return nil, errUnauthorizedKey
		}
		return &ssh.Permissions{}, nil
	}
}

// Requests rend une copie du journal des requêtes.
func (server *Server) Requests() []Request {
	server.mutex.Lock()
	defer server.mutex.Unlock()
	return append([]Request(nil), server.requests...)
}

func (server *Server) record(request Request) {
	server.mutex.Lock()
	defer server.mutex.Unlock()
	server.requests = append(server.requests, request)
}

// close arrête l'écoute, libère les requêtes figées, ferme chaque connexion et attend toutes les goroutines.
func (server *Server) close() {
	server.mutex.Lock()
	server.closed = true
	openConnections := make([]net.Conn, 0, len(server.connections))
	for connection := range server.connections {
		openConnections = append(openConnections, connection)
	}
	server.mutex.Unlock()
	close(server.stopped)
	_ = server.listener.Close()
	for _, connection := range openConnections {
		_ = connection.Close()
	}
	server.routines.Wait()
	_ = server.root.Close()
}

func (server *Server) acceptConnections() {
	defer server.routines.Done()
	for {
		connection, err := server.listener.Accept()
		if err != nil {
			return
		}
		if !server.track(connection) {
			_ = connection.Close()
			return
		}
		server.routines.Add(1)
		go server.serveConnection(connection)
	}
}

// track retient une connexion pour la fermer à l'arrêt ; faux si l'arrêt a déjà commencé.
func (server *Server) track(connection net.Conn) bool {
	server.mutex.Lock()
	defer server.mutex.Unlock()
	if server.closed {
		return false
	}
	server.connections[connection] = true
	return true
}

func (server *Server) forget(connection net.Conn) {
	server.mutex.Lock()
	defer server.mutex.Unlock()
	delete(server.connections, connection)
}

func (server *Server) serveConnection(connection net.Conn) {
	defer server.routines.Done()
	defer server.forget(connection)
	defer func() { _ = connection.Close() }()
	sshConnection, channels, globalRequests, err := ssh.NewServerConn(connection, server.config)
	if err != nil {
		return
	}
	defer func() { _ = sshConnection.Close() }()
	// keepalive@openssh.com et toute requête globale reçoivent un refus : une réponse, donc une session vivante.
	server.goTracked(func() { ssh.DiscardRequests(globalRequests) })
	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "only session channels")
			continue
		}
		channel, channelRequests, err := newChannel.Accept()
		if err != nil {
			continue
		}
		server.goTracked(func() { server.serveSession(channel, channelRequests) })
	}
}

func (server *Server) goTracked(routine func()) {
	server.routines.Add(1)
	go func() {
		defer server.routines.Done()
		routine()
	}()
}
