package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"necromanciarr/internal/app"
	"necromanciarr/internal/config"
	"necromanciarr/internal/dedupe"
	"necromanciarr/internal/pipeline"
	"necromanciarr/internal/repair"
	"necromanciarr/internal/store"
)

// Codes de sortie : 0 réussi (avertissements compris, une ligne « warning: » chacun), 1 échec, 2 usage, 3 proposition
// périmée, 4 action refusée par une règle (litige, proposition non acceptée, hors racines...).
const (
	successExitCode = 0
	staleExitCode   = 3
	refusedExitCode = 4
)

// Codes d'erreur stables de la sortie JSON {"error": {"code", "message"}}.
const (
	errorCodeUsage                = "usage"
	errorCodeConfirmationRequired = "confirmation_required"
	errorCodeStale                = "stale_proposal"
	errorCodeRefused              = "refused"
	errorCodeNotFound             = "not_found"
	errorCodeNotConfigured        = "not_configured"
	errorCodeStageRunning         = "stage_running"
	errorCodeFailure              = "failure"
)

// errConfirmationRequired : une action risquée demande --yes.
var errConfirmationRequired = errors.New("confirmation required: rerun with --yes")

// refusals sont les refus typés des règles métier : la ligne de commande est valide, l'action ne l'est pas.
var refusals = []error{
	app.ErrConflictNeedsChoose, app.ErrPartialPackNeedsForce, app.ErrNotATorrentProposal, app.ErrCandidateAlreadyAccepted,
	app.ErrOneTorrentPerFile, app.ErrNotFull, app.ErrNotForceable, app.ErrNoPrimary, app.ErrForceNotAllowed,
	app.ErrNotAccepted, app.ErrNotActionable, app.ErrWrongProposalKind, app.ErrDirectoryInLibrary,
	store.ErrInvalidTransition, pipeline.ErrGroupInconsistent, pipeline.ErrClientReadOnly,
	dedupe.ErrTorrentRootOverlapsLibrary, repair.ErrOutsideRoots,
}

type errorClass struct {
	code     string
	exitCode int
}

func classify(err error) errorClass {
	switch {
	case errors.Is(err, config.ErrUsage), errors.Is(err, app.ErrUnknownCheck):
		return errorClass{errorCodeUsage, usageExitCode}
	case errors.Is(err, errConfirmationRequired):
		return errorClass{errorCodeConfirmationRequired, usageExitCode}
	case errors.Is(err, app.ErrStaleProposal):
		return errorClass{errorCodeStale, staleExitCode}
	case slices.ContainsFunc(refusals, func(refusal error) bool { return errors.Is(err, refusal) }):
		return errorClass{errorCodeRefused, refusedExitCode}
	case errors.Is(err, store.ErrProposalNotFound), errors.Is(err, app.ErrGroupNotFound):
		return errorClass{errorCodeNotFound, failureExitCode}
	case errors.Is(err, app.ErrNotConfigured):
		return errorClass{errorCodeNotConfigured, failureExitCode}
	case errors.Is(err, pipeline.ErrStageRunning):
		return errorClass{errorCodeStageRunning, failureExitCode}
	}
	return errorClass{errorCodeFailure, failureExitCode}
}

// errorMessage donne le texte montré à l'opérateur ; une proposition périmée a son message dédié.
func errorMessage(err error) string {
	if errors.Is(err, app.ErrStaleProposal) {
		return app.StaleProposalMessage + " (" + err.Error() + ")"
	}
	return err.Error()
}

// invocation est une sous-commande analysée : arguments positionnels nommés, options propres, options de
// configuration (passées à config.Load), configuration chargée et Service ouvert.
type invocation struct {
	arguments  map[string]string
	flags      map[string][]string
	configArgs []string
	settings   config.Config
	service    app.Service
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
// commande sont gardées, toutes les autres vont à config.Load, qui refuse une option inconnue.
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
		name, inlineValue, hasInlineValue := strings.Cut(strings.TrimLeft(args[position], "-"), "=")
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
	exitCode  int
}

// printer écrit toutes les sorties, masquées par la redaction de la configuration (aucun secret, même dans une
// erreur venue d'un service).
type printer struct {
	stdout, stderr io.Writer
	jsonOutput     bool
	secrets        []string
}

func (out printer) redact(text string) string { return config.RedactText(text, out.secrets...) }

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
	return result.exitCode
}

func (out printer) warn(warnings []string) {
	for _, warning := range warnings {
		_, _ = fmt.Fprintln(out.stderr, "warning:", out.redact(warning))
	}
}

// writeJSON masque chaque chaîne avant l'encodage : un secret qui contient « & », « < », « " », « \ » ou un
// caractère non ASCII n'a plus la même forme une fois échappé en JSON. Les formes échappées des secrets sont aussi
// masquées dans le texte encodé, en défense en profondeur. Les clés des objets sortent triées.
func (out printer) writeJSON(value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var tree any
	if err := decoder.Decode(&tree); err != nil {
		return err
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(out.redactTree(tree)); err != nil {
		return err
	}
	_, _ = io.WriteString(out.stdout, config.RedactText(encoded.String(), out.escapedSecrets()...))
	return nil
}

// redactTree masque chaque chaîne (valeurs et clés) d'un arbre JSON décodé.
func (out printer) redactTree(node any) any {
	switch typed := node.(type) {
	case string:
		return out.redact(typed)
	case []any:
		for index, element := range typed {
			typed[index] = out.redactTree(element)
		}
		return typed
	case map[string]any:
		redacted := make(map[string]any, len(typed))
		for key, element := range typed {
			redacted[out.redact(key)] = out.redactTree(element)
		}
		return redacted
	}
	return node
}

// escapedSecrets donne chaque secret tel quel et sous ses formes échappées par JSON (avec et sans échappement HTML).
func (out printer) escapedSecrets() []string {
	forms := slices.Clone(out.secrets)
	for _, secret := range out.secrets {
		if secret == "" {
			continue
		}
		var plain bytes.Buffer
		encoder := json.NewEncoder(&plain)
		encoder.SetEscapeHTML(false)
		_ = encoder.Encode(secret)
		htmlEscaped, _ := json.Marshal(secret)
		for _, quoted := range []string{strings.TrimSpace(plain.String()), string(htmlEscaped)} {
			forms = append(forms, strings.TrimSuffix(strings.TrimPrefix(quoted, `"`), `"`))
		}
	}
	return forms
}

// failure affiche l'erreur (JSON sur stdout avec --json, sinon sur stderr) et renvoie son code de sortie ; une erreur
// d'usage rappelle l'usage.
func (out printer) failure(err error) int {
	class := classify(err)
	message := out.redact(errorMessage(err))
	if out.jsonOutput {
		type errorBody struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = out.writeJSON(map[string]errorBody{"error": {Code: class.code, Message: message}})
		return class.exitCode
	}
	_, _ = fmt.Fprintln(out.stderr, "error:", message)
	if class.code == errorCodeUsage {
		_, _ = fmt.Fprint(out.stderr, usageText)
	}
	return class.exitCode
}

// runCommand analyse la ligne, charge la configuration, ouvre le Service voulu, exécute et affiche.
func runCommand(ctx context.Context, spec command, args []string, stdout, stderr io.Writer) int {
	call, err := parseInvocation(spec, args)
	out := printer{stdout: stdout, stderr: stderr}
	if err != nil {
		out.jsonOutput = slices.Contains(args, "--json")
		return out.failure(err)
	}
	out.jsonOutput = call.has(flagJSON)
	call.stdout, call.stderr = stdout, stderr
	call.settings, err = config.Load(call.configArgs, lookupEnv, defaultConfigDir())
	if err != nil {
		if spec.onConfigError != nil && !errors.Is(err, config.ErrUsage) {
			return out.result(spec.onConfigError(err))
		}
		return out.failure(err)
	}
	out.secrets = call.settings.KnownSecrets()
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
	service, closer, err := openService(ctx, call.settings)
	if err != nil {
		return nil, err
	}
	call.service = service
	return func() { _ = closer.Close() }, nil
}
