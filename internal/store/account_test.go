package store

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var referenceMoment = time.Unix(1700000000, 0).UTC()

func sampleAccount() Account {
	return Account{Username: "alice", PasswordHash: "hash-1", CreatedAt: referenceMoment}
}

func sampleCredential(id string, kind string) WebAuthnCredential {
	return WebAuthnCredential{ID: []byte(id), Name: "key " + id, Kind: kind, Record: []byte(`{"n":1}`), CreatedAt: referenceMoment}
}

func sampleSession(id string, expiresAt time.Time) SessionRecord {
	return SessionRecord{
		ID: id, CreatedAt: referenceMoment, LastSeenAt: referenceMoment, ExpiresAt: expiresAt,
		Address: "192.0.2.1", UserAgent: "agent",
	}
}

func TestAccount_reportsAbsentOnAFreshDatabase(t *testing.T) {
	openedStore := openTestStore(t)
	_, present, err := openedStore.Account(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if present {
		t.Fatal("valeurObtenue present, valeurAttendue absent")
	}
}

func TestCreateAccount_refusesASecondAccountWithErrAccountExists(t *testing.T) {
	openedStore := openTestStore(t)
	ctx := context.Background()
	if err := openedStore.CreateAccount(ctx, sampleAccount()); err != nil {
		t.Fatal(err)
	}
	errorObtenue := openedStore.CreateAccount(ctx, Account{Username: "bob", PasswordHash: "x", CreatedAt: referenceMoment})
	if !errors.Is(errorObtenue, ErrAccountExists) {
		t.Fatalf("valeurObtenue %v, valeurAttendue ErrAccountExists", errorObtenue)
	}
	valeurObtenue, present, err := openedStore.Account(ctx)
	if err != nil || !present {
		t.Fatalf("present %v err %v", present, err)
	}
	if valeurObtenue != sampleAccount() {
		t.Fatalf("valeurObtenue %+v, valeurAttendue %+v", valeurObtenue, sampleAccount())
	}
}

func TestCreateAccount_concurrentCallsLeaveExactlyOneAccount(t *testing.T) {
	openedStore := openTestStore(t)
	ctx := context.Background()
	const callers = 16
	var succeeded, refused atomic.Int32
	var group sync.WaitGroup
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			err := openedStore.CreateAccount(ctx, sampleAccount())
			switch {
			case err == nil:
				succeeded.Add(1)
			case errors.Is(err, ErrAccountExists):
				refused.Add(1)
			default:
				t.Error(err)
			}
		}()
	}
	group.Wait()
	if succeeded.Load() != 1 || refused.Load() != callers-1 {
		t.Fatalf("valeurObtenue %d réussis %d refusés, valeurAttendue 1 et %d", succeeded.Load(), refused.Load(), callers-1)
	}
}

func TestSetPasswordHash_replacesTheHash(t *testing.T) {
	openedStore := openTestStore(t)
	ctx := context.Background()
	if err := openedStore.CreateAccount(ctx, sampleAccount()); err != nil {
		t.Fatal(err)
	}
	if err := openedStore.SetPasswordHash(ctx, "hash-2"); err != nil {
		t.Fatal(err)
	}
	account, _, err := openedStore.Account(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if account.PasswordHash != "hash-2" {
		t.Fatalf("valeurObtenue %q, valeurAttendue hash-2", account.PasswordHash)
	}
}

func TestResetAccount_removesAccountCredentialsAndSessionsTogether(t *testing.T) {
	openedStore := openTestStore(t)
	ctx := context.Background()
	if err := openedStore.CreateAccount(ctx, sampleAccount()); err != nil {
		t.Fatal(err)
	}
	if err := openedStore.AddCredential(ctx, sampleCredential("c1", "passkey")); err != nil {
		t.Fatal(err)
	}
	if err := openedStore.CreateSession(ctx, sampleSession("s1", referenceMoment.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := openedStore.ResetAccount(ctx); err != nil {
		t.Fatal(err)
	}
	_, present, _ := openedStore.Account(ctx)
	credentials, _ := openedStore.Credentials(ctx)
	sessions, _ := openedStore.Sessions(ctx)
	if present || len(credentials) != 0 || len(sessions) != 0 {
		t.Fatalf("valeurObtenue compte %v clés %d sessions %d, valeurAttendue tout vide", present, len(credentials), len(sessions))
	}
	if err := openedStore.CreateAccount(ctx, sampleAccount()); err != nil {
		t.Fatalf("un compte doit pouvoir être recréé après la remise à zéro : %v", err)
	}
}

func TestResetAccount_alsoRemovesTheTOTPSecret(t *testing.T) {
	openedStore := openTestStore(t)
	ctx := context.Background()
	if err := openedStore.SetTOTP(ctx, TOTPFactor{SecretSealed: []byte("sealed"), CreatedAt: referenceMoment}); err != nil {
		t.Fatal(err)
	}
	if err := openedStore.ResetAccount(ctx); err != nil {
		t.Fatal(err)
	}
	_, present, err := openedStore.TOTP(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if present {
		t.Fatal("valeurObtenue TOTP présent, valeurAttendue absent")
	}
}

func TestSession_roundTripsAndPurgesExpiredOnes(t *testing.T) {
	openedStore := openTestStore(t)
	ctx := context.Background()
	expired := sampleSession("expired", referenceMoment.Add(time.Minute))
	alive := sampleSession("alive", referenceMoment.Add(time.Hour))
	for _, session := range []SessionRecord{expired, alive} {
		if err := openedStore.CreateSession(ctx, session); err != nil {
			t.Fatal(err)
		}
	}
	valeurObtenue, present, err := openedStore.Session(ctx, "alive")
	if err != nil || !present || valeurObtenue != alive {
		t.Fatalf("valeurObtenue %+v (présent %v, err %v), valeurAttendue %+v", valeurObtenue, present, err, alive)
	}
	seenAt := referenceMoment.Add(10 * time.Second)
	if err := openedStore.TouchSession(ctx, "alive", seenAt); err != nil {
		t.Fatal(err)
	}
	touched, _, _ := openedStore.Session(ctx, "alive")
	if !touched.LastSeenAt.Equal(seenAt) {
		t.Fatalf("valeurObtenue %v, valeurAttendue %v", touched.LastSeenAt, seenAt)
	}

	purged, err := openedStore.PurgeExpiredSessions(ctx, referenceMoment.Add(30*time.Minute))
	if err != nil || purged != 1 {
		t.Fatalf("valeurObtenue %d (err %v), valeurAttendue 1", purged, err)
	}
	if _, present, _ := openedStore.Session(ctx, "expired"); present {
		t.Fatal("la session expirée doit avoir disparu")
	}
	if err := openedStore.DeleteSession(ctx, "alive"); err != nil {
		t.Fatal(err)
	}
	if _, present, _ := openedStore.Session(ctx, "alive"); present {
		t.Fatal("la session supprimée doit avoir disparu")
	}
}

func TestSessions_listsNewestFirstAndDeleteAllEmptiesThem(t *testing.T) {
	openedStore := openTestStore(t)
	ctx := context.Background()
	older := sampleSession("older", referenceMoment.Add(time.Hour))
	newer := sampleSession("newer", referenceMoment.Add(time.Hour))
	newer.CreatedAt = referenceMoment.Add(time.Minute)
	for _, session := range []SessionRecord{older, newer} {
		if err := openedStore.CreateSession(ctx, session); err != nil {
			t.Fatal(err)
		}
	}
	sessions, err := openedStore.Sessions(ctx)
	if err != nil || len(sessions) != 2 || sessions[0].ID != "newer" {
		t.Fatalf("valeurObtenue %+v (err %v), valeurAttendue newer puis older", sessions, err)
	}
	if err := openedStore.DeleteAllSessions(ctx); err != nil {
		t.Fatal(err)
	}
	if sessions, _ := openedStore.Sessions(ctx); len(sessions) != 0 {
		t.Fatalf("valeurObtenue %d sessions, valeurAttendue 0", len(sessions))
	}
}

func TestDeleteSessionsExcept_keepsOnlyTheGivenSession(t *testing.T) {
	openedStore := openTestStore(t)
	ctx := context.Background()
	expiresAt := referenceMoment.Add(time.Hour)
	for _, id := range []string{"kept", "other-1", "other-2"} {
		if err := openedStore.CreateSession(ctx, sampleSession(id, expiresAt)); err != nil {
			t.Fatal(err)
		}
	}

	if err := openedStore.DeleteSessionsExcept(ctx, "kept"); err != nil {
		t.Fatal(err)
	}

	sessions, err := openedStore.Sessions(ctx)
	if err != nil || len(sessions) != 1 || sessions[0].ID != "kept" {
		t.Fatalf("valeurAttendue la seule session kept, valeurObtenue %+v (err %v)", sessions, err)
	}
}

func TestCredentials_addListRenameUpdateDelete(t *testing.T) {
	openedStore := openTestStore(t)
	ctx := context.Background()
	first := sampleCredential("c1", "second_factor")
	second := sampleCredential("c2", "passkey")
	second.CreatedAt = referenceMoment.Add(time.Minute)
	for _, credential := range []WebAuthnCredential{second, first} {
		if err := openedStore.AddCredential(ctx, credential); err != nil {
			t.Fatal(err)
		}
	}
	if err := openedStore.AddCredential(ctx, sampleCredential("c3", "unknown")); err == nil {
		t.Fatal("un type de clé inconnu doit être refusé")
	}
	if err := openedStore.RenameCredential(ctx, []byte("c1"), "renamed"); err != nil {
		t.Fatal(err)
	}
	usedAt := referenceMoment.Add(time.Hour)
	if err := openedStore.UpdateCredential(ctx, []byte("c2"), []byte(`{"n":2}`), usedAt); err != nil {
		t.Fatal(err)
	}

	credentials, err := openedStore.Credentials(ctx)
	if err != nil || len(credentials) != 2 {
		t.Fatalf("valeurObtenue %+v (err %v), valeurAttendue 2 clés", credentials, err)
	}
	if string(credentials[0].ID) != "c1" || credentials[0].Name != "renamed" || credentials[0].LastUsedAt != nil {
		t.Fatalf("première clé obtenue %+v", credentials[0])
	}
	if string(credentials[1].Record) != `{"n":2}` || credentials[1].LastUsedAt == nil || !credentials[1].LastUsedAt.Equal(usedAt) {
		t.Fatalf("seconde clé obtenue %+v", credentials[1])
	}

	if err := openedStore.DeleteCredential(ctx, []byte("c1")); err != nil {
		t.Fatal(err)
	}
	if credentials, _ := openedStore.Credentials(ctx); len(credentials) != 1 {
		t.Fatalf("valeurObtenue %d clés, valeurAttendue 1", len(credentials))
	}
}

func TestAdvanceTOTPStep_acceptsAStepOnceAndRefusesAReplayEvenConcurrently(t *testing.T) {
	openedStore := openTestStore(t)
	ctx := context.Background()
	if err := openedStore.SetTOTP(ctx, TOTPFactor{SecretSealed: []byte("sealed"), LastStep: 10, CreatedAt: referenceMoment}); err != nil {
		t.Fatal(err)
	}
	for step, valeurAttendue := range map[int64]bool{10: false, 9: false} {
		valeurObtenue, err := openedStore.AdvanceTOTPStep(ctx, step)
		if err != nil || valeurObtenue != valeurAttendue {
			t.Fatalf("pas %d : valeurObtenue %v (err %v), valeurAttendue %v", step, valeurObtenue, err, valeurAttendue)
		}
	}

	const callers = 16
	var accepted atomic.Int32
	var group sync.WaitGroup
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			advanced, err := openedStore.AdvanceTOTPStep(ctx, 11)
			if err != nil {
				t.Error(err)
			}
			if advanced {
				accepted.Add(1)
			}
		}()
	}
	group.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("valeurObtenue %d acceptations, valeurAttendue 1", accepted.Load())
	}
	factor, _, _ := openedStore.TOTP(ctx)
	if factor.LastStep != 11 {
		t.Fatalf("valeurObtenue %d, valeurAttendue 11", factor.LastStep)
	}
}

func TestAdvanceTOTPStep_refusesWhenNoFactorIsConfigured(t *testing.T) {
	openedStore := openTestStore(t)
	valeurObtenue, err := openedStore.AdvanceTOTPStep(context.Background(), 5)
	if err != nil || valeurObtenue {
		t.Fatalf("valeurObtenue %v (err %v), valeurAttendue false", valeurObtenue, err)
	}
}
