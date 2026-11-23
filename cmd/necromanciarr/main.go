// Commande necromanciarr : point d'entrée de l'exécutable (api-key, config check ; les autres arriveront plus tard).
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	// Fuseaux horaires embarqués : l'image finale n'a pas de paquet tzdata.
	_ "time/tzdata"

	"necromanciarr/internal/config"
)

const (
	// usageExitCode est le code de sortie d'une ligne de commande invalide.
	usageExitCode   = 2
	failureExitCode = 1
	usageText       = "usage: necromanciarr <api-key|config check|serve|scan|match|verify|link|seed>"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run dispatche la sous-commande demandée et renvoie le code de sortie du processus.
func run(args []string, stdout, stderr io.Writer) int {
	switch {
	case len(args) >= 1 && args[0] == "api-key":
		return runAPIKey(args[1:], stdout, stderr)
	case len(args) >= 2 && args[0] == "config" && args[1] == "check":
		return runConfigCheck(args[2:], stdout, stderr)
	default:
		_, _ = fmt.Fprintln(stderr, usageText)
		return usageExitCode
	}
}

// reportError affiche l'erreur ; une ligne de commande invalide affiche l'usage et donne le code 2.
func reportError(stderr io.Writer, err error) int {
	if errors.Is(err, config.ErrUsage) {
		_, _ = fmt.Fprintln(stderr, usageText)
		return usageExitCode
	}
	_, _ = fmt.Fprintln(stderr, "error:", err)
	return failureExitCode
}

// defaultConfigDir vaut <dossier utilisateur>/necromanciarr ; vide si le système n'en fournit pas.
func defaultConfigDir() string {
	userConfigDir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(userConfigDir, "necromanciarr")
}

func runAPIKey(args []string, stdout, stderr io.Writer) int {
	configDir, err := config.ResolveConfigDir(args, os.LookupEnv, defaultConfigDir())
	if err != nil {
		return reportError(stderr, err)
	}
	apiKey, err := config.EnsureAPIKey(configDir)
	if err != nil {
		return reportError(stderr, err)
	}
	_, _ = fmt.Fprintln(stdout, apiKey)
	return 0
}

func runConfigCheck(args []string, stdout, stderr io.Writer) int {
	loadedConfig, err := config.Load(args, os.LookupEnv, defaultConfigDir())
	if err != nil {
		return reportError(stderr, err)
	}
	redactedJSON, err := loadedConfig.RedactedJSON()
	if err != nil {
		return reportError(stderr, err)
	}
	_, _ = fmt.Fprintln(stdout, string(redactedJSON))
	return 0
}
