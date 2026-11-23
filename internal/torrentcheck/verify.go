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

// Source.ReaderAt must be cheap and idempotent (cached handles): it is called once per overlapping file per piece.
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

type pieceSegment struct {
	reader     io.ReaderAt
	fileOffset int64
	start      int64
	end        int64
}

type scanState struct {
	verified          map[int]struct{}
	blockedByAbsence  map[int]struct{}
	absentFileIndexes map[int]struct{}
}

func newScanState() *scanState {
	return &scanState{
		verified:          map[int]struct{}{},
		blockedByAbsence:  map[int]struct{}{},
		absentFileIndexes: map[int]struct{}{},
	}
}

func (verifier Verifier) VerifyPieces(ctx context.Context, torrent *Torrent, source Source, pieceIndexes []int) ([]int, error) {
	return verifier.scan(ctx, torrent, source, pieceIndexes, newScanState())
}

func (verifier Verifier) Coverage(ctx context.Context, torrent *Torrent, source Source, alreadyVerified map[int]struct{}) (Report, error) {
	state := newScanState()
	var pending []int
	for pieceIndex := range torrent.Pieces {
		if _, done := alreadyVerified[pieceIndex]; done {
			state.verified[pieceIndex] = struct{}{}
			continue
		}
		pending = append(pending, pieceIndex)
	}
	_, err := verifier.scan(ctx, torrent, source, pending, state)
	return buildReport(torrent, state), err
}

func (verifier Verifier) scan(ctx context.Context, torrent *Torrent, source Source, pieceIndexes []int, state *scanState) ([]int, error) {
	buffer := make([]byte, readChunkSize)
	var verified []int
	for _, pieceIndex := range pieceIndexes {
		if pieceIndex < 0 || pieceIndex >= len(torrent.Pieces) {
			return verified, fmt.Errorf("%w: %d", ErrPieceIndexOutOfRange, pieceIndex)
		}
		segments, absentFileIndexes := pieceSegments(torrent, source, pieceIndex)
		if len(absentFileIndexes) > 0 {
			state.recordAbsence(pieceIndex, absentFileIndexes)
			continue
		}
		matches, err := verifier.hashMatches(ctx, torrent, pieceIndex, segments, buffer)
		if err != nil {
			return verified, err
		}
		if matches {
			state.verified[pieceIndex] = struct{}{}
			verified = append(verified, pieceIndex)
		}
	}
	return verified, nil
}

func (state *scanState) recordAbsence(pieceIndex int, absentFileIndexes []int) {
	state.blockedByAbsence[pieceIndex] = struct{}{}
	for _, fileIndex := range absentFileIndexes {
		state.absentFileIndexes[fileIndex] = struct{}{}
	}
}

func pieceSegments(torrent *Torrent, source Source, pieceIndex int) (segments []pieceSegment, absentFileIndexes []int) {
	start, end := torrent.pieceBounds(pieceIndex)
	for fileIndex, file := range torrent.Files {
		if !file.overlaps(start, end) {
			continue
		}
		reader, present := source.ReaderAt(fileIndex)
		if !present || reader == nil {
			absentFileIndexes = append(absentFileIndexes, fileIndex)
			continue
		}
		segments = append(segments, pieceSegment{
			reader:     reader,
			fileOffset: file.Offset,
			start:      max(start, file.Offset),
			end:        min(end, file.Offset+file.Size),
		})
	}
	return segments, absentFileIndexes
}

func buildReport(torrent *Torrent, state *scanState) Report {
	report := Report{TotalPieces: len(torrent.Pieces), VerifiedPieces: len(state.verified)}
	for pieceIndex := range torrent.Pieces {
		if _, isVerified := state.verified[pieceIndex]; !isVerified {
			report.MissingPieces = append(report.MissingPieces, pieceIndex)
		}
	}
	report.MediaComplete = mediaComplete(torrent, state)
	return report
}

func mediaComplete(torrent *Torrent, state *scanState) bool {
	hasMediaBytes := false
	for fileIndex, file := range torrent.Files {
		if !isMediaFile(file) || file.Size == 0 {
			continue
		}
		hasMediaBytes = true
		if _, isAbsent := state.absentFileIndexes[fileIndex]; isAbsent {
			return false
		}
		if !mediaFilePiecesAcceptable(torrent, file, state) {
			return false
		}
	}
	return hasMediaBytes
}

func mediaFilePiecesAcceptable(torrent *Torrent, file File, state *scanState) bool {
	firstPiece := int(file.Offset / torrent.PieceLength)
	lastPiece := int((file.Offset + file.Size - 1) / torrent.PieceLength)
	for pieceIndex := firstPiece; pieceIndex <= lastPiece; pieceIndex++ {
		_, isVerified := state.verified[pieceIndex]
		_, isBlockedByAbsence := state.blockedByAbsence[pieceIndex]
		if !isVerified && !isBlockedByAbsence {
			return false
		}
	}
	return true
}

func isMediaFile(file File) bool {
	return IsMediaPath(file.Path)
}

func IsMediaPath(filePath string) bool {
	_, isMedia := mediaExtensions[strings.ToLower(path.Ext(filePath))]
	return isMedia
}

func (verifier Verifier) hashMatches(ctx context.Context, torrent *Torrent, pieceIndex int, segments []pieceSegment, buffer []byte) (bool, error) {
	start, end := torrent.pieceBounds(pieceIndex)
	hasher := sha1.New()
	position := start
	for _, segment := range segments {
		writeZeros(hasher, segment.start-position, buffer)
		complete, err := verifier.hashSegment(ctx, hasher, segment.reader, segment.start-segment.fileOffset, segment.end-segment.start, buffer)
		if err != nil || !complete {
			return false, err
		}
		position = segment.end
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
