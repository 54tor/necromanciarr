package repair

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"necromanciarr/internal/store"
	"necromanciarr/internal/torrentcheck"
)

// scanDirectories cherche, dans les dossiers indiqués, les liens symboliques cassés absents de la base et les relie à
// leur torrent ; sans TorrentSource la méthode est sautée.
func (run *scanRun) scanDirectories(recorded map[string]struct{}) error {
	if run.options.TorrentSource == nil {
		return nil
	}
	directories := append([]string(nil), run.options.ScanDirectories...)
	sort.Strings(directories)
	for _, directory := range directories {
		err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := run.ctx.Err(); err != nil {
				return err
			}
			if _, known := recorded[path]; known || entry.Type()&fs.ModeSymlink == 0 {
				return nil
			}
			if _, err := os.Stat(path); err == nil {
				return nil
			}
			return run.repairThroughTorrent(path)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (run *scanRun) repairThroughTorrent(linkPath string) error {
	oldTarget, err := os.Readlink(linkPath)
	if err != nil {
		return err
	}
	reference, found, err := run.options.TorrentSource.TorrentForPath(run.ctx, linkPath)
	if err != nil {
		return err
	}
	if !found || reference.Torrent == nil || reference.FileIndex < 0 || reference.FileIndex >= len(reference.Torrent.Files) {
		run.report.Lost = append(run.report.Lost, linkPath)
		return nil
	}
	file := reference.Torrent.Files[reference.FileIndex]
	verified, truncated, err := run.verifyThroughPieces(reference, run.bySize[file.Size])
	if err != nil {
		return err
	}
	located := located{method: ByTorrentPieces, matches: paths(verified), unique: len(verified) == 1 && !truncated}
	run.conclude(linkPath, oldTarget, file.Size, !filepath.IsAbs(oldTarget), located)
	return nil
}

func (run *scanRun) verifyThroughPieces(reference TorrentRef, candidates []store.MediaFile) (verified []store.MediaFile, truncated bool, err error) {
	for readCount, candidate := range candidates {
		if readCount >= run.options.MaxCandidatesToRead {
			return verified, true, nil
		}
		matches, err := run.piecesMatch(reference, candidate.Path)
		if err != nil {
			return nil, false, err
		}
		if matches {
			verified = append(verified, candidate)
		}
	}
	return verified, false, nil
}

// piecesMatch exige que toutes les pièces entièrement contenues dans le fichier soient vérifiées par leur SHA1 ; un
// fichier trop petit pour en contenir une ne se vérifie pas.
func (run *scanRun) piecesMatch(reference TorrentRef, candidatePath string) (bool, error) {
	pieceIndexes := containedPieces(reference.Torrent, reference.FileIndex)
	if len(pieceIndexes) == 0 {
		return false, nil
	}
	file, err := os.Open(candidatePath) // #nosec G304 -- fichier de la bibliothèque, lu seulement
	if err != nil {
		return false, err
	}
	defer func() { _ = file.Close() }()
	source := singleFileSource{fileIndex: reference.FileIndex, reader: file}
	verifier := torrentcheck.Verifier{Limiter: run.options.Limiter}
	verified, err := verifier.VerifyPieces(run.ctx, reference.Torrent, source, pieceIndexes)
	if err != nil {
		return false, err
	}
	return len(verified) == len(pieceIndexes), nil
}

type singleFileSource struct {
	fileIndex int
	reader    io.ReaderAt
}

func (source singleFileSource) ReaderAt(fileIndex int) (io.ReaderAt, bool) {
	return source.reader, fileIndex == source.fileIndex
}

// containedPieces liste les pièces dont tous les octets appartiennent au fichier ; la dernière pièce, plus courte,
// compte si le fichier termine le torrent.
func containedPieces(torrent *torrentcheck.Torrent, fileIndex int) []int {
	file := torrent.Files[fileIndex]
	end := file.Offset + file.Size
	var indexes []int
	for pieceIndex := range torrent.Pieces {
		pieceStart := int64(pieceIndex) * torrent.PieceLength
		pieceEnd := min(pieceStart+torrent.PieceLength, torrentEnd(torrent))
		if pieceStart >= file.Offset && pieceEnd <= end {
			indexes = append(indexes, pieceIndex)
		}
	}
	return indexes
}

func torrentEnd(torrent *torrentcheck.Torrent) int64 {
	var end int64
	for _, file := range torrent.Files {
		end = max(end, file.Offset+file.Size)
	}
	return end
}
