package sftptest

import (
	"path/filepath"
	"testing"
	"time"

	"necromanciarr/internal/config"
)

// Methods liste chaque valeur que Request.Method peut prendre : les méthodes que pkg/sftp transmet au serveur
// (Fsetstat et la troncature arrivent en Setstat, Fstat en Stat), plus RealPath et Exec. Un test qui classe les
// requêtes en lectures et écritures doit couvrir exactement cette liste.
var Methods = []string{
	"Get", "Put", "Open", "Stat", "Lstat", "List", "Readlink", "Setstat", "Mkdir", "Rmdir", "Remove", "Rename",
	"PosixRename", "Link", "Symlink", "StatVFS", "RealPath", "Exec",
}

// providerTimeout borne chaque opération d'un fournisseur de test : court, la boucle locale répond tout de suite.
const providerTimeout = 5 * time.Second

// Provider rend un fournisseur sftp de ce nom qui joint le serveur : known_hosts à sa seule clé d'hôte (fichier
// temporaire), clé de client autorisée, réglages par défaut de la configuration.
func (server *Server) Provider(t testing.TB, name string) config.FSProvider {
	t.Helper()
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	WriteFile(t, knownHosts, []byte(server.HostKeyLine+"\n"), 0o600)
	return config.FSProvider{
		Type: config.FSProviderTypeSFTP, Name: name, Address: server.Address, User: "user",
		KnownHosts: knownHosts, IdentityFile: server.ClientKeyFile, MaxConcurrentRequests: 16,
		Timeout: providerTimeout, Keepalive: 15 * time.Second, Exec: config.SFTPExecAuto, ExecHashTimeout: 30 * time.Minute,
	}
}
