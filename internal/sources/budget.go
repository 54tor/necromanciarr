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
	// KindQuery et KindDownload sont les deux sortes de requête décomptées.
	KindQuery    = "query"
	KindDownload = "download"
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
// TakeIndexerUsage est la seule écriture du Budget : le test du plafond et l'ajout y sont atomiques, sûrs entre
// processus.
type UsageStore interface {
	CountIndexerUsage(ctx context.Context, indexer, kind string, since time.Time) (int, error)
	RecordIndexerUsage(ctx context.Context, indexer, kind string, at time.Time) error
	TakeIndexerUsage(ctx context.Context, indexer, kind string, at, since time.Time, limit int) (bool, error)
}

var _ UsageStore = (*store.Store)(nil)

// retryBoard garde, pour la vie du processus, jusqu'à quand chaque indexeur est écarté par un Retry-After : il est
// partagé par tous les budgets d'une chaîne, sinon le Verify qui suit un Match limité reprendrait aussitôt.
type retryBoard struct {
	mutex sync.Mutex
	until map[ID]time.Time
}

func newRetryBoard() *retryBoard { return &retryBoard{until: map[ID]time.Time{}} }

func (board *retryBoard) block(indexer ID, until time.Time) {
	board.mutex.Lock()
	defer board.mutex.Unlock()
	if until.After(board.until[indexer]) {
		board.until[indexer] = until
	}
}

func (board *retryBoard) remaining(indexer ID, now time.Time) time.Duration {
	board.mutex.Lock()
	defer board.mutex.Unlock()
	return board.until[indexer].Sub(now)
}

// indexerRun est l'état en mémoire d'un indexeur pendant une exécution.
type indexerRun struct {
	report      IndexerReport
	lastRequest time.Time
	// blockedErr écarte l'indexeur pour le reste de l'exécution (refus d'accès, indisponible sans délai).
	blockedErr error
}

// Budget compte ce qu'une exécution demande aux indexeurs. Les décomptes par exécution sont en mémoire, ceux par jour
// viennent du store (une relance ne remet pas la journée à zéro). Le quota est dépensé AVANT la requête, qu'elle
// réussisse ou non. Un seul verrou (gate) couvre toutes les prises : « un seul indexeur sollicité à la fois » (spec
// 4.B.5), les requêtes suivant chaque prise une à une dans l'appelant séquentiel. Passer par Chain.Call.
type Budget struct {
	gate   chan struct{}
	limits map[ID]Limits
	usage  UsageStore
	now    func() time.Time
	sleep  func(ctx context.Context, duration time.Duration) error
	board  *retryBoard
	// stateMutex protège runs : il n'est tenu que le temps d'une lecture ou d'une écriture, jamais pendant une attente.
	stateMutex sync.Mutex
	runs       map[ID]*indexerRun
	// caps garde les capacités lues pendant l'exécution : une seule requête t=caps par indexeur et par exécution.
	caps map[ID]torznab.Caps
}

func newBudget(limits map[ID]Limits, usage UsageStore, now func() time.Time, sleep func(context.Context, time.Duration) error, board *retryBoard) *Budget {
	return &Budget{gate: make(chan struct{}, 1), limits: limits, usage: usage, now: now, sleep: sleep, board: board, runs: map[ID]*indexerRun{}, caps: map[ID]torznab.Caps{}}
}

// TakeQuery dépense une recherche chez l'indexeur, après avoir attendu MinInterval depuis sa dernière requête.
func (budget *Budget) TakeQuery(ctx context.Context, indexer ID) error {
	return budget.take(ctx, indexer, KindQuery)
}

// TakeDownload dépense un téléchargement de .torrent chez l'indexeur.
func (budget *Budget) TakeDownload(ctx context.Context, indexer ID) error {
	return budget.take(ctx, indexer, KindDownload)
}

func (budget *Budget) take(ctx context.Context, indexer ID, kind string) error {
	select {
	case budget.gate <- struct{}{}:
		defer func() { <-budget.gate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	limits, known := budget.limits[indexer]
	if !known {
		return fmt.Errorf("%w: %s", ErrUnknownIndexer, indexer)
	}
	lastRequest, err := budget.admit(indexer, kind, limits.perRun(kind))
	if err != nil {
		return err
	}
	if err := budget.waitInterval(ctx, lastRequest, limits.MinInterval); err != nil {
		return err
	}
	at := budget.now()
	taken, err := budget.usage.TakeIndexerUsage(ctx, string(indexer), kind, at, at.Add(-store.IndexerUsageWindow), limits.perDay(kind))
	if err != nil {
		return err
	}
	budget.stateMutex.Lock()
	defer budget.stateMutex.Unlock()
	run := budget.runOf(indexer)
	if !taken {
		run.report.BudgetExhausted = true
		return fmt.Errorf("%w: %s daily limit", ErrBudgetExhausted, indexer)
	}
	run.lastRequest = at
	run.add(kind)
	return nil
}

// admit refuse sans dépenser de quota un indexeur écarté ou arrivé à son plafond par exécution ; il rend l'heure de
// sa dernière requête.
func (budget *Budget) admit(indexer ID, kind string, runLimit int) (time.Time, error) {
	budget.stateMutex.Lock()
	defer budget.stateMutex.Unlock()
	run := budget.runOf(indexer)
	if run.blockedErr != nil {
		return time.Time{}, run.blockedErr
	}
	if remaining := budget.board.remaining(indexer, budget.now()); remaining > 0 {
		return time.Time{}, fmt.Errorf("%w: retry in %s", torznab.ErrUnavailable, remaining.Round(time.Second))
	}
	if run.count(kind) >= runLimit {
		run.report.BudgetExhausted = true
		return time.Time{}, fmt.Errorf("%w: %s per-run limit", ErrBudgetExhausted, indexer)
	}
	return run.lastRequest, nil
}

// waitInterval attend la fin de MinInterval depuis la dernière requête à ce même indexeur.
func (budget *Budget) waitInterval(ctx context.Context, lastRequest time.Time, interval time.Duration) error {
	if lastRequest.IsZero() {
		return nil
	}
	remaining := interval - budget.now().Sub(lastRequest)
	if remaining <= 0 {
		return nil
	}
	return budget.sleep(ctx, remaining)
}

// Observe tire parti de l'erreur d'une requête : un Retry-After reçu (torznab.UnavailableError, plafonné à une heure)
// écarte l'indexeur jusqu'à son terme pour tout le processus (retryBoard) ; sans délai, ou après un refus d'accès,
// jusqu'à la fin de cette exécution. Les autres erreurs n'ont aucun effet. Le limiteur du client ne lit pas
// Retry-After : c'est ici qu'il est honoré.
func (budget *Budget) Observe(indexer ID, err error) {
	var unavailable *torznab.UnavailableError
	isUnavailable, isDenied := errors.As(err, &unavailable), errors.Is(err, torznab.ErrAccessDenied)
	if !isUnavailable && !isDenied {
		return
	}
	if isUnavailable && !isDenied && unavailable.RetryAfter > 0 {
		budget.board.block(indexer, budget.now().Add(min(unavailable.RetryAfter, maxRetryAfter)))
		return
	}
	budget.stateMutex.Lock()
	defer budget.stateMutex.Unlock()
	run := budget.runOf(indexer)
	if run.blockedErr != nil {
		return
	}
	if isDenied {
		run.blockedErr = fmt.Errorf("%w: indexer refused the request earlier in this run", torznab.ErrAccessDenied)
		return
	}
	run.blockedErr = fmt.Errorf("%w: indexer unavailable earlier in this run", torznab.ErrUnavailable)
}

// blockAfterUnusableAnswer écarte l'indexeur pour le reste de l'exécution (jamais au-delà) après une recherche à la
// réponse hostile ou illisible, ou injoignable : la reposer pour chaque candidat brûlerait le quota. Réservé aux recherches : un
// téléchargement refusé (v2 seul, .torrent invalide, aussi englobés dans ErrBadResponse) ne vise qu'un élément.
func (budget *Budget) blockAfterUnusableAnswer(ctx context.Context, indexer ID, err error) {
	var blocked error
	var withStatus *torznab.UnavailableError
	switch {
	case errors.Is(err, torznab.ErrUnavailable) && !errors.As(err, &withStatus) && ctx.Err() == nil:
		// Panne de transport ou délai par requête dépassé (pas une annulation de l'appelant) : Observe ne la retient
		// pas, et chaque candidat attendrait le délai de nouveau.
		blocked = fmt.Errorf("%w: indexer unreachable earlier in this run", torznab.ErrUnavailable)
	case errors.Is(err, torznab.ErrHostileResponse):
		blocked = fmt.Errorf("%w: indexer sent a hostile response earlier in this run", torznab.ErrHostileResponse)
	case errors.Is(err, torznab.ErrBadResponse):
		blocked = fmt.Errorf("%w: indexer sent an unreadable response earlier in this run", torznab.ErrBadResponse)
	default:
		return
	}
	budget.stateMutex.Lock()
	defer budget.stateMutex.Unlock()
	if run := budget.runOf(indexer); run.blockedErr == nil {
		run.blockedErr = blocked
	}
}

// CountMagnetOnly ajoute des résultats sans lien .torrent (magnet seul) au résumé de l'indexeur.
func (budget *Budget) CountMagnetOnly(indexer ID, count int) {
	budget.stateMutex.Lock()
	defer budget.stateMutex.Unlock()
	budget.runOf(indexer).report.MagnetOnly += count
}

// CountCacheHit compte une recherche servie par le cache : aucune requête, aucun quota.
func (budget *Budget) CountCacheHit(indexer ID) {
	budget.stateMutex.Lock()
	defer budget.stateMutex.Unlock()
	budget.runOf(indexer).report.CacheHits++
}

// Report renvoie une copie du résumé par indexeur ; budget nil donne un résumé vide.
func (budget *Budget) Report() Report {
	report := Report{}
	if budget == nil {
		return report
	}
	budget.stateMutex.Lock()
	defer budget.stateMutex.Unlock()
	for indexer, run := range budget.runs {
		report[indexer] = run.report
	}
	return report
}

// exhausted dit si un plafond de l'indexeur a déjà été atteint pendant l'exécution.
func (budget *Budget) exhausted(indexer ID) bool {
	budget.stateMutex.Lock()
	defer budget.stateMutex.Unlock()
	return budget.runOf(indexer).report.BudgetExhausted
}

func (budget *Budget) knownCaps(indexer ID) (torznab.Caps, bool) {
	budget.stateMutex.Lock()
	defer budget.stateMutex.Unlock()
	caps, known := budget.caps[indexer]
	return caps, known
}

func (budget *Budget) rememberCaps(indexer ID, caps torznab.Caps) {
	budget.stateMutex.Lock()
	defer budget.stateMutex.Unlock()
	budget.caps[indexer] = caps
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
	if kind == KindQuery {
		return run.report.Queries
	}
	return run.report.Downloads
}

func (run *indexerRun) add(kind string) {
	if kind == KindQuery {
		run.report.Queries++
		return
	}
	run.report.Downloads++
}

func (limits Limits) perRun(kind string) int {
	if kind == KindQuery {
		return limits.MaxQueriesPerRun
	}
	return limits.MaxDownloadsPerRun
}

func (limits Limits) perDay(kind string) int {
	if kind == KindQuery {
		return limits.MaxQueriesPerDay
	}
	return limits.MaxDownloadsPerDay
}
