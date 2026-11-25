package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
	// ErrUnsanitizedTorrent refuse un .torrent dont le premier niveau porte une clé qui peut contenir une passkey
	// (announceKeys), ou illisible : la colonne torrent ne garde jamais de passkey en clair. L'erreur ne cite jamais le contenu.
	ErrUnsanitizedTorrent = errors.New("store: torrent carries an announce or is not a bencoded dictionary")
)

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

// ErrSealedMissing refuse un .torrent d'indexeur sans sa copie scellée : sa passkey ne serait gardée nulle part.
var ErrSealedMissing = errors.New("store: indexer torrent without its sealed copy")

// SaveCandidateFetched enregistre un .torrent obtenu d'un indexeur en une seule instruction : la source
// (« torznab:<nom> », à laquelle le scellement est lié), la copie assainie et la copie scellée. Rien n'est écrit si
// l'une est refusée (ErrUnsanitizedTorrent, ErrTorrentTooLarge, ErrSealedMissing, ErrSealedTooLarge).
func (openedStore *Store) SaveCandidateFetched(ctx context.Context, candidateID int64, source string, sanitized, sealed []byte, now time.Time) error {
	if len(sanitized) > MaxTorrentBytes {
		return ErrTorrentTooLarge
	}
	if err := refuseUnsanitized(sanitized); err != nil {
		return err
	}
	if len(sealed) == 0 {
		return ErrSealedMissing
	}
	if len(sealed) > MaxSealedBytes {
		return ErrSealedTooLarge
	}
	result, err := openedStore.database.ExecContext(ctx,
		`UPDATE torrent_candidates SET source = ?, torrent = ?, torrent_sealed = ?, updated_at = ? WHERE id = ?`,
		source, sanitized, sealed, now.UnixNano(), candidateID)
	return requireOneRow(result, err, ErrCandidateNotFound, "save candidate fetched torrent")
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

// SealedRetention borne la conservation d'un bloc scellé : au-delà de 30 jours sans mise à jour du candidat, il est
// effacé (PurgeSealedOlderThan, au début de chaque Verify), même si une proposition attend encore une décision.
const SealedRetention = 30 * 24 * time.Hour

// PurgeSealedOlderThan efface chaque bloc scellé dont le candidat n'a pas été mis à jour depuis olderThan, à partir de
// now ; renvoie le nombre de blocs effacés.
func (openedStore *Store) PurgeSealedOlderThan(ctx context.Context, now time.Time, olderThan time.Duration) (int, error) {
	result, err := openedStore.database.ExecContext(ctx, `UPDATE torrent_candidates SET torrent_sealed = NULL
WHERE torrent_sealed IS NOT NULL AND updated_at < ?`, now.Add(-olderThan).UnixNano())
	if err != nil {
		return 0, fmt.Errorf("purge sealed torrents: %w", err)
	}
	purged, err := result.RowsAffected()
	return int(purged), err
}

// sealedEntry décrit un bloc scellé sans le lire.
type sealedEntry struct {
	torrent SealedTorrent
	size    int64
}

// EachSealedTorrent remet au visiteur, un à un et par identifiant de candidat, au plus limit blocs scellés totalisant
// au plus maxBytes ; seul le bloc en cours est en mémoire. Renvoie le nombre de blocs non remis (limite ou budget
// atteint) ; une erreur du visiteur arrête le parcours.
func (openedStore *Store) EachSealedTorrent(ctx context.Context, limit int, maxBytes int64, visit func(SealedTorrent) error) (int, error) {
	entries, total, err := openedStore.sealedEntries(ctx, limit)
	if err != nil {
		return 0, err
	}
	var visited int
	var visitedBytes int64
	for _, entry := range entries {
		if visitedBytes+entry.size > maxBytes {
			break
		}
		torrent := entry.torrent
		if err := openedStore.database.QueryRowContext(ctx, `SELECT torrent_sealed FROM torrent_candidates WHERE id = ?`,
			torrent.CandidateID).Scan(&torrent.Blob); err != nil {
			return 0, fmt.Errorf("load sealed torrent: %w", err)
		}
		visited++
		visitedBytes += entry.size
		if torrent.Blob == nil {
			continue
		}
		if err := visit(torrent); err != nil {
			return 0, err
		}
	}
	return total - visited, nil
}

// sealedEntries lit la liaison et la taille des limit premiers blocs, sans leur contenu, et le nombre total de blocs.
func (openedStore *Store) sealedEntries(ctx context.Context, limit int) ([]sealedEntry, int, error) {
	var total int
	if err := openedStore.database.QueryRowContext(ctx, `SELECT count(*) FROM torrent_candidates WHERE torrent_sealed IS NOT NULL`).
		Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count sealed torrents: %w", err)
	}
	rows, err := openedStore.database.QueryContext(ctx, `SELECT id, infohash, tracker, source, length(torrent_sealed)
FROM torrent_candidates WHERE torrent_sealed IS NOT NULL ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("list sealed torrents: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var entries []sealedEntry
	for rows.Next() {
		var entry sealedEntry
		if err := rows.Scan(&entry.torrent.CandidateID, &entry.torrent.Infohash, &entry.torrent.Tracker, &entry.torrent.Source,
			&entry.size); err != nil {
			return nil, 0, fmt.Errorf("scan sealed torrent: %w", err)
		}
		entries = append(entries, entry)
	}
	return entries, total, rows.Err()
}

// announceKeys peuvent porter une passkey au premier niveau d'un .torrent (annonces, sources web, commentaire avec
// lien) : ce garde-fou de la colonne torrent double l'assainissement (sources.Sanitize), qui ne garde que info,
// creation date, encoding et piece layers.
var announceKeys = []string{"announce", "announce-list", "url-list", "httpseeds", "comment"}

// refuseUnsanitized lit les clés du premier niveau (valeurs gardées brutes, info jamais interprété) : un fichier
// nommé « announce » dans info ne compte pas, une clé de announceKeys au premier niveau est refusée.
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
