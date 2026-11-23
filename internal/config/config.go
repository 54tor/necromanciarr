// Package config charge la configuration YAML et les secrets issus de l'environnement.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	defaultListenAddr         = "127.0.0.1:8787"
	defaultDBPath             = "necromanciarr.db"
	defaultReadBytesPerSecond = 20 * 1024 * 1024

	envAuthToken            = "NECROMANCIARR_AUTH_TOKEN"
	envLibraryProviderToken = "LIBRARY_PROVIDER_TOKEN"
	envArrAPIKeys           = "ARR_API_KEYS"
	envQbitPassword         = "QBIT_PASSWORD"
)

// PathRule traduit un préfixe de chemin entre deux vues du même stockage (hôte, conteneur, client torrent).
type PathRule struct {
	From string `yaml:"from"`
	To   string `yaml:"to"`
}

// ArrInstance décrit une instance Radarr ou Sonarr ; sa clé d'API vient de ARR_API_KEYS, au même rang.
type ArrInstance struct {
	Kind string `yaml:"kind"`
	URL  string `yaml:"url"`
}

// Config est la configuration complète. Les secrets ne viennent que de l'environnement
// et sont exclus de YAML et de JSON.
type Config struct {
	ListenAddr         string        `yaml:"listen_addr"`
	DBPath             string        `yaml:"db_path"`
	ReadBytesPerSecond int64         `yaml:"read_bytes_per_second"`
	PathRules          []PathRule    `yaml:"path_rules"`
	LibraryProviderURL string        `yaml:"library_provider_url"`
	LibraryRoots       []string      `yaml:"library_roots"`
	ArrInstances       []ArrInstance `yaml:"arr_instances"`
	QbitURL            string        `yaml:"qbit_url"`
	QbitUser           string        `yaml:"qbit_user"`

	AuthToken            string   `yaml:"-" json:"-"`
	LibraryProviderToken string   `yaml:"-" json:"-"`
	ArrAPIKeys           []string `yaml:"-" json:"-"`
	QbitPassword         string   `yaml:"-" json:"-"`
}

// Load lit le YAML à path, applique les défauts, lit les secrets de l'environnement puis valide ;
// l'erreur renvoyée ne contient aucun secret.
func Load(path string) (Config, error) {
	var loadedConfig Config
	if err := readYAML(path, &loadedConfig); err != nil {
		return Config{}, err
	}
	applyDefaults(&loadedConfig)
	readSecretsFromEnvironment(&loadedConfig)
	if err := validate(loadedConfig); err != nil {
		return Config{}, err
	}
	return loadedConfig, nil
}

func readYAML(path string, target *Config) error {
	fileContent, err := os.ReadFile(path) // #nosec G304 -- chemin de configuration fourni par l'opérateur
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(fileContent, target); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	return nil
}

func applyDefaults(target *Config) {
	if target.ListenAddr == "" {
		target.ListenAddr = defaultListenAddr
	}
	if target.DBPath == "" {
		target.DBPath = defaultDBPath
	}
	if target.ReadBytesPerSecond == 0 {
		target.ReadBytesPerSecond = defaultReadBytesPerSecond
	}
}

// Les secrets ne passent jamais par le fichier YAML, qui peut être versionné ou partagé.
func readSecretsFromEnvironment(target *Config) {
	target.AuthToken = os.Getenv(envAuthToken)
	target.LibraryProviderToken = os.Getenv(envLibraryProviderToken)
	target.QbitPassword = os.Getenv(envQbitPassword)
	if rawKeys := os.Getenv(envArrAPIKeys); rawKeys != "" {
		target.ArrAPIKeys = strings.Split(rawKeys, ",")
	}
}

// Un jeton est exigé hors boucle locale : sans lui, l'interface serait ouverte au réseau sans authentification.
func validate(loadedConfig Config) error {
	if loadedConfig.ReadBytesPerSecond < 0 {
		return errors.New("read_bytes_per_second must be positive")
	}
	if len(loadedConfig.ArrAPIKeys) != len(loadedConfig.ArrInstances) {
		return fmt.Errorf("%s has %d keys for %d arr_instances", envArrAPIKeys, len(loadedConfig.ArrAPIKeys), len(loadedConfig.ArrInstances))
	}
	if loadedConfig.AuthToken == "" && !isLoopbackAddress(loadedConfig.ListenAddr) {
		return fmt.Errorf("listen_addr %q is not loopback: set %s", loadedConfig.ListenAddr, envAuthToken)
	}
	return nil
}

func isLoopbackAddress(listenAddr string) bool {
	host, _, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	parsedIP := net.ParseIP(host)
	return parsedIP != nil && parsedIP.IsLoopback()
}

const redactedSecret = "[redacted]"

// redacted renvoie une copie dont les secrets renseignés sont masqués.
func (loadedConfig Config) redacted() Config {
	redactedCopy := loadedConfig
	if redactedCopy.AuthToken != "" {
		redactedCopy.AuthToken = redactedSecret
	}
	if redactedCopy.LibraryProviderToken != "" {
		redactedCopy.LibraryProviderToken = redactedSecret
	}
	if redactedCopy.QbitPassword != "" {
		redactedCopy.QbitPassword = redactedSecret
	}
	redactedCopy.ArrAPIKeys = nil
	for range loadedConfig.ArrAPIKeys {
		redactedCopy.ArrAPIKeys = append(redactedCopy.ArrAPIKeys, redactedSecret)
	}
	return redactedCopy
}

type plainConfig Config

// String masque les secrets pour que %v et %+v ne les affichent jamais.
func (loadedConfig Config) String() string {
	return fmt.Sprintf("%+v", plainConfig(loadedConfig.redacted()))
}

// GoString masque les secrets pour %#v.
func (loadedConfig Config) GoString() string {
	return loadedConfig.String()
}

// LogValue masque les secrets dans les journaux slog.
func (loadedConfig Config) LogValue() slog.Value {
	return slog.StringValue(loadedConfig.String())
}
