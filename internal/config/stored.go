package config

import (
	"context"
	"fmt"
	"io"

	"necromanciarr/internal/store"
)

const (
	originDefault   = "default"
	originCrossSeed = "crossseed"
	originOverride  = "override"
	originAdded     = "added"
	originEnv       = "env"
	originFlag      = "flag"
)

// StoredReader lit la configuration enregistrée en base : réglages (clé → JSON) et surcharges de liste.
type StoredReader interface {
	Settings(ctx context.Context) (map[string][]byte, error)
	ConfigOverrides(ctx context.Context) ([]store.ConfigOverride, error)
}

// ValueOrigin dit d'où vient une valeur effective : réglage (ItemID vide) ou élément de liste.
type ValueOrigin struct {
	Key    string `json:"key"`
	ItemID string `json:"itemId,omitempty"`
	Origin string `json:"origin"`
	Drift  bool   `json:"drift,omitempty"`
}

// storedRecords est le contenu brut de la base ; vide au premier lancement.
type storedRecords struct {
	settings  map[string][]byte
	overrides []store.ConfigOverride
}

// readStored lit la base ; sans ouvreur (nil) ou sans lecteur, la couche stockée est vide.
func readStored(openStored func(dbPath string) (StoredReader, io.Closer, error), dbPath string) (storedRecords, error) {
	if openStored == nil {
		return storedRecords{}, nil
	}
	reader, closer, err := openStored(dbPath)
	if err != nil {
		return storedRecords{}, fmt.Errorf("open configuration database: %w", err)
	}
	if closer != nil {
		defer func() { _ = closer.Close() }()
	}
	if reader == nil {
		return storedRecords{}, nil
	}
	return readRecords(reader)
}

func readRecords(reader StoredReader) (storedRecords, error) {
	// Load ne reçoit pas de contexte : la lecture locale de SQLite est bornée par la base elle-même.
	ctx := context.Background()
	settings, err := reader.Settings(ctx)
	if err != nil {
		return storedRecords{}, fmt.Errorf("read stored settings: %w", err)
	}
	overrides, err := reader.ConfigOverrides(ctx)
	if err != nil {
		return storedRecords{}, fmt.Errorf("read stored list overrides: %w", err)
	}
	return storedRecords{settings: settings, overrides: overrides}, nil
}
