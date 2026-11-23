// Commande necromanciarr : point d'entrée de l'exécutable (api-key, config check ; les autres arriveront plus tard).
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

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

func loadConfig(args []string) (config.Config, error) {
	return config.Load(args, os.LookupEnv, defaultConfigDir())
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
	loadedConfig, err := loadConfig(args)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "error:", err)
		return failureExitCode
	}
	apiKey, err := config.EnsureAPIKey(loadedConfig.ConfigDir)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "error:", err)
		return failureExitCode
	}
	_, _ = fmt.Fprintln(stdout, apiKey)
	return 0
}

func runConfigCheck(args []string, stdout, stderr io.Writer) int {
	loadedConfig, err := loadConfig(args)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "error:", err)
		return failureExitCode
	}
	redactedJSON, err := loadedConfig.RedactedJSON()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "error:", err)
		return failureExitCode
	}
	_, _ = fmt.Fprintln(stdout, string(redactedJSON))
	return 0
}
