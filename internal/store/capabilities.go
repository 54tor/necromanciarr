package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// hashSeparator sépare les noms de hachage dans la colonne mount_capabilities.hashes.
const hashSeparator = ","

// MountCapabilities est le résultat mis en cache de la sonde d'un montage : empreintes que son stockage fournit, date
// de modification et liens symboliques. Hashes porte les noms d'algorithme (« MD5 »), sans dépendre du paquet hashsource.
type MountCapabilities struct {
	MountPath string
	Hashes    []string
	ModTime   bool
	Symlinks  bool
	ProbedAt  time.Time
}

// SaveMountCapabilities enregistre le résultat de la sonde, en remplaçant celui déjà connu pour le même montage.
func (openedStore *Store) SaveMountCapabilities(ctx context.Context, capabilities MountCapabilities) error {
	_, err := openedStore.database.ExecContext(ctx, `
INSERT INTO mount_capabilities (mount_path, hashes, modtime, symlinks, probed_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(mount_path) DO UPDATE SET hashes = excluded.hashes, modtime = excluded.modtime,
	symlinks = excluded.symlinks, probed_at = excluded.probed_at`,
		capabilities.MountPath, strings.Join(capabilities.Hashes, hashSeparator), capabilities.ModTime,
		capabilities.Symlinks, capabilities.ProbedAt.UnixNano())
	if err != nil {
		return fmt.Errorf("save mount capabilities: %w", err)
	}
	return nil
}

// LoadMountCapabilities renvoie le résultat de la dernière sonde de mountPath ; found est faux si le montage n'a
// jamais été sondé.
func (openedStore *Store) LoadMountCapabilities(ctx context.Context, mountPath string) (capabilities MountCapabilities, found bool, err error) {
	var hashes string
	var probedAtNanoseconds int64
	err = openedStore.database.QueryRowContext(ctx,
		`SELECT hashes, modtime, symlinks, probed_at FROM mount_capabilities WHERE mount_path = ?`, mountPath).
		Scan(&hashes, &capabilities.ModTime, &capabilities.Symlinks, &probedAtNanoseconds)
	if errors.Is(err, sql.ErrNoRows) {
		return MountCapabilities{}, false, nil
	}
	if err != nil {
		return MountCapabilities{}, false, fmt.Errorf("load mount capabilities: %w", err)
	}
	capabilities.MountPath = mountPath
	if hashes != "" {
		capabilities.Hashes = strings.Split(hashes, hashSeparator)
	}
	capabilities.ProbedAt = time.Unix(0, probedAtNanoseconds).UTC()
	return capabilities, true, nil
}
