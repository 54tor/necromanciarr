package sftpfs

import (
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"necromanciarr/internal/config"
)

const (
	agentSocketVariable = "SSH_AUTH_SOCK"
	// groupOrOtherBits : une clé privée lisible ou modifiable par d'autres que son propriétaire.
	groupOrOtherBits = 0o077
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

// openAgent joint l'agent de SSH_AUTH_SOCK ; absent ou injoignable, la méthode est sautée avec un avertissement.
func (auth *authentication) openAgent(provider config.FSProvider, deps Deps) {
	socket, found := deps.LookupEnv(agentSocketVariable)
	if !found || socket == "" {
		auth.warnings = append(auth.warnings, Warning{Code: CodeAgentUnavailable})
		return
	}
	connection, err := net.DialTimeout("unix", socket, provider.Timeout)
	if err != nil {
		auth.warnings = append(auth.warnings, Warning{Code: CodeAgentUnavailable, Path: socket})
		return
	}
	auth.agentConnection = connection
	auth.agentClient = agent.NewClient(connection)
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
	return signer, nil
}

// publicKeySigners rend les clés de l'agent puis celle de identityFile : l'ordre des essais côté serveur.
func (auth *authentication) publicKeySigners() ([]ssh.Signer, error) {
	var signers []ssh.Signer
	if auth.agentClient != nil {
		agentSigners, err := auth.agentClient.Signers()
		if err == nil {
			signers = append(signers, agentSigners...)
		}
	}
	if auth.identity != nil {
		signers = append(signers, auth.identity)
	}
	return signers, nil
}

// close raccroche l'agent : il ne sert qu'à la poignée de main.
func (auth *authentication) close() {
	if auth.agentConnection != nil {
		_ = auth.agentConnection.Close()
	}
}
