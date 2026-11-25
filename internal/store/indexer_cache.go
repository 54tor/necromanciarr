package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const (
	// IndexerCacheMaxRows borne les lignes du cache par indexeur : au-delà, les plus proches de l'expiration partent.
	IndexerCacheMaxRows = 2000
	// IndexerCacheMaxItemsBytes borne items_json d'une ligne : une réponse hostile ne remplit pas la base.
	IndexerCacheMaxItemsBytes = 64 << 10
	// IndexerSearchedTTL est la durée d'une marque « groupe cherché » : passé ce délai, une nouvelle release peut être trouvée.
	IndexerSearchedTTL = 7 * 24 * time.Hour
	// indexerCacheMaxTTL est la plus longue durée de vie d'une ligne (réponse vide) : sert à purger sans horloge.
	indexerCacheMaxTTL = 24 * time.Hour
)

// ErrIndexerCacheTooLarge : items_json dépasse IndexerCacheMaxItemsBytes ; l'appelant tronque avant d'écrire.
var ErrIndexerCacheTooLarge = errors.New("store: indexer cache answer is too large")

// GetIndexerCache rend les éléments en cache de la requête (found faux si absente ou expirée à now) et si la réponse
// était vide. Les éléments ne portent ni lien de téléchargement ni clé.
func (openedStore *Store) GetIndexerCache(ctx context.Context, indexer, queryKey string, now time.Time) (items []byte, found, empty bool, err error) {
	var emptyFlag int
	err = openedStore.database.QueryRowContext(ctx,
		`SELECT items_json, empty FROM indexer_cache WHERE indexer = ? AND query_key = ? AND expires_at > ?`,
		indexer, queryKey, now.UnixNano()).Scan(&items, &emptyFlag)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, false, nil
	}
	if err != nil {
		return nil, false, false, fmt.Errorf("get indexer cache: %w", err)
	}
	return items, true, emptyFlag == 1, nil
}

// PutIndexerCache remplace la ligne (indexer, queryKey). Dans la même transaction : purge des lignes expirées depuis
// plus de 24 h avant expiresAt (toute ligne plus ancienne que expiresAt − 24 h l'est forcément) et plafond de
// IndexerCacheMaxRows lignes par indexeur (les plus fraîches restent). La clé de la requête ne porte jamais la clé d'API.
func (openedStore *Store) PutIndexerCache(ctx context.Context, indexer, queryKey, mode string, itemsJSON []byte, empty bool, expiresAt time.Time) error {
	if len(itemsJSON) > IndexerCacheMaxItemsBytes {
		return ErrIndexerCacheTooLarge
	}
	emptyFlag := 0
	if empty {
		emptyFlag = 1
	}
	err := openedStore.inTransaction(ctx, func(transaction *sql.Tx) error {
		if _, err := transaction.ExecContext(ctx, `DELETE FROM indexer_cache WHERE expires_at <= ?`, expiresAt.Add(-indexerCacheMaxTTL).UnixNano()); err != nil {
			return err
		}
		if _, err := transaction.ExecContext(ctx,
			`INSERT OR REPLACE INTO indexer_cache (indexer, query_key, mode, items_json, empty, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
			indexer, queryKey, mode, string(itemsJSON), emptyFlag, expiresAt.UnixNano()); err != nil {
			return err
		}
		_, err := transaction.ExecContext(ctx, `DELETE FROM indexer_cache WHERE indexer = ? AND query_key NOT IN
(SELECT query_key FROM indexer_cache WHERE indexer = ? ORDER BY expires_at DESC, query_key LIMIT ?)`, indexer, indexer, IndexerCacheMaxRows)
		return err
	})
	if err != nil {
		return fmt.Errorf("put indexer cache: %w", err)
	}
	return nil
}

// MarkGroupSearched note que la recherche du groupe chez l'indexeur est allée au bout (même vide) ; rejouable. Les
// marques de plus de IndexerSearchedTTL avant at sont purgées dans la même transaction.
func (openedStore *Store) MarkGroupSearched(ctx context.Context, groupKey, indexer string, at time.Time) error {
	err := openedStore.inTransaction(ctx, func(transaction *sql.Tx) error {
		if _, err := transaction.ExecContext(ctx, `DELETE FROM indexer_searched WHERE searched_at <= ?`, at.Add(-IndexerSearchedTTL).UnixNano()); err != nil {
			return err
		}
		_, err := transaction.ExecContext(ctx, `INSERT OR REPLACE INTO indexer_searched (media_group_key, indexer, searched_at) VALUES (?, ?, ?)`,
			groupKey, indexer, at.UnixNano())
		return err
	})
	if err != nil {
		return fmt.Errorf("mark group searched: %w", err)
	}
	return nil
}

// GroupSearched dit si une marque existe pour le groupe chez l'indexeur, sans regard à son âge.
func (openedStore *Store) GroupSearched(ctx context.Context, groupKey, indexer string) (bool, error) {
	return openedStore.GroupSearchedSince(ctx, groupKey, indexer, time.Time{})
}

// GroupSearchedSince dit si le groupe a été cherché chez l'indexeur après notBefore (now − IndexerSearchedTTL pour
// la reprise d'une recherche).
func (openedStore *Store) GroupSearchedSince(ctx context.Context, groupKey, indexer string, notBefore time.Time) (bool, error) {
	var count int
	err := openedStore.database.QueryRowContext(ctx,
		`SELECT count(*) FROM indexer_searched WHERE media_group_key = ? AND indexer = ? AND searched_at > ?`,
		groupKey, indexer, notBefore.UnixNano()).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("group searched: %w", err)
	}
	return count > 0, nil
}

// KnownCandidate dit si (infohash, tracker) est déjà un candidat, quel que soit son état sauf stale : une ligne stale
// est reprise par SaveFetchedCandidate, son .torrent est donc à télécharger.
func (openedStore *Store) KnownCandidate(ctx context.Context, infohash, tracker string) (bool, error) {
	var count int
	err := openedStore.database.QueryRowContext(ctx,
		`SELECT count(*) FROM torrent_candidates WHERE infohash = ? AND tracker = ? AND state != ?`,
		infohash, tracker, string(CandidateStale)).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("known candidate: %w", err)
	}
	return count > 0, nil
}
