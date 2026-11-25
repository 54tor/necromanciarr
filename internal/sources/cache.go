package sources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"necromanciarr/internal/store"
	"necromanciarr/internal/torznab"
)

const (
	// successCacheTTL et emptyCacheTTL : durée de vie d'une réponse utile et d'une réponse vide ; une erreur n'est jamais gardée.
	successCacheTTL = 6 * time.Hour
	emptyCacheTTL   = 24 * time.Hour
	// maxCachedItems borne les éléments gardés d'une réponse (au plus 500 reçus) ; les suivants sont ignorés.
	maxCachedItems = 150
)

// SearchStore est la part du store que la recherche par groupe utilise : cache des réponses, marques de reprise
// et infohash déjà connus. *store.Store convient ; sans lui (SetSearchStore jamais appelé) rien n'est gardé.
type SearchStore interface {
	GetIndexerCache(ctx context.Context, indexer, queryKey string, now time.Time) ([]byte, bool, bool, error)
	PutIndexerCache(ctx context.Context, indexer, queryKey, mode string, itemsJSON []byte, empty bool, expiresAt time.Time) error
	MarkGroupSearched(ctx context.Context, groupKey, indexer string, at time.Time) error
	GroupSearchedSince(ctx context.Context, groupKey, indexer string, notBefore time.Time) (bool, error)
	KnownCandidate(ctx context.Context, infohash, tracker string) (bool, error)
}

// SetSearchStore branche le cache, les marques de reprise et le contrôle des infohash connus ; à poser au câblage avant tout usage.
func (chain *Chain) SetSearchStore(searchStore SearchStore) { chain.searchStore = searchStore }

// cachedItem est ce que le cache garde d'un résultat : de quoi appliquer le filtre local et reconnaître un infohash.
// Jamais de lien de téléchargement, de clé, de magnet ni de titre : un lien expire et porte souvent une passkey.
type cachedItem struct {
	GUID     string `json:"g,omitempty"`
	InfoHash string `json:"h,omitempty"`
	Size     int64  `json:"s"`
}

// queryKey est l'empreinte SHA-256 (hexadécimale) de la requête ramenée à une forme unique : indexeur, mode en minuscules, texte en
// minuscules aux espaces réduits, identifiants, catégories triées sans doublon, paramètres triés. La clé d'API n'entre
// jamais (Query n'en porte pas) ; l'indexeur y entre, une réponse ne passe donc pas d'un indexeur à l'autre.
func queryKey(indexer ID, query torznab.Query) string {
	parameters := url.Values{"t": {strings.ToLower(strings.TrimSpace(query.Mode))}}
	if text := strings.Join(strings.Fields(strings.ToLower(query.Text)), " "); text != "" {
		parameters.Set("q", text)
	}
	if imdb := strings.ToLower(strings.TrimSpace(query.IMDBID)); imdb != "" {
		parameters.Set("imdbid", imdb)
	}
	for name, value := range map[string]int{"tmdbid": query.TMDBID, "tvdbid": query.TVDBID, "season": query.Season, "ep": query.Episode} {
		if value > 0 {
			parameters.Set(name, strconv.Itoa(value))
		}
	}
	categories := slices.Clone(query.Categories)
	slices.Sort(categories)
	var names []string
	for _, category := range slices.Compact(categories) {
		names = append(names, strconv.Itoa(category))
	}
	if len(names) > 0 {
		parameters.Set("cat", strings.Join(names, ","))
	}
	digest := sha256.Sum256([]byte(string(indexer) + "\n" + parameters.Encode()))
	return hex.EncodeToString(digest[:])
}

// encodeCachedItems garde au plus maxCachedItems éléments, et retire les derniers tant que le JSON dépasse la borne de
// la base : la réponse d'un indexeur hostile est tronquée, jamais refusée.
func encodeCachedItems(items []torznab.Item) []byte {
	kept := make([]cachedItem, 0, min(len(items), maxCachedItems))
	for _, item := range items[:min(len(items), maxCachedItems)] {
		kept = append(kept, cachedItem{GUID: item.GUID, InfoHash: item.InfoHash, Size: item.Size})
	}
	for {
		encoded, err := json.Marshal(kept)
		if err == nil && len(encoded) <= store.IndexerCacheMaxItemsBytes {
			return encoded
		}
		if len(kept) == 0 {
			return []byte("[]")
		}
		kept = kept[:len(kept)/2]
	}
}

// decodeCachedItems relit les éléments : sans lien, ils ne peuvent plus être téléchargés.
func decodeCachedItems(raw []byte) ([]torznab.Item, error) {
	var kept []cachedItem
	if err := json.Unmarshal(raw, &kept); err != nil {
		return nil, err
	}
	items := make([]torznab.Item, 0, len(kept))
	for _, item := range kept {
		items = append(items, torznab.Item{GUID: item.GUID, InfoHash: item.InfoHash, Size: item.Size})
	}
	return items, nil
}

// cachedAnswer rend les éléments en cache de la requête ; une lecture en échec ou illisible vaut une absence.
func (search *candidateSearch) cachedAnswer(ctx context.Context, indexer ID, key string) ([]torznab.Item, bool) {
	if search.chain.searchStore == nil {
		return nil, false
	}
	raw, found, _, err := search.chain.searchStore.GetIndexerCache(ctx, string(indexer), key, search.chain.now())
	if err != nil || !found {
		return nil, false
	}
	items, err := decodeCachedItems(raw)
	return items, err == nil
}

// rememberAnswer garde une réponse réussie (6 h, 24 h si vide) ; le cache est un confort : un échec d'écriture ne
// fait pas échouer la recherche.
func (search *candidateSearch) rememberAnswer(ctx context.Context, indexer ID, key, mode string, items []torznab.Item) {
	if search.chain.searchStore == nil {
		return
	}
	ttl := successCacheTTL
	if len(items) == 0 {
		ttl = emptyCacheTTL
	}
	_ = search.chain.searchStore.PutIndexerCache(ctx, string(indexer), key, mode, encodeCachedItems(items), len(items) == 0, search.chain.now().Add(ttl))
}

// knownInfohash dit si l'infohash annoncé est déjà un candidat de ce tracker : son .torrent n'est jamais retéléchargé
// (sauf s'il est stale, repris ailleurs). Un résultat sans infohash annoncé ne peut pas être reconnu avant le téléchargement.
func (search *candidateSearch) knownInfohash(ctx context.Context, indexer ID, infohash string) bool {
	if search.chain.searchStore == nil || infohash == "" {
		return false
	}
	known, err := search.chain.searchStore.KnownCandidate(ctx, infohash, search.chain.searchTracker(indexer))
	return err == nil && known
}

// needsFreshLink dit si une réponse en cache contient un résultat à télécharger : filtre local passé, pas encore
// essayé pour ce groupe, infohash inconnu. Le cache n'a pas de lien : il faut alors une nouvelle recherche.
func (search *candidateSearch) needsFreshLink(ctx context.Context, indexer ID, items []torznab.Item) bool {
	if search.downloads >= maxGroupDownloads {
		return false
	}
	for _, item := range eligibleItems(items, search.media) {
		if search.tried[string(indexer)+"\x00"+item.GUID] && item.GUID != "" {
			continue
		}
		if !search.knownInfohash(ctx, indexer, item.InfoHash) {
			return true
		}
	}
	return false
}

// alreadySearched dit si ce groupe a été cherché jusqu'au bout chez l'indexeur depuis moins de store.IndexerSearchedTTL.
func (search *candidateSearch) alreadySearched(ctx context.Context, indexer ID) bool {
	if search.chain.searchStore == nil || search.media.GroupKey == "" {
		return false
	}
	searched, err := search.chain.searchStore.GroupSearchedSince(ctx, search.media.GroupKey, string(indexer), search.chain.now().Add(-store.IndexerSearchedTTL))
	return err == nil && searched
}

func (search *candidateSearch) markSearched(ctx context.Context, indexer ID) {
	if search.chain.searchStore == nil || search.media.GroupKey == "" {
		return
	}
	_ = search.chain.searchStore.MarkGroupSearched(ctx, search.media.GroupKey, string(indexer), search.chain.now())
}
