package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const dotEnvFileMode = 0o600

var (
	ErrBootstrapNotWritable        = errors.New("bootstrap: config dir not writable")
	ErrBootstrapWouldOrphanSecrets = errors.New("bootstrap: database exists but " + envSecretKey + " is missing, a new key would make stored secrets unreadable")
)

var bootstrapLock sync.Mutex

type BootstrapResult struct {
	Created   []string
	AuthToken string
}

// EnsureBootstrap ajoute au .env du dossier de configuration les secrets absents, sans jamais toucher une valeur présente.
func EnsureBootstrap(args []string, lookupEnv func(string) (string, bool), defaultConfigDir string) (BootstrapResult, error) {
	options, err := parseCommandLine(args)
	if err != nil {
		return BootstrapResult{}, err
	}
	configDir, err := resolveConfigDir(options, lookupEnv, defaultConfigDir)
	if err != nil {
		return BootstrapResult{}, err
	}

	bootstrapLock.Lock()
	defer bootstrapLock.Unlock()

	lookup, _, err := loadDotEnvLookup(configDir, lookupEnv)
	if err != nil {
		return BootstrapResult{}, err
	}
	_, hasKey := lookup(envSecretKey)
	_, hasToken := lookup(envAuthToken)
	if hasKey && hasToken {
		return BootstrapResult{}, nil
	}
	if !hasKey && databaseExists(configDir, options) {
		return BootstrapResult{}, ErrBootstrapWouldOrphanSecrets
	}
	return writeMissingSecrets(configDir, !hasKey, !hasToken)
}

func databaseExists(configDir string, options commandLine) bool {
	settings := Config{ConfigDir: configDir, DBPath: defaultDBPath}
	if options.set["db-path"] {
		settings.DBPath = options.dbPath
	}
	_, err := os.Stat(ResolveDBPath(settings))
	return err == nil
}

func writeMissingSecrets(configDir string, needKey, needToken bool) (BootstrapResult, error) {
	var result BootstrapResult
	var lines strings.Builder
	if needKey {
		secretKey, err := GenerateSecretKey()
		if err != nil {
			return BootstrapResult{}, err
		}
		lines.WriteString(envSecretKey + "=" + secretKey + "\n")
		result.Created = append(result.Created, envSecretKey)
	}
	if needToken {
		authToken, err := GenerateAuthToken()
		if err != nil {
			return BootstrapResult{}, err
		}
		lines.WriteString(envAuthToken + "=" + authToken + "\n")
		result.Created = append(result.Created, envAuthToken)
		result.AuthToken = authToken
	}
	if err := appendToDotEnv(filepath.Join(configDir, dotEnvFileName), lines.String()); err != nil {
		return BootstrapResult{}, fmt.Errorf("%w: %s (%s, %s): %w", ErrBootstrapNotWritable, configDir, envSecretKey, envAuthToken, err)
	}
	return result, nil
}

func appendToDotEnv(path, lines string) error {
	existing, err := os.ReadFile(path) // #nosec G304 -- chemin dérivé du dossier de configuration de l'opérateur
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if len(existing) > 0 && !bytes.HasSuffix(existing, []byte("\n")) {
		lines = "\n" + lines
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, dotEnvFileMode) // #nosec G304 -- idem
	if err != nil {
		return err
	}
	if _, err := file.WriteString(lines); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
