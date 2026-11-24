package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// sealedClearingDecisions closent une proposition : quand plus aucune proposition du candidat qui peut mener au seed
// n'est ouverte (nonTerminalDecisions, hors conflict), son bloc scellé ne sert plus et la base l'efface dans la même
// instruction.
var sealedClearingDecisions = []ProposalDecision{ProposalApplied, ProposalRejected, ProposalSuperseded}

// sealedClearingStates effacent le bloc scellé du candidat : Verify, RetryCandidate et SaveCandidate retéléchargent
// le .torrent, l'ancien bloc ne resservirait jamais.
var sealedClearingStates = []CandidateState{CandidateFailed, CandidateStale}

// schemaTrigger est un déclencheur et le texte exact de sa création, tel que SQLite le garde dans sqlite_master.
type schemaTrigger struct {
	name, create string
}

// sealedTriggers effacent le bloc scellé dans l'instruction même qui écrit la décision ou l'état : aucun chemin
// d'écriture ne peut l'oublier, et un retour arrière le rend.
func sealedTriggers() []schemaTrigger {
	return []schemaTrigger{
		{"proposals_clear_sealed", fmt.Sprintf(`CREATE TRIGGER proposals_clear_sealed AFTER UPDATE OF decision ON proposals
WHEN NEW.candidate_id IS NOT NULL AND NEW.decision IN (%s)
BEGIN
	UPDATE torrent_candidates SET torrent_sealed = NULL WHERE id = NEW.candidate_id AND torrent_sealed IS NOT NULL
		AND NOT EXISTS (SELECT 1 FROM proposals WHERE candidate_id = NEW.candidate_id AND kind != '%s' AND decision IN (%s));
END`, quotedValues(sealedClearingDecisions), ProposalConflict, quotedValues(nonTerminalDecisions))},
		{"torrent_candidates_clear_sealed", fmt.Sprintf(`CREATE TRIGGER torrent_candidates_clear_sealed AFTER UPDATE OF state ON torrent_candidates
WHEN NEW.state IN (%s) AND NEW.torrent_sealed IS NOT NULL
BEGIN
	UPDATE torrent_candidates SET torrent_sealed = NULL WHERE id = NEW.id;
END`, quotedValues(sealedClearingStates))},
	}
}

// ensureTriggers recrée chaque déclencheur dont le texte en base diffère : une base à jour n'est pas réécrite.
func ensureTriggers(ctx context.Context, connection *sql.Conn, triggers []schemaTrigger) error {
	for _, trigger := range triggers {
		var stored string
		err := connection.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = ?`, trigger.name).Scan(&stored)
		if err == nil && stored == trigger.create {
			continue
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		// #nosec G202 -- nom et texte constants de sealedTriggers
		if _, err := connection.ExecContext(ctx, "DROP TRIGGER IF EXISTS "+trigger.name+";\n"+trigger.create); err != nil {
			return fmt.Errorf("create trigger %s: %w", trigger.name, err)
		}
	}
	return nil
}

// quotedValues rend une liste SQL de littéraux ; les valeurs sont des constantes du paquet sans apostrophe.
func quotedValues[Value ~string](values []Value) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, "'"+string(value)+"'")
	}
	return strings.Join(quoted, ", ")
}
