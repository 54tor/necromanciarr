package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"
)

const validPassword = "correct horse battery"

func TestValidatePassword_enforcesTwelveCharactersAndAHundredTwentyEightBytes(t *testing.T) {
	cases := []struct {
		name           string
		password       string
		valeurAttendue error
	}{
		{"eleven characters refused", strings.Repeat("a", 11), ErrPasswordTooShort},
		{"twelve characters accepted", strings.Repeat("a", 12), nil},
		{"twelve multibyte characters accepted", strings.Repeat("é", 12), nil},
		{"128 bytes accepted", strings.Repeat("a", 128), nil},
		{"129 bytes refused", strings.Repeat("a", 129), ErrPasswordTooLong},
		{"ten megabytes refused", strings.Repeat("a", 10<<20), ErrPasswordTooLong},
	}
	for _, cas := range cases {
		t.Run(cas.name, func(t *testing.T) {
			valeurObtenue := ValidatePassword(cas.password)
			if !errors.Is(valeurObtenue, cas.valeurAttendue) {
				t.Fatalf("valeurAttendue %v, valeurObtenue %v", cas.valeurAttendue, valeurObtenue)
			}
		})
	}
}

func TestHashPassword_roundTripsAndRejectsAWrongPassword(t *testing.T) {
	encoded, err := HashPassword(validPassword)
	if err != nil {
		t.Fatal(err)
	}
	matches, needsRehash, err := VerifyPassword(validPassword, encoded)
	if err != nil || !matches || needsRehash {
		t.Fatalf("valeurObtenue matches=%v needsRehash=%v err=%v", matches, needsRehash, err)
	}
	matches, _, err = VerifyPassword("another password!", encoded)
	if err != nil || matches {
		t.Fatalf("valeurObtenue matches=%v err=%v pour un mauvais mot de passe", matches, err)
	}
}

func TestHashPassword_refusesAnOversizedInputWithoutHashing(t *testing.T) {
	if _, err := HashPassword(strings.Repeat("a", MaxPasswordBytes+1)); !errors.Is(err, ErrPasswordTooLong) {
		t.Fatalf("valeurObtenue %v", err)
	}
	if _, _, err := VerifyPassword(strings.Repeat("a", 10<<20), "$argon2id$v=19$m=65536,t=2,p=2$AAAA$AAAA"); !errors.Is(err, ErrPasswordTooLong) {
		t.Fatalf("valeurObtenue %v", err)
	}
}

func TestVerifyPassword_flagsAHashWithWeakerParametersForRehash(t *testing.T) {
	salt := []byte("0123456789abcdef")
	key := argon2.IDKey([]byte(validPassword), salt, 1, 8*1024, 1, 32)
	encoded := fmt.Sprintf("$argon2id$v=%d$m=8192,t=1,p=1$%s$%s", argon2.Version,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))

	matches, needsRehash, err := VerifyPassword(validPassword, encoded)
	if err != nil || !matches || !needsRehash {
		t.Fatalf("valeurObtenue matches=%v needsRehash=%v err=%v", matches, needsRehash, err)
	}
}

func TestVerifyPassword_rejectsMalformedHashesWithoutLeakingSecrets(t *testing.T) {
	malformed := []string{"", "plain", "$argon2i$v=19$m=65536,t=2,p=2$AAAA$AAAA", "$argon2id$v=19$m=x,t=2,p=2$AAAA$AAAA", "$argon2id$v=19$m=65536,t=2,p=2$!!$!!"}
	for _, encoded := range malformed {
		matches, _, err := VerifyPassword(validPassword, encoded)
		if matches || err == nil {
			t.Fatalf("valeurObtenue matches=%v err=%v pour %q", matches, err, encoded)
		}
		if strings.Contains(err.Error(), validPassword) || (encoded != "" && strings.Contains(err.Error(), encoded)) {
			t.Fatalf("l'erreur divulgue un secret : %v", err)
		}
	}
}

func TestHashPassword_neverContainsThePassword(t *testing.T) {
	encoded, err := HashPassword(validPassword)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encoded, validPassword) {
		t.Fatal("le hachage contient le mot de passe")
	}
	if !strings.HasPrefix(encoded, "$argon2id$v=19$m=65536,t=2,p=2$") {
		t.Fatalf("valeurObtenue %q", encoded)
	}
	other, _ := HashPassword(validPassword)
	if other == encoded {
		t.Fatal("deux hachages du même mot de passe sont identiques : sel non aléatoire")
	}
}

func TestVerifyDummy_zeroValueSafeAndCostsLikeARealVerification(t *testing.T) {
	VerifyDummy("")
	VerifyDummy(validPassword)
	VerifyDummy(strings.Repeat("a", 10<<20))
}

func TestNewSessionToken_idIsTheSha256OfTheTokenAndTokensDiffer(t *testing.T) {
	token, id, err := NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(token))
	valeurAttendue := hex.EncodeToString(digest[:])
	if id != valeurAttendue || SessionIDOf(token) != valeurAttendue {
		t.Fatalf("valeurAttendue %s, valeurObtenue %s", valeurAttendue, id)
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 {
		t.Fatalf("jeton mal formé : len=%d err=%v", len(raw), err)
	}
	other, _, _ := NewSessionToken()
	if other == token {
		t.Fatal("deux jetons identiques")
	}
}

type fakeClock struct{ current time.Time }

func (clock *fakeClock) now() time.Time { return clock.current }

func TestThrottle_delaysDoubleFromOneSecondUpToSixtySecondsAndSuccessResets(t *testing.T) {
	clock := &fakeClock{current: time.Unix(1_000_000, 0)}
	throttle := NewThrottle(clock.now)
	const key = "alice"

	if _, allowed := throttle.Allow(key); !allowed {
		t.Fatal("premier essai refusé")
	}
	for _, valeurAttendue := range []time.Duration{1, 2, 4, 8, 16, 32, 60, 60} {
		throttle.RecordFailure(key)
		valeurAttendue *= time.Second
		retryAfter, allowed := throttle.Allow(key)
		if allowed || retryAfter != valeurAttendue {
			t.Fatalf("valeurAttendue %v, valeurObtenue %v (allowed=%v)", valeurAttendue, retryAfter, allowed)
		}
		clock.current = clock.current.Add(valeurAttendue)
		if _, allowed := throttle.Allow(key); !allowed {
			t.Fatal("essai refusé après l'échéance")
		}
	}

	throttle.RecordSuccess(key)
	throttle.RecordFailure(key)
	if retryAfter, _ := throttle.Allow(key); retryAfter != ThrottleBaseDelay {
		t.Fatalf("le succès n'a pas remis à zéro : %v", retryAfter)
	}
}

func TestThrottle_refusedAttemptsDoNotCountAsFailures(t *testing.T) {
	clock := &fakeClock{current: time.Unix(1_000_000, 0)}
	throttle := NewThrottle(clock.now)
	throttle.RecordFailure("alice")
	for range 10 {
		clock.current = clock.current.Add(50 * time.Millisecond)
		if _, allowed := throttle.Allow("alice"); allowed {
			t.Fatal("essai admis pendant le délai")
		}
	}
	clock.current = time.Unix(1_000_000, 0).Add(ThrottleBaseDelay)
	if _, allowed := throttle.Allow("alice"); !allowed {
		t.Fatal("les essais refusés ont prolongé le délai")
	}
}

func TestThrottle_keysAreIndependentAndZeroClockDefaultsToTimeNow(t *testing.T) {
	throttle := NewThrottle(nil)
	throttle.RecordFailure("alice")
	if _, allowed := throttle.Allow("bob"); !allowed {
		t.Fatal("bob est limité par alice")
	}
	if _, allowed := throttle.Allow("alice"); allowed {
		t.Fatal("alice devrait être limitée")
	}
}

func TestThrottle_evictsStaleEntries(t *testing.T) {
	clock := &fakeClock{current: time.Unix(1_000_000, 0)}
	throttle := NewThrottle(clock.now)
	throttle.RecordFailure("alice")
	clock.current = clock.current.Add(3 * ThrottleMaxDelay)
	throttle.Allow("bob")
	if _, found := throttle.states["alice"]; found {
		t.Fatal("l'entrée périmée d'alice n'a pas été évincée")
	}
}

func TestThrottle_zeroValueIsUsable(t *testing.T) {
	var throttle Throttle
	if _, allowed := throttle.Allow("alice"); !allowed {
		t.Fatal("premier essai refusé")
	}
	throttle.RecordFailure("alice")
	if _, allowed := throttle.Allow("alice"); allowed {
		t.Fatal("alice devrait être limitée")
	}
	throttle.Cancel("alice")
	throttle.RecordSuccess("alice")
	if _, allowed := throttle.Allow("alice"); !allowed {
		t.Fatal("le succès aurait dû libérer alice")
	}
}

func TestThrottle_concurrentAttemptsOnOneKeyAdmitExactlyOne(t *testing.T) {
	clock := &fakeClock{current: time.Unix(1_000_000, 0)}
	throttle := NewThrottle(clock.now)
	const attempts = 64
	var admitted atomic.Int32
	var group sync.WaitGroup
	for range attempts {
		group.Add(1)
		go func() {
			defer group.Done()
			retryAfter, allowed := throttle.Allow("alice")
			if allowed {
				admitted.Add(1)
			} else if retryAfter <= 0 {
				t.Errorf("retryAfter doit être positif, valeurObtenue %v", retryAfter)
			}
		}()
	}
	group.Wait()
	if valeurObtenue := admitted.Load(); valeurObtenue != 1 {
		t.Fatalf("valeurAttendue 1, valeurObtenue %d", valeurObtenue)
	}
}

func TestThrottle_reservationIsReleasedByFailureSuccessAndCancel(t *testing.T) {
	clock := &fakeClock{current: time.Unix(1_000_000, 0)}
	releases := map[string]func(*Throttle){
		"success": func(throttle *Throttle) { throttle.RecordSuccess("alice") },
		"cancel":  func(throttle *Throttle) { throttle.Cancel("alice") },
		"failure": func(throttle *Throttle) { throttle.RecordFailure("alice") },
	}
	for name, release := range releases {
		t.Run(name, func(t *testing.T) {
			throttle := NewThrottle(clock.now)
			if _, allowed := throttle.Allow("alice"); !allowed {
				t.Fatal("premier essai refusé")
			}
			if retryAfter, allowed := throttle.Allow("alice"); allowed || retryAfter < ThrottleBaseDelay {
				t.Fatalf("essai concurrent admis ou retryAfter trop court : %v", retryAfter)
			}
			release(throttle)
			clock.current = clock.current.Add(ThrottleBaseDelay)
			if _, allowed := throttle.Allow("alice"); !allowed {
				t.Fatal("la réservation n'a pas été libérée")
			}
			clock.current = clock.current.Add(time.Hour)
		})
	}
}

func TestThrottle_aStaleReservationExpires(t *testing.T) {
	clock := &fakeClock{current: time.Unix(1_000_000, 0)}
	throttle := NewThrottle(clock.now)
	throttle.Allow("alice")
	clock.current = clock.current.Add(throttleReservationTTL - time.Second)
	if _, allowed := throttle.Allow("alice"); allowed {
		t.Fatal("réservation expirée trop tôt")
	}
	clock.current = clock.current.Add(time.Second)
	if _, allowed := throttle.Allow("alice"); !allowed {
		t.Fatal("la réservation périmée verrouille le compte")
	}
}
