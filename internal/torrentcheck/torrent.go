package torrentcheck

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/anacrolix/torrent/metainfo"
)

const (
	// pieceHashSize est la taille en octets d'un hash SHA1 de pièce ; paddingAttribute et legacyPaddingPrefix repèrent les fichiers de remplissage.
	pieceHashSize          = 20
	paddingAttribute       = "p"
	legacyPaddingPrefix    = "_____padding_file_"
	torrentPathSeparator   = "/"
	bep52MetaVersionV2Only = 2
)

// ErrV2Only et ErrInvalidTorrent sont les erreurs de Parse ; ErrInvalidTorrent porte le détail en erreur enrobée.
var (
	ErrV2Only         = errors.New("torrentcheck: v2-only torrent is not supported")
	ErrInvalidTorrent = errors.New("torrentcheck: invalid torrent")
)

// Torrent est un torrent v1 validé : Files exclut les fichiers de remplissage, Offset est la position dans le flux de pièces.
type Torrent struct {
	InfoHash    string
	Root        string
	PieceLength int64
	Pieces      [][20]byte
	Files       []File
}

// File est un fichier du torrent ; Path utilise « / » et ne contient aucun segment dangereux.
type File struct {
	Path   string
	Size   int64
	Offset int64
}

// Parse décode et valide un .torrent v1 ; les v2 seuls sont refusés (ErrV2Only), toute incohérence donne ErrInvalidTorrent.
func Parse(raw []byte) (*Torrent, error) {
	metaInfo, err := metainfo.Load(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidTorrent, err)
	}
	info, err := metaInfo.UnmarshalInfo()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidTorrent, err)
	}
	if len(info.Pieces) == 0 && info.MetaVersion == bep52MetaVersionV2Only {
		return nil, ErrV2Only
	}
	if info.PieceLength <= 0 || len(info.Pieces)%pieceHashSize != 0 {
		return nil, fmt.Errorf("%w: bad piece length or pieces field", ErrInvalidTorrent)
	}
	files, totalLength, err := v1Files(&info)
	if err != nil {
		return nil, err
	}
	if err := checkPieceCount(len(info.Pieces)/pieceHashSize, totalLength, info.PieceLength); err != nil {
		return nil, err
	}
	return &Torrent{
		InfoHash:    metaInfo.HashInfoBytes().HexString(),
		Root:        info.BestName(),
		PieceLength: info.PieceLength,
		Pieces:      splitPieceHashes(info.Pieces),
		Files:       files,
	}, nil
}

func v1Files(info *metainfo.Info) (files []File, totalLength int64, err error) {
	if !isSafePathPart(info.BestName()) {
		return nil, 0, fmt.Errorf("%w: unsafe name %q", ErrInvalidTorrent, info.BestName())
	}
	if len(info.Files) == 0 {
		if info.Length < 0 {
			return nil, 0, fmt.Errorf("%w: negative length", ErrInvalidTorrent)
		}
		return []File{{Path: info.BestName(), Size: info.Length, Offset: 0}}, info.Length, nil
	}
	for _, fileInfo := range info.Files {
		bestPath := fileInfo.BestPath()
		if err := checkFileEntry(bestPath, fileInfo.Length); err != nil {
			return nil, 0, err
		}
		if fileInfo.Length > math.MaxInt64-totalLength {
			return nil, 0, fmt.Errorf("%w: total length overflows int64", ErrInvalidTorrent)
		}
		if !isPaddingFile(fileInfo.Attr, bestPath) {
			files = append(files, File{Path: strings.Join(bestPath, torrentPathSeparator), Size: fileInfo.Length, Offset: totalLength})
		}
		totalLength += fileInfo.Length
	}
	return files, totalLength, nil
}

func checkFileEntry(pathParts []string, length int64) error {
	if length < 0 {
		return fmt.Errorf("%w: negative file length", ErrInvalidTorrent)
	}
	if len(pathParts) == 0 {
		return fmt.Errorf("%w: empty file path", ErrInvalidTorrent)
	}
	for _, pathPart := range pathParts {
		if !isSafePathPart(pathPart) {
			return fmt.Errorf("%w: unsafe path part %q", ErrInvalidTorrent, pathPart)
		}
	}
	return nil
}

// isSafePathPart refuse les segments qui sortiraient du répertoire de destination (traversée de chemin).
func isSafePathPart(pathPart string) bool {
	isTraversal := pathPart == "" || pathPart == "." || pathPart == ".."
	hasSeparatorOrNul := strings.ContainsAny(pathPart, "/\\\x00")
	return !isTraversal && !hasSeparatorOrNul
}

// isPaddingFile reconnaît le remplissage BEP 47 et l'ancien nommage _____padding_file_.
func isPaddingFile(attributes string, pathParts []string) bool {
	if strings.Contains(attributes, paddingAttribute) {
		return true
	}
	hasLegacyPaddingName := len(pathParts) > 0 && strings.HasPrefix(pathParts[len(pathParts)-1], legacyPaddingPrefix)
	return hasLegacyPaddingName
}

func checkPieceCount(pieceCount int, totalLength, pieceLength int64) error {
	expectedPieceCount := totalLength / pieceLength
	if totalLength%pieceLength != 0 {
		expectedPieceCount++
	}
	if int64(pieceCount) != expectedPieceCount {
		return fmt.Errorf("%w: %d pieces for %d bytes of %d-byte pieces", ErrInvalidTorrent, pieceCount, totalLength, pieceLength)
	}
	return nil
}

func splitPieceHashes(concatenated []byte) [][20]byte {
	hashes := make([][20]byte, len(concatenated)/pieceHashSize)
	for pieceIndex := range hashes {
		copy(hashes[pieceIndex][:], concatenated[pieceIndex*pieceHashSize:])
	}
	return hashes
}

func (torrent *Torrent) pieceBounds(pieceIndex int) (start, end int64) {
	start = int64(pieceIndex) * torrent.PieceLength
	isLastPiece := pieceIndex == len(torrent.Pieces)-1
	if !isLastPiece {
		return start, start + torrent.PieceLength
	}
	return start, start + max(0, min(torrent.PieceLength, torrent.dataEnd()-start))
}

func (torrent *Torrent) dataEnd() int64 {
	var end int64
	for _, file := range torrent.Files {
		end = max(end, file.Offset+file.Size)
	}
	return end
}

func (file File) overlaps(start, end int64) bool {
	return file.Size > 0 && file.Offset < end && file.Offset+file.Size > start
}
