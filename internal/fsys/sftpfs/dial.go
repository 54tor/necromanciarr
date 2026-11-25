package sftpfs

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"necromanciarr/internal/config"
	"necromanciarr/internal/fsys"
)

// hostKeyAlgorithmsByType : signatures acceptées pour chaque type de clé épinglée, dans l'ordre de
// préférence ; ssh-rsa (SHA-1) est exclu.
var hostKeyAlgorithmsByType = []struct {
	keyType    string
	algorithms []string
}{
	{ssh.KeyAlgoED25519, []string{ssh.KeyAlgoED25519}},
	{ssh.KeyAlgoECDSA256, []string{ssh.KeyAlgoECDSA256}},
	{ssh.KeyAlgoECDSA384, []string{ssh.KeyAlgoECDSA384}},
	{ssh.KeyAlgoECDSA521, []string{ssh.KeyAlgoECDSA521}},
	{ssh.KeyAlgoRSA, []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}},
}

// authRefusalMarkers : x/crypto/ssh ne type pas le refus d'authentification ; ces textes stables le reconnaissent,
// une fois la clé d'hôte acceptée.
var authRefusalMarkers = []string{"unable to authenticate", "authentication failures"}

// hostKeyVerdict garde l'erreur rendue par la vérification de la clé d'hôte, appelée par la goroutine d'échange de
// clés de x/crypto/ssh.
type hostKeyVerdict struct {
	mutex sync.Mutex
	err   error
}

func (verdict *hostKeyVerdict) check(callback ssh.HostKeyCallback) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := callback(hostname, remote, key)
		verdict.mutex.Lock()
		defer verdict.mutex.Unlock()
		verdict.err = err
		return err
	}
}

func (verdict *hostKeyVerdict) result() error {
	verdict.mutex.Lock()
	defer verdict.mutex.Unlock()
	return verdict.err
}

// connect ouvre une session SSH + SFTP sous ctx et timeout. knownHosts et identityFile sont relus à chaque appel ;
// la clé d'hôte n'est acceptée que par le rappel de knownhosts, sans jamais rien ajouter au fichier.
func connect(ctx context.Context, provider config.FSProvider, deps Deps) (*session, []Warning, error) {
	hostKeyCallback, err := knownhosts.New(provider.KnownHosts)
	if err != nil {
		return nil, nil, fmt.Errorf("sftpfs: %s: knownHosts: %w", provider.Address, err)
	}
	auth, err := newAuthentication(provider, deps)
	if err != nil {
		return nil, nil, err
	}
	defer auth.close()
	verdict := &hostKeyVerdict{}
	clientConfig := &ssh.ClientConfig{
		User: provider.User, Auth: auth.methods, Timeout: provider.Timeout,
		HostKeyCallback:   verdict.check(hostKeyCallback),
		HostKeyAlgorithms: pinnedHostKeyAlgorithms(hostKeyCallback, provider.Address),
	}
	current, err := dialBounded(ctx, provider, clientConfig)
	if err != nil {
		return nil, nil, classifyDialError(provider.Address, err, verdict.result())
	}
	return current, auth.warnings, nil
}

// dialBounded ouvre TCP, SSH et SFTP en timeout au plus ; une annulation de ctx ferme la connexion, ce qui débloque
// la poignée de main sans laisser de goroutine en attente.
func dialBounded(ctx context.Context, provider config.FSProvider, clientConfig *ssh.ClientConfig) (*session, error) {
	dialContext, cancel := context.WithTimeout(ctx, provider.Timeout)
	defer cancel()
	var dialer net.Dialer
	connection, err := dialer.DialContext(dialContext, "tcp", provider.Address)
	if err != nil {
		return nil, err
	}
	stopWatching := context.AfterFunc(dialContext, func() { _ = connection.Close() })
	current, err := handshake(connection, provider, clientConfig)
	if !stopWatching() {
		if current != nil {
			current.close()
		}
		return nil, dialContext.Err()
	}
	if err != nil {
		_ = connection.Close()
		return nil, err
	}
	return current, nil
}

func handshake(connection net.Conn, provider config.FSProvider, clientConfig *ssh.ClientConfig) (*session, error) {
	sshConnection, channels, requests, err := ssh.NewClientConn(connection, provider.Address, clientConfig)
	if err != nil {
		return nil, err
	}
	sshClient := ssh.NewClient(sshConnection, channels, requests)
	var options []sftp.ClientOption
	if provider.MaxConcurrentRequests > 0 {
		options = append(options, sftp.MaxConcurrentRequestsPerFile(provider.MaxConcurrentRequests))
	}
	client, err := sftp.NewClient(sshClient, options...)
	if err != nil {
		_ = sshClient.Close()
		return nil, err
	}
	return newSession(sshClient, client), nil
}

// classifyDialError range l'échec : clé d'hôte, authentification, sinon volume injoignable.
func classifyDialError(address string, err, hostKeyErr error) error {
	if hostKeyErr != nil {
		return hostKeyError(address, hostKeyErr)
	}
	if isAuthRefusal(err) {
		return fmt.Errorf("%w: %s: %w", ErrAuthRefused, address, err)
	}
	return fmt.Errorf("%w: sftpfs: %s: %w", fsys.ErrRemoteUnavailable, address, err)
}

func hostKeyError(address string, err error) error {
	var keyError *knownhosts.KeyError
	if errors.As(err, &keyError) && len(keyError.Want) == 0 {
		return fmt.Errorf("%w: %s", ErrHostKeyUnknown, address)
	}
	return fmt.Errorf("%w: %s: %w", ErrHostKeyMismatch, address, err)
}

func isAuthRefusal(err error) bool {
	message := err.Error()
	return slices.ContainsFunc(authRefusalMarkers, func(marker string) bool { return strings.Contains(message, marker) })
}

// pinnedHostKeyAlgorithms demande au serveur d'abord les types de clé épinglés pour cet hôte : sans cela, un serveur
// qui a aussi une clé d'un autre type la présenterait et serait refusé à tort. x/crypto/ssh/knownhosts n'expose pas
// ses entrées : une clé sonde, jamais épinglée, fait rendre au rappel la liste des clés connues (KeyError.Want).
// Sans entrée, nil laisse l'ordre par défaut ; le rappel refusera la clé.
func pinnedHostKeyAlgorithms(callback ssh.HostKeyCallback, address string) []string {
	var keyError *knownhosts.KeyError
	if !errors.As(callback(address, probeRemoteAddress, probeHostKey), &keyError) || len(keyError.Want) == 0 {
		return nil
	}
	var pinnedTypes []string
	for _, known := range keyError.Want {
		pinnedTypes = append(pinnedTypes, known.Key.Type())
	}
	var preferred, others []string
	for _, entry := range hostKeyAlgorithmsByType {
		if slices.Contains(pinnedTypes, entry.keyType) {
			preferred = append(preferred, entry.algorithms...)
		} else {
			others = append(others, entry.algorithms...)
		}
	}
	return append(preferred, others...)
}

// probeRemoteAddress et probeHostKey servent seulement à lire les entrées de knownHosts : la clé dérive d'une graine
// nulle, publique, que personne n'épingle.
var (
	probeRemoteAddress net.Addr = &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}
	probeHostKey                = mustProbeKey()
)

func mustProbeKey() ssh.PublicKey {
	publicKey, err := ssh.NewPublicKey(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)).Public())
	if err != nil {
		panic(err)
	}
	return publicKey
}
