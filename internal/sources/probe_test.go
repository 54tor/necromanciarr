package sources

import (
	"context"
	"errors"
	"testing"
	"time"

	"necromanciarr/internal/store"
	"necromanciarr/internal/torznab"
)

func TestTestIndexer_sendsCapsThenOneEmptySearchThroughTheQuota(t *testing.T) {
	setup := newSearchSetup(t)
	indexer := setup.add("torznab:a", movieCaps())
	chain := setup.chain(t)

	err := chain.TestIndexer(t.Context(), "torznab:a")

	used, countErr := setup.usage.CountIndexerUsage(t.Context(), "torznab:a", KindQuery, time.Now().Add(-store.IndexerUsageWindow))
	expectedQuery := torznab.Query{Mode: testSearchMode}
	isExpected := err == nil && countErr == nil && used == 2 && len(indexer.queries) == 1 && indexer.queries[0].Mode == expectedQuery.Mode &&
		indexer.queries[0].Text == ""
	if !isExpected {
		t.Fatalf("valeurObtenue err %v, %d quota rows (%v), queries %+v, valeurAttendue caps and one empty search, 2 rows", err, used, countErr, indexer.queries)
	}
}

func TestTestIndexer_refusesAnIndexerOutsideTheChain(t *testing.T) {
	chain := newSearchSetup(t).chain(t)

	if err := chain.TestIndexer(t.Context(), "torznab:missing"); !errors.Is(err, ErrUnknownIndexer) {
		t.Fatalf("valeurObtenue %v, valeurAttendue ErrUnknownIndexer", err)
	}
}

func TestHealth_keepsTheLastErrorUntilASuccessAndReportsRetryAfter(t *testing.T) {
	setup := newSearchSetup(t)
	indexer := setup.add("torznab:a", movieCaps())
	chain := setup.chain(t)
	indexer.capsErr = torznab.ErrAccessDenied

	_ = chain.TestIndexer(t.Context(), "torznab:a")
	afterFailure := chain.Health("torznab:a")
	indexer.capsErr = nil
	_ = chain.TestIndexer(t.Context(), "torznab:a")
	afterSuccess := chain.Health("torznab:a")
	indexer.capsErr = &torznab.UnavailableError{RetryAfter: time.Minute}
	_ = chain.TestIndexer(t.Context(), "torznab:a")
	afterRetryAfter := chain.Health("torznab:a")

	isExpected := errors.Is(afterFailure.LastError, torznab.ErrAccessDenied) && afterSuccess.LastError == nil &&
		afterRetryAfter.BlockedFor > 0 && errors.Is(afterRetryAfter.LastError, torznab.ErrUnavailable)
	if !isExpected {
		t.Fatalf("valeurObtenue %+v, %+v, %+v, valeurAttendue denied, cleared, then blocked", afterFailure, afterSuccess, afterRetryAfter)
	}
}

func TestHealth_ignoresACancelledRequest(t *testing.T) {
	setup := newSearchSetup(t)
	setup.add("torznab:a", movieCaps())
	chain := setup.chain(t)
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	_ = chain.TestIndexer(cancelled, "torznab:a")

	if health := chain.Health("torznab:a"); health.LastError != nil {
		t.Fatalf("valeurObtenue %+v, valeurAttendue no last error for a cancelled call", health)
	}
}

// cachingIndexer ajoute à searchIndexer un cache de capacités dont l'état est fixé par le test.
type cachingIndexer struct {
	*searchIndexer
	cached bool
}

func (indexer *cachingIndexer) HasCachedCaps() bool { return indexer.cached }

func TestHealth_reportsTheCapsCacheOfTheClient(t *testing.T) {
	setup := newSearchSetup(t)
	setup.add("torznab:a", movieCaps())
	setup.add("torznab:b", movieCaps())
	chain := setup.chain(t)
	chain.indexers["torznab:a"] = &cachingIndexer{searchIndexer: setup.indexers["torznab:a"], cached: true}

	cachedHealth, plainHealth := chain.Health("torznab:a"), chain.Health("torznab:b")

	if !cachedHealth.CapsCached || plainHealth.CapsCached {
		t.Fatalf("valeurObtenue %+v / %+v, valeurAttendue cached for a only", cachedHealth, plainHealth)
	}
}
