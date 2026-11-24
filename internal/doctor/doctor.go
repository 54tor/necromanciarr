// Package doctor diagnostique l'installation : configuration, dossiers, services joignables, liens possibles.
// Les contrôles s'exécutent l'un après l'autre, chacun borné par un délai, et leur rapport ne contient aucun secret.
package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"necromanciarr/internal/config"
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

// Result est le constat d'un contrôle ; Hint propose la correction.
type Result struct {
	Status Status `json:"status"`
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
}

// Check est un contrôle nommé par un identifiant stable.
type Check struct {
	ID    string
	Title string
	Run   func(ctx context.Context) Result
}

// NamedResult associe un résultat au contrôle qui l'a produit.
type NamedResult struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Result
	DurationMilliseconds int64 `json:"durationMs"`
}

// Report est le rapport d'une exécution ; Worst est le pire statut rencontré (Skipped ignoré).
type Report struct {
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
// gêner les suivants ; si ctx est annulé, les contrôles restants sont notés Skipped. Détail et conseil sont
// masqués par la redaction de la configuration.
func Run(ctx context.Context, checks []Check, options ...Option) Report {
	settings := runSettings{timeout: DefaultTimeout}
	for _, option := range options {
		option(&settings)
	}
	report := Report{Checks: make([]NamedResult, 0, len(checks))}
	for _, check := range checks {
		named := NamedResult{ID: check.ID, Title: check.Title}
		if ctx.Err() != nil {
			named.Result = Result{Status: Skipped, Detail: "not run: cancelled"}
		} else {
			started := time.Now()
			named.Result = runOne(ctx, check, settings.timeout)
			named.DurationMilliseconds = time.Since(started).Milliseconds()
		}
		named.Detail, named.Hint = config.RedactText(named.Detail), config.RedactText(named.Hint)
		report.Checks = append(report.Checks, named)
		if named.Status.severity() > report.Worst.severity() {
			report.Worst = named.Status
		}
	}
	return report
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
				resultChannel <- Result{Status: Fail, Detail: fmt.Sprintf("check panicked: %v", recovered)}
			}
		}()
		resultChannel <- check.Run(checkCtx)
	}()
	select {
	case result := <-resultChannel:
		return result
	case <-checkCtx.Done():
		return Result{
			Status: Fail, Detail: "check did not finish: " + checkCtx.Err().Error(),
			Hint: fmt.Sprintf("the check is limited to %s", timeout),
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

// MarshalJSONReport sérialise le rapport en JSON indenté (champs stables, aucun secret).
func MarshalJSONReport(report Report) ([]byte, error) {
	return json.MarshalIndent(report, "", "  ")
}
