package sources

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"necromanciarr/internal/store"
	"necromanciarr/internal/torznab"
)

const (
	indexerA ID = "torznab:a"
	indexerB ID = "torznab:b"
)

var budgetNow = time.Unix(1700000000, 0).UTC()

// fakeClock avance seulement quand le budget attend : aucun vrai sommeil dans les tests.
type fakeClock struct {
	mutex  sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func (clock *fakeClock) Now() time.Time {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	return clock.now
}

func (clock *fakeClock) Advance(duration time.Duration) {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	clock.now = clock.now.Add(duration)
}

func (clock *fakeClock) Sleep(_ context.Context, duration time.Duration) error {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	clock.sleeps = append(clock.sleeps, duration)
	clock.now = clock.now.Add(duration)
	return nil
}

func testLimits() Limits {
	return Limits{MinInterval: 3 * time.Second, MaxQueriesPerRun: 30, MaxDownloadsPerRun: 10, MaxQueriesPerDay: 200, MaxDownloadsPerDay: 50}
}

func openUsageStore(t *testing.T, path string) *store.Store {
	t.Helper()
	opened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	return opened
}

func newTestBudget(t *testing.T, usage UsageStore, limits Limits) (*Budget, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: budgetNow}
	budget := newBudget(map[ID]Limits{indexerA: limits, indexerB: limits}, usage, clock.Now, clock.Sleep)
	return budget, clock
}

func realUsage(t *testing.T) *store.Store {
	return openUsageStore(t, filepath.Join(t.TempDir(), "necromanciarr.db"))
}

func TestBudget_stopsAtMaxDownloadsPerRun(t *testing.T) {
	limits := testLimits()
	limits.MinInterval = 0
	budget, _ := newTestBudget(t, realUsage(t), limits)

	for downloadNumber := 1; downloadNumber <= 10; downloadNumber++ {
		if err := budget.TakeDownload(context.Background(), indexerA); err != nil {
			t.Fatalf("download %d: %v", downloadNumber, err)
		}
	}
	valeurObtenue := budget.TakeDownload(context.Background(), indexerA)

	if !errors.Is(valeurObtenue, ErrBudgetExhausted) {
		t.Fatalf("valeurObtenue %v, valeurAttendue ErrBudgetExhausted on the 11th download", valeurObtenue)
	}
	report := budget.Report()[indexerA]
	if report.Downloads != 10 || !report.BudgetExhausted {
		t.Fatalf("valeurObtenue %+v, valeurAttendue 10 downloads and BudgetExhausted", report)
	}
}

func TestBudget_stopsAtMaxQueriesPerRun(t *testing.T) {
	limits := testLimits()
	limits.MinInterval, limits.MaxQueriesPerRun = 0, 2
	budget, _ := newTestBudget(t, realUsage(t), limits)

	_ = budget.TakeQuery(context.Background(), indexerA)
	_ = budget.TakeQuery(context.Background(), indexerA)
	valeurObtenue := budget.TakeQuery(context.Background(), indexerA)

	if !errors.Is(valeurObtenue, ErrBudgetExhausted) || budget.Report()[indexerA].Queries != 2 {
		t.Fatalf("valeurObtenue %v / %+v, valeurAttendue ErrBudgetExhausted after 2 queries", valeurObtenue, budget.Report()[indexerA])
	}
}

func TestBudget_slidingDayWindow(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		age           time.Duration
		expectedAllow bool
	}{
		{"23 hours ago still counts", 23 * time.Hour, false},
		{"25 hours ago left the window", 25 * time.Hour, true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			usage := realUsage(t)
			for range 200 {
				if err := usage.RecordIndexerUsage(context.Background(), string(indexerA), kindQuery, budgetNow.Add(-testCase.age)); err != nil {
					t.Fatal(err)
				}
			}
			limits := testLimits()
			limits.MinInterval = 0
			budget, _ := newTestBudget(t, usage, limits)

			err := budget.TakeQuery(context.Background(), indexerA)

			if valeurObtenue := err == nil; valeurObtenue != testCase.expectedAllow {
				t.Fatalf("valeurObtenue allowed=%v (%v), valeurAttendue %v", valeurObtenue, err, testCase.expectedAllow)
			}
			if !testCase.expectedAllow && !errors.Is(err, ErrBudgetExhausted) {
				t.Fatalf("valeurObtenue %v, valeurAttendue ErrBudgetExhausted", err)
			}
		})
	}
}

func TestBudget_dayCountSurvivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "necromanciarr.db")
	limits := testLimits()
	limits.MinInterval, limits.MaxDownloadsPerDay = 0, 3
	firstProcess, _ := newTestBudget(t, openUsageStore(t, path), limits)
	for range 3 {
		if err := firstProcess.TakeDownload(context.Background(), indexerA); err != nil {
			t.Fatal(err)
		}
	}

	secondProcess, _ := newTestBudget(t, openUsageStore(t, path), limits)
	valeurObtenue := secondProcess.TakeDownload(context.Background(), indexerA)

	if !errors.Is(valeurObtenue, ErrBudgetExhausted) {
		t.Fatalf("valeurObtenue %v, valeurAttendue the day quota to survive a restart", valeurObtenue)
	}
}

func TestBudget_spendsTheQuotaBeforeTheRequest(t *testing.T) {
	usage := realUsage(t)
	limits := testLimits()
	limits.MinInterval = 0
	budget, _ := newTestBudget(t, usage, limits)

	if err := budget.TakeDownload(context.Background(), indexerA); err != nil {
		t.Fatal(err)
	}

	// Aucune requête n'a encore été faite, ni réussie : la ligne d'usage existe déjà.
	valeurObtenue, err := usage.CountIndexerUsage(context.Background(), string(indexerA), kindDownload, budgetNow.Add(-time.Hour))
	if err != nil || valeurObtenue != 1 {
		t.Fatalf("valeurObtenue %d rows (err %v), valeurAttendue 1 recorded at take time", valeurObtenue, err)
	}
}

func TestBudget_dayQuotaHoldsAcrossBudgetsOnSeparateHandles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "necromanciarr.db")
	limits := testLimits()
	limits.MinInterval, limits.MaxDownloadsPerRun, limits.MaxDownloadsPerDay = 0, 100, 7
	var granted atomic.Int32
	var group sync.WaitGroup
	for range 2 {
		budget, _ := newTestBudget(t, openUsageStore(t, path), limits)
		for range 20 {
			group.Add(1)
			go func() {
				defer group.Done()
				if budget.TakeDownload(context.Background(), indexerA) == nil {
					granted.Add(1)
				}
			}()
		}
	}
	group.Wait()

	if valeurObtenue := int(granted.Load()); valeurObtenue != 7 {
		t.Fatalf("valeurObtenue %d downloads granted, valeurAttendue 7 (the day quota, shared by two processes)", valeurObtenue)
	}
}

// blockingUsage mesure combien de prises sont en cours en même temps.
type blockingUsage struct {
	inFlight, maxInFlight atomic.Int32
}

func (usage *blockingUsage) enter() func() {
	current := usage.inFlight.Add(1)
	for {
		seen := usage.maxInFlight.Load()
		if current <= seen || usage.maxInFlight.CompareAndSwap(seen, current) {
			break
		}
	}
	time.Sleep(5 * time.Millisecond)
	return func() { usage.inFlight.Add(-1) }
}

func (usage *blockingUsage) CountIndexerUsage(context.Context, string, string, time.Time) (int, error) {
	defer usage.enter()()
	return 0, nil
}

func (usage *blockingUsage) RecordIndexerUsage(context.Context, string, string, time.Time) error {
	defer usage.enter()()
	return nil
}

// Spec 4.B.5 : « un seul indexeur sollicité à la fois » : deux indexeurs différents ne se chevauchent pas.
func TestBudget_oneIndexerAtATime(t *testing.T) {
	usage := &blockingUsage{}
	limits := testLimits()
	limits.MinInterval = 0
	budget, _ := newTestBudget(t, usage, limits)
	var group sync.WaitGroup
	for _, indexer := range []ID{indexerA, indexerB, indexerA, indexerB} {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := budget.TakeQuery(context.Background(), indexer); err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()

	if valeurObtenue := usage.maxInFlight.Load(); valeurObtenue != 1 {
		t.Fatalf("valeurObtenue %d takes at once, valeurAttendue 1 (one indexer at a time)", valeurObtenue)
	}
}

func TestBudget_waitsMinIntervalBetweenRequestsToTheSameIndexer(t *testing.T) {
	budget, clock := newTestBudget(t, realUsage(t), testLimits())

	for _, indexer := range []ID{indexerA, indexerA, indexerB} {
		if err := budget.TakeQuery(context.Background(), indexer); err != nil {
			t.Fatal(err)
		}
	}

	if valeurObtenue := clock.sleeps; len(valeurObtenue) != 1 || valeurObtenue[0] != 3*time.Second {
		t.Fatalf("valeurObtenue sleeps %v, valeurAttendue one wait of 3s (second request to A only)", valeurObtenue)
	}
	clock.Advance(10 * time.Second)
	if err := budget.TakeQuery(context.Background(), indexerA); err != nil || len(clock.sleeps) != 1 {
		t.Fatalf("err %v sleeps %v, valeurAttendue no wait once the interval has passed", err, clock.sleeps)
	}
}

func TestBudget_waitIsCancelledByTheContext(t *testing.T) {
	clock := &fakeClock{now: budgetNow}
	budget := newBudget(map[ID]Limits{indexerA: testLimits()}, realUsage(t), clock.Now,
		func(ctx context.Context, _ time.Duration) error { return ctx.Err() })
	if err := budget.TakeQuery(context.Background(), indexerA); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	valeurObtenue := budget.TakeQuery(cancelled, indexerA)

	if !errors.Is(valeurObtenue, context.Canceled) || budget.Report()[indexerA].Queries != 1 {
		t.Fatalf("valeurObtenue %v / %+v, valeurAttendue context.Canceled and no quota spent", valeurObtenue, budget.Report()[indexerA])
	}
}

func TestBudget_retryAfterBlocksTheIndexerWithoutSpendingQuota(t *testing.T) {
	usage := realUsage(t)
	limits := testLimits()
	limits.MinInterval = 0
	budget, clock := newTestBudget(t, usage, limits)
	budget.Observe(indexerA, &torznab.UnavailableError{RetryAfter: 90 * time.Second})

	blocked := budget.TakeQuery(context.Background(), indexerA)
	otherIndexer := budget.TakeQuery(context.Background(), indexerB)

	if !errors.Is(blocked, torznab.ErrUnavailable) || errors.Is(blocked, ErrBudgetExhausted) {
		t.Fatalf("valeurObtenue %v, valeurAttendue ErrUnavailable while Retry-After runs", blocked)
	}
	rows, err := usage.CountIndexerUsage(context.Background(), string(indexerA), kindQuery, budgetNow.Add(-time.Hour))
	if err != nil || rows != 0 || budget.Report()[indexerA].Queries != 0 {
		t.Fatalf("valeurObtenue %d rows (err %v), valeurAttendue no quota spent while blocked", rows, err)
	}
	if otherIndexer != nil {
		t.Fatalf("valeurObtenue %v, valeurAttendue the other indexer to stay usable", otherIndexer)
	}
	clock.Advance(91 * time.Second)
	if err := budget.TakeQuery(context.Background(), indexerA); err != nil {
		t.Fatalf("valeurObtenue %v, valeurAttendue the indexer back after Retry-After", err)
	}
}

func TestBudget_retryAfterIsCappedAtOneHour(t *testing.T) {
	limits := testLimits()
	limits.MinInterval = 0
	budget, clock := newTestBudget(t, realUsage(t), limits)
	budget.Observe(indexerA, &torznab.UnavailableError{RetryAfter: 100 * time.Hour})

	clock.Advance(time.Hour + time.Second)

	if err := budget.TakeQuery(context.Background(), indexerA); err != nil {
		t.Fatalf("valeurObtenue %v, valeurAttendue a Retry-After capped at one hour", err)
	}
}

func TestBudget_unavailableWithoutRetryAfterBlocksTheRun(t *testing.T) {
	limits := testLimits()
	limits.MinInterval = 0
	budget, clock := newTestBudget(t, realUsage(t), limits)
	budget.Observe(indexerA, &torznab.UnavailableError{})

	clock.Advance(48 * time.Hour)

	if err := budget.TakeDownload(context.Background(), indexerA); !errors.Is(err, torznab.ErrUnavailable) {
		t.Fatalf("valeurObtenue %v, valeurAttendue the indexer unavailable for the whole run", err)
	}
}

func TestBudget_accessDeniedBlocksTheRun(t *testing.T) {
	limits := testLimits()
	limits.MinInterval = 0
	budget, _ := newTestBudget(t, realUsage(t), limits)
	budget.Observe(indexerA, torznab.ErrAccessDenied)

	if err := budget.TakeQuery(context.Background(), indexerA); !errors.Is(err, torznab.ErrAccessDenied) {
		t.Fatalf("valeurObtenue %v, valeurAttendue ErrAccessDenied", err)
	}
}

func TestBudget_ignoresOtherErrors(t *testing.T) {
	limits := testLimits()
	limits.MinInterval = 0
	budget, _ := newTestBudget(t, realUsage(t), limits)
	budget.Observe(indexerA, torznab.ErrBadResponse)
	budget.Observe(indexerA, nil)

	if err := budget.TakeQuery(context.Background(), indexerA); err != nil {
		t.Fatalf("valeurObtenue %v, valeurAttendue a bad response not to block the indexer", err)
	}
}

type failingUsage struct{ blockingUsage }

var errUsageStore = errors.New("usage store down")

func (*failingUsage) RecordIndexerUsage(context.Context, string, string, time.Time) error {
	return errUsageStore
}

func TestBudget_storeFailureIsNeitherExhaustionNorCounted(t *testing.T) {
	limits := testLimits()
	limits.MinInterval = 0
	budget, _ := newTestBudget(t, &failingUsage{}, limits)

	valeurObtenue := budget.TakeQuery(context.Background(), indexerA)

	if !errors.Is(valeurObtenue, errUsageStore) || errors.Is(valeurObtenue, ErrBudgetExhausted) || budget.Report()[indexerA].Queries != 0 {
		t.Fatalf("valeurObtenue %v / %+v, valeurAttendue the store error, nothing counted", valeurObtenue, budget.Report()[indexerA])
	}
}

func TestBudget_refusesUnknownIndexer(t *testing.T) {
	budget, _ := newTestBudget(t, realUsage(t), testLimits())

	if err := budget.TakeQuery(context.Background(), "torznab:unknown"); !errors.Is(err, ErrUnknownIndexer) {
		t.Fatalf("valeurObtenue %v, valeurAttendue ErrUnknownIndexer", err)
	}
}

func TestBudget_reportCarriesOnlyCountsAndIDs(t *testing.T) {
	limits := testLimits()
	limits.MinInterval = 0
	budget, _ := newTestBudget(t, realUsage(t), limits)
	_ = budget.TakeQuery(context.Background(), indexerA)
	budget.CountMagnetOnly(indexerA, 3)

	valeurObtenue := budget.Report()

	if expected := (IndexerReport{Queries: 1, MagnetOnly: 3}); valeurObtenue[indexerA] != expected {
		t.Fatalf("valeurObtenue %+v, valeurAttendue %+v", valeurObtenue[indexerA], expected)
	}
	valeurObtenue[indexerA] = IndexerReport{Queries: 99}
	if budget.Report()[indexerA].Queries != 1 {
		t.Fatal("Report must return a copy")
	}
}
