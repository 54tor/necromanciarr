package sources

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"

	"necromanciarr/internal/store"
	"necromanciarr/internal/torrentcheck"
	"necromanciarr/internal/torznab"
)

// ErrNoExactSize : aucun fichier du .torrent n'a exactement la taille d'un fichier local du groupe ; le .torrent est
// écarté (raison torznab_no_exact_size), jamais enregistré.
var ErrNoExactSize = errors.New("sources: no file of the torrent has the exact size of a local file")

// SearchError porte ce que la recherche par groupe a refusé : éléments écartés (Skipped) et indexeurs laissés en route
// (Interrupted). Elle accompagne les .torrent retenus, sans URL, clé ni contenu.
type SearchError struct {
	Skipped, Interrupted []error
}

func (*SearchError) Error() string {
	return "sources: some indexer items or indexers were refused during the candidate search"
}

// SetSearchCategories déclare les catégories configurées de chaque indexeur (config torznab[].categories), à poser au
// câblage avant tout usage.
func (chain *Chain) SetSearchCategories(categories map[ID][]int) {
	chain.searchCategories = maps.Clone(categories)
	for indexer, indexerCategories := range chain.searchCategories {
		chain.searchCategories[indexer] = slices.Clone(indexerCategories)
	}
}

// SearchCandidates cherche chez les indexeurs, dans l'ordre de la chaîne, des .torrent pour un groupe de fichiers
// locaux : chez chaque indexeur, la recherche par identifiant (si ses capacités l'annoncent) puis par texte, arrêt au
// premier .torrent retenu ; l'indexeur suivant n'est sollicité que tant qu'une taille de Uncovered reste sans
// .torrent. Un résultat n'est téléchargé que si sa taille annoncée passe le filtre local, et n'est retenu que si un de
// ses fichiers a exactement la taille d'un fichier local. Chaque requête passe par Call. Sans clé de scellement,
// ErrSecretKeyMissing sans aucune requête ; les refus reviennent dans une *SearchError, avec les .torrent retenus.
func (chain *Chain) SearchCandidates(ctx context.Context, media MediaQuery, budget *Budget) ([]Found, Report, error) {
	if len(chain.order) < 2 || budget == nil {
		return nil, budget.Report(), nil
	}
	if chain.sealer == nil {
		return nil, budget.Report(), ErrSecretKeyMissing
	}
	search := &candidateSearch{chain: chain, budget: budget, media: media, uncovered: sizeSet(media.Uncovered), tried: map[string]bool{}}
	for _, indexer := range chain.order[1:] {
		if len(search.uncovered) == 0 || search.downloads >= maxGroupDownloads {
			break
		}
		if budget.exhausted(indexer) {
			// Déjà compté (indexer_budget_exhausted) au premier groupe refusé : les suivants passent sans bruit.
			continue
		}
		if search.alreadySearched(ctx, indexer) {
			continue
		}
		interruptedBefore := len(search.outcome.Interrupted)
		search.indexer(ctx, indexer)
		if err := ctx.Err(); err != nil {
			return search.found, budget.Report(), err
		}
		if len(search.outcome.Interrupted) == interruptedBefore {
			search.markSearched(ctx, indexer)
		}
	}
	return search.found, budget.Report(), search.refusals()
}

// candidateSearch est la recherche d'un groupe : ce qui reste à couvrir et ce qui a été retenu ou refusé.
type candidateSearch struct {
	chain     *Chain
	budget    *Budget
	media     MediaQuery
	uncovered map[int64]bool
	// tried : indexeur et GUID des éléments déjà téléchargés pour ce groupe ; la recherche texte rend souvent les mêmes que celle par
	// identifiant, les retélécharger brûlerait le quota.
	tried     map[string]bool
	downloads int
	found     []Found
	outcome   SearchError
}

func (search *candidateSearch) refusals() error {
	if len(search.outcome.Skipped) == 0 && len(search.outcome.Interrupted) == 0 {
		return nil
	}
	return &search.outcome
}

func (search *candidateSearch) indexer(ctx context.Context, indexer ID) {
	caps, err := search.caps(ctx, indexer)
	if err != nil {
		search.outcome.Interrupted = append(search.outcome.Interrupted, err)
		return
	}
	categories, searchable := searchCategories(caps.Categories, search.chain.searchCategories[indexer], search.media.Series)
	if !searchable {
		return
	}
	for _, query := range planQueries(caps, search.media, categories) {
		useful, stop := search.query(ctx, indexer, query)
		if useful || stop {
			return
		}
	}
}

// capsCache est la part optionnelle d'un client qui dit si ses capacités sont en cache (*torznab.Client).
type capsCache interface {
	CachedCaps() (torznab.Caps, bool)
}

// caps lit les capacités une fois par exécution : une vraie requête t=caps est décomptée comme une recherche, une
// réponse du cache du client ne coûte rien (ni quota ni MinInterval).
func (search *candidateSearch) caps(ctx context.Context, indexer ID) (torznab.Caps, error) {
	if caps, known := search.budget.knownCaps(indexer); known {
		return caps, nil
	}
	client := search.chain.indexers[indexer]
	if cache, hasCache := client.(capsCache); hasCache {
		if caps, cached := cache.CachedCaps(); cached {
			search.budget.rememberCaps(indexer, caps)
			return caps, nil
		}
	}
	var caps torznab.Caps
	err := search.chain.Call(ctx, search.budget, indexer, KindQuery, func(requestCtx context.Context) error {
		var capsErr error
		caps, capsErr = client.Caps(requestCtx)
		return capsErr
	})
	if err != nil {
		return torznab.Caps{}, err
	}
	search.budget.rememberCaps(indexer, caps)
	return caps, nil
}

// query lance une recherche et télécharge ses résultats un à un ; useful : un .torrent est retenu, stop : l'indexeur
// est à laisser pour ce groupe (recherche en échec, budget, indisponibilité, refus).
func (search *candidateSearch) query(ctx context.Context, indexer ID, query torznab.Query) (useful, stop bool) {
	key := queryKey(indexer, query)
	if cached, hit := search.cachedAnswer(ctx, indexer, key); hit && !search.needsFreshLink(ctx, indexer, cached) {
		search.budget.CountCacheHit(indexer)
		return false, false
	}
	client := search.chain.indexers[indexer]
	var results torznab.Results
	err := search.chain.Call(ctx, search.budget, indexer, KindQuery, func(requestCtx context.Context) error {
		var searchErr error
		results, searchErr = client.Search(requestCtx, query)
		search.budget.CountMagnetOnly(indexer, results.MagnetOnly)
		return searchErr
	})
	if err != nil {
		search.outcome.Interrupted = append(search.outcome.Interrupted, err)
		return false, true
	}
	search.rememberAnswer(ctx, indexer, key, query.Mode, results.Items)
	for _, item := range eligibleItems(results.Items, search.media) {
		if search.downloads >= maxGroupDownloads {
			return false, true
		}
		triedKey := string(indexer) + "\x00" + item.GUID
		if search.tried[triedKey] {
			continue
		}
		if item.GUID != "" {
			search.tried[triedKey] = true
		}
		if search.knownInfohash(ctx, indexer, item.InfoHash) {
			continue
		}
		search.downloads++
		kept, err := search.download(ctx, indexer, item)
		if err == nil {
			search.accept(kept)
			return true, false
		}
		if stopsTheIndexer(err) {
			search.outcome.Interrupted = append(search.outcome.Interrupted, err)
			return false, true
		}
		search.outcome.Skipped = append(search.outcome.Skipped, err)
	}
	return false, false
}

// keptTorrent est un .torrent retenu avec les tailles de ses fichiers, pour savoir ce qu'il couvre.
type keptTorrent struct {
	found     Found
	fileSizes []int64
}

func (search *candidateSearch) accept(kept keptTorrent) {
	search.found = append(search.found, kept.found)
	for _, size := range kept.fileSizes {
		delete(search.uncovered, size)
	}
}

// download télécharge l'élément par Call (quota et MinInterval comme une recherche) ; le tampon en clair est effacé
// avant de rendre la main (au mieux : Go a pu en copier ailleurs).
func (search *candidateSearch) download(ctx context.Context, indexer ID, item torznab.Item) (keptTorrent, error) {
	client := search.chain.indexers[indexer]
	var raw []byte
	err := search.chain.Call(ctx, search.budget, indexer, KindDownload, func(requestCtx context.Context) error {
		var downloadErr error
		raw, downloadErr = client.Download(requestCtx, item)
		return downloadErr
	})
	defer clear(raw)
	if err != nil {
		return keptTorrent{}, err
	}
	return search.keep(indexer, item, raw)
}

// keep prouve la taille (un fichier de la taille exacte d'un fichier local encore sans candidat : un .torrent qui ne
// couvre qu'un fichier déjà couvert n'est pas utile) avant tout le reste, assainit, contrôle
// que l'assainissement n'a pas changé l'infohash calculé, puis scelle avec la liaison des valeurs qui seront
// enregistrées sur la ligne du candidat (infohash calculé, tracker de l'indexeur, source) ; quand une entrée trackers
// couvre ce tracker, rien n'est scellé.
func (search *candidateSearch) keep(indexer ID, item torznab.Item, raw []byte) (keptTorrent, error) {
	if len(raw) > store.MaxTorrentBytes {
		return keptTorrent{}, ErrTorrentTooLarge
	}
	parsed, err := parseIndexerTorrent(raw)
	if err != nil {
		return keptTorrent{}, err
	}
	fileSizes := torrentFileSizes(parsed)
	if !hasLocalSize(fileSizes, slices.Collect(maps.Keys(search.uncovered))) {
		return keptTorrent{}, ErrNoExactSize
	}
	sanitized, err := Sanitize(raw)
	if err != nil {
		return keptTorrent{}, err
	}
	if err := requireInfohash(sanitized, parsed.InfoHash); err != nil {
		return keptTorrent{}, err
	}
	release := Release{Infohash: parsed.InfoHash, Tracker: search.chain.searchTracker(indexer), Name: cmp.Or(fallbackQueryText(item.Title), parsed.Root)}
	found := Found{Source: indexer, Release: release, TotalSize: sumSizes(fileSizes), Fetched: Fetched{Source: indexer, Sanitized: sanitized}}
	if search.media.AnnounceConfigured != nil && search.media.AnnounceConfigured(release.Tracker) {
		return keptTorrent{found: found, fileSizes: fileSizes}, nil
	}
	sealed, err := search.chain.sealer.Seal(raw, Binding{Infohash: release.Infohash, Tracker: release.Tracker, Source: indexer})
	if err != nil {
		return keptTorrent{}, err
	}
	found.Fetched.Sealed = sealed
	return keptTorrent{found: found, fileSizes: fileSizes}, nil
}

// searchTracker est le tracker d'un candidat trouvé chez cet indexeur : celui déclaré tel qu'écrit, sinon
// « torznab:<nom> » (spec 4.B.9).
func (chain *Chain) searchTracker(indexer ID) string {
	return cmp.Or(chain.fallbackTrackers[indexer], string(indexer))
}

// parseIndexerTorrent calcule l'infohash v1 (un hybride compte par sa partie v1) ; le v2 seul est nommé d'abord,
// l'erreur ne recopie rien du .torrent.
func parseIndexerTorrent(raw []byte) (*torrentcheck.Torrent, error) {
	parsed, err := torrentcheck.Parse(raw)
	if errors.Is(err, torrentcheck.ErrV2Only) {
		return nil, torrentcheck.ErrV2Only
	}
	if err != nil {
		return nil, ErrInvalidTorrent
	}
	return parsed, nil
}

func torrentFileSizes(parsed *torrentcheck.Torrent) []int64 {
	sizes := make([]int64, 0, len(parsed.Files))
	for _, file := range parsed.Files {
		sizes = append(sizes, file.Size)
	}
	return sizes
}

func hasLocalSize(fileSizes, localSizes []int64) bool {
	return slices.ContainsFunc(fileSizes, func(size int64) bool { return size > 0 && slices.Contains(localSizes, size) })
}

func sumSizes(sizes []int64) int64 {
	var total int64
	for _, size := range sizes {
		total += size
	}
	return total
}

func sizeSet(sizes []int64) map[int64]bool {
	set := map[int64]bool{}
	for _, size := range sizes {
		set[size] = true
	}
	return set
}
