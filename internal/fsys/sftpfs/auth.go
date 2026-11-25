package sftpfs

import (
	"bytes"
	"crypto/rsa"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"slices"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"necromanciarr/internal/config"
)

const (
	agentSocketVariable = "SSH_AUTH_SOCK"
	// groupOrOtherBits : une clé privée lisible ou modifiable par d'autres que son propriétaire.
	groupOrOtherBits = 0o077
	// minimumRSABits : en dessous, une clé RSA est refusée (spec 4.C.5).
	minimumRSABits = 3072
)

// authentication porte les méthodes configurées, dans l'ordre agent, clé, mot de passe. L'agent et la clé partagent
// une seule méthode « publickey » : x/crypto/ssh n'essaie que la première méthode de chaque nom.
type authentication struct {
	methods         []ssh.AuthMethod
	warnings        []Warning
	agentClient     agent.ExtendedAgent
	agentConnection net.Conn
	identity        ssh.Signer
}

// newAuthentication prépare les méthodes configurées seulement. Un agent absent est sauté avec un avertissement ;
// sans aucune méthode utilisable, c'est ErrAuthRefused, avant tout appel réseau.
func newAuthentication(provider config.FSProvider, deps Deps) (*authentication, error) {
	auth := &authentication{}
	if provider.Agent {
		auth.openAgent(provider, deps)
	}
	if provider.IdentityFile != "" {
		identity, err := auth.loadIdentity(provider)
		if err != nil {
			auth.close()
			return nil, err
		}
		auth.identity = identity
	}
	if auth.agentClient != nil || auth.identity != nil {
		auth.methods = append(auth.methods, ssh.PublicKeysCallback(auth.publicKeySigners))
	}
	if provider.Password != "" {
		auth.methods = append(auth.methods, ssh.Password(provider.Password))
	}
	if len(auth.methods) == 0 {
		auth.close()
		return nil, fmt.Errorf("%w: %s: no configured method is usable", ErrAuthRefused, provider.Address)
	}
	return auth, nil
}

// openAgent joint l'agent de SSH_AUTH_SOCK ; absent ou injoignable, la méthode est sautée avec un avertissement qui
// nomme la variable, jamais le chemin de la socket. L'échange avec l'agent est borné par timeout : x/crypto/ssh
// l'appelle sans délai pendant la poignée de main.
func (auth *authentication) openAgent(provider config.FSProvider, deps Deps) {
	socket, found := deps.LookupEnv(agentSocketVariable)
	if !found || socket == "" {
		auth.warnAgentUnavailable()
		return
	}
	connection, err := net.DialTimeout("unix", socket, provider.Timeout)
	if err == nil {
		err = connection.SetDeadline(deps.Now().Add(provider.Timeout))
	}
	if err != nil {
		if connection != nil {
			_ = connection.Close()
		}
		auth.warnAgentUnavailable()
		return
	}
	auth.agentConnection = connection
	auth.agentClient = agent.NewClient(connection)
}

func (auth *authentication) warnAgentUnavailable() {
	warning := Warning{Code: CodeAgentUnavailable, Path: agentSocketVariable}
	if !slices.Contains(auth.warnings, warning) {
		auth.warnings = append(auth.warnings, warning)
	}
}

// loadIdentity lit identityFile à la connexion seulement. Les erreurs ne citent que le chemin : jamais le texte de
// l'analyseur, qui pourrait reprendre des octets de la clé.
func (auth *authentication) loadIdentity(provider config.FSProvider) (ssh.Signer, error) {
	info, err := os.Stat(provider.IdentityFile)
	if err != nil {
		return nil, fmt.Errorf("sftpfs: identityFile: %w", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&groupOrOtherBits != 0 {
		auth.warnings = append(auth.warnings, Warning{Code: CodeIdentityFilePermissions, Path: provider.IdentityFile})
	}
	keyBytes, err := os.ReadFile(provider.IdentityFile)
	if err != nil {
		return nil, fmt.Errorf("sftpfs: identityFile: %w", err)
	}
	defer clear(keyBytes)
	signer, err := ssh.ParsePrivateKey(keyBytes)
	var passphraseMissing *ssh.PassphraseMissingError
	if errors.As(err, &passphraseMissing) {
		if provider.IdentityPassphrase == "" {
			return nil, fmt.Errorf("%w: %s", ErrIdentityPassphraseMissing, provider.IdentityFile)
		}
		signer, err = ssh.ParsePrivateKeyWithPassphrase(keyBytes, []byte(provider.IdentityPassphrase))
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrIdentityUnreadable, provider.IdentityFile)
	}
	if isWeakRSAKey(signer.PublicKey()) {
		return nil, fmt.Errorf("%w: %s", ErrIdentityKeyTooWeak, provider.IdentityFile)
	}
	return signer, nil
}

// isWeakRSAKey : une clé RSA de moins de minimumRSABits. Les clés de l'agent ne passent pas par ici : elles sont
// celles de l'opérateur, offertes telles quelles.
func isWeakRSAKey(publicKey ssh.PublicKey) bool {
	cryptoKey, isCryptoKey := publicKey.(ssh.CryptoPublicKey)
	if !isCryptoKey {
		return false
	}
	rsaKey, isRSA := cryptoKey.CryptoPublicKey().(*rsa.PublicKey)
	return isRSA && rsaKey.N.BitLen() < minimumRSABits
}

// publicKeySigners rend les clés de l'agent puis celle de identityFile : l'ordre des essais côté serveur.
func (auth *authentication) publicKeySigners() ([]ssh.Signer, error) {
	var agentSigners []ssh.Signer
	if auth.agentClient != nil {
		signers, err := auth.agentClient.Signers()
		if err != nil {
			auth.warnAgentUnavailable()
		}
		agentSigners = signers
	}
	return orderedSigners(agentSigners, auth.identity), nil
}

// orderedSigners rend les clés de l'agent puis identity ; une clé de l'agent égale à identity n'est offerte qu'une
// fois, à la place de identity : chaque essai compte dans le MaxAuthTries du serveur.
func orderedSigners(agentSigners []ssh.Signer, identity ssh.Signer) []ssh.Signer {
	var signers []ssh.Signer
	for _, agentSigner := range agentSigners {
		if identity == nil || !samePublicKey(agentSigner, identity) {
			signers = append(signers, agentSigner)
		}
	}
	if identity != nil {
		signers = append(signers, identity)
	}
	return signers
}

func samePublicKey(first, second ssh.Signer) bool {
	return bytes.Equal(first.PublicKey().Marshal(), second.PublicKey().Marshal())
}

// close raccroche l'agent : il ne sert qu'à la poignée de main.
func (auth *authentication) close() {
	if auth.agentConnection != nil {
		_ = auth.agentConnection.Close()
	}
}
