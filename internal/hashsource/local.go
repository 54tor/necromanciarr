package hashsource

import (
	"context"
	"crypto/md5"  // #nosec G501 -- empreinte de comparaison avec celle du stockage, pas une protection
	"crypto/sha1" // #nosec G505 -- idem
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
)

// readChunkSize tient dans la rafale minimale d'un Limiter (64 Kio).
const readChunkSize = 64 << 10

type local struct {
	limiter Limiter
}

// NewLocal renvoie une source qui lit le fichier en flux, débit plafonné par limiter (nil : sans plafond). Son
// résultat vient toujours de ReadLocally.
func NewLocal(limiter Limiter) HashSource {
	return &local{limiter: limiter}
}

func newHasher(algorithm Algorithm) (hash.Hash, error) {
	switch algorithm {
	case MD5:
		return md5.New(), nil // #nosec G401 -- voir l'import
	case SHA1:
		return sha1.New(), nil // #nosec G401 -- voir l'import
	case SHA256:
		return sha256.New(), nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownAlgorithm, string(algorithm))
	}
}

// Hash lit path en entier ; un fichier qui n'existe pas ou ne se lit pas est une erreur, pas une absence.
func (source *local) Hash(ctx context.Context, path string, algorithm Algorithm) (Result, error) {
	hasher, err := newHasher(algorithm)
	if err != nil {
		return Result{}, err
	}
	file, err := os.Open(path) // #nosec G304 -- fichier de la bibliothèque ou des racines torrent, lu seulement
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = file.Close() }()
	buffer := make([]byte, readChunkSize)
	for {
		readCount, readErr := file.Read(buffer)
		hasher.Write(buffer[:readCount])
		// Le plafond est chargé après la lecture, pour le nombre exact d'octets : le dernier appel (fin de fichier) ne coûte rien.
		if err := source.wait(ctx, readCount); err != nil {
			return Result{}, err
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return Result{}, readErr
		}
	}
	return Result{Value: hex.EncodeToString(hasher.Sum(nil)), Origin: ReadLocally, Available: true}, nil
}

func (source *local) wait(ctx context.Context, byteCount int) error {
	if source.limiter == nil || byteCount == 0 {
		return ctx.Err()
	}
	return source.limiter.WaitN(ctx, byteCount)
}
