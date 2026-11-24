// Package doctor diagnostique l'installation : configuration, dossiers, services joignables, liens possibles.
// Les contrôles s'exécutent l'un après l'autre, chacun borné par un délai, et leur rapport ne contient aucun secret.
package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"necromanciarr/internal/config"
	"necromanciarr/internal/i18n"
)

// Status est le résultat d'un contrôle.
type Status int

// États possibles ; Warn et Skipped ne font pas échouer le diagnostic.
const (
	OK Status = iota
	Warn
	Fail
	Skipped
)

const (
	// DefaultTimeout borne chaque contrôle.
	DefaultTimeout = 10 * time.Second
	exitFailure    = 1
)

var statusNames = map[Status]string{OK: "ok", Warn: "warn", Fail: "fail", Skipped: "skipped"}

// String nomme le statut (ok, warn, fail, skipped).
func (status Status) String() string {
	if name, known := statusNames[status]; known {
		return name
	}
	return "unknown"
}

// MarshalText sérialise le statut par son nom : les sorties JSON restent lisibles et stables.
func (status Status) MarshalText() ([]byte, error) { return []byte(status.String()), nil }

// UnmarshalText relit un statut sérialisé par MarshalText.
func (status *Status) UnmarshalText(text []byte) error {
	for candidate, name := range statusNames {
		if name == string(text) {
			*status = candidate
			return nil
		}
	}
	return fmt.Errorf("doctor: unknown status %q", text)
}

// severity ordonne les états du moins grave au plus grave ; Skipped ne pèse pas.
func (status Status) severity() int {
	switch status {
	case Warn:
		return 1
	case Fail:
		return 2
	default:
		return 0
	}
}

// Result est le constat d'un contrôle. Code est stable et jamais traduit ; Detail et Hint sont des messages rendus à
// l'affichage (Hint.Key vide : aucun conseil). Un contrôle qui examine plusieurs éléments remplit Details et Hints,
// rendus joints par « ; » ; Code est alors celui du pire constat.
type Result struct {
	Status  Status
	Code    string
	Detail  i18n.Message
	Hint    i18n.Message
	Details []i18n.Message
	Hints   []i18n.Message
}

// Check est un contrôle nommé par un identifiant stable ; son titre est une clé du catalogue.
type Check struct {
	ID          string
	TitleKey    i18n.Key
	TitleParams map[string]string
	Run         func(ctx context.Context) Result
}

// Outcome associe un résultat au contrôle qui l'a produit.
type Outcome struct {
	ID                   string
	TitleKey             i18n.Key
	TitleParams          map[string]string
	Result               Result
	DurationMilliseconds int64
}

// Report est le rapport d'une exécution ; Worst est le pire statut rencontré (Skipped ignoré).
type Report struct {
	Checks []Outcome
	Worst  Status
}

// NamedResult est un résultat rendu dans une langue : seuls Title, Detail et Hint sont du texte traduit.
type NamedResult struct {
	ID                   string `json:"id"`
	Title                string `json:"title"`
	Status               Status `json:"status"`
	Code                 string `json:"code"`
	Detail               string `json:"detail"`
	Hint                 string `json:"hint,omitempty"`
	DurationMilliseconds int64  `json:"durationMs"`
}

// RenderedReport est le rapport rendu, tel que l'API et la CLI le sérialisent.
type RenderedReport struct {
	Checks []NamedResult `json:"checks"`
	Worst  Status        `json:"worst"`
}

type runSettings struct {
	timeout time.Duration
}

// Option règle Run.
type Option func(*runSettings)

// WithTimeout fixe le délai de chaque contrôle (10 s par défaut).
func WithTimeout(timeout time.Duration) Option {
	return func(settings *runSettings) { settings.timeout = timeout }
}

// Run exécute les contrôles dans l'ordre donné. Un contrôle qui dépasse son délai ou panique devient Fail sans
// gêner les suivants ; si ctx est annulé, le contrôle en cours et les suivants sont notés Skipped. Les paramètres des
// messages sont masqués par la redaction de la configuration.
// Limite : un contrôle bloqué qui ignore son contexte est abandonné, pas arrêté : sa goroutine fuit jusqu'à son retour.
func Run(ctx context.Context, checks []Check, options ...Option) Report {
	settings := runSettings{timeout: DefaultTimeout}
	for _, option := range options {
		option(&settings)
	}
	report := Report{Checks: make([]Outcome, 0, len(checks))}
	for _, check := range checks {
		outcome := Outcome{ID: check.ID, TitleKey: check.TitleKey, TitleParams: check.TitleParams}
		if ctx.Err() != nil {
			outcome.Result = cancelledResult()
		} else {
			started := time.Now()
			outcome.Result = runOne(ctx, check, settings.timeout)
			outcome.DurationMilliseconds = time.Since(started).Milliseconds()
		}
		outcome.Result = redactResult(outcome.Result, func(text string) string { return config.RedactText(text) })
		report.Checks = append(report.Checks, outcome)
		if outcome.Result.Status.severity() > report.Worst.severity() {
			report.Worst = outcome.Result.Status
		}
	}
	return report
}

func cancelledResult() Result {
	return Result{Status: Skipped, Code: CodeCancelled, Detail: i18n.Message{Key: i18n.KeyDoctorCancelled}}
}

// runOne ne attend le contrôle que jusqu'au délai : un contrôle qui ignore ctx continue en arrière-plan, mais
// n'est plus attendu.
func runOne(ctx context.Context, check Check, timeout time.Duration) Result {
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resultChannel := make(chan Result, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				resultChannel <- panicResult(recovered, func(text string) string { return config.RedactText(text) })
			}
		}()
		resultChannel <- check.Run(checkCtx)
	}()
	select {
	case result := <-resultChannel:
		return result
	case <-checkCtx.Done():
		if ctx.Err() != nil {
			return cancelledResult()
		}
		return Result{
			Status: Fail, Code: CodeCheckTimeout,
			Detail: i18n.Message{Key: i18n.KeyDoctorTimeout, Params: map[string]string{"error": checkCtx.Err().Error()}},
			Hint:   i18n.Message{Key: i18n.KeyDoctorTimeoutHint, Params: map[string]string{"timeout": timeout.String()}},
		}
	}
}

// ExitCode vaut 0 sans Fail, 1 sinon ; Warn et Skipped ne font pas échouer.
func ExitCode(report Report) int {
	if report.Worst == Fail {
		return exitFailure
	}
	return 0
}

// Render rend le rapport dans la langue du traducteur ; le traducteur masque les secrets des paramètres.
func Render(report Report, translator i18n.Translator) RenderedReport {
	rendered := RenderedReport{Checks: make([]NamedResult, 0, len(report.Checks)), Worst: report.Worst}
	for _, outcome := range report.Checks {
		result := outcome.Result
		rendered.Checks = append(rendered.Checks, NamedResult{
			ID: outcome.ID, Title: translator.Text(outcome.TitleKey, outcome.TitleParams), Status: result.Status, Code: result.Code,
			Detail: joinMessages(translator, result.Detail, result.Details), Hint: joinMessages(translator, result.Hint, result.Hints),
			DurationMilliseconds: outcome.DurationMilliseconds,
		})
	}
	return rendered
}

// joinMessages rend la liste si elle existe, sinon le message seul ; un message sans clé ne produit rien.
func joinMessages(translator i18n.Translator, single i18n.Message, list []i18n.Message) string {
	if len(list) == 0 {
		list = []i18n.Message{single}
	}
	parts := make([]string, 0, len(list))
	for _, message := range list {
		if message.Key != "" {
			parts = append(parts, translator.Render(message))
		}
	}
	return strings.Join(parts, separatorDetail)
}

// MarshalJSONReport sérialise le rapport rendu en JSON indenté (champs stables, aucun secret).
func MarshalJSONReport(report Report, translator i18n.Translator) ([]byte, error) {
	return json.MarshalIndent(Render(report, translator), "", "  ")
}

const maxPanicMessageRunes = 200

// panicResult résume une panique sans jamais l'afficher brute : type de la valeur, puis message masqué par redact
// sur le texte complet et seulement ensuite tronqué à 200 caractères (un secret à cheval sur la coupure resterait sinon).
func panicResult(recovered any, redact func(string) string) Result {
	message := []rune(redact(fmt.Sprint(recovered)))
	if len(message) > maxPanicMessageRunes {
		message = message[:maxPanicMessageRunes]
	}
	return Result{
		Status: Fail, Code: CodePanic,
		Detail: i18n.Message{Key: i18n.KeyDoctorPanic, Params: map[string]string{"type": fmt.Sprintf("%T", recovered), "message": string(message)}},
	}
}

// redactResult applique redact à chaque paramètre de chaque message, sans modifier les paramètres de l'appelant.
func redactResult(result Result, redact func(string) string) Result {
	result.Detail, result.Hint = redactMessage(result.Detail, redact), redactMessage(result.Hint, redact)
	result.Details, result.Hints = redactMessages(result.Details, redact), redactMessages(result.Hints, redact)
	return result
}

func redactMessages(messages []i18n.Message, redact func(string) string) []i18n.Message {
	if messages == nil {
		return nil
	}
	redacted := make([]i18n.Message, len(messages))
	for messageIndex, message := range messages {
		redacted[messageIndex] = redactMessage(message, redact)
	}
	return redacted
}

func redactMessage(message i18n.Message, redact func(string) string) i18n.Message {
	if len(message.Params) == 0 {
		return message
	}
	params := make(map[string]string, len(message.Params))
	for name, value := range message.Params {
		params[name] = redact(value)
	}
	return i18n.Message{Key: message.Key, Params: params}
}
