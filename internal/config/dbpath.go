package config

import "path/filepath"

// ResolveDBPath donne le chemin effectif de la base : un chemin relatif se lit depuis le dossier de configuration.
// Doctor et le service doivent passer par cette fonction pour viser le même fichier.
func ResolveDBPath(settings Config) string {
	if filepath.IsAbs(settings.DBPath) {
		return settings.DBPath
	}
	return filepath.Join(settings.ConfigDir, settings.DBPath)
}
