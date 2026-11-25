package sources

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"necromanciarr/internal/libraryprovider"
	"necromanciarr/internal/torznab"
)

// IndexerSource est la part de *torznab.Client que la chaîne utilise.
type IndexerSource interface {
	Caps(ctx context.Context) (torznab.Caps, error)
	Search(ctx context.Context, query torznab.Query) (torznab.Results, error)
	Download(ctx context.Context, item torznab.Item) ([]byte, error)
}

// Chain interroge les sources dans l'ordre de la configuration, fournisseur toujours premier. Elle tient UN SEUL
// client Torznab par indexeur et par processus : le limiteur et le cache des capacités vivent dans le client, deux
// clients pour un même indexeur doubleraient le débit. L'appelant les construit une fois et les passe à NewChain.
type Chain struct {
	provider ProviderSource
	indexers map[ID]IndexerSource
	order    []ID
	limits   map[ID]Limits
	usage    UsageStore
	sealer   *Sealer
	now      func() time.Time
	// board : les Retry-After reçus, communs à tous les budgets du processus.
	board *retryBoard
	// fallbackTrackers : tracker déclaré de chaque indexeur, pour le secours du .torrent et la recherche par groupe.
	fallbackTrackers map[ID]string
	// searchCategories : catégories configurées par indexeur (vide : celles de ses capacités).
	searchCategories map[ID][]int
	// searchStore : cache des réponses, marques de reprise et infohash connus (SetSearchStore) ; nil : aucun.
	searchStore SearchStore
	// sleep attend MinInterval ; remplacé par un faux sommeil dans les tests.
	sleep func(ctx context.Context, duration time.Duration) error
}

// NewChain vérifie l'ordre (fournisseur en tête, une fois, chaque indexeur cité présent avec ses limites) et refuse
// sinon. Les indexeurs absents de order sont ignorés. usage est obligatoire dès qu'un indexeur est cité ; sealer peut
// être nil (pas de clé de scellement) ; now nil vaut time.Now.
func NewChain(provider ProviderSource, indexers map[ID]IndexerSource, order []ID, limits map[ID]Limits,
	usage UsageStore, sealer *Sealer, now func() time.Time,
) (*Chain, error) {
	if provider == nil {
		return nil, errors.New("sources: the chain needs the library provider")
	}
	if len(order) == 0 || order[0] != ProviderID {
		return nil, errors.New("sources: the provider must be the first source of the chain")
	}
	cited, citedLimits := map[ID]IndexerSource{}, map[ID]Limits{}
	for position, identifier := range order[1:] {
		if identifier == ProviderID || slices.Contains(order[1:position+1], identifier) {
			return nil, fmt.Errorf("sources: source %q is listed twice", identifier)
		}
		indexer, known := indexers[identifier]
		if !known || indexer == nil {
			return nil, fmt.Errorf("sources: source %q has no indexer", identifier)
		}
		indexerLimits, hasLimits := limits[identifier]
		if !hasLimits {
			return nil, fmt.Errorf("sources: source %q has no limits", identifier)
		}
		cited[identifier], citedLimits[identifier] = indexer, indexerLimits
	}
	if len(cited) > 0 && usage == nil {
		return nil, errors.New("sources: indexers need a usage store for their daily quotas")
	}
	if now == nil {
		now = time.Now
	}
	return &Chain{
		provider: provider, indexers: cited, order: slices.Clone(order), limits: citedLimits, usage: usage, sealer: sealer, now: now,
		board: newRetryBoard(), sleep: sleepContext,
	}, nil
}

// Order renvoie l'ordre des sources, fournisseur en tête.
func (chain *Chain) Order() []ID { return slices.Clone(chain.order) }

// NewBudget ouvre les décomptes d'une exécution ; les quotas par jour viennent du store, partagés entre exécutions.
func (chain *Chain) NewBudget() *Budget {
	return newBudget(chain.limits, chain.usage, chain.now, chain.sleep, chain.board)
}

// Call est le seul chemin vers un indexeur : il dépense le quota (TakeQuery ou TakeDownload selon kind), exécute la
// requête, puis rend l'erreur à Observe (Retry-After, refus d'accès) et la retient pour Health ; une recherche à la réponse hostile ou illisible
// écarte aussi l'indexeur pour l'exécution. La requête ne s'exécute pas si le budget refuse ;
// l'erreur du budget ou de la requête est rendue telle quelle. budget doit venir de NewBudget.
func (chain *Chain) Call(ctx context.Context, budget *Budget, indexer ID, kind string, request func(ctx context.Context) error) error {
	if budget == nil {
		return errors.New("sources: an indexer call needs a budget")
	}
	var spend func(context.Context, ID) error
	switch kind {
	case KindQuery:
		spend = budget.TakeQuery
	case KindDownload:
		spend = budget.TakeDownload
	default:
		return fmt.Errorf("sources: unknown request kind %q", kind)
	}
	if err := spend(ctx, indexer); err != nil {
		chain.board.recordOutcome(ctx, indexer, err)
		return err
	}
	err := request(ctx)
	chain.board.recordOutcome(ctx, indexer, err)
	budget.Observe(indexer, err)
	if kind == KindQuery {
		budget.blockAfterUnusableAnswer(ctx, indexer, err)
	}
	return err
}

// Search cherche chez le fournisseur.
func (chain *Chain) Search(ctx context.Context, query libraryprovider.Query) ([]libraryprovider.Release, error) {
	return chain.provider.Search(ctx, query)
}

// Nfos renvoie les .nfo du fournisseur : Torznab n'expose pas de .nfo.
func (chain *Chain) Nfos(ctx context.Context, infohash, tracker string) ([]libraryprovider.Nfo, error) {
	return chain.provider.Nfos(ctx, infohash, tracker)
}

// FetchTorrent obtient le .torrent chez le fournisseur ; sur son seul « introuvable », il le cherche chez les
// indexeurs du même tracker (SetFallbackTrackers) avec le budget de l'exécution. Sans indexeur du tracker, sans nom
// de release, ou budget nil, l'erreur du fournisseur revient telle quelle ; sans clé de scellement, alors qu'aucune
// entrée trackers ne couvre le tracker, ErrSecretKeyMissing sans aucune requête ; sinon un échec du secours est une
// *FallbackError.
func (chain *Chain) FetchTorrent(ctx context.Context, release Release, budget *Budget) (Fetched, error) {
	fetched, err := fetchFromProvider(ctx, chain.provider, release)
	if !errors.Is(err, libraryprovider.ErrNotFound) {
		return fetched, err
	}
	indexers, query := chain.fallbackIndexers(release.Tracker), fallbackQueryText(release.Name)
	if len(indexers) == 0 || budget == nil || !fallbackEligible(release, query) {
		return Fetched{}, err
	}
	if chain.sealer == nil && !release.AnnounceConfigured {
		return Fetched{}, ErrSecretKeyMissing
	}
	return chain.fetchFromIndexers(ctx, release, budget, indexers, query)
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
