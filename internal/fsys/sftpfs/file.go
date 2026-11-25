package sftpfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"

	"github.com/pkg/sftp"
	"golang.org/x/time/rate"

	"necromanciarr/internal/config"
	"necromanciarr/internal/fsys"
)

const (
	// providerBurstBytes : rafale du limiteur du fournisseur, celle du limiteur global (1 Mio, au moins 64 Kio).
	providerBurstBytes = 1 << 20
	// limiterChunkBytes découpe une attente : rate.Limiter.WaitN échoue au-delà de sa rafale, et une rafale
	// valide (au moins 64 Kio) contient 32 Kio, la taille d'un paquet de lecture SFTP.
	limiterChunkBytes = 32 << 10
)

func newProviderLimiter(provider config.FSProvider) fsys.Limiter {
	if provider.ReadBytesPerSecond <= 0 {
		return nil
	}
	return rate.NewLimiter(rate.Limit(provider.ReadBytesPerSecond), providerBurstBytes)
}

// throttle attend, avant toute lecture, le débit de byteCount octets sur le limiteur du fournisseur puis sur le
// limiteur global : le plus strict des deux gouverne. Les octets sont comptés à la demande, jamais après coup.
func (volume *Volume) throttle(ctx context.Context, byteCount int) error {
	for remaining := byteCount; remaining > 0; remaining -= limiterChunkBytes {
		chunk := min(remaining, limiterChunkBytes)
		for _, limiter := range []fsys.Limiter{volume.providerLimiter, volume.deps.Limiter} {
			if limiter == nil {
				continue
			}
			if err := limiter.WaitN(ctx, chunk); err != nil {
				return err
			}
		}
	}
	return nil
}

// readOnlyFile cache *sftp.File : seules ReadAt, Read, Seek, Close et Stat restent atteignables, et chaque
// opération reste liée à la session qui a ouvert le fichier.
type readOnlyFile struct {
	volume  *Volume
	current *session
	file    *sftp.File
	name    string
}

// ReadAt attend les limiteurs pour len(buffer) octets, puis lit. pkg/sftp peut découper la lecture en
// maxConcurrentRequests requêtes en vol : elles ne transportent jamais plus que les octets déjà payés.
func (readOnly readOnlyFile) ReadAt(buffer []byte, offset int64) (int, error) {
	if offset < 0 {
		return 0, fmt.Errorf("sftpfs: read %q: %w: negative offset", readOnly.name, fs.ErrInvalid)
	}
	endOffset := func(readCount int) (int64, error) { return offset + int64(readCount), nil }
	return readOnly.read(len(buffer), func(file *sftp.File) (int, error) { return file.ReadAt(buffer, offset) }, endOffset)
}

// Read lit à la position courante, sous les mêmes limiteurs que ReadAt.
func (readOnly readOnlyFile) Read(buffer []byte) (int, error) {
	endOffset := func(int) (int64, error) { return readOnly.file.Seek(0, io.SeekCurrent) }
	return readOnly.read(len(buffer), func(file *sftp.File) (int, error) { return file.Read(buffer) }, endOffset)
}

// read attend les limiteurs puis lit ; une fin de fichier est confirmée par la taille du fichier (fstat) avant
// d'être rendue. endOffset rend la position atteinte après readCount octets.
func (readOnly readOnlyFile) read(byteCount int, readFn func(file *sftp.File) (int, error), endOffset func(readCount int) (int64, error)) (int, error) {
	if err := readOnly.volume.sessionGone(readOnly.current, "read"); err != nil {
		return 0, err
	}
	if err := readOnly.volume.throttle(readOnly.current.lifetime, byteCount); err != nil {
		return 0, readOnly.throttleError(err)
	}
	readCount := 0
	err := readOnly.volume.callOn(context.Background(), readOnly.current, "read", func(*session) error {
		var readErr error
		readCount, readErr = readFn(readOnly.file)
		return readErr
	})
	isUnconfirmedEndOfFile := errors.Is(err, io.EOF) && !errors.Is(err, fsys.ErrRemoteUnavailable)
	if isUnconfirmedEndOfFile {
		err = readOnly.confirmEndOfFile(endOffset(readCount))
	}
	return readCount, mapError("read", readOnly.name, err)
}

// confirmEndOfFile ne rend io.EOF que si la position atteinte touche la taille du fichier (fstat). Une fin de fichier
// avant cette taille, fstat réussi, rend ErrShortRead (sous fsys.ErrRemoteUnavailable, jamais io.EOF) et garde la
// session, qui répond : torrentcheck et dedupe rangent alors le fichier comme indisponible, jamais comme faux (un
// fichier qui grandit encore, un serveur qui coupe une lecture). Un fstat en échec rend fsys.ErrRemoteUnavailable.
func (readOnly readOnlyFile) confirmEndOfFile(end int64, positionErr error) error {
	if positionErr != nil {
		return fmt.Errorf("%w: sftpfs: read %q: end of file unconfirmed: %v", fsys.ErrRemoteUnavailable, readOnly.name, positionErr) //nolint:errorlint // jamais io.EOF dans la chaîne
	}
	info, err := readOnly.Stat()
	if err != nil {
		return fmt.Errorf("%w: sftpfs: read %q: end of file unconfirmed: %v", fsys.ErrRemoteUnavailable, readOnly.name, err) //nolint:errorlint // jamais io.EOF dans la chaîne
	}
	if end >= info.Size() {
		return io.EOF
	}
	return fmt.Errorf("%w: %w: sftpfs: read %q: end of file at %d before the size %d", fsys.ErrRemoteUnavailable, ErrShortRead, readOnly.name, end, info.Size())
}

// throttleError : une attente coupée par la fermeture de la session est une indisponibilité du volume.
func (readOnly readOnlyFile) throttleError(err error) error {
	if readOnly.current.lifetime.Err() == nil {
		return err
	}
	return fmt.Errorf("%w: sftpfs: read %q: session closed while waiting for the read rate: %v", fsys.ErrRemoteUnavailable, readOnly.name, err) //nolint:errorlint // context.Canceled ne doit pas passer pour l'annulation de l'étape
}

// Seek déplace la position de Read.
func (readOnly readOnlyFile) Seek(offset int64, whence int) (int64, error) {
	position := int64(0)
	err := readOnly.volume.callOn(context.Background(), readOnly.current, "seek", func(*session) error {
		var seekErr error
		position, seekErr = readOnly.file.Seek(offset, whence)
		return seekErr
	})
	return position, mapError("seek", readOnly.name, err)
}

// Close libère le handle distant.
func (readOnly readOnlyFile) Close() error {
	err := readOnly.volume.callOn(context.Background(), readOnly.current, "close", func(*session) error {
		return readOnly.file.Close()
	})
	return mapError("close", readOnly.name, err)
}

// Stat rend l'information du fichier ouvert (fstat) ; une taille impossible est refusée.
func (readOnly readOnlyFile) Stat() (fs.FileInfo, error) {
	var info fs.FileInfo
	err := readOnly.volume.callOn(context.Background(), readOnly.current, "fstat", func(*session) error {
		var statErr error
		info, statErr = readOnly.file.Stat()
		return statErr
	})
	if err != nil {
		return nil, mapError("fstat", readOnly.name, err)
	}
	return checkInfo("fstat", readOnly.name, info)
}
