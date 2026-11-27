package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"necromanciarr/internal/auth"
	"necromanciarr/internal/config"
	"necromanciarr/internal/store"
)

// Valeurs de test à faible entropie (gitleaks).
const (
	testUsername    = "alice"
	testPassword    = "correct horse battery"
	testNewPassword = "another horse battery"
	testWrong       = "wrong horse battery!"
)

var testClient = ClientInfo{Address: "192.0.2.10", UserAgent: "test-agent"}

// manualClock est une horloge de test sûre sous accès concurrents.
type manualClock struct {
	mutex   sync.Mutex
	current time.Time
}

func newManualClock() *manualClock {
	return &manualClock{current: time.Unix(1_800_000_000, 0).UTC()}
}

func (clock *manualClock) now() time.Time {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	return clock.current
}

func (clock *manualClock) advance(duration time.Duration) {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	clock.current = clock.current.Add(duration)
}

type accountFixture struct {
	store        *store.Store
	clock        *manualClock
	service      *service
	dummyCalls   atomic.Int32
	argonRunning atomic.Int32
	argonPeak    atomic.Int32
	argonRuns    atomic.Int32
}

func newAccountFixture(t *testing.T) *accountFixture {
	t.Helper()
	return newAccountFixtureOn(t, openTestStore(t))
}

func newAccountFixtureOn(t *testing.T, accountStore Store) *accountFixture {
	t.Helper()
	fixture := &accountFixture{clock: newManualClock()}
	if openedStore, isStore := accountStore.(*store.Store); isStore {
		fixture.store = openedStore
	}
	deps := Deps{Store: accountStore, Now: fixture.clock.now, Auth: AuthDeps{VerifyDummy: func(password string) {
		fixture.dummyCalls.Add(1)
		auth.VerifyDummy(password)
	}}}
	fixture.service = New(deps).(*service)
	fixture.service.argonObserver = fixture.observeArgon
	return fixture
}

func (fixture *accountFixture) observeArgon(delta int32) {
	running := fixture.argonRunning.Add(delta)
	if delta > 0 {
		fixture.argonRuns.Add(1)
	}
	for {
		peak := fixture.argonPeak.Load()
		if running <= peak || fixture.argonPeak.CompareAndSwap(peak, running) {
			return
		}
	}
}

func (fixture *accountFixture) setup(t *testing.T) SessionGrant {
	t.Helper()
	grant, err := fixture.service.SetupAccount(context.Background(), testUsername, testPassword, testClient)
	if err != nil {
		t.Fatal(err)
	}
	return grant
}

// passThrottleDelay avance l'horloge au-delà du plus long délai du limiteur.
func (fixture *accountFixture) passThrottleDelay() {
	fixture.clock.advance(auth.ThrottleMaxDelay)
}

func TestSetupAccount_createsTheAccountAndAFreshSession(t *testing.T) {
	fixture := newAccountFixture(t)
	ctx := context.Background()

	grant, err := fixture.service.SetupAccount(ctx, "  "+testUsername+" ", testPassword, testClient)
	if err != nil {
		t.Fatal(err)
	}

	account, found, err := fixture.store.Account(ctx)
	if err != nil || !found || account.Username != testUsername || strings.Contains(account.PasswordHash, testPassword) {
		t.Fatalf("valeurObtenue compte %+v found=%v err=%v", account, found, err)
	}
	valeurAttendue := fixture.clock.now().Add(auth.SessionAbsoluteLifetime)
	if grant.Token == "" || !grant.ExpiresAt.Equal(valeurAttendue) {
		t.Fatalf("valeurAttendue expiration %v, valeurObtenue %+v", valeurAttendue, grant)
	}
	session, found, err := fixture.store.Session(ctx, auth.SessionIDOf(grant.Token))
	if err != nil || !found || session.ID == grant.Token || session.Address != testClient.Address {
		t.Fatalf("valeurObtenue session %+v found=%v err=%v", session, found, err)
	}
	view, err := fixture.service.ResumeSession(ctx, grant.Token)
	if err != nil || view.Username != testUsername || !view.Current {
		t.Fatalf("valeurObtenue %+v err=%v", view, err)
	}
}

func TestSetupAccount_secondCallReturnsErrAccountExists(t *testing.T) {
	fixture := newAccountFixture(t)
	fixture.setup(t)

	_, valeurObtenue := fixture.service.SetupAccount(context.Background(), "bob", testNewPassword, testClient)

	if !errors.Is(valeurObtenue, ErrAccountExists) || !errors.Is(valeurObtenue, store.ErrAccountExists) {
		t.Fatalf("valeurAttendue ErrAccountExists, valeurObtenue %v", valeurObtenue)
	}
}

func TestSetupAccount_refusesAShortPassword(t *testing.T) {
	fixture := newAccountFixture(t)

	_, valeurObtenue := fixture.service.SetupAccount(context.Background(), testUsername, "too short", testClient)

	if !errors.Is(valeurObtenue, auth.ErrPasswordTooShort) {
		t.Fatalf("valeurAttendue ErrPasswordTooShort, valeurObtenue %v", valeurObtenue)
	}
	if status, _ := fixture.service.AuthStatus(context.Background()); status.AccountExists {
		t.Fatal("un compte a été créé malgré le refus")
	}
}

func TestSetupAccount_refusesAnInvalidUsername(t *testing.T) {
	invalidUsernames := map[string]string{
		"empty":             "",
		"only spaces":       "   ",
		"sixty-five runes":  strings.Repeat("é", maxLabelRunes+1),
		"control character": "ali\x07ce",
		"invalid utf-8":     "ali\xffce",
	}
	for name, username := range invalidUsernames {
		t.Run(name, func(t *testing.T) {
			fixture := newAccountFixture(t)
			_, valeurObtenue := fixture.service.SetupAccount(context.Background(), username, testPassword, testClient)
			if !errors.Is(valeurObtenue, ErrInvalidUsername) {
				t.Fatalf("valeurAttendue ErrInvalidUsername, valeurObtenue %v", valeurObtenue)
			}
		})
	}
	fixture := newAccountFixture(t)
	if _, err := fixture.service.SetupAccount(context.Background(), strings.Repeat("é", maxLabelRunes), testPassword, testClient); err != nil {
		t.Fatalf("valeurAttendue 64 caractères acceptés, valeurObtenue %v", err)
	}
}

func TestLogin_unknownUsernameAndWrongPasswordReturnTheSameErrorAndRunTheDummyVerification(t *testing.T) {
	cases := []struct {
		name               string
		createAccount      bool
		username, password string
		dummyRuns          int32
	}{
		{"no account at all", false, testUsername, testPassword, 1},
		{"unknown username", true, "bob", testPassword, 1},
		{"username differs only by case", true, "Alice", testPassword, 1},
		{"wrong password", true, testUsername, testWrong, 0},
	}
	for _, cas := range cases {
		t.Run(cas.name, func(t *testing.T) {
			fixture := newAccountFixture(t)
			if cas.createAccount {
				fixture.setup(t)
			}
			fixture.dummyCalls.Store(0)
			fixture.argonRuns.Store(0)

			result, valeurObtenue := fixture.service.Login(context.Background(), cas.username, cas.password, testClient)

			if !errors.Is(valeurObtenue, ErrInvalidCredentials) || valeurObtenue.Error() != ErrInvalidCredentials.Error() || result.Session != nil {
				t.Fatalf("valeurAttendue ErrInvalidCredentials (même valeur), valeurObtenue %v %+v", valeurObtenue, result)
			}
			if dummyRuns := fixture.dummyCalls.Load(); dummyRuns != cas.dummyRuns {
				t.Fatalf("valeurAttendue %d vérification factice, valeurObtenue %d", cas.dummyRuns, dummyRuns)
			}
			if argonRuns := fixture.argonRuns.Load(); argonRuns != 1 {
				t.Fatalf("valeurAttendue exactement un calcul argon2 (même coût), valeurObtenue %d", argonRuns)
			}
		})
	}
}

func TestLogin_throttlesAfterFailuresWithARetryAfter(t *testing.T) {
	fixture := newAccountFixture(t)
	fixture.setup(t)
	ctx := context.Background()

	if _, err := fixture.service.Login(ctx, testUsername, testWrong, testClient); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("valeurObtenue %v", err)
	}
	for _, valeurAttendue := range []time.Duration{time.Second, 2 * time.Second} {
		_, err := fixture.service.Login(ctx, testUsername, testPassword, testClient)
		var throttled *ThrottledError
		if !errors.As(err, &throttled) || !errors.Is(err, ErrThrottled) || throttled.RetryAfter != valeurAttendue {
			t.Fatalf("valeurAttendue ThrottledError de %v, valeurObtenue %v", valeurAttendue, err)
		}
		fixture.clock.advance(valeurAttendue)
		if _, err := fixture.service.Login(ctx, "bob", testPassword, testClient); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("un identifiant inconnu doit passer par le même limiteur, valeurObtenue %v", err)
		}
	}

	fixture.clock.advance(4 * time.Second)
	result, err := fixture.service.Login(ctx, testUsername, testPassword, testClient)
	if err != nil || result.Session == nil {
		t.Fatalf("valeurObtenue %+v err=%v", result, err)
	}
	if _, err := fixture.service.Login(ctx, testUsername, testWrong, testClient); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("valeurObtenue %v", err)
	}
	fixture.clock.advance(time.Second)
	if _, err := fixture.service.Login(ctx, testUsername, testWrong, testClient); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("la session émise aurait dû remettre le délai à une seconde, valeurObtenue %v", err)
	}
}

func TestLogin_returnsTheSecondFactorsAndNoSessionWhenOneIsRegistered(t *testing.T) {
	cases := []struct {
		name           string
		credentials    []string
		totp           bool
		valeurAttendue []string
	}{
		{"passkey alone is not a second factor", []string{credentialKindPasskey}, false, nil},
		{"security key", []string{credentialKindSecondFactor, credentialKindPasskey}, false, []string{SecondFactorWebAuthn}},
		{"totp", nil, true, []string{SecondFactorTOTP}},
		{"security key and totp", []string{credentialKindSecondFactor}, true, []string{SecondFactorWebAuthn, SecondFactorTOTP}},
	}
	for _, cas := range cases {
		t.Run(cas.name, func(t *testing.T) {
			fixture := newAccountFixture(t)
			fixture.setup(t)
			ctx := context.Background()
			for credentialIndex, kind := range cas.credentials {
				credential := store.WebAuthnCredential{
					ID: []byte{byte(credentialIndex + 1)}, Name: "key", Kind: kind, Record: []byte(`{}`), CreatedAt: fixture.clock.now(),
				}
				if err := fixture.store.AddCredential(ctx, credential); err != nil {
					t.Fatal(err)
				}
			}
			if cas.totp {
				if err := fixture.store.SetTOTP(ctx, store.TOTPFactor{SecretSealed: []byte("sealed"), CreatedAt: fixture.clock.now()}); err != nil {
					t.Fatal(err)
				}
			}
			sessionsBefore, _ := fixture.store.Sessions(ctx)

			result, err := fixture.service.Login(ctx, testUsername, testPassword, testClient)
			if err != nil {
				t.Fatal(err)
			}

			if !slices.Equal(result.SecondFactors, cas.valeurAttendue) {
				t.Fatalf("valeurAttendue %v, valeurObtenue %v", cas.valeurAttendue, result.SecondFactors)
			}
			if ticketIssued := result.Ticket != ""; ticketIssued != (len(cas.valeurAttendue) > 0) {
				t.Fatalf("valeurAttendue un ticket exactement avec des facteurs, valeurObtenue %+v", result)
			}
			sessionsAfter, _ := fixture.store.Sessions(ctx)
			sessionIssued := len(sessionsAfter) > len(sessionsBefore)
			if wantSession := len(cas.valeurAttendue) == 0; (result.Session != nil) != wantSession || sessionIssued != wantSession {
				t.Fatalf("valeurAttendue session=%v, valeurObtenue %+v (stockée=%v)", wantSession, result.Session, sessionIssued)
			}
		})
	}
}

func TestLogin_correctPasswordWithAPendingSecondFactorDoesNotResetTheThrottle(t *testing.T) {
	fixture := newAccountFixture(t)
	fixture.setup(t)
	ctx := context.Background()
	if err := fixture.store.SetTOTP(ctx, store.TOTPFactor{SecretSealed: []byte("sealed"), CreatedAt: fixture.clock.now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Login(ctx, testUsername, testWrong, testClient); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("valeurObtenue %v", err)
	}
	fixture.clock.advance(time.Second)

	result, err := fixture.service.Login(ctx, testUsername, testPassword, testClient)
	if err != nil || result.Session != nil || len(result.SecondFactors) == 0 || result.Ticket == "" {
		t.Fatalf("valeurObtenue %+v err=%v", result, err)
	}
	if _, err := fixture.service.Login(ctx, testUsername, testWrong, testClient); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("valeurObtenue %v", err)
	}

	_, err = fixture.service.Login(ctx, testUsername, testPassword, testClient)
	var throttled *ThrottledError
	if valeurAttendue := 2 * time.Second; !errors.As(err, &throttled) || throttled.RetryAfter != valeurAttendue {
		t.Fatalf("valeurAttendue un délai de %v (échecs non remis à zéro), valeurObtenue %v", valeurAttendue, err)
	}
}

// failingCredentialsStore fait échouer la lecture des clés, après la vérification du mot de passe.
type failingCredentialsStore struct{ *store.Store }

var errInjectedStoreFailure = errors.New("injected store failure")

func (failingCredentialsStore) Credentials(context.Context) ([]store.WebAuthnCredential, error) {
	return nil, errInjectedStoreFailure
}

func TestLogin_aStoreFailureReleasesTheThrottleReservation(t *testing.T) {
	openedStore := openTestStore(t)
	healthy := newAccountFixtureOn(t, openedStore)
	healthy.setup(t)
	fixture := newAccountFixtureOn(t, failingCredentialsStore{openedStore})
	ctx := context.Background()

	if _, err := fixture.service.Login(ctx, testUsername, testPassword, testClient); !errors.Is(err, errInjectedStoreFailure) {
		t.Fatalf("valeurAttendue l'erreur du store, valeurObtenue %v", err)
	}

	if _, err := fixture.service.Login(ctx, testUsername, testWrong, testClient); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("valeurAttendue un essai admis aussitôt (réservation libérée, aucun échec compté), valeurObtenue %v", err)
	}
}

func TestLogin_refusesAnOversizedPasswordBeforeHashingAndWithoutCountingAFailure(t *testing.T) {
	fixture := newAccountFixture(t)
	fixture.setup(t)
	fixture.argonRuns.Store(0)
	ctx := context.Background()

	_, valeurObtenue := fixture.service.Login(ctx, testUsername, strings.Repeat("a", 10<<20), testClient)

	if !errors.Is(valeurObtenue, ErrInvalidCredentials) || fixture.argonRuns.Load() != 0 || fixture.dummyCalls.Load() != 0 {
		t.Fatalf("valeurObtenue %v, argon2=%d, factice=%d", valeurObtenue, fixture.argonRuns.Load(), fixture.dummyCalls.Load())
	}
	result, err := fixture.service.Login(ctx, testUsername, testPassword, testClient)
	if err != nil || result.Session == nil {
		t.Fatalf("valeurAttendue une connexion admise aussitôt, valeurObtenue %+v err=%v", result, err)
	}
}

func TestConcurrentLogins_throttleAdmitsOneAtATimeAndArgonStaysBounded(t *testing.T) {
	fixture := newAccountFixture(t)
	fixture.setup(t)
	fixture.argonPeak.Store(0)
	const attempts = 32
	var invalid, throttled atomic.Int32
	var group sync.WaitGroup
	for range attempts {
		group.Go(func() {
			_, err := fixture.service.Login(context.Background(), testUsername, testWrong, testClient)
			switch {
			case errors.Is(err, ErrInvalidCredentials):
				invalid.Add(1)
			case errors.Is(err, ErrThrottled):
				throttled.Add(1)
			default:
				t.Errorf("valeurObtenue %v", err)
			}
		})
	}
	group.Wait()

	if invalid.Load() != 1 || throttled.Load() != attempts-1 {
		t.Fatalf("valeurAttendue un seul essai admis, valeurObtenue %d admis, %d limités", invalid.Load(), throttled.Load())
	}
	if peak := fixture.argonPeak.Load(); peak > maxConcurrentArgon {
		t.Fatalf("valeurAttendue au plus %d dérivations argon2, valeurObtenue %d", maxConcurrentArgon, peak)
	}
}

func TestConcurrentArgon_neverRunsMoreThanFourComputationsAtOnce(t *testing.T) {
	fixture := newAccountFixture(t)
	release := make(chan struct{})
	const computations = 32
	var group sync.WaitGroup
	for range computations {
		group.Go(func() {
			if err := fixture.service.runArgon(context.Background(), func() { <-release }); err != nil {
				t.Error(err)
			}
		})
	}
	deadline := time.Now().Add(5 * time.Second)
	for fixture.argonRunning.Load() < maxConcurrentArgon && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	running := fixture.argonRunning.Load()
	close(release)
	group.Wait()

	if running != maxConcurrentArgon || fixture.argonPeak.Load() != maxConcurrentArgon || fixture.argonRuns.Load() != computations {
		t.Fatalf("valeurAttendue %d en cours au plus, valeurObtenue en cours=%d pic=%d total=%d",
			maxConcurrentArgon, running, fixture.argonPeak.Load(), fixture.argonRuns.Load())
	}
}

func TestConcurrentArgon_waitingHonoursContextCancellation(t *testing.T) {
	fixture := newAccountFixture(t)
	release := make(chan struct{})
	var group sync.WaitGroup
	for range maxConcurrentArgon {
		group.Go(func() { _ = fixture.service.runArgon(context.Background(), func() { <-release }) })
	}
	for fixture.argonRunning.Load() < maxConcurrentArgon {
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	valeurObtenue := fixture.service.runArgon(ctx, func() { t.Error("calcul lancé sans place libre") })
	close(release)
	group.Wait()

	if !errors.Is(valeurObtenue, context.DeadlineExceeded) {
		t.Fatalf("valeurAttendue DeadlineExceeded, valeurObtenue %v", valeurObtenue)
	}
}

func TestLogin_issuesAFreshTokenEveryTime(t *testing.T) {
	fixture := newAccountFixture(t)
	tokens := []string{fixture.setup(t).Token}
	for range 2 {
		result, err := fixture.service.Login(context.Background(), testUsername, testPassword, testClient)
		if err != nil || result.Session == nil {
			t.Fatalf("valeurObtenue %+v err=%v", result, err)
		}
		tokens = append(tokens, result.Session.Token)
	}

	distinctTokens := slices.Compact(slices.Sorted(slices.Values(tokens)))
	if len(distinctTokens) != len(tokens) {
		t.Fatalf("valeurAttendue des jetons tous différents, valeurObtenue %v", tokens)
	}
}

func TestLogoutAll_invalidatesEverySession(t *testing.T) {
	fixture := newAccountFixture(t)
	ctx := context.Background()
	tokens := []string{fixture.setup(t).Token}
	result, err := fixture.service.Login(ctx, testUsername, testPassword, testClient)
	if err != nil {
		t.Fatal(err)
	}
	tokens = append(tokens, result.Session.Token)

	if err := fixture.service.LogoutAll(ctx); err != nil {
		t.Fatal(err)
	}

	for _, token := range tokens {
		if _, valeurObtenue := fixture.service.ResumeSession(ctx, token); !errors.Is(valeurObtenue, ErrSessionInvalid) {
			t.Fatalf("valeurAttendue ErrSessionInvalid, valeurObtenue %v", valeurObtenue)
		}
	}
}

func TestLogout_closesOnlyTheGivenSession(t *testing.T) {
	fixture := newAccountFixture(t)
	ctx := context.Background()
	closed := fixture.setup(t).Token
	result, err := fixture.service.Login(ctx, testUsername, testPassword, testClient)
	if err != nil {
		t.Fatal(err)
	}

	if err := fixture.service.Logout(ctx, closed); err != nil {
		t.Fatal(err)
	}

	if _, err := fixture.service.ResumeSession(ctx, closed); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("valeurAttendue ErrSessionInvalid, valeurObtenue %v", err)
	}
	if _, err := fixture.service.ResumeSession(ctx, result.Session.Token); err != nil {
		t.Fatalf("valeurAttendue l'autre session ouverte, valeurObtenue %v", err)
	}
}

func TestResumeSession_rejectsIdleAndAbsoluteExpiry(t *testing.T) {
	fixture := newAccountFixture(t)
	ctx := context.Background()
	absolute := fixture.setup(t).Token
	keepAlive := auth.SessionIdleTimeout - time.Hour
	for elapsed := keepAlive; elapsed < auth.SessionAbsoluteLifetime; elapsed += keepAlive {
		fixture.clock.advance(keepAlive)
		if _, err := fixture.service.ResumeSession(ctx, absolute); err != nil {
			t.Fatalf("session refusée avant son échéance après %v : %v", elapsed, err)
		}
	}
	fixture.clock.advance(auth.SessionAbsoluteLifetime % keepAlive)
	if _, err := fixture.service.ResumeSession(ctx, absolute); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("valeurAttendue ErrSessionInvalid après quatorze jours, valeurObtenue %v", err)
	}
	if _, found, _ := fixture.store.Session(ctx, auth.SessionIDOf(absolute)); found {
		t.Fatal("la session expirée n'a pas été supprimée")
	}

	result, err := fixture.service.Login(ctx, testUsername, testPassword, testClient)
	if err != nil {
		t.Fatal(err)
	}
	fixture.clock.advance(auth.SessionIdleTimeout)
	if _, err := fixture.service.ResumeSession(ctx, result.Session.Token); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("valeurAttendue ErrSessionInvalid après l'inactivité, valeurObtenue %v", err)
	}
}

func TestResumeSession_rejectsAnUnknownToken(t *testing.T) {
	fixture := newAccountFixture(t)
	fixture.setup(t)
	for _, token := range []string{"", "not-a-session-token"} {
		if _, valeurObtenue := fixture.service.ResumeSession(context.Background(), token); !errors.Is(valeurObtenue, ErrSessionInvalid) {
			t.Fatalf("valeurAttendue ErrSessionInvalid, valeurObtenue %v", valeurObtenue)
		}
	}
}

func (fixture *accountFixture) loginTokens(t *testing.T, count int) []string {
	t.Helper()
	tokens := make([]string, 0, count)
	for range count {
		result, err := fixture.service.Login(context.Background(), testUsername, testPassword, testClient)
		if err != nil || result.Session == nil {
			t.Fatalf("valeurObtenue %+v err=%v", result, err)
		}
		tokens = append(tokens, result.Session.Token)
	}
	return tokens
}

func TestChangePassword_revokesEveryOtherSessionAndKeepsTheCurrentOne(t *testing.T) {
	fixture := newAccountFixture(t)
	ctx := context.Background()
	current := fixture.setup(t).Token
	others := fixture.loginTokens(t, 2)

	if err := fixture.service.ChangePassword(ctx, current, testPassword, testNewPassword); err != nil {
		t.Fatal(err)
	}

	for _, token := range others {
		if _, valeurObtenue := fixture.service.ResumeSession(ctx, token); !errors.Is(valeurObtenue, ErrSessionInvalid) {
			t.Fatalf("valeurAttendue ErrSessionInvalid pour une autre session, valeurObtenue %v", valeurObtenue)
		}
	}
	if _, err := fixture.service.ResumeSession(ctx, current); err != nil {
		t.Fatalf("valeurAttendue la session courante gardée, valeurObtenue %v", err)
	}
	if _, err := fixture.service.Login(ctx, testUsername, testPassword, testClient); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("valeurAttendue l'ancien mot de passe refusé, valeurObtenue %v", err)
	}
	fixture.passThrottleDelay()
	if result, err := fixture.service.Login(ctx, testUsername, testNewPassword, testClient); err != nil || result.Session == nil {
		t.Fatalf("valeurAttendue le nouveau mot de passe accepté, valeurObtenue %+v err=%v", result, err)
	}
}

func TestChangePassword_refusedAttemptsChangeNothingAndRevokeNothing(t *testing.T) {
	fixture := newAccountFixture(t)
	ctx := context.Background()
	current := fixture.setup(t).Token
	other := fixture.loginTokens(t, 1)[0]

	if err := fixture.service.ChangePassword(ctx, current, testWrong, testNewPassword); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("valeurAttendue ErrInvalidCredentials, valeurObtenue %v", err)
	}
	if err := fixture.service.ChangePassword(ctx, current, testPassword, testNewPassword); !errors.Is(err, ErrThrottled) {
		t.Fatalf("valeurAttendue la ressaisie limitée, valeurObtenue %v", err)
	}
	fixture.passThrottleDelay()
	if err := fixture.service.ChangePassword(ctx, current, testPassword, "short"); !errors.Is(err, auth.ErrPasswordTooShort) {
		t.Fatalf("valeurAttendue ErrPasswordTooShort, valeurObtenue %v", err)
	}
	if err := fixture.service.ChangePassword(ctx, "unknown", testPassword, testNewPassword); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("valeurAttendue ErrSessionInvalid, valeurObtenue %v", err)
	}

	if _, err := fixture.service.ResumeSession(ctx, other); err != nil {
		t.Fatalf("valeurAttendue l'autre session gardée après un refus, valeurObtenue %v", err)
	}
	if result, err := fixture.service.Login(ctx, testUsername, testPassword, testClient); err != nil || result.Session == nil {
		t.Fatalf("valeurAttendue l'ancien mot de passe inchangé, valeurObtenue %+v err=%v", result, err)
	}
}

// failingSessionRevocationStore fait échouer la révocation des autres sessions.
type failingSessionRevocationStore struct{ *store.Store }

func (failingSessionRevocationStore) DeleteSessionsExcept(context.Context, string) error {
	return errInjectedStoreFailure
}

func TestChangePassword_reportsAFailedRevocationWithoutLeakingSecrets(t *testing.T) {
	openedStore := openTestStore(t)
	healthy := newAccountFixtureOn(t, openedStore)
	current := healthy.setup(t).Token
	fixture := newAccountFixtureOn(t, failingSessionRevocationStore{openedStore})

	valeurObtenue := fixture.service.ChangePassword(context.Background(), current, testPassword, testNewPassword)

	if !errors.Is(valeurObtenue, errInjectedStoreFailure) || !strings.Contains(valeurObtenue.Error(), "password changed") {
		t.Fatalf("valeurAttendue l'échec de révocation après le changement, valeurObtenue %v", valeurObtenue)
	}
	for _, secret := range []string{testPassword, testNewPassword, current} {
		if strings.Contains(valeurObtenue.Error(), secret) {
			t.Fatalf("l'erreur divulgue une saisie : %q", valeurObtenue)
		}
	}
}

func TestListSessions_marksTheCurrentOneAndRequiresASession(t *testing.T) {
	fixture := newAccountFixture(t)
	ctx := context.Background()
	first := fixture.setup(t).Token
	if _, err := fixture.service.Login(ctx, testUsername, testPassword, testClient); err != nil {
		t.Fatal(err)
	}

	views, err := fixture.service.ListSessions(ctx, first)
	if err != nil {
		t.Fatal(err)
	}

	currentCount := 0
	for _, view := range views {
		if view.Current {
			currentCount++
		}
	}
	if len(views) != 2 || currentCount != 1 || views[0].Username != testUsername {
		t.Fatalf("valeurObtenue %+v", views)
	}
	if _, err := fixture.service.ListSessions(ctx, "unknown"); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("valeurAttendue ErrSessionInvalid, valeurObtenue %v", err)
	}
}

func TestResetAccount_returnsToNoAccountLive(t *testing.T) {
	fixture := newAccountFixture(t)
	ctx := context.Background()
	token := fixture.setup(t).Token

	if err := fixture.store.ResetAccount(ctx); err != nil {
		t.Fatal(err)
	}

	status, err := fixture.service.AuthStatus(ctx)
	if err != nil || status.AccountExists {
		t.Fatalf("valeurAttendue aucun compte vu en direct, valeurObtenue %+v err=%v", status, err)
	}
	if _, err := fixture.service.ResumeSession(ctx, token); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("valeurAttendue ErrSessionInvalid, valeurObtenue %v", err)
	}
	fresh := fixture.setup(t).Token
	if err := fixture.service.ResetAccount(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.ResumeSession(ctx, fresh); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("valeurAttendue ErrSessionInvalid après ResetAccount, valeurObtenue %v", err)
	}
	if summary, _ := fixture.service.AccountSummary(ctx); summary.Exists {
		t.Fatalf("valeurObtenue %+v", summary)
	}
}

func TestAccountSummary_countsKeysAndTOTP(t *testing.T) {
	fixture := newAccountFixture(t)
	fixture.setup(t)
	ctx := context.Background()
	for credentialIndex, kind := range []string{credentialKindSecondFactor, credentialKindSecondFactor, credentialKindPasskey} {
		credential := store.WebAuthnCredential{ID: []byte{byte(credentialIndex + 1)}, Name: "key", Kind: kind, Record: []byte(`{}`), CreatedAt: fixture.clock.now()}
		if err := fixture.store.AddCredential(ctx, credential); err != nil {
			t.Fatal(err)
		}
	}
	if err := fixture.store.SetTOTP(ctx, store.TOTPFactor{SecretSealed: []byte("sealed"), CreatedAt: fixture.clock.now()}); err != nil {
		t.Fatal(err)
	}

	valeurObtenue, err := fixture.service.AccountSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}

	valeurAttendue := AccountSummary{Exists: true, Username: testUsername, SecondFactorKeys: 2, Passkeys: 1, TOTP: true}
	if valeurObtenue != valeurAttendue {
		t.Fatalf("valeurAttendue %+v, valeurObtenue %+v", valeurAttendue, valeurObtenue)
	}
}

func TestAuthStatus_reportsWebAuthnFromThePublicURL(t *testing.T) {
	cases := map[string]struct {
		settings       config.Config
		valeurAttendue bool
	}{
		"https public url":            {config.Config{PublicURL: "https://example.com", ListenAddr: "0.0.0.0:8787"}, true},
		"no public url, open listen":  {config.Config{ListenAddr: "0.0.0.0:8787"}, false},
		"no public url, loopback":     {config.Config{ListenAddr: "127.0.0.1:8787"}, true},
		"plain http remote publicUrl": {config.Config{PublicURL: "http://example.com", ListenAddr: "0.0.0.0:8787"}, false},
	}
	for name, cas := range cases {
		t.Run(name, func(t *testing.T) {
			service := New(Deps{Store: openTestStore(t), Config: cas.settings})
			status, err := service.AuthStatus(context.Background())
			if err != nil || status.WebAuthnAvailable != cas.valeurAttendue || status.AccountExists {
				t.Fatalf("valeurAttendue WebAuthnAvailable=%v, valeurObtenue %+v err=%v", cas.valeurAttendue, status, err)
			}
		})
	}
}

func TestNew_minimalDepsCreateTheAccountAndSignIn(t *testing.T) {
	service := New(Deps{Store: openTestStore(t)})
	ctx := context.Background()

	if _, err := service.SetupAccount(ctx, testUsername, testPassword, testClient); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Login(ctx, "bob", testPassword, testClient); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("valeurAttendue ErrInvalidCredentials, valeurObtenue %v", err)
	}
	time.Sleep(auth.ThrottleBaseDelay)
	result, err := service.Login(ctx, testUsername, testPassword, testClient)
	if err != nil || result.Session == nil {
		t.Fatalf("valeurObtenue %+v err=%v", result, err)
	}
	if _, err := service.ResumeSession(ctx, result.Session.Token); err != nil {
		t.Fatal(err)
	}
}

func TestAuthErrors_neverContainTheTypedPasswordOrUsername(t *testing.T) {
	const secretUsername = "secretuser"
	const secretPassword = "secret-typed-password"
	fixture := newAccountFixture(t)
	ctx := context.Background()
	var failures []error
	record := func(err error) {
		if err == nil {
			t.Fatal("valeurAttendue une erreur")
		}
		failures = append(failures, err)
	}
	_, err := fixture.service.SetupAccount(ctx, secretUsername, "short-pw", testClient)
	record(err)
	_, err = fixture.service.SetupAccount(ctx, secretUsername+"\x00", secretPassword, testClient)
	record(err)
	_, err = fixture.service.SetupAccount(ctx, secretUsername, strings.Repeat(secretPassword, 10), testClient)
	record(err)
	token, err := fixture.service.SetupAccount(ctx, secretUsername, secretPassword, testClient)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.service.SetupAccount(ctx, secretUsername, secretPassword, testClient)
	record(err)
	_, err = fixture.service.Login(ctx, "unknown-"+secretUsername, secretPassword, testClient)
	record(err)
	_, err = fixture.service.Login(ctx, secretUsername, "wrong-"+secretPassword, testClient)
	record(err)
	fixture.passThrottleDelay()
	_, err = fixture.service.Login(ctx, secretUsername, strings.Repeat(secretPassword, 1<<16), testClient)
	record(err)
	record(fixture.service.ChangePassword(ctx, token.Token, "wrong-"+secretPassword, secretPassword+"-new"))
	record(fixture.service.ChangePassword(ctx, token.Token, secretPassword, "short-pw"))
	record(fixture.service.ChangePassword(ctx, "forged-"+token.Token, secretPassword, secretPassword+"-new"))
	_, err = fixture.service.ResumeSession(ctx, "forged-"+token.Token)
	record(err)

	for _, failure := range failures {
		message := failure.Error()
		if strings.Contains(message, secretPassword) || strings.Contains(message, secretUsername) || strings.Contains(message, token.Token) ||
			strings.Contains(message, "short-pw") {
			t.Fatalf("l'erreur divulgue une saisie : %q", message)
		}
	}
}
