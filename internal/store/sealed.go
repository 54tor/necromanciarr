package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anacrolix/torrent/bencode"
)

// SourceProvider est la source d'un candidat venu du fournisseur de bibliothèque, et celle de tout candidat enregistré
// avant ce champ ; un indexeur s'écrit « torznab:<nom> » (config.SourceProvider et config.SourceTorznabPrefix).
const SourceProvider = "provider"

// MaxSealedBytes plafonne un .torrent scellé : le .torrent complet (MaxTorrentBytes) plus l'enveloppe du scellement
// (version, nonce, tag), avec de la marge. La base garantit aussi ce plafond (CHECK de torrent_sealed).
const MaxSealedBytes = MaxTorrentBytes + 128

// Erreurs typées des .torrent conservés.
var (
	// ErrSealedTooLarge refuse un bloc scellé au-delà de MaxSealedBytes.
	ErrSealedTooLarge = errors.New("store: sealed torrent exceeds its size limit")
	// ErrUnsanitizedTorrent refuse un .torrent dont le premier niveau porte une annonce (announce ou announce-list),
	// ou illisible : la colonne torrent ne garde jamais de passkey en clair. L'erreur ne cite jamais le contenu.
	ErrUnsanitizedTorrent = errors.New("store: torrent carries an announce or is not a bencoded dictionary")
)

// sealedClearingDecisions closent une proposition : quand plus aucune proposition du candidat n'est ouverte
// (nonTerminalDecisions), son bloc scellé ne peut plus servir au seed et la base l'efface dans la même instruction.
var sealedClearingDecisions = []ProposalDecision{ProposalApplied, ProposalRejected, ProposalSuperseded}

// clearSealedTrigger efface le bloc scellé dans l'instruction même qui écrit la décision : aucun chemin d'écriture
// (SetProposalDecision, ApplyProposal, péremption, réassociation) ne peut l'oublier, et un retour arrière le rend.
// Recréé à chaque ouverture, il suit toujours les listes Go.
func clearSealedTrigger() string {
	return fmt.Sprintf(`DROP TRIGGER IF EXISTS proposals_clear_sealed;
CREATE TRIGGER proposals_clear_sealed AFTER UPDATE OF decision ON proposals
WHEN NEW.candidate_id IS NOT NULL AND NEW.decision IN (%s)
BEGIN
	UPDATE torrent_candidates SET torrent_sealed = NULL WHERE id = NEW.candidate_id AND torrent_sealed IS NOT NULL
		AND NOT EXISTS (SELECT 1 FROM proposals WHERE candidate_id = NEW.candidate_id AND decision IN (%s));
END;`, quotedDecisions(sealedClearingDecisions), quotedDecisions(nonTerminalDecisions))
}

// quotedDecisions rend une liste SQL de littéraux ; les décisions sont des constantes du paquet sans apostrophe.
func quotedDecisions(decisions []ProposalDecision) string {
	quoted := make([]string, 0, len(decisions))
	for _, decision := range decisions {
		quoted = append(quoted, "'"+string(decision)+"'")
	}
	return strings.Join(quoted, ", ")
}

// SealedTorrent est un bloc scellé en base avec les champs de sa liaison, tels que stockés sur la ligne du candidat :
// le déscellement les reprend toujours de là, jamais de la configuration courante. Blob n'est jamais sérialisé.
type SealedTorrent struct {
	CandidateID int64
	Infohash    string
	Tracker     string
	Source      string
	Blob        []byte `json:"-"`
}

// SaveCandidateSealed conserve le .torrent scellé du candidat (bloc opaque, le store ne le déchiffre pas) ; un bloc
// vide efface le précédent. ErrSealedTooLarge au-delà de MaxSealedBytes, ErrCandidateNotFound sans candidat.
func (openedStore *Store) SaveCandidateSealed(ctx context.Context, candidateID int64, sealed []byte, now time.Time) error {
	if len(sealed) > MaxSealedBytes {
		return ErrSealedTooLarge
	}
	var stored any
	if len(sealed) > 0 {
		stored = sealed
	}
	result, err := openedStore.database.ExecContext(ctx, `UPDATE torrent_candidates SET torrent_sealed = ?, updated_at = ? WHERE id = ?`,
		stored, now.UnixNano(), candidateID)
	return requireOneRow(result, err, ErrCandidateNotFound, "save candidate sealed torrent")
}

// CandidateSealed renvoie le bloc scellé du candidat ; found est faux s'il n'en a pas, ErrCandidateNotFound sans
// candidat.
func (openedStore *Store) CandidateSealed(ctx context.Context, candidateID int64) (sealed []byte, found bool, err error) {
	err = openedStore.database.QueryRowContext(ctx, `SELECT torrent_sealed FROM torrent_candidates WHERE id = ?`, candidateID).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, ErrCandidateNotFound
	}
	if err != nil {
		return nil, false, fmt.Errorf("load candidate sealed torrent: %w", err)
	}
	return sealed, sealed != nil, nil
}

// ClearSealedForTrackers efface le bloc scellé de chaque candidat de ces trackers (une entrée trackers de la configuration
// fournit désormais l'annonce) ; le tracker est comparé octet pour octet à la valeur stockée : l'appelant passe les
// valeurs stockées qu'il a reconnues. Renvoie le nombre de blocs effacés.
func (openedStore *Store) ClearSealedForTrackers(ctx context.Context, trackers []string) (int, error) {
	if len(trackers) == 0 {
		return 0, nil
	}
	encoded, err := json.Marshal(trackers)
	if err != nil {
		return 0, fmt.Errorf("clear sealed torrents: %w", err)
	}
	result, err := openedStore.database.ExecContext(ctx, `UPDATE torrent_candidates SET torrent_sealed = NULL
WHERE torrent_sealed IS NOT NULL AND tracker IN (SELECT value FROM json_each(?))`, string(encoded))
	if err != nil {
		return 0, fmt.Errorf("clear sealed torrents: %w", err)
	}
	cleared, err := result.RowsAffected()
	return int(cleared), err
}

// SealedTorrents renvoie au plus limit blocs scellés, par identifiant de candidat (lecture du diagnostic secret-key).
func (openedStore *Store) SealedTorrents(ctx context.Context, limit int) ([]SealedTorrent, error) {
	rows, err := openedStore.database.QueryContext(ctx, `SELECT id, infohash, tracker, source, torrent_sealed FROM torrent_candidates
WHERE torrent_sealed IS NOT NULL ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list sealed torrents: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var sealed []SealedTorrent
	for rows.Next() {
		var torrent SealedTorrent
		if err := rows.Scan(&torrent.CandidateID, &torrent.Infohash, &torrent.Tracker, &torrent.Source, &torrent.Blob); err != nil {
			return nil, fmt.Errorf("scan sealed torrent: %w", err)
		}
		sealed = append(sealed, torrent)
	}
	return sealed, rows.Err()
}

// announceKeys portent l'annonce, donc la passkey, au premier niveau d'un .torrent.
var announceKeys = []string{"announce", "announce-list"}

// refuseUnsanitized lit les clés du premier niveau (valeurs gardées brutes, info jamais interprété) : un fichier
// nommé « announce » dans info ne compte pas, une clé announce ou announce-list du premier niveau est refusée.
func refuseUnsanitized(raw []byte) error {
	var topLevel map[string]bencode.Bytes
	if err := bencode.Unmarshal(raw, &topLevel); err != nil || topLevel == nil {
		return ErrUnsanitizedTorrent
	}
	for _, key := range announceKeys {
		if _, present := topLevel[key]; present {
			return ErrUnsanitizedTorrent
		}
	}
	return nil
}
