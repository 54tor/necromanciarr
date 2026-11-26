package config

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"testing"
)

// absPath construit un chemin absolu pour le système qui exécute les tests : sous Windows, « /x » n'est pas absolu
// (filepath.IsAbs exige une lettre de lecteur), et la validation des montages le refuse à raison.
func absPath(segments ...string) string {
	root := string(filepath.Separator)
	if runtime.GOOS == "windows" {
		root = `C:\`
	}
	return filepath.Join(append([]string{root}, segments...)...)
}

// jsonText échappe text pour l'insérer entre guillemets dans un JSON (barres obliques inverses de Windows comprises).
func jsonText(text string) string {
	encoded, _ := json.Marshal(text)
	return string(encoded[1 : len(encoded)-1])
}

// skipOnWindows écarte un test dont la fixture ne porte que des chemins POSIX (config.example.js de cross-seed).
func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fixture with POSIX-only absolute paths: a Windows mount path needs a drive letter")
	}
}

// onThisSystem donne à un chemin POSIX de test son lecteur sous Windows ; ailleurs il reste tel quel.
func onThisSystem(posixPath string) string {
	if runtime.GOOS == "windows" {
		return "C:" + filepath.FromSlash(posixPath)
	}
	return posixPath
}
