package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrRunLocked signale qu'une autre exécution tient le verrou d'étape et que son bail n'a pas expiré.
var ErrRunLocked = errors.New("store: another stage is running")

// AcquireRunLock prend le verrou unique des étapes jusqu'à expiresAt ; un verrou dont le bail est dépassé à now est
// repris (processus mort sans le rendre), un verrou tenu par holder est prolongé.
func (openedStore *Store) AcquireRunLock(ctx context.Context, holder, stage string, now, expiresAt time.Time) error {
	result, err := openedStore.database.ExecContext(ctx, `INSERT INTO run_lock (id, holder, stage, expires_at) VALUES (1, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET holder = excluded.holder, stage = excluded.stage, expires_at = excluded.expires_at
WHERE run_lock.expires_at < ? OR run_lock.holder = excluded.holder`, holder, stage, expiresAt.UnixNano(), now.UnixNano())
	if err != nil {
		return fmt.Errorf("acquire run lock: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("acquire run lock: %w", err)
	}
	if affected == 0 {
		return openedStore.lockedError(ctx)
	}
	return nil
}

func (openedStore *Store) lockedError(ctx context.Context) error {
	var stage string
	if err := openedStore.database.QueryRowContext(ctx, `SELECT stage FROM run_lock WHERE id = 1`).Scan(&stage); err != nil {
		return ErrRunLocked
	}
	return fmt.Errorf("%w (stage %s)", ErrRunLocked, stage)
}

// RefreshRunLock prolonge le bail du verrou tenu par holder ; ErrRunLocked s'il a été repris entre-temps.
func (openedStore *Store) RefreshRunLock(ctx context.Context, holder string, expiresAt time.Time) error {
	result, err := openedStore.database.ExecContext(ctx, `UPDATE run_lock SET expires_at = ? WHERE id = 1 AND holder = ?`,
		expiresAt.UnixNano(), holder)
	return requireOneRow(result, err, ErrRunLocked, "refresh run lock")
}

// ReleaseRunLock rend le verrou s'il est tenu par holder ; sans effet sinon.
func (openedStore *Store) ReleaseRunLock(ctx context.Context, holder string) error {
	if _, err := openedStore.database.ExecContext(ctx, `DELETE FROM run_lock WHERE holder = ?`, holder); err != nil {
		return fmt.Errorf("release run lock: %w", err)
	}
	return nil
}
