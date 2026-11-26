package main

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"

	"necromanciarr/internal/config"
	"necromanciarr/internal/store"
)

const languageSettingKey = "language"

// emptyStoredReader tient lieu de base au premier lancement : aucun réglage, aucune surcharge.
type emptyStoredReader struct{}

func (emptyStoredReader) Settings(context.Context) (map[string][]byte, error) {
	return map[string][]byte{}, nil
}

func (emptyStoredReader) ConfigOverrides(context.Context) ([]store.ConfigOverride, error) {
	return nil, nil
}

// loadConfig charge la configuration, base comprise (lue sans être créée).
func loadConfig(args []string) (config.Config, error) {
	return config.LoadWith(args, lookupEnv, defaultConfigDir(), openStoredReader)
}

// languageOrigin donne l'origine de la langue parmi celles des réglages (default, override, env ou flag).
func languageOrigin(loadedConfig config.Config) string {
	for _, origin := range loadedConfig.Origins {
		if origin.Key == languageSettingKey && origin.ItemID == "" {
			return origin.Origin
		}
	}
	return ""
}

// openStoredReader ouvre la base en lecture seule ; absente, elle n'est pas créée (le Service la crée à son ouverture).
func openStoredReader(dbPath string) (config.StoredReader, io.Closer, error) {
	_, err := os.Stat(dbPath)
	if errors.Is(err, fs.ErrNotExist) {
		return emptyStoredReader{}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	readOnlyStore, err := store.OpenReadOnly(dbPath)
	if err != nil {
		return nil, nil, err
	}
	return readOnlyStore, readOnlyStore, nil
}
