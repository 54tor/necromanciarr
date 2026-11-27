package app

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/descope/virtualwebauthn"

	"necromanciarr/internal/auth"
	"necromanciarr/internal/store"
)

// accountChangeBeforeSessionStore applique, une fois armé, un changement du compte juste avant l'écriture qui suit la
// preuve (CreateSession, ChangePasswordHash), ou avant la lecture du compte ou du TOTP qui suit le ticket.
type accountChangeBeforeSessionStore struct {
	*store.Store
	armed               atomic.Bool
	accountReadsToSkip  atomic.Int32
	armedBeforeAccount  atomic.Bool
	armedBeforeTOTPRead atomic.Bool
	change              func(ctx context.Context) error
}

func (interleaving *accountChangeBeforeSessionStore) changeIfArmed(ctx context.Context, armed *atomic.Bool) error {
	if armed.CompareAndSwap(true, false) {
		return interleaving.change(ctx)
	}
	return nil
}

func (interleaving *accountChangeBeforeSessionStore) ChangePasswordHash(ctx context.Context, verified store.Account, newHash, keepSessionID string) error {
	if err := interleaving.changeIfArmed(ctx, &interleaving.armed); err != nil {
		return err
	}
	return interleaving.Store.ChangePasswordHash(ctx, verified, newHash, keepSessionID)
}

// Account applique le changement avant la lecture qui suit les accountReadsToSkip premières.
func (interleaving *accountChangeBeforeSessionStore) Account(ctx context.Context) (store.Account, bool, error) {
	if interleaving.armedBeforeAccount.Load() && interleaving.accountReadsToSkip.Add(-1) < 0 {
		if err := interleaving.changeIfArmed(ctx, &interleaving.armedBeforeAccount); err != nil {
			return store.Account{}, false, err
		}
	}
	return interleaving.Store.Account(ctx)
}

func (interleaving *accountChangeBeforeSessionStore) TOTP(ctx context.Context) (store.TOTPFactor, bool, error) {
	if err := interleaving.changeIfArmed(ctx, &interleaving.armedBeforeTOTPRead); err != nil {
		return store.TOTPFactor{}, false, err
	}
	return interleaving.Store.TOTP(ctx)
}

func (interleaving *accountChangeBeforeSessionStore) CreateSession(ctx context.Context, session store.SessionRecord, provenAgainst store.Account) error {
	if interleaving.armed.CompareAndSwap(true, false) {
		if err := interleaving.change(ctx); err != nil {
			return err
		}
	}
	return interleaving.Store.CreateSession(ctx, session, provenAgainst)
}

func newInterleavedAccountFixture(t *testing.T) (*accountFixture, *accountChangeBeforeSessionStore) {
	t.Helper()
	interleaving := &accountChangeBeforeSessionStore{Store: openTestStore(t)}
	fixture := newAccountFixtureOn(t, interleaving)
	fixture.store = interleaving.Store
	return fixture, interleaving
}

// resetAndRecreate efface le compte comme `account reset`, puis crée un nouveau compte comme une nouvelle mise en
// route.
func resetAndRecreate(fixture *accountFixture) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		if err := fixture.store.ResetAccount(ctx); err != nil {
			return err
		}
		hash, err := auth.HashPassword(testNewPassword)
		if err != nil {
			return err
		}
		recreated := store.Account{Username: "bob", PasswordHash: hash, CreatedAt: fixture.clock.now().Add(time.Hour)}
		return fixture.store.CreateAccount(ctx, recreated)
	}
}

// resetAndRecreateKeepingFactors fait comme resetAndRecreate, puis réenregistre sur le nouveau compte les clés et le
// TOTP de l'ancien : seule la génération du compte distingue alors la preuve de l'ancien compte.
func resetAndRecreateKeepingFactors(fixture *accountFixture) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		credentials, err := fixture.store.Credentials(ctx)
		if err != nil {
			return err
		}
		factor, hasTOTP, err := fixture.store.TOTP(ctx)
		if err != nil {
			return err
		}
		if err := resetAndRecreate(fixture)(ctx); err != nil {
			return err
		}
		for _, credential := range credentials {
			if err := fixture.store.AddCredential(ctx, credential); err != nil {
				return err
			}
		}
		if hasTOTP {
			return fixture.store.SetTOTP(ctx, factor)
		}
		return nil
	}
}

func storedSessionCount(t *testing.T, openedStore *store.Store) int {
	t.Helper()
	sessions, err := openedStore.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return len(sessions)
}

func TestLogin_anAccountResetBetweenThePasswordCheckAndTheSessionIssuesNoSession(t *testing.T) {
	fixture, interleaving := newInterleavedAccountFixture(t)
	fixture.setup(t)
	interleaving.change = resetAndRecreate(fixture)
	interleaving.armed.Store(true)

	result, err := fixture.service.Login(context.Background(), testUsername, testPassword, testClient)

	if !errors.Is(err, ErrInvalidCredentials) || result.Session != nil {
		t.Fatalf("valeurAttendue ErrInvalidCredentials sans session, valeurObtenue %+v err=%v", result, err)
	}
	if valeurObtenue := storedSessionCount(t, fixture.store); valeurObtenue != 0 {
		t.Fatalf("valeurAttendue aucune session pour le nouveau compte, valeurObtenue %d", valeurObtenue)
	}
	_, nextErr := fixture.service.Login(context.Background(), "bob", testNewPassword, testClient)
	if !errors.Is(nextErr, ErrThrottled) {
		t.Fatalf("valeurAttendue l'essai compté comme un échec, valeurObtenue %v", nextErr)
	}
}

func TestLogin_aPasswordChangeBetweenTheOldPasswordCheckAndTheSessionIssuesNoSession(t *testing.T) {
	fixture, interleaving := newInterleavedAccountFixture(t)
	kept := fixture.setup(t)
	newHash, err := auth.HashPassword(testNewPassword)
	if err != nil {
		t.Fatal(err)
	}
	interleaving.change = func(ctx context.Context) error {
		account, _, err := fixture.store.Account(ctx)
		if err != nil {
			return err
		}
		return fixture.store.ChangePasswordHash(ctx, account, newHash, auth.SessionIDOf(kept.Token))
	}
	interleaving.armed.Store(true)

	result, err := fixture.service.Login(context.Background(), testUsername, testPassword, testClient)

	if !errors.Is(err, ErrInvalidCredentials) || result.Session != nil {
		t.Fatalf("valeurAttendue ErrInvalidCredentials sans session, valeurObtenue %+v err=%v", result, err)
	}
	if valeurObtenue := storedSessionCount(t, fixture.store); valeurObtenue != 1 {
		t.Fatalf("valeurAttendue la seule session de celui qui a changé le mot de passe, valeurObtenue %d", valeurObtenue)
	}
}

func TestPasskeyLogin_anAccountResetBetweenTheAssertionAndTheSessionIssuesNoSession(t *testing.T) {
	accountFixture, interleaving := newInterleavedAccountFixture(t)
	accountFixture.service.deps.Config = testWebAuthnConfig
	fixture := &webAuthnFixture{accountFixture: accountFixture, grant: accountFixture.setup(t)}
	key := newVirtualKey(virtualwebauthn.AuthenticatorOptions{})
	fixture.register(t, key, credentialKindPasskey)
	interleaving.change = resetAndRecreate(accountFixture)
	interleaving.armed.Store(true)

	grant, err := fixture.passkeyLogin(t, key)

	expectInvalidAssertion(t, err)
	if grant.Token != "" || fixture.sessionCount(t) != 0 {
		t.Fatalf("valeurAttendue aucune session, valeurObtenue %+v et %d sessions", grant, fixture.sessionCount(t))
	}
}

// staleGenerationStore présente les sessions listées comme émises pour une génération antérieure du compte.
type staleGenerationStore struct {
	*store.Store
	staleIDs map[string]bool
}

func (stale *staleGenerationStore) ageGeneration(session store.SessionRecord) store.SessionRecord {
	if stale.staleIDs[session.ID] {
		session.AccountCreatedAt = session.AccountCreatedAt.Add(-time.Second)
	}
	return session
}

func (stale *staleGenerationStore) Session(ctx context.Context, id string) (store.SessionRecord, bool, error) {
	session, found, err := stale.Store.Session(ctx, id)
	return stale.ageGeneration(session), found, err
}

func (stale *staleGenerationStore) Sessions(ctx context.Context) ([]store.SessionRecord, error) {
	sessions, err := stale.Store.Sessions(ctx)
	for sessionIndex, session := range sessions {
		sessions[sessionIndex] = stale.ageGeneration(session)
	}
	return sessions, err
}

func TestResumeSession_rejectsAndDeletesASessionOfAnEarlierAccountGeneration(t *testing.T) {
	healthy := newAccountFixture(t)
	staleToken := healthy.setup(t).Token
	staleStore := &staleGenerationStore{Store: healthy.store, staleIDs: map[string]bool{auth.SessionIDOf(staleToken): true}}
	fixture := newAccountFixtureOn(t, staleStore)
	fixture.store = healthy.store

	_, err := fixture.service.ResumeSession(context.Background(), staleToken)

	if !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("valeurAttendue ErrSessionInvalid, valeurObtenue %v", err)
	}
	if _, found, _ := healthy.store.Session(context.Background(), auth.SessionIDOf(staleToken)); found {
		t.Fatal("valeurAttendue la session périmée supprimée, valeurObtenue encore présente")
	}
}

func TestListSessions_omitsASessionOfAnEarlierAccountGeneration(t *testing.T) {
	healthy := newAccountFixture(t)
	currentToken := healthy.setup(t).Token
	staleToken := healthy.loginTokens(t, 1)[0]
	staleStore := &staleGenerationStore{Store: healthy.store, staleIDs: map[string]bool{auth.SessionIDOf(staleToken): true}}
	fixture := newAccountFixtureOn(t, staleStore)

	views, err := fixture.service.ListSessions(context.Background(), currentToken)

	if err != nil || len(views) != 1 || !views[0].Current {
		t.Fatalf("valeurAttendue la seule session courante, valeurObtenue %+v err=%v", views, err)
	}
}

const legacySessionSchema = `
CREATE TABLE account (id INTEGER PRIMARY KEY CHECK (id = 1), username TEXT NOT NULL, password_hash TEXT NOT NULL,
	created_at INTEGER NOT NULL);
CREATE TABLE sessions (id TEXT PRIMARY KEY, created_at INTEGER NOT NULL, last_seen_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL, address TEXT NOT NULL, user_agent TEXT NOT NULL);`

// createLegacySessionDatabase écrit une base au schéma des sessions sans génération, avec un compte et une session
// encore vivante, et rend le jeton de cette session.
func createLegacySessionDatabase(t *testing.T, databasePath string, now time.Time) string {
	t.Helper()
	token, id, err := auth.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	legacyDatabase, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = legacyDatabase.Close() }()
	if _, err := legacyDatabase.Exec(legacySessionSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := legacyDatabase.Exec(`INSERT INTO account VALUES (1, 'alice', 'legacy-hash', ?)`, now.Add(-time.Hour).UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := legacyDatabase.Exec(`INSERT INTO sessions VALUES (?, ?, ?, ?, '192.0.2.10', 'test-agent')`,
		id, now.UnixNano(), now.UnixNano(), now.Add(auth.SessionAbsoluteLifetime).UnixNano()); err != nil {
		t.Fatal(err)
	}
	return token
}

func TestResumeSession_aSessionFromBeforeTheGenerationColumnDoesNotResume(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "necromanciarr.db")
	clock := newManualClock()
	legacyToken := createLegacySessionDatabase(t, databasePath, clock.now())
	openedStore, err := store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = openedStore.Close() })
	service := New(Deps{Store: openedStore, Now: clock.now})

	_, err = service.ResumeSession(context.Background(), legacyToken)

	if !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("valeurAttendue ErrSessionInvalid, valeurObtenue %v", err)
	}
	if valeurObtenue := storedSessionCount(t, openedStore); valeurObtenue != 0 {
		t.Fatalf("valeurAttendue la session d'avant la mise à jour supprimée, valeurObtenue %d", valeurObtenue)
	}
}

func TestWebAuthnLogin_anAccountResetBetweenTheTicketAndTheAssertionIssuesNoSession(t *testing.T) {
	accountFixture, interleaving := newInterleavedAccountFixture(t)
	accountFixture.service.deps.Config = testWebAuthnConfig
	fixture := &webAuthnFixture{accountFixture: accountFixture, grant: accountFixture.setup(t)}
	key := newVirtualKey(virtualwebauthn.AuthenticatorOptions{})
	fixture.register(t, key, credentialKindSecondFactor)
	ticket := fixture.loginTicket(t)
	ceremony := fixture.beginSecondFactor(t, ticket)
	body := key.assert(t, ceremony.Options, testRelyingParty)
	interleaving.change = resetAndRecreateKeepingFactors(accountFixture)
	interleaving.accountReadsToSkip.Store(1)
	interleaving.armedBeforeAccount.Store(true)

	grant, err := fixture.service.FinishWebAuthnLogin(context.Background(), ticket, ceremony.State, strings.NewReader(body), testClient)

	if interleaving.armedBeforeAccount.Load() {
		t.Fatal("valeurAttendue la remise à zéro faite entre le ticket et l'assertion, valeurObtenue jamais faite")
	}
	expectInvalidAssertion(t, err)
	if grant.Token != "" || fixture.sessionCount(t) != 0 {
		t.Fatalf("valeurAttendue aucune session pour le nouveau compte, valeurObtenue %+v et %d sessions", grant, fixture.sessionCount(t))
	}
}

func TestTOTPLogin_anAccountResetBetweenTheTicketAndTheCodeIssuesNoSession(t *testing.T) {
	accountFixture, interleaving := newInterleavedAccountFixture(t)
	accountFixture.service.deps.Config = testTOTPSettings
	fixture := &totpFixture{accountFixture: accountFixture, grant: accountFixture.setup(t)}
	fixture.enrol(t)
	fixture.clock.advance(stepsAfterEnrolment * totpPeriod)
	ticket := fixture.ticket(t)
	interleaving.change = resetAndRecreateKeepingFactors(accountFixture)
	interleaving.armedBeforeTOTPRead.Store(true)

	grant, err := fixture.service.LoginWithTOTP(context.Background(), ticket, fixture.codeAtOffset(t, 0), testClient)

	if interleaving.armedBeforeTOTPRead.Load() {
		t.Fatal("valeurAttendue la remise à zéro faite entre le ticket et le code, valeurObtenue jamais faite")
	}
	if !errors.Is(err, ErrLoginTicketInvalid) || grant.Token != "" {
		t.Fatalf("valeurAttendue ErrLoginTicketInvalid sans session, valeurObtenue %+v err=%v", grant, err)
	}
	if valeurObtenue := storedSessionCount(t, fixture.store); valeurObtenue != 0 {
		t.Fatalf("valeurAttendue aucune session pour le nouveau compte, valeurObtenue %d", valeurObtenue)
	}
}

func TestChangePassword_aResetBetweenTheVerificationAndTheCommitChangesNothing(t *testing.T) {
	fixture, interleaving := newInterleavedAccountFixture(t)
	current := fixture.setup(t).Token
	interleaving.change = resetAndRecreate(fixture)
	interleaving.armed.Store(true)

	err := fixture.service.ChangePassword(context.Background(), current, testPassword, "a third horse battery")

	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("valeurAttendue ErrInvalidCredentials, valeurObtenue %v", err)
	}
	account, _, readErr := fixture.store.Account(context.Background())
	if matches, _, _ := auth.VerifyPassword(testNewPassword, account.PasswordHash); readErr != nil || account.Username != "bob" || !matches {
		t.Fatalf("valeurAttendue le nouveau compte intact, valeurObtenue %+v (err %v)", account, readErr)
	}
}

func TestChangePassword_aCompetingPasswordChangeBetweenTheVerificationAndTheCommitRevokesNothing(t *testing.T) {
	fixture, interleaving := newInterleavedAccountFixture(t)
	current := fixture.setup(t).Token
	other := fixture.loginTokens(t, 1)[0]
	competingHash, err := auth.HashPassword(testNewPassword)
	if err != nil {
		t.Fatal(err)
	}
	interleaving.change = func(ctx context.Context) error {
		account, _, readErr := fixture.store.Account(ctx)
		if readErr != nil {
			return readErr
		}
		_, replaceErr := fixture.store.ReplacePasswordHash(ctx, account.PasswordHash, competingHash)
		return replaceErr
	}
	interleaving.armed.Store(true)

	changeErr := fixture.service.ChangePassword(context.Background(), current, testPassword, "a third horse battery")

	if !errors.Is(changeErr, ErrInvalidCredentials) {
		t.Fatalf("valeurAttendue ErrInvalidCredentials, valeurObtenue %v", changeErr)
	}
	if _, resumeErr := fixture.service.ResumeSession(context.Background(), other); resumeErr != nil {
		t.Fatalf("valeurAttendue l'autre session gardée, valeurObtenue %v", resumeErr)
	}
	account, _, _ := fixture.store.Account(context.Background())
	if account.PasswordHash != competingHash {
		t.Fatal("valeurAttendue le mot de passe concurrent conservé, valeurObtenue remplacé")
	}
}
