package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"necromanciarr/internal/app"
	"necromanciarr/internal/config"
	"necromanciarr/internal/i18n"
)

// Codes de sortie : 0 réussi (avertissements compris, une ligne « warning: » chacun), 1 échec, 2 usage, 3 proposition
// périmée, 4 action refusée par une règle (litige, proposition non acceptée, hors racines...).
const (
	successExitCode = 0
	staleExitCode   = 3
	refusedExitCode = 4
)

// Codes d'erreur stables de la sortie JSON {"error": {"code", "message"}} ; la table vient d'app.Classify (même table
// que le web), seule la confirmation manquante est propre à la CLI.
const (
	errorCodeUsage                = app.ErrorCodeUsage
	errorCodeConfirmationRequired = "confirmation_required"
	errorCodeStale                = app.ErrorCodeStale
	errorCodeRefused              = app.ErrorCodeRefused
	errorCodeNotFound             = app.ErrorCodeNotFound
	errorCodeFailure              = app.ErrorCodeFailure
)

// errConfirmationRequired : une action risquée demande --yes.
var errConfirmationRequired = errors.New("confirmation required: rerun with --yes")

// exitCodeByErrorCode donne le code de sortie de chaque code d'erreur ; un code absent vaut un échec.
var exitCodeByErrorCode = map[string]int{
	errorCodeUsage:                usageExitCode,
	errorCodeConfirmationRequired: usageExitCode,
	errorCodeStale:                staleExitCode,
	errorCodeRefused:              refusedExitCode,
}

type errorClass struct {
	code     string
	exitCode int
}

func classify(err error) errorClass {
	code := errorCodeConfirmationRequired
	if !errors.Is(err, errConfirmationRequired) {
		code, _ = app.Classify(err)
	}
	exitCode, known := exitCodeByErrorCode[code]
	if !known {
		exitCode = failureExitCode
	}
	return errorClass{code, exitCode}
}

// errorMessage donne le texte montré à l'opérateur : le message de l'erreur typée dans la langue, suivi du détail de la
// chaîne (anglais, masqué à l'affichage) quand l'erreur en porte un. Le détail reste ici, contrairement au web : la CLI
// est locale à l'opérateur et n'a pas de journal serveur où le retrouver.
func errorMessage(translator i18n.Translator, err error) string {
	if errors.Is(err, errConfirmationRequired) {
		return translator.Text(i18n.KeyCLIConfirmationRequired, nil)
	}
	if unknownLanguage, isUnknown := errors.AsType[*config.UnknownLanguageError](err); isUnknown {
		return translator.Text(i18n.KeyErrorUsageUnknownLanguage, textParams("value", unknownLanguage.Value))
	}
	if _, isInvalidKey := errors.AsType[config.InvalidSecretKeyError](err); isInvalidKey {
		return translator.Text(i18n.KeyErrorUsageInvalidSecretKey, nil)
	}
	if errors.Is(err, config.ErrSecretKeyRequired) {
		return translator.Text(i18n.KeyErrorConfigSecretKeyRequired, nil)
	}
	_, typed := app.Classify(err)
	if typed == nil {
		return translator.Text(i18n.KeyCLIErrorInternal, nil) + " (" + err.Error() + ")"
	}
	message := translator.Render(app.MessageOf(err))
	detail := err.Error()
	if detail == typed.Error() {
		return message
	}
	return message + " (" + strings.TrimPrefix(detail, typed.Error()+": ") + ")"
}

// languageFromArguments devine la langue avant que la configuration soit chargée (ligne invalide, aide) : --language,
// puis l'environnement, sinon le français ; une valeur invalide est ignorée, la configuration la refusera plus tard.
func languageFromArguments(args []string) i18n.Language {
	rawValue, _ := lookupEnv("NECROMANCIARR_LANGUAGE")
	for position, argument := range args {
		switch {
		case argument == "--language" && position+1 < len(args):
			rawValue = args[position+1]
		case strings.HasPrefix(argument, "--language="):
			rawValue = strings.TrimPrefix(argument, "--language=")
		}
	}
	language, err := i18n.ParseLanguage(rawValue)
	if err != nil {
		return i18n.French
	}
	return language
}

// invocation est une sous-commande analysée : arguments positionnels nommés, options propres, options de
// configuration (passées à loadConfig), configuration chargée et Service ouvert.
type invocation struct {
	arguments  map[string]string
	flags      map[string][]string
	configArgs []string
	settings   config.Config
	service    app.Service
	translator i18n.Translator
	stdout     io.Writer
	stderr     io.Writer
}

func (call *invocation) has(flag string) bool { return len(call.flags[flag]) > 0 }

func (call *invocation) value(flag string) string {
	values := call.flags[flag]
	if len(values) == 0 {
		return ""
	}
	return values[len(values)-1]
}

func (call *invocation) proposalID() (int64, error) {
	return parseID(call.arguments[app.ArgumentProposalID])
}

func parseID(raw string) (int64, error) {
	identifier, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || identifier <= 0 {
		return 0, fmt.Errorf("%w: %q is not a positive id", config.ErrUsage, raw)
	}
	return identifier, nil
}

// parseInvocation lit d'abord les arguments positionnels (avant toute option), puis les options : celles de la
// commande sont gardées, toutes les autres vont à loadConfig, qui refuse une option inconnue.
func parseInvocation(spec command, args []string) (*invocation, error) {
	call := &invocation{arguments: map[string]string{}, flags: map[string][]string{}}
	position := 0
	for position < len(args) && !strings.HasPrefix(args[position], "-") {
		if position >= len(spec.arguments) {
			return nil, fmt.Errorf("%w: unexpected argument %q", config.ErrUsage, args[position])
		}
		call.arguments[spec.arguments[position]] = args[position]
		position++
	}
	if position < len(spec.arguments) && !spec.optionalArguments {
		return nil, fmt.Errorf("%w: missing %s", config.ErrUsage, strings.Join(spec.arguments[position:], ", "))
	}
	for ; position < len(args); position++ {
		name, inlineValue, hasInlineValue := "", "", false
		if strings.HasPrefix(args[position], "--") {
			name, inlineValue, hasInlineValue = strings.Cut(strings.TrimPrefix(args[position], "--"), "=")
		}
		switch {
		case slices.Contains(spec.boolFlags, name) && !hasInlineValue:
			call.flags[name] = append(call.flags[name], "true")
		case slices.Contains(spec.valueFlags, name) && hasInlineValue:
			call.flags[name] = append(call.flags[name], inlineValue)
		case slices.Contains(spec.valueFlags, name):
			position++
			if position >= len(args) {
				return nil, fmt.Errorf("%w: --%s needs a value", config.ErrUsage, name)
			}
			call.flags[name] = append(call.flags[name], args[position])
		default:
			call.configArgs = append(call.configArgs, args[position])
		}
	}
	return call, nil
}

// output est le résultat d'une sous-commande : jsonValue pour --json, text pour la sortie lisible, warnings une ligne
// « warning: » chacun en sortie texte (déjà présents dans jsonValue), exitCode pour un résultat qui échoue sans
// erreur (diagnostic).
type output struct {
	jsonValue any
	text      func(io.Writer)
	warnings  []string
	failures  []string
	exitCode  int
}

// errorBody est le corps stable d'une erreur JSON.
type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// printer écrit toutes les sorties, masquées par la redaction de la configuration (aucun secret, même dans une
// erreur venue d'un service).
type printer struct {
	stdout, stderr io.Writer
	jsonOutput     bool
	secrets        []string
	translator     i18n.Translator
}

// newPrinter prépare un printer dont la langue est devinée sur la ligne de commande, en attendant la configuration.
func newPrinter(stdout, stderr io.Writer, args []string) printer {
	return printer{stdout: stdout, stderr: stderr, translator: i18n.New(languageFromArguments(args), nil)}
}

// useSettings retient les secrets et la langue de la configuration chargée ; le traducteur masque les valeurs de paramètres.
func (out *printer) useSettings(settings config.Config) {
	secrets := settings.KnownSecrets()
	out.secrets = secrets
	out.translator = i18n.New(settings.Language, func(text string) string { return config.RedactText(text, secrets...) })
}

// redact retire les caractères de contrôle (ESC, OSC, C1...) d'un texte destiné au terminal, puis masque les secrets :
// un nom de torrent ou une réponse de service ne doit jamais piloter le terminal, et un octet de contrôle glissé dans
// un secret ne doit pas le soustraire au masquage. Seuls saut de ligne et tabulation restent.
func (out printer) redact(text string) string {
	return config.RedactText(stripControlCharacters(text), out.secrets...)
}

func stripControlCharacters(text string) string {
	return strings.Map(func(character rune) rune {
		isKeptControl := character == '\n' || character == '\t'
		if unicode.IsControl(character) && !isKeptControl {
			return -1
		}
		return character
	}, text)
}

func (out printer) result(result output) int {
	if out.jsonOutput {
		if err := out.writeJSON(result.jsonValue); err != nil {
			return out.failure(err)
		}
		return result.exitCode
	}
	var text strings.Builder
	result.text(&text)
	_, _ = io.WriteString(out.stdout, out.redact(text.String()))
	out.warn(result.warnings)
	for _, failure := range result.failures {
		_, _ = fmt.Fprintln(out.stderr, "error:", out.redact(failure))
	}
	return result.exitCode
}

func (out printer) warn(warnings []string) {
	for _, warning := range warnings {
		_, _ = fmt.Fprintln(out.stderr, "warning:", out.redact(warning))
	}
}

func (out printer) writeJSON(value any) error {
	encoded, err := config.RedactJSON(value, out.secrets...)
	if err != nil {
		return err
	}
	_, _ = out.stdout.Write(encoded)
	return nil
}

// failure affiche l'erreur (JSON sur stdout avec --json, sinon sur stderr) et renvoie son code de sortie ; une erreur
// d'usage rappelle l'usage.
func (out printer) failure(err error) int {
	class := classify(err)
	message := out.redact(errorMessage(out.translator, err))
	if out.jsonOutput {
		_ = out.writeJSON(map[string]errorBody{"error": {Code: class.code, Message: message}})
		return class.exitCode
	}
	_, _ = fmt.Fprintln(out.stderr, "error:", message)
	if class.code == errorCodeUsage {
		_, _ = fmt.Fprint(out.stderr, usageText(out.translator))
	}
	return class.exitCode
}

// runCommand analyse la ligne, charge la configuration, ouvre le Service voulu, exécute et affiche.
func runCommand(ctx context.Context, spec command, args []string, stdout, stderr io.Writer) int {
	call, err := parseInvocation(spec, args)
	out := newPrinter(stdout, stderr, args)
	if err != nil {
		out.jsonOutput = slices.Contains(args, "--json")
		return out.failure(err)
	}
	out.jsonOutput = call.has(flagJSON)
	call.stdout, call.stderr = stdout, stderr
	call.settings, err = loadConfigFor(spec, call.configArgs)
	if err != nil {
		isDiagnosable := !errors.Is(err, config.ErrUsage) || errors.Is(err, config.ErrInvalidStoredConfig)
		if spec.onConfigError != nil && isDiagnosable {
			return out.result(spec.onConfigError(out.translator, err))
		}
		return out.failure(err)
	}
	out.useSettings(call.settings)
	call.translator = out.translator
	closeService, err := openFor(ctx, spec, call)
	if err != nil {
		return out.failure(err)
	}
	defer closeService()
	result, err := spec.execute(ctx, call)
	if err != nil {
		return out.failure(err)
	}
	return out.result(result)
}

func openFor(ctx context.Context, spec command, call *invocation) (func(), error) {
	if spec.diagnostic {
		call.service = openDoctorService(call.settings)
		return func() {}, nil
	}
	service, closer, err := openService(ctx, call.settings, storedConfigLoader(call.configArgs))
	if err != nil {
		return nil, err
	}
	call.service = service
	return func() { _ = closer.Close() }, nil
}
