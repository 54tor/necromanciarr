package sources

import (
	"context"
	"fmt"
	"time"

	"necromanciarr/internal/torznab"
)

// testSearchMode est la recherche vide de TestIndexer : sans texte, elle ne vise aucun média.
const testSearchMode = "search"

// Health est l'état d'un indexeur vu par ce processus : BlockedFor reste d'un Retry-After reçu (0 : aucun),
// LastError l'erreur de sa dernière requête (nil : réussie ou aucune), CapsCached dit si des capacités valables sont
// dans le cache du client. Rien n'est persisté : un nouveau processus repart d'un état vierge.
type Health struct {
	BlockedFor time.Duration
	LastError  error
	CapsCached bool
}

// capsCacheProbe est la part optionnelle d'un client qui dit, sans attendre, si ses capacités sont en cache.
type capsCacheProbe interface {
	HasCachedCaps() bool
}

// Health rend l'état de l'indexeur dans ce processus.
func (chain *Chain) Health(indexer ID) Health {
	now := chain.now()
	health := Health{BlockedFor: max(chain.board.remaining(indexer, now), 0), LastError: chain.board.lastErrorOf(indexer)}
	if probe, hasProbe := chain.indexers[indexer].(capsCacheProbe); hasProbe {
		health.CapsCached = probe.HasCachedCaps()
	}
	return health
}

// TestIndexer envoie à l'indexeur t=caps puis une recherche vide, chacune par Call avec un budget neuf : elles comptent
// au quota du jour et respectent blocages et plafonds. Des capacités déjà en cache dans le client ne coûtent rien,
// comme pendant Match. L'erreur rendue est celle du budget ou de la requête.
func (chain *Chain) TestIndexer(ctx context.Context, indexer ID) error {
	client, known := chain.indexers[indexer]
	if !known {
		return fmt.Errorf("%w: %s", ErrUnknownIndexer, indexer)
	}
	budget := chain.NewBudget()
	if cache, hasCache := client.(capsCache); hasCache {
		if _, cached := cache.CachedCaps(); cached {
			return chain.testSearch(ctx, budget, indexer, client)
		}
	}
	err := chain.Call(ctx, budget, indexer, KindQuery, func(requestCtx context.Context) error {
		_, capsErr := client.Caps(requestCtx)
		return capsErr
	})
	if err != nil {
		return err
	}
	return chain.testSearch(ctx, budget, indexer, client)
}

func (chain *Chain) testSearch(ctx context.Context, budget *Budget, indexer ID, client IndexerSource) error {
	return chain.Call(ctx, budget, indexer, KindQuery, func(requestCtx context.Context) error {
		_, searchErr := client.Search(requestCtx, torznab.Query{Mode: testSearchMode})
		return searchErr
	})
}
