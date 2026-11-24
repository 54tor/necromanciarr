package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	"necromanciarr/internal/i18n"
)

// ProposalStep est l'avancement de l'application d'une proposition acceptée : liée, puis ajoutée au client.
type ProposalStep string

// Étapes d'application : linked (liens et annexes posés), seeded (ajouté et repris après une garde à 100 %),
// seeded_paused_incomplete (ajouté et laissé en pause car incomplet), forced (démarré par Force, choix de l'opérateur).
const (
	StepLinked                 ProposalStep = "linked"
	StepSeeded                 ProposalStep = "seeded"
	StepSeededPausedIncomplete ProposalStep = "seeded_paused_incomplete"
	StepForced                 ProposalStep = "forced"
)

// ErrUnknownProposalStep refuse une étape d'application inconnue.
var ErrUnknownProposalStep = errors.New("store: unknown proposal step")

// ProposalProgress est l'étape atteinte par une proposition et le dossier de liens du torrent (vue de ce processus).
type ProposalProgress struct {
	ProposalID int64
	Step       ProposalStep
	LinkRoot   string
	UpdatedAt  time.Time
}

func checkProposalStep(step ProposalStep) error {
	switch step {
	case StepLinked, StepSeeded, StepSeededPausedIncomplete, StepForced:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrUnknownProposalStep, step)
	}
}

// SaveProposalProgress enregistre (ou remplace) l'étape atteinte par la proposition.
func (openedStore *Store) SaveProposalProgress(ctx context.Context, progress ProposalProgress, now time.Time) error {
	if err := checkProposalStep(progress.Step); err != nil {
		return err
	}
	return openedStore.inTransaction(ctx, func(transaction *sql.Tx) error {
		return writeProposalStep(ctx, transaction, progress.ProposalID, progress.Step, progress.LinkRoot, now)
	})
}

func writeProposalStep(ctx context.Context, transaction *sql.Tx, proposalID int64, step ProposalStep, linkRoot string, now time.Time) error {
	_, err := transaction.ExecContext(ctx, `INSERT INTO proposal_steps (proposal_id, step, link_root, updated_at) VALUES (?, ?, ?, ?)
ON CONFLICT(proposal_id) DO UPDATE SET step = excluded.step, link_root = excluded.link_root, updated_at = excluded.updated_at`,
		proposalID, string(step), linkRoot, now.UnixNano())
	if err != nil {
		return fmt.Errorf("save proposal step: %w", err)
	}
	return nil
}

// ApplyProposal passe la proposition à applied (mêmes contrôles que SetProposalDecision, génération comprise) et
// enregistre son étape dans la même transaction : l'une ne va jamais sans l'autre.
func (openedStore *Store) ApplyProposal(ctx context.Context, proposalID int64, step ProposalStep, linkRoot string, now time.Time) error {
	if err := checkProposalStep(step); err != nil {
		return err
	}
	return openedStore.inTransaction(ctx, func(transaction *sql.Tx) error {
		if err := decideProposal(ctx, transaction, proposalID, ProposalApplied, now); err != nil {
			return err
		}
		return writeProposalStep(ctx, transaction, proposalID, step, linkRoot, now)
	})
}

// ClearProposalStep efface l'étape de la proposition (liens abîmés constatés par Seed : Link doit les refaire) mais garde
// son dossier de liens : Link le reprend tel quel, sans le renommer.
func (openedStore *Store) ClearProposalStep(ctx context.Context, proposalID int64, now time.Time) error {
	_, err := openedStore.database.ExecContext(ctx, `UPDATE proposal_steps SET step = '', updated_at = ? WHERE proposal_id = ?`,
		now.UnixNano(), proposalID)
	if err != nil {
		return fmt.Errorf("clear proposal step: %w", err)
	}
	return nil
}

// ListProposalProgress renvoie l'étape de chaque proposition qui en a une, par identifiant de proposition ; une étape
// effacée par ClearProposalStep est vide et garde son dossier de liens.
func (openedStore *Store) ListProposalProgress(ctx context.Context) (map[int64]ProposalProgress, error) {
	rows, err := openedStore.database.QueryContext(ctx, `SELECT proposal_id, step, link_root, updated_at FROM proposal_steps`)
	if err != nil {
		return nil, fmt.Errorf("list proposal steps: %w", err)
	}
	defer func() { _ = rows.Close() }()
	progress := map[int64]ProposalProgress{}
	for rows.Next() {
		var entry ProposalProgress
		var step string
		var updatedAt int64
		if err := rows.Scan(&entry.ProposalID, &step, &entry.LinkRoot, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan proposal step: %w", err)
		}
		entry.Step, entry.UpdatedAt = ProposalStep(step), time.Unix(0, updatedAt).UTC()
		progress[entry.ProposalID] = entry
	}
	return progress, rows.Err()
}

// AddProposalWarning ajoute un avertissement à la proposition s'il n'y figure pas déjà (échec de notification, garde
// incomplète) ; ErrProposalNotFound si elle n'existe pas.
func (openedStore *Store) AddProposalWarning(ctx context.Context, proposalID int64, warning i18n.Message) error {
	return openedStore.inTransaction(ctx, func(transaction *sql.Tx) error {
		var encoded string
		err := transaction.QueryRowContext(ctx, `SELECT warnings FROM proposals WHERE id = ?`, proposalID).Scan(&encoded)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrProposalNotFound
		}
		if err != nil {
			return fmt.Errorf("load proposal warnings: %w", err)
		}
		warnings, err := i18n.DecodeMessages([]byte(encoded))
		if err != nil {
			return fmt.Errorf("decode proposal warnings: %w", err)
		}
		if slices.ContainsFunc(warnings, func(existing i18n.Message) bool { return reflect.DeepEqual(existing, warning) }) {
			return nil
		}
		updated, err := json.Marshal(append(warnings, warning))
		if err != nil {
			return err
		}
		_, err = transaction.ExecContext(ctx, `UPDATE proposals SET warnings = ? WHERE id = ?`, string(updated), proposalID)
		return err
	})
}
