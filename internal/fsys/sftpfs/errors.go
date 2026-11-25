// Package sftpfs joint un fournisseur sftp de fsProviders : connexion SSH à clé d'hôte épinglée (known_hosts
// configuré, jamais celui de l'utilisateur), méthodes agent, clé puis mot de passe, délai sur chaque opération,
// keepalive, et au plus une reconnexion par étape.
package sftpfs

import (
	"errors"

	"necromanciarr/internal/fsys"
)

// Codes stables des erreurs et avertissements de connexion, identiques dans toutes les langues (doctor, interface).
const (
	CodeHostKeyUnknown          = "host_key_unknown"
	CodeHostKeyMismatch         = "host_key_mismatch"
	CodeAuthRefused             = "auth_refused"
	CodeIdentityFilePermissions = "identity_file_permissions"
	CodeAgentUnavailable        = "agent_unavailable"
	CodeIdentityKeyTooWeak      = "identity_key_too_weak"
	CodeKnownHostsUnreadable    = "known_hosts_unreadable"
	// CodeLinkDirWorldWritable : un dossier créé dans un dossier de liens est modifiable par le groupe ou les autres.
	CodeLinkDirWorldWritable = "link_dir_world_writable"
	// CodeRemoteExecUnavailable : exec auto, mais l'exécution distante est refusée, absente, pas GNU ou incohérente
	// avec SFTP ; lecture SFTP seule (Path : le nom du volume, un seul avertissement par session).
	CodeRemoteExecUnavailable = "remote_exec_unavailable"
	// CodeRemoteExecFailed : une commande a échoué après une sonde réussie ; l'élément est relu par SFTP (Count : le
	// nombre d'éléments).
	CodeRemoteExecFailed = "remote_exec_failed"
	// CodeRemoteExecRequired : exec required et la sonde a échoué ; les étapes qui écrivent sur le volume sont refusées.
	CodeRemoteExecRequired = "remote_exec_required"
)

// Erreurs de connexion, à tester avec errors.Is. Leurs messages ne citent que l'adresse et les chemins configurés,
// jamais un mot de passe, une passphrase ni le contenu d'une clé.
var (
	// ErrHostKeyUnknown : l'hôte n'a aucune entrée dans knownHosts ; la clé n'est jamais ajoutée d'office.
	ErrHostKeyUnknown = errors.New("sftpfs: host key unknown: no entry for this host in knownHosts")
	// ErrHostKeyMismatch : la clé présentée n'est pas celle épinglée (ou elle est révoquée) ; possible interception.
	ErrHostKeyMismatch = errors.New("sftpfs: host key differs from the one pinned in knownHosts")
	// ErrAuthRefused : le serveur a refusé toutes les méthodes configurées, ou aucune n'était utilisable.
	ErrAuthRefused = errors.New("sftpfs: authentication refused")
	// ErrIdentityPassphraseMissing : identityFile est chiffrée et identityPassphrase est vide.
	ErrIdentityPassphraseMissing = errors.New("sftpfs: identityFile is passphrase-protected and identityPassphrase is empty")
	// ErrIdentityUnreadable : identityFile n'est pas une clé privée lisible, ou la passphrase ne la déchiffre pas.
	ErrIdentityUnreadable = errors.New("sftpfs: identityFile is not a usable private key, or identityPassphrase does not decrypt it")
	// ErrIdentityKeyTooWeak : identityFile est une clé RSA de moins de 3072 bits.
	ErrIdentityKeyTooWeak = errors.New("sftpfs: identityFile is an rsa key under 3072 bits")
	// ErrKnownHostsUnreadable : knownHosts est absent, illisible ou sans aucune entrée valide.
	ErrKnownHostsUnreadable = errors.New("sftpfs: knownHosts is missing, unreadable or has no valid entry")
	// ErrHostileResponse : le serveur a rendu une valeur impossible (taille, statvfs, cible de lien, liste démesurée) ;
	// elle est refusée, jamais tronquée.
	ErrHostileResponse = errors.New("sftpfs: hostile response from the server")
	// ErrSymlinkOrderUnverified est fsys.ErrSymlinkOrderUnverified : un lien symbolique reste refusé tant que la sonde
	// de la session n'a pas prouvé l'ordre des arguments d'OpenSSH.
	ErrSymlinkOrderUnverified = fsys.ErrSymlinkOrderUnverified
	// ErrShortRead : une lecture a fini avant la taille que donne fstat sur une session vivante ; toujours sous
	// fsys.ErrRemoteUnavailable, jamais io.EOF : le fichier est indisponible pour cette lecture, pas différent.
	ErrShortRead = errors.New("sftpfs: read ended before the file size")
	// ErrNoProbeDir : ProbeCapabilities n'a reçu aucune racine d'écriture de ce volume (pas encore de dossier de liens
	// ouvert par Registry.WriteRoot) ; l'appelant décide d'attendre ou de s'en passer.
	ErrNoProbeDir = errors.New("sftpfs: no link directory write root of this volume to probe in")
	// ErrOperationTimeout : une opération a dépassé timeout ; la connexion a été fermée.
	ErrOperationTimeout = errors.New("sftpfs: operation timed out")
)

var errorCodes = []struct {
	sentinel error
	code     string
}{
	{ErrHostKeyUnknown, CodeHostKeyUnknown},
	{ErrHostKeyMismatch, CodeHostKeyMismatch},
	{ErrAuthRefused, CodeAuthRefused},
	{ErrIdentityKeyTooWeak, CodeIdentityKeyTooWeak},
	{ErrKnownHostsUnreadable, CodeKnownHostsUnreadable},
}

// Code rend le code stable d'une erreur de connexion (host_key_unknown, host_key_mismatch, auth_refused,
// identity_key_too_weak, known_hosts_unreadable), vide sinon.
func Code(err error) string {
	for _, known := range errorCodes {
		if errors.Is(err, known.sentinel) {
			return known.code
		}
	}
	return ""
}

// Warning est un constat qui n'empêche pas la connexion : Code stable, Path le fichier ou la socket en cause, ou la
// racine d'écriture entre guillemets (strconv.Quote) pour un constat d'écriture.
type Warning struct {
	Code, Path string
	// Count : nombre de constats regroupés sous ce code et ce chemin (0 pour un constat unique de connexion).
	Count int
}
