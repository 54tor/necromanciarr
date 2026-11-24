package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// CandidateHold est un verdict de Verify mis de côté au lieu de devenir une proposition (pièces manquantes au-delà
// du plafond) : Reason le nomme, Kind, Warnings et Details sont ceux de la proposition qu'il aurait créée.
type CandidateHold struct {
	CandidateID int64
	Generation  int64
	Reason      string
	Kind        ProposalKind
	Warnings    []string
	Details     json.RawMessage
	UpdatedAt   time.Time
}

// SaveCandidateHold enregistre (ou remplace) le verdict mis de côté, rattaché à la génération courante du candidat.
func (openedStore *Store) SaveCandidateHold(ctx context.Context, hold CandidateHold, now time.Time) error {
	encoded, err := encodeProposal(Proposal{Kind: hold.Kind, Warnings: hold.Warnings, Details: hold.Details})
	if err != nil {
		return err
	}
	result, err := openedStore.database.ExecContext(ctx, `INSERT INTO candidate_holds
(candidate_id, generation, reason, kind, warnings, details, updated_at)
SELECT id, generation, ?, ?, ?, ?, ? FROM torrent_candidates WHERE id = ?
ON CONFLICT(candidate_id) DO UPDATE SET generation = excluded.generation, reason = excluded.reason, kind = excluded.kind,
warnings = excluded.warnings, details = excluded.details, updated_at = excluded.updated_at`,
		hold.Reason, string(hold.Kind), encoded.warnings, encoded.details, now.UnixNano(), hold.CandidateID)
	if err != nil {
		return fmt.Errorf("save candidate hold: %w", err)
	}
	if inserted, err := result.RowsAffected(); err == nil && inserted == 0 {
		return ErrCandidateNotFound
	}
	return nil
}

// ListCandidateHolds renvoie les verdicts mis de côté des candidats verified, de leur génération courante seulement :
// un candidat revérifié ou périmé n'a plus de verdict valable.
func (openedStore *Store) ListCandidateHolds(ctx context.Context) ([]CandidateHold, error) {
	rows, err := openedStore.database.QueryContext(ctx, `SELECT holds.candidate_id, holds.generation, holds.reason,
holds.kind, holds.warnings, holds.details, holds.updated_at FROM candidate_holds AS holds
JOIN torrent_candidates ON torrent_candidates.id = holds.candidate_id
WHERE torrent_candidates.state = ? AND torrent_candidates.generation = holds.generation ORDER BY holds.candidate_id`,
		string(CandidateVerified))
	if err != nil {
		return nil, fmt.Errorf("list candidate holds: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var holds []CandidateHold
	for rows.Next() {
		var hold CandidateHold
		var kind, warnings, details string
		var updatedAt int64
		if err := rows.Scan(&hold.CandidateID, &hold.Generation, &hold.Reason, &kind, &warnings, &details, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan candidate hold: %w", err)
		}
		if err := json.Unmarshal([]byte(warnings), &hold.Warnings); err != nil {
			return nil, fmt.Errorf("decode candidate hold warnings: %w", err)
		}
		hold.Kind, hold.Details, hold.UpdatedAt = ProposalKind(kind), json.RawMessage(details), time.Unix(0, updatedAt).UTC()
		holds = append(holds, hold)
	}
	return holds, rows.Err()
}

// DeleteCandidateHold retire le verdict mis de côté du candidat ; sans effet s'il n'y en a pas.
func (openedStore *Store) DeleteCandidateHold(ctx context.Context, candidateID int64) error {
	if _, err := openedStore.database.ExecContext(ctx, `DELETE FROM candidate_holds WHERE candidate_id = ?`, candidateID); err != nil {
		return fmt.Errorf("delete candidate hold: %w", err)
	}
	return nil
}
