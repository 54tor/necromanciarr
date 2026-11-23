package dedupe

import (
	"context"
	"errors"
	"io/fs"
	"time"

	"necromanciarr/internal/store"
)

// TemporaryMarker est le motif des noms temporaires de cet outil, que Recover sait nettoyer.
const TemporaryMarker = temporaryMarker

// ErrNoCachedFingerprint est renvoyée par HeadTailFingerprint quand le cache n'a rien et que la lecture est refusée.
var ErrNoCachedFingerprint = errors.New("dedupe: no cached fingerprint and reading not allowed")

// FileIdentity renvoie périphérique et inode d'un fichier ; known est faux si le système ne les fournit pas.
func FileIdentity(info fs.FileInfo) (device, inode uint64, known bool) {
	return fileIdentity(info)
}

// HeadTailFingerprint renvoie l'empreinte début et fin du fichier : depuis cache s'il la connaît pour cette taille et
// cette date, sinon en la lisant (débit plafonné par limiter) puis en l'y enregistrant. Si allowRead est faux et que le
// cache n'a rien, rien n'est lu et l'erreur est ErrNoCachedFingerprint. cache et limiter peuvent être nil ; fromCache
// dit qu'aucun octet n'a été lu.
func HeadTailFingerprint(ctx context.Context, cache FingerprintCache, limiter Limiter, path string, size int64, modTime time.Time, allowRead bool) (fingerprint string, fromCache bool, err error) {
	if cache != nil {
		cached, found, err := cache.LookupFingerprint(ctx, path, size, modTime)
		if err != nil || found {
			return cached, found, err
		}
	}
	if !allowRead {
		return "", false, ErrNoCachedFingerprint
	}
	fingerprint, err = fingerprintFile(&meter{ctx: ctx, limiter: limiter}, path, size)
	if err != nil || cache == nil {
		return fingerprint, false, err
	}
	entry := store.CachedFingerprint{Path: path, Size: size, ModTime: modTime, HeadTailHash: fingerprint, VerifiedAt: time.Now()}
	return fingerprint, false, cache.RecordFingerprint(ctx, entry)
}
