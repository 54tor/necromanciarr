package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	apiKeyFileName = "api-key"
	apiKeyBytes    = 32
	configDirMode  = 0o700
)

// EnsureAPIKey renvoie le jeton de l'interface web stocké dans configDir/api-key, en le créant s'il n'existe pas.
// La création passe par un fichier temporaire puis un lien dur : atomique, et un jeton déjà présent n'est jamais écrasé.
func EnsureAPIKey(configDir string) (string, error) {
	keyPath := filepath.Join(configDir, apiKeyFileName)
	if existingKey, err := readAPIKey(keyPath); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return existingKey, err
	}
	if err := os.MkdirAll(configDir, configDirMode); err != nil {
		return "", fmt.Errorf("create config dir: %w", err)
	}
	randomBytes := make([]byte, apiKeyBytes)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", fmt.Errorf("generate api key: %w", err)
	}
	newKey := hex.EncodeToString(randomBytes)
	if err := publishAPIKey(configDir, keyPath, newKey); err != nil {
		return "", err
	}
	return readAPIKey(keyPath)
}

func publishAPIKey(configDir, keyPath, key string) error {
	temporaryFile, err := os.CreateTemp(configDir, apiKeyFileName+".tmp-*") // 0600 par défaut
	if err != nil {
		return fmt.Errorf("write api key: %w", err)
	}
	defer func() { _ = os.Remove(temporaryFile.Name()) }()
	_, writeErr := temporaryFile.WriteString(key + "\n")
	closeErr := temporaryFile.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("write api key: %w", err)
	}
	if err := os.Link(temporaryFile.Name(), keyPath); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("publish api key: %w", err)
	}
	return nil
}

func readAPIKey(keyPath string) (string, error) {
	content, err := os.ReadFile(keyPath) // #nosec G304 -- chemin dérivé du dossier de configuration de l'opérateur
	if err != nil {
		return "", fmt.Errorf("read api key: %w", err)
	}
	key := strings.TrimSpace(string(content))
	if key == "" {
		return "", errors.New("api key file is empty: delete it to generate a new key")
	}
	return key, nil
}
