// Commande necromanciarr : point d'entrée de l'exécutable. Chaque sous-commande d'action est un appel à app.Service
// (parité avec le web), en texte lisible ou en JSON avec --json.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"

	// Fuseaux horaires embarqués : l'image finale n'a pas de paquet tzdata.
	_ "time/tzdata"

	"necromanciarr/internal/app"
	"necromanciarr/internal/config"
)

const (
	// usageExitCode est le code de sortie d'une ligne de commande invalide.
	usageExitCode   = 2
	failureExitCode = 1
	serveStubText   = "serve: the web interface is not available yet, it comes with a later version"
)

// lookupEnv lit l'environnement pour config.Load.
var lookupEnv = os.LookupEnv

// openService ouvre le Service de production ; les tests le remplacent par un faux.
var openService = func(ctx context.Context, settings config.Config) (app.Service, io.Closer, error) {
	return app.NewFromConfig(ctx, settings)
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run dispatche la sous-commande demandée et renvoie le code de sortie du processus ; Ctrl-C annule l'action en
// cours (une étape s'arrête proprement et reste rejouable).
func run(args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	switch {
	case len(args) >= 1 && args[0] == "api-key":
		return runAPIKey(args[1:], stdout, stderr)
	case len(args) >= 2 && args[0] == "config" && args[1] == "check":
		return runConfigCheck(args[2:], stdout, stderr)
	case len(args) >= 1 && args[0] == "serve":
		_, _ = fmt.Fprintln(stderr, serveStubText)
		return usageExitCode
	}
	spec, rest, found := findCommand(args)
	if !found {
		_, _ = fmt.Fprint(stderr, usageText)
		return usageExitCode
	}
	return runCommand(ctx, spec, rest, stdout, stderr)
}

// findCommand retient la commande dont le chemin est le plus long préfixe des arguments.
func findCommand(args []string) (command, []string, bool) {
	var best command
	found := false
	for _, spec := range commands {
		matches := len(args) >= len(spec.path) && slices.Equal(args[:len(spec.path)], spec.path)
		if matches && len(spec.path) > len(best.path) {
			best, found = spec, true
		}
	}
	if !found {
		return command{}, nil, false
	}
	return best, args[len(best.path):], true
}

// reportError affiche l'erreur ; une ligne de commande invalide affiche l'usage et donne le code 2.
func reportError(stderr io.Writer, err error) int {
	if errors.Is(err, config.ErrUsage) {
		_, _ = fmt.Fprint(stderr, usageText)
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
