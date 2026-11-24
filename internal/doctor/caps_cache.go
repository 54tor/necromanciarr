package doctor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"necromanciarr/internal/config"
	"necromanciarr/internal/torznab"
)

// DefaultCapsCacheTTL est la durée pendant laquelle le résultat des capacités d'un indexeur, réussi ou non, est
// réutilisé : relancer le diagnostic en boucle ne doit pas marteler l'indexeur ni son quota.
const DefaultCapsCacheTTL = 5 * time.Minute

type capsOutcome struct {
	caps      torznab.Caps
	err       error
	expiresAt time.Time
}

// CapsCache garde en mémoire, par indexeur, le dernier résultat de Caps. La clé dérive du nom, de l'URL et des délais :
// toute modification de la configuration d'un indexeur l'invalide. Sûr pour un usage concurrent.
type CapsCache struct {
	mutex   sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	entries map[string]capsOutcome
}

// NewCapsCache crée un cache ; now nil vaut time.Now.
func NewCapsCache(ttl time.Duration, now func() time.Time) *CapsCache {
	if now == nil {
		now = time.Now
	}
	return &CapsCache{ttl: ttl, now: now, entries: map[string]capsOutcome{}}
}

func capsCacheKey(indexer config.TorznabIndexer) string {
	digest := sha256.Sum256([]byte(indexer.Name + "\x00" + indexer.URL + "\x00" + indexer.MinInterval.String() + "\x00" + indexer.Timeout.String()))
	return hex.EncodeToString(digest[:])
}

// caps renvoie le résultat gardé, sinon interroge le lecteur et le garde. Un échec dû à l'annulation de l'appelant n'est
// pas gardé : il ne dit rien de l'indexeur. Un cache nil interroge toujours.
func (cache *CapsCache) caps(ctx context.Context, indexer config.TorznabIndexer, reader CapsReader) (torznab.Caps, error) {
	if cache == nil {
		return reader.Caps(ctx)
	}
	key := capsCacheKey(indexer)
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	if entry, found := cache.entries[key]; found && cache.now().Before(entry.expiresAt) {
		return entry.caps, entry.err
	}
	caps, err := reader.Caps(ctx)
	if ctx.Err() == nil {
		cache.entries[key] = capsOutcome{caps: caps, err: err, expiresAt: cache.now().Add(cache.ttl)}
	}
	return caps, err
}
