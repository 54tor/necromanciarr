package store

import (
	"context"
	"fmt"
)

// ConfigOverride est la modification d'une entrée de liste de la configuration : un remplacement (Value) ou une
// suppression (Deleted, Value nil) ; UpstreamFingerprint garde l'empreinte de l'entrée amont au moment de l'écriture.
type ConfigOverride struct {
	List, ItemID        string
	Deleted             bool
	Value               []byte
	UpstreamFingerprint string
}

// Settings renvoie tous les réglages (clé → valeur brute).
func (openedStore *Store) Settings(ctx context.Context) (map[string][]byte, error) {
	rows, err := openedStore.database.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, fmt.Errorf("list settings: %w", err)
	}
	defer func() { _ = rows.Close() }()

	settings := map[string][]byte{}
	for rows.Next() {
		var key string
		var value []byte
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("scan setting: %w", err)
		}
		settings[key] = value
	}
	return settings, rows.Err()
}

// SetSetting crée le réglage ou remplace sa valeur.
func (openedStore *Store) SetSetting(ctx context.Context, key string, value []byte) error {
	if value == nil {
		value = []byte{}
	}
	_, err := openedStore.database.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value)
	if err != nil {
		return fmt.Errorf("set setting: %w", err)
	}
	return nil
}

// DeleteSetting supprime le réglage ; sans effet s'il n'existe pas.
func (openedStore *Store) DeleteSetting(ctx context.Context, key string) error {
	if _, err := openedStore.database.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key); err != nil {
		return fmt.Errorf("delete setting: %w", err)
	}
	return nil
}

// ConfigOverrides renvoie toutes les modifications de liste, triées par liste puis identifiant.
func (openedStore *Store) ConfigOverrides(ctx context.Context) ([]ConfigOverride, error) {
	rows, err := openedStore.database.QueryContext(ctx,
		`SELECT list, item_id, deleted, value, upstream_fingerprint FROM list_overrides ORDER BY list, item_id`)
	if err != nil {
		return nil, fmt.Errorf("list config overrides: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var overrides []ConfigOverride
	for rows.Next() {
		var override ConfigOverride
		if err := rows.Scan(&override.List, &override.ItemID, &override.Deleted, &override.Value,
			&override.UpstreamFingerprint); err != nil {
			return nil, fmt.Errorf("scan config override: %w", err)
		}
		overrides = append(overrides, override)
	}
	return overrides, rows.Err()
}

// PutConfigOverride crée la modification ou remplace celle de la même (liste, identifiant) ; la valeur d'une
// suppression n'est pas conservée.
func (openedStore *Store) PutConfigOverride(ctx context.Context, override ConfigOverride) error {
	value := override.Value
	if override.Deleted {
		value = nil
	}
	_, err := openedStore.database.ExecContext(ctx, `
INSERT INTO list_overrides (list, item_id, deleted, value, upstream_fingerprint) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(list, item_id) DO UPDATE SET deleted = excluded.deleted, value = excluded.value,
	upstream_fingerprint = excluded.upstream_fingerprint`,
		override.List, override.ItemID, override.Deleted, value, override.UpstreamFingerprint)
	if err != nil {
		return fmt.Errorf("put config override: %w", err)
	}
	return nil
}

// DeleteConfigOverride supprime la modification ; sans effet si elle n'existe pas.
func (openedStore *Store) DeleteConfigOverride(ctx context.Context, list, itemID string) error {
	_, err := openedStore.database.ExecContext(ctx,
		`DELETE FROM list_overrides WHERE list = ? AND item_id = ?`, list, itemID)
	if err != nil {
		return fmt.Errorf("delete config override: %w", err)
	}
	return nil
}
