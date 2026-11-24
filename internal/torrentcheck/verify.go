package torrentcheck

import (
	"bytes"
	"context"
	"crypto/sha1" // #nosec G505 -- SHA-1 imposé par le format BitTorrent v1
	"errors"
	"fmt"
	"hash"
	"io"
	"path"
	"strings"
)

const readChunkSize = 64 << 10

// ErrPieceIndexOutOfRange signale un index de pièce hors du torrent.
var ErrPieceIndexOutOfRange = errors.New("torrentcheck: piece index out of range")

// mediaExtensions liste les extensions traitées comme média, en minuscules.
var mediaExtensions = map[string]struct{}{
	".mkv": {}, ".mp4": {}, ".avi": {}, ".m4v": {}, ".ts": {}, ".m2ts": {}, ".wmv": {}, ".mpg": {},
	".mpeg": {}, ".mov": {}, ".flac": {}, ".mp3": {}, ".m4a": {}, ".aac": {}, ".ogg": {}, ".opus": {},
}

// Source fournit les fichiers locaux du torrent par index ; false si le fichier est absent.
// ReaderAt doit être peu coûteux et idempotent (handles en cache) : il est appelé une fois par fichier chevauchant, pour chaque pièce.
type Source interface {
	ReaderAt(fileIndex int) (io.ReaderAt, bool)
}

// Limiter plafonne le débit de lecture ; compatible avec golang.org/x/time/rate.Limiter.
// Sa rafale doit valoir au moins readChunkSize (64 Kio), sinon WaitN échoue.
type Limiter interface {
	WaitN(ctx context.Context, n int) error
}

// Verifier vérifie les pièces ; Limiter nil signifie lecture sans plafond de débit. Progress, s'il est posé, est
// appelé après chaque pièce hachée (pas pour une pièce sautée faute de fichier) ; une erreur arrête la vérification,
// ce qui permet d'enregistrer l'avancement au fil de l'eau et de s'arrêter si cet enregistrement n'est plus permis.
type Verifier struct {
	Limiter  Limiter
	Progress func(pieceIndex int, verified bool) error
}

// Report résume la couverture : MissingPieces liste les pièces non vérifiées ; MediaComplete est vrai si tous les fichiers média sont présents
// et si chacune de leurs pièces est vérifiée ou ne bloque que sur un fichier non média absent. FailedPieces, sous-ensemble
// trié de MissingPieces, liste les pièces dont tous les fichiers étaient présents et dont le SHA1 ne concorde pas
// (contenu faux, pas simplement absent).
type Report struct {
	VerifiedPieces int
	TotalPieces    int
	MissingPieces  []int
	FailedPieces   []int
	MediaComplete  bool
}

// VerifyPieces vérifie les pièces demandées sans plafond de débit et renvoie celles dont le SHA1 correspond.
func VerifyPieces(torrent *Torrent, source Source, pieceIndexes []int) (verified []int, err error) {
	return Verifier{}.VerifyPieces(context.Background(), torrent, source, pieceIndexes)
}

// Coverage vérifie toutes les pièces sans plafond de débit et renvoie le rapport.
func Coverage(torrent *Torrent, source Source) (Report, error) {
	return Verifier{}.Coverage(context.Background(), torrent, source, nil)
}

// VerifiedSet renvoie l'ensemble des index de pièces vérifiées.
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
	failed            map[int]struct{}
	blockedByAbsence  map[int]struct{}
	absentFileIndexes map[int]struct{}
}

func newScanState() *scanState {
	return &scanState{
		verified:          map[int]struct{}{},
		failed:            map[int]struct{}{},
		blockedByAbsence:  map[int]struct{}{},
		absentFileIndexes: map[int]struct{}{},
	}
}

// VerifyPieces vérifie les pièces demandées et renvoie celles dont le SHA1 correspond ; s'arrête sur ctx annulé,
// une erreur de lecture ou un index hors limites, en renvoyant les pièces déjà vérifiées.
func (verifier Verifier) VerifyPieces(ctx context.Context, torrent *Torrent, source Source, pieceIndexes []int) ([]int, error) {
	return verifier.scan(ctx, torrent, source, pieceIndexes, newScanState())
}

// Coverage vérifie les pièces absentes de alreadyVerified (reprise sans relire), puis renvoie le rapport ;
// le rapport reflète le travail fait même si l'erreur est non nulle. Une pièce de alreadyVerified dont un fichier
// manque à source n'est pas reprise : un octet n'est compté possédé que s'il est disponible.
func (verifier Verifier) Coverage(ctx context.Context, torrent *Torrent, source Source, alreadyVerified map[int]struct{}) (Report, error) {
	state := newScanState()
	var pending []int
	for pieceIndex := range torrent.Pieces {
		if _, done := alreadyVerified[pieceIndex]; done && allFilesPresent(torrent, source, pieceIndex) {
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
		// Une pièce chevauchant un fichier absent n'est jamais hachée : le hash ne pourrait pas correspondre
		// et la lecture serait gaspillée.
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
		} else {
			state.failed[pieceIndex] = struct{}{}
		}
		if verifier.Progress == nil {
			continue
		}
		if err := verifier.Progress(pieceIndex, matches); err != nil {
			return verified, err
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

func allFilesPresent(torrent *Torrent, source Source, pieceIndex int) bool {
	_, absentFileIndexes := pieceSegments(torrent, source, pieceIndex)
	return len(absentFileIndexes) == 0
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
		if _, hasFailed := state.failed[pieceIndex]; hasFailed {
			report.FailedPieces = append(report.FailedPieces, pieceIndex)
		}
	}
	report.MediaComplete = mediaComplete(torrent, state)
	return report
}

// mediaComplete tolère qu'une pièce d'un média présent soit bloquée par un fichier absent voisin (ex. .nfo) ;
// un média absent, lui, rend le résultat faux.
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

// IsMediaPath indique si l'extension du chemin est celle d'un fichier média.
func IsMediaPath(filePath string) bool {
	_, isMedia := mediaExtensions[strings.ToLower(path.Ext(filePath))]
	return isMedia
}

func (verifier Verifier) hashMatches(ctx context.Context, torrent *Torrent, pieceIndex int, segments []pieceSegment, buffer []byte) (bool, error) {
	start, end := torrent.pieceBounds(pieceIndex)
	hasher := sha1.New() // #nosec G401 -- SHA-1 imposé par BitTorrent v1
	position := start
	for _, segment := range segments {
		// Les octets hors fichiers (remplissage) sont hachés comme des zéros, comme le fait le format BEP 47.
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
