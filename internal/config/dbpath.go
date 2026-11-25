package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// errRemoteDBPath : SQLite verrouille et synchronise son fichier ; sur un volume distant, ni l'un ni l'autre n'est sûr.
var errRemoteDBPath = errors.New("dbPath: the database stays on a local disk, never on an sftp:// location")

// ResolveDBPath donne le chemin effectif de la base : un chemin relatif se lit depuis le dossier de configuration.
// Doctor et le service doivent passer par cette fonction pour viser le même fichier.
func ResolveDBPath(settings Config) string {
	if filepath.IsAbs(settings.DBPath) {
		return settings.DBPath
	}
	return filepath.Join(settings.ConfigDir, settings.DBPath)
}

// validateDBPath refuse une base sur un volume sftp, qu'elle vienne de config.json ou de --db-path.
func validateDBPath(dbPath string) error {
	if strings.HasPrefix(dbPath, remoteRuleScheme) {
		return fmt.Errorf("%w: %w", ErrUsage, errRemoteDBPath)
	}
	return nil
}
