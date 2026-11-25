package store

import (
	"context"
	"fmt"
	"time"
)

// IndexerUsageWindow est la fenêtre glissante des quotas par jour ; les lignes plus anciennes sont purgées à chaque écriture.
const IndexerUsageWindow = 24 * time.Hour

const (
	purgeIndexerUsageSQL = `DELETE FROM indexer_usage WHERE at <= ?`
	insertIndexerUsage   = `INSERT INTO indexer_usage (indexer, at, kind) VALUES (?, ?, ?)`
	// takeIndexerUsageSQL compte et insère dans la même instruction : deux processus ne peuvent pas franchir le plafond.
	takeIndexerUsageSQL = `INSERT INTO indexer_usage (indexer, at, kind)
SELECT ?, ?, ? WHERE (SELECT count(*) FROM indexer_usage WHERE indexer = ? AND kind = ? AND at > ?) < ?`
)

// CountIndexerUsage compte les requêtes (kind « query » ou « download ») faites à l'indexeur après since.
func (openedStore *Store) CountIndexerUsage(ctx context.Context, indexer, kind string, since time.Time) (int, error) {
	var count int
	err := openedStore.database.QueryRowContext(ctx,
		`SELECT count(*) FROM indexer_usage WHERE indexer = ? AND kind = ? AND at > ?`,
		indexer, kind, since.UnixNano()).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count indexer usage: %w", err)
	}
	return count, nil
}

// RecordIndexerUsage ajoute une ligne d'usage et purge celles de plus de IndexerUsageWindow avant at.
func (openedStore *Store) RecordIndexerUsage(ctx context.Context, indexer, kind string, at time.Time) error {
	_, err := openedStore.writeIndexerUsage(ctx, at, insertIndexerUsage, indexer, at.UnixNano(), kind)
	return err
}

// TakeIndexerUsage ajoute la ligne seulement si l'indexeur a moins de limit lignes de ce kind après since, et dit si
// elle l'a fait ; le test et l'ajout sont une seule instruction dans une transaction qui prend le verrou d'écriture,
// donc sûrs entre processus. ponytail: une ligne datée dans le futur (horloge reculée) compte jusqu'à ce que
// l'horloge la rattrape, puis vieillit normalement ; pas de correction tant que cela ne s'est pas vu. La purge des lignes de plus d'un jour avant at a lieu dans la même transaction.
func (openedStore *Store) TakeIndexerUsage(ctx context.Context, indexer, kind string, at, since time.Time, limit int) (bool, error) {
	return openedStore.writeIndexerUsage(ctx, at, takeIndexerUsageSQL,
		indexer, at.UnixNano(), kind, indexer, kind, since.UnixNano(), limit)
}

func (openedStore *Store) writeIndexerUsage(ctx context.Context, at time.Time, statement string, arguments ...any) (bool, error) {
	transaction, err := openedStore.database.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("record indexer usage: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if _, err := transaction.ExecContext(ctx, purgeIndexerUsageSQL, at.Add(-IndexerUsageWindow).UnixNano()); err != nil {
		return false, fmt.Errorf("purge indexer usage: %w", err)
	}
	result, err := transaction.ExecContext(ctx, statement, arguments...)
	if err != nil {
		return false, fmt.Errorf("record indexer usage: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("record indexer usage: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return false, fmt.Errorf("record indexer usage: %w", err)
	}
	return inserted == 1, nil
}
