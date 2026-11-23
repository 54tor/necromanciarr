package torrentcheck

import (
	"bytes"
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"hash"
	"io"
	"path"
	"strings"
)

const readChunkSize = 64 << 10

var ErrPieceIndexOutOfRange = errors.New("torrentcheck: piece index out of range")

var mediaExtensions = map[string]struct{}{
	".mkv": {}, ".mp4": {}, ".avi": {}, ".m4v": {}, ".ts": {}, ".m2ts": {}, ".wmv": {}, ".mpg": {},
	".mpeg": {}, ".mov": {}, ".flac": {}, ".mp3": {}, ".m4a": {}, ".aac": {}, ".ogg": {}, ".opus": {},
}

type Source interface {
	ReaderAt(fileIndex int) (io.ReaderAt, bool)
}

// Limiter matches golang.org/x/time/rate.Limiter; its burst must be at least readChunkSize (64 KiB).
type Limiter interface {
	WaitN(ctx context.Context, n int) error
}

type Verifier struct {
	Limiter Limiter
}

type Report struct {
	VerifiedPieces int
	TotalPieces    int
	MissingPieces  []int
	MediaComplete  bool
}

func VerifyPieces(torrent *Torrent, source Source, pieceIndexes []int) (verified []int, err error) {
	return Verifier{}.VerifyPieces(context.Background(), torrent, source, pieceIndexes)
}

func Coverage(torrent *Torrent, source Source) (Report, error) {
	return Verifier{}.Coverage(context.Background(), torrent, source, nil)
}

func (report Report) VerifiedSet() map[int]struct{} {
	missing := make(map[int]struct{}, len(report.MissingPieces))
	for _, pieceIndex := range report.MissingPieces {
		missing[pieceIndex] = struct{}{}
	}
	verified := make(map[int]struct{}, report.VerifiedPieces)
	for pieceIndex := range report.TotalPieces {
		if _, isMissing := missing[pieceIndex]; !isMissing {
			verified[pieceIndex] = struct{}{}
		}
	}
	return verified
}

func (verifier Verifier) VerifyPieces(ctx context.Context, torrent *Torrent, source Source, pieceIndexes []int) ([]int, error) {
	buffer := make([]byte, readChunkSize)
	var verified []int
	for _, pieceIndex := range pieceIndexes {
		if pieceIndex < 0 || pieceIndex >= len(torrent.Pieces) {
			return verified, fmt.Errorf("%w: %d", ErrPieceIndexOutOfRange, pieceIndex)
		}
		matches, err := verifier.pieceMatches(ctx, torrent, source, pieceIndex, buffer)
		if err != nil {
			return verified, err
		}
		if matches {
			verified = append(verified, pieceIndex)
		}
	}
	return verified, nil
}

func (verifier Verifier) Coverage(ctx context.Context, torrent *Torrent, source Source, alreadyVerified map[int]struct{}) (Report, error) {
	verified := make(map[int]struct{}, len(torrent.Pieces))
	var pending []int
	for pieceIndex := range torrent.Pieces {
		if _, done := alreadyVerified[pieceIndex]; done {
			verified[pieceIndex] = struct{}{}
			continue
		}
		pending = append(pending, pieceIndex)
	}
	newlyVerified, err := verifier.VerifyPieces(ctx, torrent, source, pending)
	for _, pieceIndex := range newlyVerified {
		verified[pieceIndex] = struct{}{}
	}
	return buildReport(torrent, source, verified), err
}

func buildReport(torrent *Torrent, source Source, verified map[int]struct{}) Report {
	report := Report{TotalPieces: len(torrent.Pieces), VerifiedPieces: len(verified)}
	for pieceIndex := range torrent.Pieces {
		if _, isVerified := verified[pieceIndex]; !isVerified {
			report.MissingPieces = append(report.MissingPieces, pieceIndex)
		}
	}
	report.MediaComplete = mediaComplete(torrent, source, verified)
	return report
}

func mediaComplete(torrent *Torrent, source Source, verified map[int]struct{}) bool {
	hasMediaBytes := false
	for fileIndex, file := range torrent.Files {
		if !isMediaFile(file) || file.Size == 0 {
			continue
		}
		hasMediaBytes = true
		if !isPresent(source, fileIndex) {
			return false
		}
		if !mediaFilePiecesAcceptable(torrent, source, file, verified) {
			return false
		}
	}
	return hasMediaBytes
}

func mediaFilePiecesAcceptable(torrent *Torrent, source Source, file File, verified map[int]struct{}) bool {
	firstPiece := int(file.Offset / torrent.PieceLength)
	lastPiece := int((file.Offset + file.Size - 1) / torrent.PieceLength)
	for pieceIndex := firstPiece; pieceIndex <= lastPiece; pieceIndex++ {
		_, isVerified := verified[pieceIndex]
		if !isVerified && !touchesAbsentFile(torrent, source, pieceIndex) {
			return false
		}
	}
	return true
}

func isMediaFile(file File) bool {
	_, isMedia := mediaExtensions[strings.ToLower(path.Ext(file.Path))]
	return isMedia
}

func isPresent(source Source, fileIndex int) bool {
	_, present := source.ReaderAt(fileIndex)
	return present
}

func touchesAbsentFile(torrent *Torrent, source Source, pieceIndex int) bool {
	start, end := torrent.pieceBounds(pieceIndex)
	for fileIndex, file := range torrent.Files {
		if file.overlaps(start, end) && !isPresent(source, fileIndex) {
			return true
		}
	}
	return false
}

func (verifier Verifier) pieceMatches(ctx context.Context, torrent *Torrent, source Source, pieceIndex int, buffer []byte) (bool, error) {
	if touchesAbsentFile(torrent, source, pieceIndex) {
		return false, nil
	}
	start, end := torrent.pieceBounds(pieceIndex)
	hasher := sha1.New()
	position := start
	for fileIndex, file := range torrent.Files {
		if !file.overlaps(start, end) {
			continue
		}
		segmentStart := max(start, file.Offset)
		segmentEnd := min(end, file.Offset+file.Size)
		writeZeros(hasher, segmentStart-position, buffer)
		reader, _ := source.ReaderAt(fileIndex)
		complete, err := verifier.hashSegment(ctx, hasher, reader, segmentStart-file.Offset, segmentEnd-segmentStart, buffer)
		if err != nil || !complete {
			return false, err
		}
		position = segmentEnd
	}
	writeZeros(hasher, end-position, buffer)
	return bytes.Equal(hasher.Sum(nil), torrent.Pieces[pieceIndex][:]), nil
}

func (verifier Verifier) hashSegment(ctx context.Context, hasher hash.Hash, reader io.ReaderAt, offset, length int64, buffer []byte) (bool, error) {
	for length > 0 {
		chunk := buffer[:min(int64(len(buffer)), length)]
		if err := verifier.waitForBudget(ctx, len(chunk)); err != nil {
			return false, err
		}
		readCount, err := reader.ReadAt(chunk, offset)
		hasher.Write(chunk[:readCount])
		isShortRead := readCount < len(chunk)
		if isShortRead && (err == nil || errors.Is(err, io.EOF)) {
			return false, nil
		}
		if isShortRead {
			return false, err
		}
		offset += int64(readCount)
		length -= int64(readCount)
	}
	return true, nil
}

func (verifier Verifier) waitForBudget(ctx context.Context, byteCount int) error {
	if verifier.Limiter == nil {
		return ctx.Err()
	}
	return verifier.Limiter.WaitN(ctx, byteCount)
}

func writeZeros(hasher hash.Hash, count int64, buffer []byte) {
	if count <= 0 {
		return
	}
	clear(buffer)
	for count > 0 {
		chunk := buffer[:min(int64(len(buffer)), count)]
		hasher.Write(chunk)
		count -= int64(len(chunk))
	}
}
