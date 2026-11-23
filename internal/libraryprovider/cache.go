package libraryprovider

import (
	"sync"
	"time"
)

const (
	successTTL   = time.Hour
	notFoundTTL  = 10 * time.Minute
	maxCacheSize = 2000
)

type cacheEntry struct {
	value     any
	err       error
	expiresAt time.Time
	storedAt  time.Time
}

type cache struct {
	mutex   sync.Mutex
	entries map[string]cacheEntry
	now     func() time.Time
}

func newCache() *cache {
	return &cache{entries: map[string]cacheEntry{}, now: time.Now}
}

func (store *cache) get(key string) (cacheEntry, bool) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	entry, found := store.entries[key]
	if !found {
		return cacheEntry{}, false
	}
	if !store.now().Before(entry.expiresAt) {
		delete(store.entries, key)
		return cacheEntry{}, false
	}
	return entry, true
}

func (store *cache) put(key string, value any, err error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	currentTime := store.now()
	timeToLive := successTTL
	if err != nil {
		timeToLive = notFoundTTL
	}
	if _, exists := store.entries[key]; !exists && len(store.entries) >= maxCacheSize {
		store.evict(currentTime)
	}
	store.entries[key] = cacheEntry{value: value, err: err, expiresAt: currentTime.Add(timeToLive), storedAt: currentTime}
}

func (store *cache) evict(currentTime time.Time) {
	for key, entry := range store.entries {
		if !currentTime.Before(entry.expiresAt) {
			delete(store.entries, key)
		}
	}
	if len(store.entries) < maxCacheSize {
		return
	}
	oldestKey := ""
	var oldestTime time.Time
	for key, entry := range store.entries {
		if oldestKey == "" || entry.storedAt.Before(oldestTime) {
			oldestKey, oldestTime = key, entry.storedAt
		}
	}
	delete(store.entries, oldestKey)
}
