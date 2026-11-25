package sftpfs

import (
	"testing"

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
