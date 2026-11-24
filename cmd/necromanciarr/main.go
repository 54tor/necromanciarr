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
	"syscall"

	// Fuseaux horaires embarqués : l'image finale n'a pas de paquet tzdata.
	_ "time/tzdata"

	"necromanciarr/internal/app"
	"necromanciarr/internal/config"
	"necromanciarr/internal/i18n"
)

const (
	// usageExitCode est le code de sortie d'une ligne de commande invalide.
	usageExitCode   = 2
	failureExitCode = 1
)

// lookupEnv lit l'environnement pour config.Load.
var lookupEnv = os.LookupEnv

// openService ouvre le Service de production ; les tests le remplacent par un faux.
var openService = app.NewFromConfig

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run dispatche la sous-commande demandée et renvoie le code de sortie du processus ; Ctrl-C ou SIGTERM annule
// l'action en cours (une étape s'arrête proprement et reste rejouable).
func run(args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Après le premier signal, le comportement par défaut revient : un second Ctrl-C tue le processus.
	go func() {
		<-ctx.Done()
		stop()
	}()
	switch {
	case len(args) >= 1 && args[0] == "api-key":
		return runAPIKey(args[1:], stdout, stderr)
	case len(args) >= 2 && args[0] == "config" && args[1] == "check":
		return runConfigCheck(args[2:], stdout, stderr)
	case len(args) >= 1 && args[0] == "serve":
		return runServe(ctx, args[1:], stdout, stderr)
	case len(args) >= 1 && args[0] == "healthcheck":
		return runHealthcheck(ctx, args[1:], stdout, stderr)
	}
	spec, rest, found := findCommand(args)
	if !found {
		_, _ = fmt.Fprint(stderr, usageText(i18n.New(languageFromArguments(args), nil)))
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
func reportError(stderr io.Writer, args []string, err error) int {
	translator := i18n.New(languageFromArguments(args), nil)
	_, _ = fmt.Fprintln(stderr, "error:", stripControlCharacters(config.RedactText(errorMessage(translator, err))))
	if errors.Is(err, config.ErrUsage) {
		_, _ = fmt.Fprint(stderr, usageText(translator))
		return usageExitCode
	}
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

// runAPIKey tire un jeton neuf pour NECROMANCIARR_AUTH_TOKEN et l'affiche sur stdout seulement ; il n'est jamais écrit
// sur le disque ni consigné dans un log.
func runAPIKey(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		_, _ = fmt.Fprint(stderr, usageText(i18n.New(languageFromArguments(args), nil)))
		return usageExitCode
	}
	token, err := config.GenerateAuthToken()
	if err != nil {
		return reportError(stderr, args, err)
	}
	_, _ = fmt.Fprintln(stdout, token)
	_, _ = fmt.Fprintln(stderr, i18n.New(languageFromArguments(args), nil).Text(i18n.KeyCLIAPIKeyHint, nil))
	return 0
}

func runConfigCheck(args []string, stdout, stderr io.Writer) int {
	loadedConfig, err := config.Load(args, os.LookupEnv, defaultConfigDir())
	if err != nil {
		return reportError(stderr, args, err)
	}
	redactedJSON, err := loadedConfig.RedactedJSON()
	if err != nil {
		return reportError(stderr, args, err)
	}
	_, _ = fmt.Fprintln(stdout, string(redactedJSON))
	translator := i18n.New(loadedConfig.Language, func(text string) string {
		return config.RedactText(text, loadedConfig.KnownSecrets()...)
	})
	_, _ = fmt.Fprintln(stderr, translator.Text(i18n.KeyConfigCheckLanguage, map[string]string{
		"language": string(loadedConfig.Language), "source": loadedConfig.LanguageSource,
	}))
	return 0
}
