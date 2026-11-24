package sources

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"necromanciarr/internal/store"
	"necromanciarr/internal/torznab"
)

const (
	kindQuery    = "query"
	kindDownload = "download"
	// maxRetryAfter plafonne un Retry-After reçu, comme httpsafe.
	maxRetryAfter = time.Hour
)

var (
	// ErrBudgetExhausted : un plafond par exécution ou par jour est atteint ; ce n'est pas un échec, la suite attend
	// la prochaine exécution (raison indexer_budget_exhausted).
	ErrBudgetExhausted = errors.New("sources: indexer budget exhausted")
	// ErrUnknownIndexer : l'indexeur n'a pas de limites dans cette chaîne.
	ErrUnknownIndexer = errors.New("sources: unknown indexer")
)

// Limits borne les requêtes à un indexeur (défauts et bornes : config) ; un plafond à zéro refuse tout.
type Limits struct {
	MinInterval                                                                time.Duration
	MaxQueriesPerRun, MaxDownloadsPerRun, MaxQueriesPerDay, MaxDownloadsPerDay int
}

// UsageStore persiste l'usage des indexeurs pour la fenêtre glissante d'un jour ; *store.Store l'implémente.
type UsageStore interface {
	CountIndexerUsage(ctx context.Context, indexer, kind string, since time.Time) (int, error)
	RecordIndexerUsage(ctx context.Context, indexer, kind string, at time.Time) error
}

// atomicUsageStore ajoute la prise atomique : le test du plafond et l'écriture sont une seule instruction, sûre
// entre processus. Sans elle, Budget compte puis écrit sous son verrou, sûr dans ce seul processus.
type atomicUsageStore interface {
	UsageStore
	TakeIndexerUsage(ctx context.Context, indexer, kind string, at, since time.Time, limit int) (bool, error)
}

var _ atomicUsageStore = (*store.Store)(nil)

// indexerRun est l'état en mémoire d'un indexeur pendant une exécution.
type indexerRun struct {
	report        IndexerReport
	lastRequest   time.Time
	blockedErr    error
	blockedUntil  time.Time
	blockedForRun bool
}

// Budget compte ce qu'une exécution demande aux indexeurs. Les décomptes par exécution sont en mémoire, ceux par jour
// viennent du store (une relance ne remet pas la journée à zéro). Le quota est dépensé AVANT la requête, qu'elle
// réussisse ou non. Un verrou unique couvre toutes les prises : « un seul indexeur sollicité à la fois » (spec
// 4.B.5), les requêtes suivant chaque prise une à une dans l'appelant séquentiel.
type Budget struct {
	gate   sync.Mutex
	limits map[ID]Limits
	usage  UsageStore
	now    func() time.Time
	sleep  func(ctx context.Context, duration time.Duration) error
	runs   map[ID]*indexerRun
}

func newBudget(limits map[ID]Limits, usage UsageStore, now func() time.Time, sleep func(context.Context, time.Duration) error) *Budget {
	return &Budget{limits: limits, usage: usage, now: now, sleep: sleep, runs: map[ID]*indexerRun{}}
}

// TakeQuery dépense une recherche chez l'indexeur, après avoir attendu MinInterval depuis sa dernière requête.
func (budget *Budget) TakeQuery(ctx context.Context, indexer ID) error {
	return budget.take(ctx, indexer, kindQuery)
}

// TakeDownload dépense un téléchargement de .torrent chez l'indexeur.
func (budget *Budget) TakeDownload(ctx context.Context, indexer ID) error {
	return budget.take(ctx, indexer, kindDownload)
}

func (budget *Budget) take(ctx context.Context, indexer ID, kind string) error {
	budget.gate.Lock()
	defer budget.gate.Unlock()
	limits, known := budget.limits[indexer]
	if !known {
		return fmt.Errorf("%w: %s", ErrUnknownIndexer, indexer)
	}
	run := budget.runOf(indexer)
	if err := budget.checkBlocked(run); err != nil {
		return err
	}
	runLimit, dayLimit := limits.perRun(kind), limits.perDay(kind)
	if run.count(kind) >= runLimit {
		run.report.BudgetExhausted = true
		return fmt.Errorf("%w: %s per-run limit", ErrBudgetExhausted, indexer)
	}
	if err := budget.waitInterval(ctx, run, limits.MinInterval); err != nil {
		return err
	}
	taken, err := budget.spendDay(ctx, indexer, kind, dayLimit)
	if err != nil {
		return err
	}
	if !taken {
		run.report.BudgetExhausted = true
		return fmt.Errorf("%w: %s daily limit", ErrBudgetExhausted, indexer)
	}
	run.lastRequest = budget.now()
	run.add(kind)
	return nil
}

// spendDay écrit la ligne d'usage si le plafond du jour le permet.
func (budget *Budget) spendDay(ctx context.Context, indexer ID, kind string, dayLimit int) (bool, error) {
	at := budget.now()
	since := at.Add(-store.IndexerUsageWindow)
	if atomic, isAtomic := budget.usage.(atomicUsageStore); isAtomic {
		return atomic.TakeIndexerUsage(ctx, string(indexer), kind, at, since, dayLimit)
	}
	used, err := budget.usage.CountIndexerUsage(ctx, string(indexer), kind, since)
	if err != nil {
		return false, err
	}
	if used >= dayLimit {
		return false, nil
	}
	return true, budget.usage.RecordIndexerUsage(ctx, string(indexer), kind, at)
}

// waitInterval attend la fin de MinInterval depuis la dernière requête à ce même indexeur.
func (budget *Budget) waitInterval(ctx context.Context, run *indexerRun, interval time.Duration) error {
	if run.lastRequest.IsZero() {
		return nil
	}
	remaining := interval - budget.now().Sub(run.lastRequest)
	if remaining <= 0 {
		return nil
	}
	return budget.sleep(ctx, remaining)
}

// checkBlocked refuse sans dépenser de quota tant qu'un Retry-After court ou que l'indexeur est écarté pour l'exécution.
func (budget *Budget) checkBlocked(run *indexerRun) error {
	if run.blockedErr == nil {
		return nil
	}
	if run.blockedForRun {
		return run.blockedErr
	}
	remaining := run.blockedUntil.Sub(budget.now())
	if remaining <= 0 {
		run.blockedErr = nil
		return nil
	}
	return fmt.Errorf("%w: retry in %s", torznab.ErrUnavailable, remaining.Round(time.Second))
}

// Observe tire parti de l'erreur d'une requête : un Retry-After reçu (torznab.UnavailableError, plafonné à une heure)
// écarte l'indexeur jusqu'à son terme ; sans délai, ou après un refus d'accès, jusqu'à la fin de l'exécution. Les
// autres erreurs n'ont aucun effet. Le limiteur du client ne lit pas Retry-After : c'est ici qu'il est honoré.
func (budget *Budget) Observe(indexer ID, err error) {
	var unavailable *torznab.UnavailableError
	isUnavailable, isDenied := errors.As(err, &unavailable), errors.Is(err, torznab.ErrAccessDenied)
	if !isUnavailable && !isDenied {
		return
	}
	budget.gate.Lock()
	defer budget.gate.Unlock()
	run := budget.runOf(indexer)
	if run.blockedForRun {
		return
	}
	switch {
	case isDenied:
		run.blockedErr, run.blockedForRun = fmt.Errorf("%w: indexer refused the request earlier in this run", torznab.ErrAccessDenied), true
	case unavailable.RetryAfter > 0:
		run.blockedErr, run.blockedForRun = torznab.ErrUnavailable, false
		run.blockedUntil = budget.now().Add(min(unavailable.RetryAfter, maxRetryAfter))
	default:
		run.blockedErr, run.blockedForRun = fmt.Errorf("%w: indexer unavailable earlier in this run", torznab.ErrUnavailable), true
	}
}

// CountMagnetOnly ajoute des résultats sans lien .torrent (magnet seul) au résumé de l'indexeur.
func (budget *Budget) CountMagnetOnly(indexer ID, count int) {
	budget.gate.Lock()
	defer budget.gate.Unlock()
	budget.runOf(indexer).report.MagnetOnly += count
}

// Report renvoie une copie du résumé par indexeur ; budget nil donne un résumé vide.
func (budget *Budget) Report() Report {
	report := Report{}
	if budget == nil {
		return report
	}
	budget.gate.Lock()
	defer budget.gate.Unlock()
	for indexer, run := range budget.runs {
		report[indexer] = run.report
	}
	return report
}

func (budget *Budget) runOf(indexer ID) *indexerRun {
	run, known := budget.runs[indexer]
	if !known {
		run = &indexerRun{}
		budget.runs[indexer] = run
	}
	return run
}

func (run *indexerRun) count(kind string) int {
	if kind == kindQuery {
		return run.report.Queries
	}
	return run.report.Downloads
}

func (run *indexerRun) add(kind string) {
	if kind == kindQuery {
		run.report.Queries++
		return
	}
	run.report.Downloads++
}

func (limits Limits) perRun(kind string) int {
	if kind == kindQuery {
		return limits.MaxQueriesPerRun
	}
	return limits.MaxDownloadsPerRun
}

func (limits Limits) perDay(kind string) int {
	if kind == kindQuery {
		return limits.MaxQueriesPerDay
	}
	return limits.MaxDownloadsPerDay
}
