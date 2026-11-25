package dedupe

import (
	"context"
	"errors"
	"io"
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
	return recordedFingerprint(ctx, cache, path, size, modTime, fingerprint, err)
}

// ReadAtCloser est un fichier ouvert en lecture seule (un fichier d'un volume fsys, distant compris).
type ReadAtCloser interface {
	io.ReaderAt
	io.Closer
}

// HeadTailFingerprintOf est HeadTailFingerprint sur un fichier qu'ouvre open, appelé seulement si le cache n'a
// rien ; path n'est que la clé du cache. Le limiteur est chargé avant chaque lecture, comme pour un fichier local.
func HeadTailFingerprintOf(ctx context.Context, cache FingerprintCache, limiter Limiter, open func() (ReadAtCloser, error), path string, size int64, modTime time.Time) (string, error) {
	if cache != nil {
		cached, found, err := cache.LookupFingerprint(ctx, path, size, modTime)
		if err != nil || found {
			return cached, err
		}
	}
	file, err := open()
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	fingerprint, err := fingerprintReader(&meter{ctx: ctx, limiter: limiter}, file, size)
	fingerprint, _, err = recordedFingerprint(ctx, cache, path, size, modTime, fingerprint, err)
	return fingerprint, err
}

func recordedFingerprint(ctx context.Context, cache FingerprintCache, path string, size int64, modTime time.Time, fingerprint string, err error) (string, bool, error) {
	if err != nil || cache == nil {
		return fingerprint, false, err
	}
	entry := store.CachedFingerprint{Path: path, Size: size, ModTime: modTime, HeadTailHash: fingerprint, VerifiedAt: time.Now()}
	return fingerprint, false, cache.RecordFingerprint(ctx, entry)
}
