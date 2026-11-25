package sftpfs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"necromanciarr/internal/config"
	"necromanciarr/internal/fsys"
)

// Ce fichier est le seul de sftpfs qui écrit sur le disque local : l'ajout d'une clé d'hôte au known_hosts de
// l'opérateur (sftp trust, CLI seulement, sur confirmation explicite). Exclusion forbidigo limitée à ce fichier.

// CodeKnownHostsNotWritable : knownHosts n'est pas un fichier ordinaire modifiable dans un dossier déjà présent.
const CodeKnownHostsNotWritable = "known_hosts_not_writable"

// ErrKnownHostsNotWritable : sftp trust refuse d'écrire dans knownHosts (lien symbolique, autre chose qu'un fichier
// ordinaire, fichier remplacé pendant l'écriture, dossier parent absent, écriture refusée).
var ErrKnownHostsNotWritable = errors.New("sftpfs: knownHosts is not a regular writable file in an existing folder")

// knownHostsFileMode : un known_hosts créé n'est lisible que par son propriétaire, comme celui d'OpenSSH.
const knownHostsFileMode = 0o600

// errHostKeyCaptured interrompt la poignée de main une fois la clé lue : aucune authentification n'a lieu.
var errHostKeyCaptured = errors.New("sftpfs: host key captured, handshake stopped before authentication")

// TrustOutcome est l'issue de TrustHostKey.
type TrustOutcome string

const (
	// TrustAdded : la ligne de la clé a été ajoutée à knownHosts.
	TrustAdded TrustOutcome = "added"
	// TrustAlreadyPinned : knownHosts épingle déjà cette clé pour cet hôte ; rien n'est écrit.
	TrustAlreadyPinned TrustOutcome = "already_pinned"
	// TrustPending : l'hôte n'est pas épinglé et la confirmation manque ; rien n'est écrit.
	TrustPending TrustOutcome = "pending"
)

// HostKey est la clé d'hôte présentée par un serveur, avec le nom et l'adresse vus pendant la poignée de main :
// TrustHostKey la compare à knownHosts exactement comme le fera Dial.
type HostKey struct {
	Key      ssh.PublicKey
	hostname string
	remote   net.Addr
}

// Fingerprint rend l'empreinte SHA-256 de la clé (« SHA256:… »), celle qu'affiche ssh-keygen -lf.
func (hostKey HostKey) Fingerprint() string { return ssh.FingerprintSHA256(hostKey.Key) }

// hostKeyCapture garde la clé reçue par la goroutine d'échange de clés de x/crypto/ssh.
type hostKeyCapture struct {
	mutex   sync.Mutex
	hostKey *HostKey
}

func (capture *hostKeyCapture) callback(hostname string, remote net.Addr, key ssh.PublicKey) error {
	capture.mutex.Lock()
	defer capture.mutex.Unlock()
	capture.hostKey = &HostKey{Key: key, hostname: hostname, remote: remote}
	return errHostKeyCaptured
}

func (capture *hostKeyCapture) captured() *HostKey {
	capture.mutex.Lock()
	defer capture.mutex.Unlock()
	return capture.hostKey
}

// FetchHostKey ouvre TCP et l'échange de clés SSH, lit la clé d'hôte présentée puis coupe avant toute
// authentification : aucune méthode n'est offerte, aucun secret n'est lu ni envoyé, rien n'est écrit. Si knownHosts
// épingle déjà cet hôte, le type de clé épinglé est demandé d'abord (même choix que Dial). Échec réseau :
// fsys.ErrRemoteUnavailable.
func FetchHostKey(ctx context.Context, provider config.FSProvider) (HostKey, error) {
	capture := &hostKeyCapture{}
	clientConfig := &ssh.ClientConfig{
		HostKeyCallback: capture.callback, HostKeyAlgorithms: trustHostKeyAlgorithms(provider), Timeout: provider.Timeout,
	}
	dialContext, cancel := context.WithTimeout(ctx, provider.Timeout)
	defer cancel()
	var dialer net.Dialer
	connection, err := dialer.DialContext(dialContext, "tcp", provider.Address)
	if err != nil {
		return HostKey{}, fmt.Errorf("%w: sftpfs: %s: %w", fsys.ErrRemoteUnavailable, provider.Address, err)
	}
	defer func() { _ = connection.Close() }()
	stopWatching := context.AfterFunc(dialContext, func() { _ = connection.Close() })
	defer stopWatching()
	_, _, _, handshakeErr := ssh.NewClientConn(connection, provider.Address, clientConfig)
	hostKey := capture.captured()
	if hostKey == nil {
		return HostKey{}, fmt.Errorf("%w: sftpfs: %s: no host key presented: %w", fsys.ErrRemoteUnavailable, provider.Address, handshakeErr)
	}
	return *hostKey, nil
}

// trustHostKeyAlgorithms reprend l'ordre de Dial ; sans knownHosts lisible, l'ordre de préférence par défaut.
func trustHostKeyAlgorithms(provider config.FSProvider) []string {
	if callback, err := knownhosts.New(provider.KnownHosts); err == nil {
		if pinned := pinnedHostKeyAlgorithms(callback, provider.Address); pinned != nil {
			return pinned
		}
	}
	var algorithms []string
	for _, entry := range hostKeyAlgorithmsByType {
		algorithms = append(algorithms, entry.algorithms...)
	}
	return algorithms
}

// TrustHostKey compare hostKey à knownHosts : clé déjà épinglée, rien à faire ; autre clé épinglée pour cet hôte,
// ErrHostKeyMismatch (jamais remplacée ni doublée : l'opérateur corrige le fichier) ; hôte absent, la ligne
// knownhosts.Line est ajoutée seulement si confirmed. L'ajout se fait en fin de fichier, sans jamais le tronquer ;
// un fichier absent est créé en 0600 dans un dossier qui doit exister ; un lien symbolique est refusé.
func TrustHostKey(provider config.FSProvider, hostKey HostKey, confirmed bool) (TrustOutcome, error) {
	existing, exists, err := regularKnownHosts(provider.KnownHosts)
	if err != nil {
		return "", err
	}
	if exists {
		pinned, err := pinnedVerdict(provider, hostKey)
		if err != nil || pinned {
			return TrustAlreadyPinned, err
		}
	}
	if !confirmed {
		return TrustPending, nil
	}
	if err := appendHostKeyLine(provider, hostKey, existing); err != nil {
		return "", err
	}
	return TrustAdded, nil
}

// regularKnownHosts rend les informations de knownHosts s'il existe ; un lien symbolique ou un fichier spécial est
// refusé, un fichier illisible est ErrKnownHostsUnreadable.
func regularKnownHosts(path string) (fs.FileInfo, bool, error) {
	if path == "" {
		return nil, false, fmt.Errorf("%w: knownHosts is not set", ErrKnownHostsUnreadable)
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("%w: %s: %w", ErrKnownHostsUnreadable, path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%w: %s", ErrKnownHostsNotWritable, path)
	}
	return info, true, nil
}

// pinnedVerdict dit si knownHosts épingle déjà hostKey ; une autre clé (ou une clé révoquée) pour cet hôte est
// ErrHostKeyMismatch.
func pinnedVerdict(provider config.FSProvider, hostKey HostKey) (bool, error) {
	callback, err := knownhosts.New(provider.KnownHosts)
	if err != nil {
		return false, fmt.Errorf("%w: %s: %w", ErrKnownHostsUnreadable, provider.KnownHosts, err)
	}
	verdict := callback(hostKey.hostname, hostKey.remote, hostKey.Key)
	var keyError *knownhosts.KeyError
	switch {
	case verdict == nil:
		return true, nil
	case errors.As(verdict, &keyError) && len(keyError.Want) == 0:
		return false, nil
	default:
		return false, fmt.Errorf("%w: %s: %w", ErrHostKeyMismatch, provider.Address, verdict)
	}
}

// appendHostKeyLine ajoute la ligne en O_APPEND. Fichier présent : ouvert sans O_CREATE puis comparé à celui vu par
// Lstat (un remplacement par un lien entre-temps est refusé) ; fichier absent : créé en O_EXCL (un lien posé
// entre-temps fait échouer la création).
func appendHostKeyLine(provider config.FSProvider, hostKey HostKey, existing fs.FileInfo) error {
	path := provider.KnownHosts
	// O_RDWR : le dernier octet se relit par le même descripteur, jamais par le chemin.
	flags := os.O_RDWR | os.O_APPEND
	if existing == nil {
		flags |= os.O_CREATE | os.O_EXCL
	}
	file, err := os.OpenFile(path, flags, knownHostsFileMode) // #nosec G304 -- knownHosts configuré par l'opérateur, lien refusé
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrKnownHostsNotWritable, path, err)
	}
	if err := writeHostKeyLine(file, existing, knownhosts.Line([]string{provider.Address}, hostKey.Key)); err != nil {
		_ = file.Close()
		return fmt.Errorf("%w: %s: %w", ErrKnownHostsNotWritable, path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrKnownHostsNotWritable, path, err)
	}
	return nil
}

var errKnownHostsReplaced = errors.New("knownHosts was replaced while being opened")

// writeHostKeyLine écrit la ligne, précédée d'un saut de ligne si le fichier ne finit pas par un.
func writeHostKeyLine(file *os.File, existing fs.FileInfo, line string) error {
	opened, err := file.Stat()
	if err != nil {
		return err
	}
	if existing != nil && !os.SameFile(existing, opened) {
		return errKnownHostsReplaced
	}
	needsNewline, err := endsWithoutNewline(file, opened.Size())
	if err != nil {
		return err
	}
	if needsNewline {
		line = "\n" + line
	}
	_, err = file.WriteString(line + "\n")
	return err
}

// endsWithoutNewline lit le dernier octet d'un fichier non vide.
func endsWithoutNewline(file *os.File, size int64) (bool, error) {
	if size == 0 {
		return false, nil
	}
	lastByte := make([]byte, 1)
	if _, err := file.ReadAt(lastByte, size-1); err != nil {
		return false, err
	}
	return lastByte[0] != '\n', nil
}
