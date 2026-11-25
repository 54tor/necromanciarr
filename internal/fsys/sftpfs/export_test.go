package sftpfs

import (
	"slices"
	"testing"
	"time"

	"necromanciarr/internal/fsys"
)

// OrderedSigners expose aux tests l'ordre et le dédoublonnage des clés offertes.
var OrderedSigners = orderedSigners

// KeepListingName expose le filtre des noms d'une liste distante.
var KeepListingName = keepListingName

// MapError expose la traduction des erreurs SFTP en erreurs typées de fs.
var MapError = mapError

// LimiterChunkBytes est la plus grosse attente demandée à un limiteur.
const LimiterChunkBytes = limiterChunkBytes

// SetProviderLimiter remplace le limiteur du fournisseur par un limiteur de test.
func SetProviderLimiter(volume *Volume, limiter fsys.Limiter) { volume.providerLimiter = limiter }

// ProviderLimiter rend le limiteur du fournisseur, nil sans readBytesPerSecond.
func ProviderLimiter(volume *Volume) fsys.Limiter { return volume.providerLimiter }

// SetMaxListingEntries abaisse le plafond d'une liste pour un test.
func SetMaxListingEntries(t testing.TB, ceiling int) {
	previous := maxListingEntries
	maxListingEntries = ceiling
	t.Cleanup(func() { maxListingEntries = previous })
}

// IsTransportFailure expose le classement des échecs de transport de pkg/sftp.
var IsTransportFailure = isTransportFailure

// ClassifyOnLiveSession passe err à operationError sur la session vivante du volume, avant que la fin de session
// soit remarquée : le cas d'un envoi qui échoue juste après la coupure.
func ClassifyOnLiveSession(volume *Volume, err error) error {
	return volume.operationError(volume.live(), "stat", err)
}

// CommandIDs rend les identifiants de la table fermée, triés.
func CommandIDs() []CommandID {
	ids := make([]CommandID, 0, len(commands))
	for id := range commands {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// CommandArgv rend une copie de l'argv constant d'une commande de la table.
func CommandArgv(id CommandID) []string { return slices.Clone(commands[id]) }

// CommandLine rend la ligne constante d'une commande, construite à l'initialisation.
func CommandLine(id CommandID) string { return commandLines[id] }

// LowPriorityLine rend la ligne d'empreinte préfixée de nice et ionice.
func LowPriorityLine(id CommandID) string { return lowPriorityLines[id] }

// LowPriorityPrefix rend une copie de l'argv constant du préfixe nice et ionice.
func LowPriorityPrefix() []string { return slices.Clone(lowPriorityPrefix) }

// ProbeLine rend la ligne de sonde d'un programme (stat, xargs, sha1sum, md5sum, nice, ionice).
func ProbeLine(program string) string {
	for _, probe := range versionProbes {
		if probe.program == program {
			return probe.line
		}
	}
	return ""
}

// FallbackLine rend la ligne « un fichier par commande » d'une commande pour un chemin.
func FallbackLine(id CommandID, remotePath string) (string, error) {
	return fallbackLine(id, remotePath)
}

// SetStatExecTimeout abaisse le délai d'un lot stat et d'une ligne de sonde pour un test.
func SetStatExecTimeout(t testing.TB, timeout time.Duration) {
	previous := statExecTimeout
	statExecTimeout = timeout
	t.Cleanup(func() { statExecTimeout = previous })
}

// SetBeforeKnownHostsOpen pose, pour un test, l'appel fait entre le Lstat de knownHosts et son ouverture.
func SetBeforeKnownHostsOpen(t *testing.T, hook func(path string)) {
	t.Helper()
	previous := beforeKnownHostsOpen
	beforeKnownHostsOpen = hook
	t.Cleanup(func() { beforeKnownHostsOpen = previous })
}
