package web

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
	"sync"
	"time"
)

// Durées de vie et borne des états gardés côté serveur : l'état d'avant session (lié au navigateur par un cookie) et
// les états d'enregistrement d'une clé ou d'un TOTP (liés à la session).
const (
	preAuthLifetime    = 5 * time.Minute
	ceremonyLifetime   = 10 * time.Minute
	heldStatesCapacity = 1024
	preAuthIDBytes     = 32
)

// Usages d'un état gardé : un état présenté pour un autre usage est refusé.
const (
	purposeLogin        = "login"
	purposePasskey      = "passkey"
	purposeRegistration = "registration"
	purposeTOTPSetup    = "totp_setup"
)

// heldState est un état d'étape : le ticket du second facteur et l'état scellé de la cérémonie rendus par le Service.
type heldState struct {
	purpose string
	ticket  string
	state   []byte
	heldAt  time.Time
}

// heldStates garde des états à usage unique, expirés après lifetime ; plein, il oublie le plus ancien. Sa valeur zéro
// est utilisable (5 min, 1024 entrées, horloge système).
type heldStates struct {
	mutex    sync.Mutex
	entries  map[string]heldState
	lifetime time.Duration
	capacity int
	now      func() time.Time
}

func newHeldStates(lifetime time.Duration, now func() time.Time) *heldStates {
	return &heldStates{lifetime: lifetime, capacity: heldStatesCapacity, now: now}
}

func (states *heldStates) clock() time.Time {
	if states.now == nil {
		return time.Now()
	}
	return states.now()
}

func (states *heldStates) effectiveLifetime() time.Duration {
	if states.lifetime <= 0 {
		return preAuthLifetime
	}
	return states.lifetime
}

func (states *heldStates) effectiveCapacity() int {
	if states.capacity <= 0 {
		return heldStatesCapacity
	}
	return states.capacity
}

func (states *heldStates) expired(entry heldState, now time.Time) bool {
	return now.Sub(entry.heldAt) >= states.effectiveLifetime()
}

// put garde l'état sous key (en remplaçant celui qui y était).
func (states *heldStates) put(key string, entry heldState) {
	states.mutex.Lock()
	defer states.mutex.Unlock()
	if states.entries == nil {
		states.entries = map[string]heldState{}
	}
	now := states.clock()
	states.dropExpired(now)
	if _, replacing := states.entries[key]; !replacing && len(states.entries) >= states.effectiveCapacity() {
		states.dropOldest()
	}
	entry.heldAt = now
	states.entries[key] = entry
}

// take rend l'état de key et l'oublie dans tous les cas : un état ne se présente qu'une fois, même refusé.
func (states *heldStates) take(key, purpose string) (heldState, bool) {
	states.mutex.Lock()
	defer states.mutex.Unlock()
	entry, found := states.entries[key]
	delete(states.entries, key)
	if !found || entry.purpose != purpose || states.expired(entry, states.clock()) {
		return heldState{}, false
	}
	return entry, true
}

// dropPrefix oublie les états dont la clé commence par prefix ; un préfixe vide les oublie tous.
func (states *heldStates) dropPrefix(prefix string) {
	states.mutex.Lock()
	defer states.mutex.Unlock()
	for key := range states.entries {
		if strings.HasPrefix(key, prefix) {
			delete(states.entries, key)
		}
	}
}

func (states *heldStates) count() int {
	states.mutex.Lock()
	defer states.mutex.Unlock()
	return len(states.entries)
}

func (states *heldStates) dropExpired(now time.Time) {
	for key, entry := range states.entries {
		if states.expired(entry, now) {
			delete(states.entries, key)
		}
	}
}

// dropOldest parcourt toute la table : la borne de 1024 entrées rend ce parcours négligeable.
func (states *heldStates) dropOldest() {
	oldestKey, oldestAt, hasOldest := "", time.Time{}, false
	for key, entry := range states.entries {
		if !hasOldest || entry.heldAt.Before(oldestAt) {
			oldestKey, oldestAt, hasOldest = key, entry.heldAt, true
		}
	}
	delete(states.entries, oldestKey)
}

// newPreAuthID tire 256 bits aléatoires ; crypto/rand.Read n'échoue jamais depuis Go 1.24.
func newPreAuthID() string {
	random := make([]byte, preAuthIDBytes)
	_, _ = rand.Read(random)
	return base64.RawURLEncoding.EncodeToString(random)
}

// ceremonyKey range l'état d'une cérémonie sous l'empreinte de la session, une place par usage.
func ceremonyKey(sessionID, purpose string) string { return sessionID + "|" + purpose }

func ceremonyPrefix(sessionID string) string { return sessionID + "|" }
