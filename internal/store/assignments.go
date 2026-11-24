package store

import (
	"cmp"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"time"
)

// ErrAssignmentMismatch signale une association prouvée incohérente : empreinte recalculée différente de celle de la
// vérification, ou génération périmée. Elle ne doit jamais servir à créer un lien.
var ErrAssignmentMismatch = errors.New("store: verified assignment does not match its fingerprint")

// AssignedFile est un fichier du torrent (index dans torrentcheck.Torrent.Files, remplissage exclu, distinct de
// CandidateFile.FileIndex qui suit la liste du fournisseur) associé par le hash à un fichier de la bibliothèque, avec la
// taille et la date du fichier local au moment de la vérification.
type AssignedFile struct {
	TorrentFileIndex int
	MediaFileID      int64
	Path             string
	Size             int64
	ModTime          time.Time
}

// VerifiedAssignment est l'association prouvée d'un candidat vérifié, pour la génération et l'empreinte données.
type VerifiedAssignment struct {
	CandidateID int64
	Generation  int64
	Fingerprint string
	Files       []AssignedFile
}

// AssignmentFingerprint est le sha256 hexadécimal des fichiers associés triés par index du torrent.
func AssignmentFingerprint(files []AssignedFile) string {
	sorted := slices.Clone(files)
	slices.SortFunc(sorted, func(left, right AssignedFile) int { return cmp.Compare(left.TorrentFileIndex, right.TorrentFileIndex) })
	hasher := sha256.New()
	for _, file := range sorted {
		_, _ = fmt.Fprintf(hasher, "%d\x00%d\x00%s\x00%d\x00%d\n", file.TorrentFileIndex, file.MediaFileID, file.Path, file.Size,
			file.ModTime.UnixNano())
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

// CompleteVerification enregistre, dans une seule transaction, la vérification terminée avec l'empreinte de
// l'association, l'association prouvée (qui remplace la précédente) et l'état verified du candidat (depuis fetched,
// ou verified sans effet) ; sinon ErrInvalidTransition et rien n'est écrit.
func (openedStore *Store) CompleteVerification(ctx context.Context, verification Verification, files []AssignedFile, now time.Time) error {
	verification.State = VerificationComplete
	verification.MappingFingerprint = AssignmentFingerprint(files)
	return openedStore.inTransaction(ctx, func(transaction *sql.Tx) error {
		current, err := candidateStateOf(ctx, transaction, verification.CandidateID)
		if err != nil {
			return err
		}
		if current != CandidateFetched && current != CandidateVerified {
			return fmt.Errorf("%w: complete verification from %s", ErrInvalidTransition, current)
		}
		if err := writeVerification(ctx, transaction, verification, now); err != nil {
			return fmt.Errorf("complete verification: %w", err)
		}
		if err := replaceAssignment(ctx, transaction, verification.CandidateID, files); err != nil {
			return err
		}
		return writeCandidateState(ctx, transaction, verification.CandidateID, CandidateVerified, now)
	})
}

func replaceAssignment(ctx context.Context, transaction *sql.Tx, candidateID int64, files []AssignedFile) error {
	if _, err := transaction.ExecContext(ctx, `DELETE FROM verified_assignments WHERE candidate_id = ?`, candidateID); err != nil {
		return fmt.Errorf("replace verified assignment: %w", err)
	}
	for _, file := range files {
		_, err := transaction.ExecContext(ctx, `INSERT INTO verified_assignments
	(candidate_id, generation, torrent_file_index, media_file_id, path, size, mtime_ns)
	VALUES (?, (SELECT generation FROM torrent_candidates WHERE id = ?), ?, ?, ?, ?, ?)`,
			candidateID, candidateID, file.TorrentFileIndex, file.MediaFileID, file.Path, file.Size, file.ModTime.UnixNano())
		if err != nil {
			return fmt.Errorf("insert verified assignment: %w", err)
		}
	}
	return nil
}

// LoadVerifiedAssignment renvoie l'association prouvée du candidat ; found est faux s'il n'en a pas (jamais vérifié,
// ou invalidé). ErrAssignmentMismatch si l'empreinte recalculée diffère de celle de la vérification ou si une ligne
// n'est pas de la génération courante du candidat.
func (openedStore *Store) LoadVerifiedAssignment(ctx context.Context, candidateID int64) (VerifiedAssignment, bool, error) {
	assignment := VerifiedAssignment{CandidateID: candidateID}
	var candidateGeneration int64
	err := openedStore.database.QueryRowContext(ctx, `SELECT verifications.generation, verifications.mapping_fingerprint,
	torrent_candidates.generation FROM verifications JOIN torrent_candidates ON torrent_candidates.id = verifications.candidate_id
	WHERE verifications.candidate_id = ? AND verifications.mapping_fingerprint != ''`, candidateID).
		Scan(&assignment.Generation, &assignment.Fingerprint, &candidateGeneration)
	if errors.Is(err, sql.ErrNoRows) {
		return VerifiedAssignment{}, false, nil
	}
	if err != nil {
		return VerifiedAssignment{}, false, fmt.Errorf("load verified assignment: %w", err)
	}
	files, generations, err := listAssignedFiles(ctx, openedStore.database, candidateID)
	if err != nil {
		return VerifiedAssignment{}, false, err
	}
	assignment.Files = files
	isCurrent := assignment.Generation == candidateGeneration && !slices.ContainsFunc(generations, func(generation int64) bool {
		return generation != candidateGeneration
	})
	if !isCurrent || AssignmentFingerprint(files) != assignment.Fingerprint {
		return VerifiedAssignment{}, true, ErrAssignmentMismatch
	}
	return assignment, true, nil
}

func listAssignedFiles(ctx context.Context, database queryer, candidateID int64) ([]AssignedFile, []int64, error) {
	rows, err := database.QueryContext(ctx, `SELECT generation, torrent_file_index, media_file_id, path, size, mtime_ns
FROM verified_assignments WHERE candidate_id = ? ORDER BY torrent_file_index`, candidateID)
	if err != nil {
		return nil, nil, fmt.Errorf("list verified assignment: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var files []AssignedFile
	var generations []int64
	for rows.Next() {
		var file AssignedFile
		var generation, modTimeNanoseconds int64
		if err := rows.Scan(&generation, &file.TorrentFileIndex, &file.MediaFileID, &file.Path, &file.Size, &modTimeNanoseconds); err != nil {
			return nil, nil, fmt.Errorf("scan verified assignment: %w", err)
		}
		file.ModTime = time.Unix(0, modTimeNanoseconds).UTC()
		files = append(files, file)
		generations = append(generations, generation)
	}
	return files, generations, rows.Err()
}
