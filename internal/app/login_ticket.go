package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"time"

	"necromanciarr/internal/auth"
	"necromanciarr/internal/store"
)

const (
	loginTicketLifetime = webAuthnCeremonyTimeout
	maxExpiringEntries  = 1024
	burnedNonceMargin   = time.Minute
)

var (
	// ErrLoginTicketInvalid : ticket du second facteur absent, expiré, déjà utilisé ou d'une autre version du compte ;
	// il faut repasser par le mot de passe.
	ErrLoginTicketInvalid  = errors.New("app: sign-in step expired, enter the password again")
	errTooManyPendingSteps = errors.New("app: too many pending sign-in steps, retry later")
)

type expiringEntry[Value any] struct {
	value     Value
	expiresAt time.Time
}

// expiringEntries est un ensemble borné à durée de vie, sûr sous accès concurrents et à sa valeur zéro.
type expiringEntries[Value any] struct {
	mutex   sync.Mutex
	entries map[string]expiringEntry[Value]
}

// insertIfAbsent ajoute la clé si elle est absente ou expirée ; faux pour un doublon vivant ou un ensemble plein.
func (set *expiringEntries[Value]) insertIfAbsent(key string, value Value, expiresAt, now time.Time) bool {
	set.mutex.Lock()
	defer set.mutex.Unlock()
	if set.entries == nil {
		set.entries = map[string]expiringEntry[Value]{}
	}
	if existing, found := set.entries[key]; found && now.Before(existing.expiresAt) {
		return false
	}
	if len(set.entries) >= maxExpiringEntries {
		set.purgeExpired(now)
	}
	if len(set.entries) >= maxExpiringEntries {
		return false
	}
	set.entries[key] = expiringEntry[Value]{value: value, expiresAt: expiresAt}
	return true
}

func (set *expiringEntries[Value]) purgeExpired(now time.Time) {
	for key, entry := range set.entries {
		if !now.Before(entry.expiresAt) {
			delete(set.entries, key)
		}
	}
}

func (set *expiringEntries[Value]) peek(key string, now time.Time) (Value, bool) {
	set.mutex.Lock()
	defer set.mutex.Unlock()
	entry, found := set.entries[key]
	return entry.value, found && now.Before(entry.expiresAt)
}

// take retire la clé et rend sa valeur si elle était vivante.
func (set *expiringEntries[Value]) take(key string, now time.Time) (Value, bool) {
	set.mutex.Lock()
	defer set.mutex.Unlock()
	entry, found := set.entries[key]
	delete(set.entries, key)
	return entry.value, found && now.Before(entry.expiresAt)
}

// loginTicket lie l'étape du second facteur à la version du compte dont le mot de passe a été vérifié.
type loginTicket struct {
	accountHandle, passwordFingerprint []byte
}

func passwordFingerprint(account store.Account) []byte {
	digest := sha256.Sum256([]byte(account.PasswordHash))
	return digest[:]
}

func (svc *service) issueLoginTicket(ctx context.Context) (string, error) {
	account, found, err := svc.deps.Store.Account(ctx)
	if err != nil {
		return "", err
	}
	if !found {
		return "", ErrInvalidCredentials
	}
	ticket, ticketHash, err := auth.NewSessionToken()
	if err != nil {
		return "", err
	}
	now := svc.now()
	entry := loginTicket{accountHandle: accountUserHandle(account), passwordFingerprint: passwordFingerprint(account)}
	if !svc.loginTickets.insertIfAbsent(ticketHash, entry, now.Add(loginTicketLifetime), now) {
		return "", errTooManyPendingSteps
	}
	return ticket, nil
}

// peekLoginTicket vérifie le ticket sans le consommer (début d'une étape second facteur).
func (svc *service) peekLoginTicket(ctx context.Context, ticket string) (store.Account, error) {
	entry, live := svc.loginTickets.peek(auth.SessionIDOf(ticket), svc.now())
	return svc.accountOfTicket(ctx, entry, live)
}

// consumeLoginTicket brûle le ticket, quelle que soit l'issue de la preuve qui suit ; l'appelant le fait sous le
// limiteur.
func (svc *service) consumeLoginTicket(ctx context.Context, ticket string) (store.Account, error) {
	entry, live := svc.loginTickets.take(auth.SessionIDOf(ticket), svc.now())
	return svc.accountOfTicket(ctx, entry, live)
}

// accountOfTicket relit le compte : une remise à zéro, un nouveau compte ou un nouveau mot de passe invalident le ticket.
func (svc *service) accountOfTicket(ctx context.Context, entry loginTicket, live bool) (store.Account, error) {
	if !live {
		return store.Account{}, ErrLoginTicketInvalid
	}
	account, found, err := svc.deps.Store.Account(ctx)
	if err != nil {
		return store.Account{}, err
	}
	sameGeneration := found && bytes.Equal(entry.accountHandle, accountUserHandle(account)) &&
		bytes.Equal(entry.passwordFingerprint, passwordFingerprint(account))
	if !sameGeneration {
		return store.Account{}, ErrLoginTicketInvalid
	}
	return account, nil
}
