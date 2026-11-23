package dedupe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
)

const (
	readChunkSize    = 64 << 10
	headTailSize     = 1 << 20
	sampleBlockSize  = 1 << 20
	sampleBlockCount = 8
)

// Limiter plafonne le débit de lecture ; compatible avec golang.org/x/time/rate.Limiter, rafale d'au moins 64 Kio.
type Limiter interface {
	WaitN(ctx context.Context, n int) error
}

// errContentDiffers arrête la comparaison au premier octet différent ; jamais renvoyée à l'appelant.
var errContentDiffers = errors.New("dedupe: content differs")

type byteRange struct {
	offset, length int64
}

// meter charge le limiteur avant chaque lecture et compte les octets lus, pour BytesToRead.
type meter struct {
	ctx       context.Context
	limiter   Limiter
	bytesRead int64
}

func (readMeter *meter) readAt(file *os.File, buffer []byte, offset int64) error {
	if err := readMeter.wait(len(buffer)); err != nil {
		return err
	}
	readCount, err := file.ReadAt(buffer, offset)
	readMeter.bytesRead += int64(readCount)
	if readCount == len(buffer) {
		return nil
	}
	// Un fichier raccourci pendant la lecture n'est plus celui que l'on compare.
	if err == nil || errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

// WaitN permet à hashsource.NewLocal de charger le même limiteur et de compter les octets lus.
func (readMeter *meter) WaitN(_ context.Context, byteCount int) error {
	readMeter.bytesRead += int64(byteCount)
	return readMeter.wait(byteCount)
}

func (readMeter *meter) wait(byteCount int) error {
	if readMeter.limiter == nil {
		return readMeter.ctx.Err()
	}
	return readMeter.limiter.WaitN(readMeter.ctx, byteCount)
}

func forEachChunk(span byteRange, visit func(offset int64, length int) error) error {
	for done := int64(0); done < span.length; {
		length := min(int64(readChunkSize), span.length-done)
		if err := visit(span.offset+done, int(length)); err != nil {
			return err
		}
		done += length
	}
	return nil
}

// headTailRanges : 1 Mio de début et 1 Mio de fin, ou tout le fichier s'il fait moins de 2 Mio.
func headTailRanges(size int64) []byteRange {
	if size < 2*headTailSize {
		return []byteRange{{0, size}}
	}
	return []byteRange{{0, headTailSize}, {size - headTailSize, headTailSize}}
}

// comparisonRanges : tout le fichier en mode full ; en mode sampled, début, fin et 8 blocs à des positions fixées par
// la taille seule, donc identiques d'un Plan à l'autre.
func comparisonRanges(size int64, verification Verification) []byteRange {
	sampledBytes := int64(2*headTailSize + sampleBlockCount*sampleBlockSize)
	if verification != VerifySampled || size <= sampledBytes {
		return []byteRange{{0, size}}
	}
	ranges := headTailRanges(size)
	for blockIndex := int64(1); blockIndex <= sampleBlockCount; blockIndex++ {
		offset := blockIndex * (size - sampleBlockSize) / (sampleBlockCount + 1)
		ranges = append(ranges, byteRange{offset, sampleBlockSize})
	}
	return ranges
}

// fingerprintFile renvoie le SHA-256 hexadécimal du début et de la fin du fichier (voir headTailRanges).
func fingerprintFile(readMeter *meter, path string, size int64) (string, error) {
	file, err := os.Open(path) // #nosec G304 -- fichier de la bibliothèque ou des racines torrent, lu seulement
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hasher := sha256.New()
	buffer := make([]byte, readChunkSize)
	for _, span := range headTailRanges(size) {
		err := forEachChunk(span, func(offset int64, length int) error {
			if err := readMeter.readAt(file, buffer[:length], offset); err != nil {
				return err
			}
			hasher.Write(buffer[:length])
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// sameBytes compare octet par octet les plages données des deux fichiers, sans jamais charger un fichier entier.
func sameBytes(readMeter *meter, firstPath, secondPath string, ranges []byteRange) (bool, error) {
	firstFile, err := os.Open(firstPath) // #nosec G304 -- fichier des racines torrent, lu seulement
	if err != nil {
		return false, err
	}
	defer func() { _ = firstFile.Close() }()
	secondFile, err := os.Open(secondPath) // #nosec G304 -- fichier de la bibliothèque, lu seulement
	if err != nil {
		return false, err
	}
	defer func() { _ = secondFile.Close() }()
	firstBuffer, secondBuffer := make([]byte, readChunkSize), make([]byte, readChunkSize)
	for _, span := range ranges {
		err := forEachChunk(span, func(offset int64, length int) error {
			if err := readMeter.readAt(firstFile, firstBuffer[:length], offset); err != nil {
				return err
			}
			if err := readMeter.readAt(secondFile, secondBuffer[:length], offset); err != nil {
				return err
			}
			if !bytes.Equal(firstBuffer[:length], secondBuffer[:length]) {
				return errContentDiffers
			}
			return nil
		})
		if errors.Is(err, errContentDiffers) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
	return true, nil
}
