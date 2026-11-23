package torrentcheck

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"io"
	"slices"
	"sync"
	"testing"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

const testPieceLength = 16

type testFile struct {
	path    []string
	content []byte
	attr    string
}

// Layout (piece length 16, total 68 bytes, 5 pieces):
// movie.mkv [0,40) movie.nfo [40,45) empty.txt [45,45) cover.jpg [45,48) bonus.mp4 [48,68).
// Piece 2 [32,48) spans movie.mkv, movie.nfo and cover.jpg; piece 4 [64,68) is short.
const (
	indexMovie = 0
	indexNfo   = 1
	indexEmpty = 2
	indexCover = 3
	indexBonus = 4
)

func deterministicBytes(seed byte, size int) []byte {
	content := make([]byte, size)
	for position := range content {
		content[position] = seed + byte(position*7)
	}
	return content
}

func standardFiles() []testFile {
	return []testFile{
		{path: []string{"movie.mkv"}, content: deterministicBytes(1, 40)},
		{path: []string{"movie.nfo"}, content: deterministicBytes(50, 5)},
		{path: []string{"empty.txt"}, content: []byte{}},
		{path: []string{"cover.jpg"}, content: deterministicBytes(90, 3)},
		{path: []string{"extras", "bonus.mp4"}, content: deterministicBytes(120, 20)},
	}
}

func pieceHashes(data []byte, pieceLength int) []byte {
	var hashes []byte
	for start := 0; start < len(data); start += pieceLength {
		end := min(start+pieceLength, len(data))
		sum := sha1.Sum(data[start:end])
		hashes = append(hashes, sum[:]...)
	}
	return hashes
}

func encodeTorrent(t *testing.T, info metainfo.Info) (raw []byte, infoBytes []byte) {
	t.Helper()
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatalf("marshal info: %v", err)
	}
	raw, err = bencode.Marshal(metainfo.MetaInfo{InfoBytes: infoBytes, Announce: "http://tracker.example.com/announce"})
	if err != nil {
		t.Fatalf("marshal metainfo: %v", err)
	}
	return raw, infoBytes
}

func buildMultiFileTorrent(t *testing.T, files []testFile) []byte {
	t.Helper()
	var concatenated []byte
	info := metainfo.Info{Name: "Movie.2020", PieceLength: testPieceLength}
	for _, file := range files {
		concatenated = append(concatenated, file.content...)
		info.Files = append(info.Files, metainfo.FileInfo{
			Path:              file.path,
			Length:            int64(len(file.content)),
			ExtendedFileAttrs: metainfo.ExtendedFileAttrs{Attr: file.attr},
		})
	}
	info.Pieces = pieceHashes(concatenated, testPieceLength)
	raw, _ := encodeTorrent(t, info)
	return raw
}

func parseStandardTorrent(t *testing.T) *Torrent {
	t.Helper()
	parsed, err := Parse(buildMultiFileTorrent(t, standardFiles()))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return parsed
}

type memorySource struct {
	contents map[int][]byte
}

func (source memorySource) ReaderAt(fileIndex int) (io.ReaderAt, bool) {
	content, present := source.contents[fileIndex]
	if !present {
		return nil, false
	}
	return bytes.NewReader(content), true
}

func sourceFromFiles(files []testFile) memorySource {
	contents := map[int][]byte{}
	for fileIndex, file := range files {
		contents[fileIndex] = slices.Clone(file.content)
	}
	return memorySource{contents: contents}
}

type readRecord struct {
	fileIndex int
	offset    int64
	length    int
}

type recordingSource struct {
	inner      Source
	mutex      sync.Mutex
	reads      []readRecord
	failOnFile int
}

func (source *recordingSource) ReaderAt(fileIndex int) (io.ReaderAt, bool) {
	reader, present := source.inner.ReaderAt(fileIndex)
	if !present {
		return nil, false
	}
	return recordingReader{source: source, fileIndex: fileIndex, inner: reader}, true
}

type recordingReader struct {
	source    *recordingSource
	fileIndex int
	inner     io.ReaderAt
}

var errSimulatedReadFailure = errors.New("simulated read failure")

func (reader recordingReader) ReadAt(buffer []byte, offset int64) (int, error) {
	reader.source.mutex.Lock()
	reader.source.reads = append(reader.source.reads, readRecord{fileIndex: reader.fileIndex, offset: offset, length: len(buffer)})
	shouldFail := reader.source.failOnFile == reader.fileIndex
	reader.source.mutex.Unlock()
	if shouldFail {
		return 0, errSimulatedReadFailure
	}
	return reader.inner.ReadAt(buffer, offset)
}

func pieceIndexesOfRead(parsed *Torrent, read readRecord) []int {
	file := parsed.Files[read.fileIndex]
	firstByte := file.Offset + read.offset
	lastByte := firstByte + int64(read.length) - 1
	var indexes []int
	for pieceIndex := firstByte / parsed.PieceLength; pieceIndex <= lastByte/parsed.PieceLength; pieceIndex++ {
		indexes = append(indexes, int(pieceIndex))
	}
	return indexes
}

func allPieceIndexes(parsed *Torrent) []int {
	var indexes []int
	for pieceIndex := range parsed.Pieces {
		indexes = append(indexes, pieceIndex)
	}
	return indexes
}

func TestParse_multiFileTorrentKeepsRootPathsAndOffsets(t *testing.T) {
	parsed := parseStandardTorrent(t)

	expectedFiles := []File{
		{Path: "movie.mkv", Size: 40, Offset: 0},
		{Path: "movie.nfo", Size: 5, Offset: 40},
		{Path: "empty.txt", Size: 0, Offset: 45},
		{Path: "cover.jpg", Size: 3, Offset: 45},
		{Path: "extras/bonus.mp4", Size: 20, Offset: 48},
	}
	if !slices.Equal(parsed.Files, expectedFiles) {
		t.Fatalf("files: expected %+v, got %+v", expectedFiles, parsed.Files)
	}
	if parsed.Root != "Movie.2020" || parsed.PieceLength != testPieceLength || len(parsed.Pieces) != 5 {
		t.Fatalf("unexpected header: root %q, piece length %d, pieces %d", parsed.Root, parsed.PieceLength, len(parsed.Pieces))
	}
}

func TestParse_infoHashIsSHA1OfRawInfoDictionary(t *testing.T) {
	info := metainfo.Info{Name: "a.mkv", PieceLength: testPieceLength, Length: 3, Pieces: pieceHashes([]byte("abc"), testPieceLength)}
	raw, infoBytes := encodeTorrent(t, info)
	expectedSum := sha1.Sum(infoBytes)
	expectedValue := hex.EncodeToString(expectedSum[:])

	parsed, err := Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	gotValue := parsed.InfoHash
	if gotValue != expectedValue {
		t.Fatalf("expected %s, got %s", expectedValue, gotValue)
	}
}

func TestParse_singleFileTorrentUsesNameAsRootAndPath(t *testing.T) {
	content := deterministicBytes(3, 20)
	info := metainfo.Info{Name: "single.mkv", PieceLength: testPieceLength, Length: 20, Pieces: pieceHashes(content, testPieceLength)}
	raw, _ := encodeTorrent(t, info)

	parsed, err := Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	expectedValue := []File{{Path: "single.mkv", Size: 20, Offset: 0}}
	if parsed.Root != "single.mkv" || !slices.Equal(parsed.Files, expectedValue) {
		t.Fatalf("expected root single.mkv and %+v, got %q and %+v", expectedValue, parsed.Root, parsed.Files)
	}
	report, err := Coverage(parsed, memorySource{contents: map[int][]byte{0: content}})
	if err != nil || report.VerifiedPieces != 2 || !report.MediaComplete {
		t.Fatalf("expected 2 verified pieces and complete media, got %+v (err %v)", report, err)
	}
}

func TestParse_rejectsV2OnlyTorrent(t *testing.T) {
	info := metainfo.Info{Name: "v2.mkv", PieceLength: testPieceLength, MetaVersion: 2}
	raw, _ := encodeTorrent(t, info)

	_, err := Parse(raw)
	if !errors.Is(err, ErrV2Only) {
		t.Fatalf("expected ErrV2Only, got %v", err)
	}
}

func TestParse_rejectsPieceCountNotMatchingLength(t *testing.T) {
	info := metainfo.Info{Name: "a.mkv", PieceLength: testPieceLength, Length: 40, Pieces: pieceHashes([]byte("abc"), testPieceLength)}
	raw, _ := encodeTorrent(t, info)

	_, err := Parse(raw)
	if !errors.Is(err, ErrInvalidTorrent) {
		t.Fatalf("expected ErrInvalidTorrent, got %v", err)
	}
}

func TestVerifyPieces_acceptsExactBytes(t *testing.T) {
	parsed := parseStandardTorrent(t)
	expectedValue := []int{0, 1, 2, 3, 4}

	gotValue, err := VerifyPieces(parsed, sourceFromFiles(standardFiles()), expectedValue)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !slices.Equal(gotValue, expectedValue) {
		t.Fatalf("expected %v, got %v", expectedValue, gotValue)
	}
}

func TestVerifyPieces_rejectsCorruptedByte(t *testing.T) {
	parsed := parseStandardTorrent(t)
	source := sourceFromFiles(standardFiles())
	source.contents[indexMovie][33] ^= 0xFF
	expectedValue := []int{0, 1, 3, 4}

	gotValue, err := VerifyPieces(parsed, source, allPieceIndexes(parsed))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !slices.Equal(gotValue, expectedValue) {
		t.Fatalf("expected %v, got %v", expectedValue, gotValue)
	}
}

func TestVerifyPieces_rejectsShorterFile(t *testing.T) {
	parsed := parseStandardTorrent(t)
	source := sourceFromFiles(standardFiles())
	source.contents[indexBonus] = source.contents[indexBonus][:18]
	expectedValue := []int{0, 1, 2, 3}

	gotValue, err := VerifyPieces(parsed, source, allPieceIndexes(parsed))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !slices.Equal(gotValue, expectedValue) {
		t.Fatalf("expected %v, got %v", expectedValue, gotValue)
	}
}

func TestVerifyPieces_neverReadsPieceTouchingAbsentFile(t *testing.T) {
	parsed := parseStandardTorrent(t)
	inner := sourceFromFiles(standardFiles())
	delete(inner.contents, indexCover)
	source := &recordingSource{inner: inner, failOnFile: -1}

	gotValue, err := VerifyPieces(parsed, source, []int{2})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(gotValue) != 0 || len(source.reads) != 0 {
		t.Fatalf("expected no verified piece and no read, got %v and %d reads", gotValue, len(source.reads))
	}
}

func TestVerifyPieces_rejectsOutOfRangeIndex(t *testing.T) {
	parsed := parseStandardTorrent(t)

	_, err := VerifyPieces(parsed, sourceFromFiles(standardFiles()), []int{5})
	if !errors.Is(err, ErrPieceIndexOutOfRange) {
		t.Fatalf("expected ErrPieceIndexOutOfRange, got %v", err)
	}
}

func TestVerifyPieces_hashesPaddingFilesAsZeros(t *testing.T) {
	files := []testFile{
		{path: []string{"movie.mkv"}, content: deterministicBytes(1, 10)},
		{path: []string{".pad", "6"}, content: make([]byte, 6), attr: "p"},
		{path: []string{"movie.nfo"}, content: deterministicBytes(50, 5)},
	}
	parsed, err := Parse(buildMultiFileTorrent(t, files))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	expectedFiles := []File{{Path: "movie.mkv", Size: 10, Offset: 0}, {Path: "movie.nfo", Size: 5, Offset: 16}}
	if !slices.Equal(parsed.Files, expectedFiles) {
		t.Fatalf("files: expected %+v, got %+v", expectedFiles, parsed.Files)
	}
	source := memorySource{contents: map[int][]byte{0: files[0].content, 1: files[2].content}}
	expectedValue := []int{0, 1}

	gotValue, err := VerifyPieces(parsed, source, []int{0, 1})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !slices.Equal(gotValue, expectedValue) {
		t.Fatalf("expected %v, got %v", expectedValue, gotValue)
	}
}

func TestCoverage_missingAnnexLeavesStraddlingPiecesMissing(t *testing.T) {
	parsed := parseStandardTorrent(t)
	source := sourceFromFiles(standardFiles())
	delete(source.contents, indexNfo)
	delete(source.contents, indexEmpty)

	report, err := Coverage(parsed, source)
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	expectedValue := Report{VerifiedPieces: 4, TotalPieces: 5, MissingPieces: []int{2}, MediaComplete: true}
	if report.VerifiedPieces != expectedValue.VerifiedPieces || report.TotalPieces != expectedValue.TotalPieces ||
		!slices.Equal(report.MissingPieces, expectedValue.MissingPieces) || !report.MediaComplete {
		t.Fatalf("expected %+v, got %+v", expectedValue, report)
	}
}

func TestCoverage_corruptedMediaIsNotComplete(t *testing.T) {
	parsed := parseStandardTorrent(t)
	source := sourceFromFiles(standardFiles())
	source.contents[indexBonus][0] ^= 0xFF

	report, err := Coverage(parsed, source)
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	if report.MediaComplete || !slices.Equal(report.MissingPieces, []int{3}) {
		t.Fatalf("expected incomplete media with piece 3 missing, got %+v", report)
	}
}

func TestCoverage_absentMediaIsNotComplete(t *testing.T) {
	parsed := parseStandardTorrent(t)
	source := sourceFromFiles(standardFiles())
	delete(source.contents, indexBonus)

	report, err := Coverage(parsed, source)
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	if report.MediaComplete || !slices.Equal(report.MissingPieces, []int{3, 4}) {
		t.Fatalf("expected incomplete media with pieces 3 and 4 missing, got %+v", report)
	}
}

func TestCoverage_resumesWithoutRereadingVerifiedPieces(t *testing.T) {
	parsed := parseStandardTorrent(t)
	interrupted := &recordingSource{inner: sourceFromFiles(standardFiles()), failOnFile: indexBonus}

	partial, err := Coverage(parsed, interrupted)
	if !errors.Is(err, errSimulatedReadFailure) {
		t.Fatalf("expected simulated failure, got %v", err)
	}
	alreadyVerified := partial.VerifiedSet()
	if len(alreadyVerified) != 3 {
		t.Fatalf("expected pieces 0-2 verified before the failure, got %+v", partial)
	}

	resumed := &recordingSource{inner: sourceFromFiles(standardFiles()), failOnFile: -1}
	report, err := Verifier{}.Coverage(context.Background(), parsed, resumed, alreadyVerified)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	for _, read := range resumed.reads {
		for _, pieceIndex := range pieceIndexesOfRead(parsed, read) {
			if _, verified := alreadyVerified[pieceIndex]; verified {
				t.Fatalf("piece %d re-read on resume (read %+v)", pieceIndex, read)
			}
		}
	}
	if report.VerifiedPieces != 5 || len(report.MissingPieces) != 0 || !report.MediaComplete {
		t.Fatalf("expected full coverage after resume, got %+v", report)
	}
}

type countingLimiter struct {
	totalBytes int
}

func (limiter *countingLimiter) WaitN(_ context.Context, byteCount int) error {
	limiter.totalBytes += byteCount
	return nil
}

func TestCoverage_limiterIsChargedForEveryReadByte(t *testing.T) {
	parsed := parseStandardTorrent(t)
	limiter := &countingLimiter{}
	expectedValue := 68

	_, err := Verifier{Limiter: limiter}.Coverage(context.Background(), parsed, sourceFromFiles(standardFiles()), nil)
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	gotValue := limiter.totalBytes
	if gotValue != expectedValue {
		t.Fatalf("expected %d bytes charged, got %d", expectedValue, gotValue)
	}
}

func variantLabels(variants []Variant) []string {
	var labels []string
	for _, variant := range variants {
		labels = append(labels, variant.Label)
	}
	return labels
}

func TestNfoVariants_includesCRLFAndNoBOM(t *testing.T) {
	raw := []byte("\xEF\xBB\xBFline1\nline2\r\nline3\r")
	expectedValue := []Variant{
		{Label: "brut", Data: raw},
		{Label: "sans-bom", Data: []byte("line1\nline2\r\nline3\r")},
		{Label: "lf", Data: []byte("line1\nline2\nline3\n")},
		{Label: "crlf", Data: []byte("line1\r\nline2\r\nline3\r\n")},
	}

	gotValue := NfoVariants(raw)
	if len(gotValue) != len(expectedValue) {
		t.Fatalf("expected labels %v, got %v", variantLabels(expectedValue), variantLabels(gotValue))
	}
	for variantIndex, expectedVariant := range expectedValue {
		gotVariant := gotValue[variantIndex]
		if gotVariant.Label != expectedVariant.Label || !bytes.Equal(gotVariant.Data, expectedVariant.Data) {
			t.Fatalf("variant %d: expected %q %q, got %q %q", variantIndex, expectedVariant.Label, expectedVariant.Data, gotVariant.Label, gotVariant.Data)
		}
	}
}

func TestNfoVariants_dropsDuplicatesKeepingFirstLabel(t *testing.T) {
	raw := []byte("line1\nline2\n")
	expectedValue := []string{"brut", "crlf"}

	gotValue := variantLabels(NfoVariants(raw))
	if !slices.Equal(gotValue, expectedValue) {
		t.Fatalf("expected %v, got %v", expectedValue, gotValue)
	}
}
