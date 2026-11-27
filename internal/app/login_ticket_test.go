package app

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/descope/virtualwebauthn"
	"github.com/go-webauthn/webauthn/webauthn"
)

func expectTicketRefused(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrLoginTicketInvalid) || err.Error() != ErrLoginTicketInvalid.Error() {
		t.Fatalf("valeurAttendue exactement ErrLoginTicketInvalid, valeurObtenue %v", err)
	}
}

func newSecondFactorFixture(t *testing.T) (*webAuthnFixture, *virtualKey) {
	t.Helper()
	fixture := newWebAuthnFixture(t)
	key := newVirtualKey(virtualwebauthn.AuthenticatorOptions{})
	fixture.register(t, key, credentialKindSecondFactor)
	return fixture, key
}

func (fixture *webAuthnFixture) finishSecondFactor(t *testing.T, ticket string, ceremony WebAuthnCeremony, key *virtualKey) (SessionGrant, error) {
	t.Helper()
	body := key.assert(t, ceremony.Options, testRelyingParty)
	return fixture.service.FinishWebAuthnLogin(context.Background(), ticket, ceremony.State, strings.NewReader(body), testClient)
}

func TestSecondFactorLogin_requiresAFreshTicketIssuedByLogin(t *testing.T) {
	ctx := context.Background()
	t.Run("no ticket or a forged one", func(t *testing.T) {
		fixture, key := newSecondFactorFixture(t)
		ceremony := fixture.beginSecondFactor(t, fixture.loginTicket(t))
		for _, presented := range []string{"", "forged-ticket"} {
			_, err := fixture.service.BeginWebAuthnLogin(ctx, presented)
			expectTicketRefused(t, err)
			_, err = fixture.finishSecondFactor(t, presented, ceremony, key)
			expectTicketRefused(t, err)
			fixture.passThrottleDelay()
		}
	})
	t.Run("a state issued for another ticket", func(t *testing.T) {
		fixture, key := newSecondFactorFixture(t)
		firstTicket := fixture.loginTicket(t)
		secondTicket := fixture.loginTicket(t)
		ceremony := fixture.beginSecondFactor(t, firstTicket)
		_, err := fixture.finishSecondFactor(t, secondTicket, ceremony, key)
		expectInvalidAssertion(t, err)
	})
	t.Run("an expired ticket", func(t *testing.T) {
		fixture, key := newSecondFactorFixture(t)
		ticket := fixture.loginTicket(t)
		ceremony := fixture.beginSecondFactor(t, ticket)
		fixture.clock.advance(loginTicketLifetime)
		_, err := fixture.service.BeginWebAuthnLogin(ctx, ticket)
		expectTicketRefused(t, err)
		_, err = fixture.finishSecondFactor(t, ticket, ceremony, key)
		expectTicketRefused(t, err)
	})
	t.Run("a ticket already used", func(t *testing.T) {
		fixture, key := newSecondFactorFixture(t)
		ticket := fixture.loginTicket(t)
		ceremony := fixture.beginSecondFactor(t, ticket)
		if _, err := fixture.finishSecondFactor(t, ticket, ceremony, key); err != nil {
			t.Fatal(err)
		}
		_, err := fixture.service.BeginWebAuthnLogin(ctx, ticket)
		expectTicketRefused(t, err)
		_, err = fixture.finishSecondFactor(t, ticket, ceremony, key)
		expectTicketRefused(t, err)
	})
	t.Run("a ticket from a previous account", func(t *testing.T) {
		fixture, key := newSecondFactorFixture(t)
		ticket := fixture.loginTicket(t)
		ceremony := fixture.beginSecondFactor(t, ticket)
		if err := fixture.service.ResetAccount(ctx); err != nil {
			t.Fatal(err)
		}
		fixture.grant = fixture.setup(t)
		fixture.register(t, key, credentialKindSecondFactor)
		_, err := fixture.service.BeginWebAuthnLogin(ctx, ticket)
		expectTicketRefused(t, err)
		_, err = fixture.finishSecondFactor(t, ticket, ceremony, key)
		expectTicketRefused(t, err)
	})
}

func TestTicket_isInvalidatedByChangePasswordAndByResetAccount(t *testing.T) {
	ctx := context.Background()
	changes := map[string]func(fixture *webAuthnFixture) error{
		"change password": func(fixture *webAuthnFixture) error {
			return fixture.service.ChangePassword(ctx, fixture.grant.Token, testPassword, testNewPassword)
		},
		"reset account": func(fixture *webAuthnFixture) error { return fixture.service.ResetAccount(ctx) },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			fixture, _ := newSecondFactorFixture(t)
			ticket := fixture.loginTicket(t)
			if err := change(fixture); err != nil {
				t.Fatal(err)
			}

			_, err := fixture.service.BeginWebAuthnLogin(ctx, ticket)

			expectTicketRefused(t, err)
		})
	}
}

func TestBeginWebAuthnLogin_requiresATicketAndRevealsNothingWithout(t *testing.T) {
	ctx := context.Background()
	withoutKeys := newWebAuthnFixture(t)
	withKeys, _ := newSecondFactorFixture(t)

	for _, fixture := range []*webAuthnFixture{withoutKeys, withKeys} {
		for _, presented := range []string{"", "forged-ticket"} {
			ceremony, err := fixture.service.BeginWebAuthnLogin(ctx, presented)
			expectTicketRefused(t, err)
			if len(ceremony.Options) != 0 || len(ceremony.State) != 0 {
				t.Fatalf("valeurAttendue aucune option sans ticket, valeurObtenue %+v", ceremony)
			}
		}
	}
	result, err := withoutKeys.service.Login(ctx, testUsername, testPassword, testClient)
	if err != nil || result.Ticket != "" || result.Session == nil {
		t.Fatalf("valeurAttendue une session sans ticket, valeurObtenue %+v err=%v", result, err)
	}
}

func TestFinishWebAuthnLogin_burnsTheTicketEvenOnFailure(t *testing.T) {
	fixture, key := newSecondFactorFixture(t)
	ctx := context.Background()
	ticket := fixture.loginTicket(t)
	ceremony := fixture.beginSecondFactor(t, ticket)
	sessionsBefore := fixture.sessionCount(t)

	_, err := fixture.finishSecondFactor(t, ticket, ceremony, newVirtualKey(virtualwebauthn.AuthenticatorOptions{}))
	expectInvalidAssertion(t, err)

	fixture.passThrottleDelay()
	_, err = fixture.service.BeginWebAuthnLogin(ctx, ticket)
	expectTicketRefused(t, err)
	_, err = fixture.finishSecondFactor(t, ticket, ceremony, key)
	expectTicketRefused(t, err)
	if valeurObtenue := fixture.sessionCount(t); valeurObtenue != sessionsBefore {
		t.Fatalf("valeurAttendue %d sessions, valeurObtenue %d", sessionsBefore, valeurObtenue)
	}
}

func TestFinishWebAuthnLogin_aThrottledAttemptKeepsTheTicket(t *testing.T) {
	fixture, key := newSecondFactorFixture(t)
	ctx := context.Background()
	ticket := fixture.loginTicket(t)
	ceremony := fixture.beginSecondFactor(t, ticket)
	body := key.assert(t, ceremony.Options, testRelyingParty)
	_, err := fixture.service.FinishPasskeyLogin(ctx, []byte("not json"), strings.NewReader("{}"), testClient)
	expectInvalidAssertion(t, err)

	_, err = fixture.service.FinishWebAuthnLogin(ctx, ticket, ceremony.State, strings.NewReader(body), testClient)
	var throttled *ThrottledError
	if !errors.As(err, &throttled) {
		t.Fatalf("valeurAttendue ThrottledError, valeurObtenue %v", err)
	}

	fixture.passThrottleDelay()
	if _, err := fixture.service.FinishWebAuthnLogin(ctx, ticket, ceremony.State, strings.NewReader(body), testClient); err != nil {
		t.Fatalf("valeurAttendue une session avec le ticket gardé, valeurObtenue %v", err)
	}
}

func TestStateEnvelope_aLoginStateCannotFinishARegistrationAndViceVersa(t *testing.T) {
	fixture, _ := newSecondFactorFixture(t)
	ctx := context.Background()

	ticket := fixture.loginTicket(t)
	loginCeremony := fixture.beginSecondFactor(t, ticket)
	loginOptions, err := virtualwebauthn.ParseAssertionOptions(string(loginCeremony.Options))
	if err != nil {
		t.Fatal(err)
	}
	attacker := newVirtualKey(virtualwebauthn.AuthenticatorOptions{})
	attestationOverTheLoginChallenge := virtualwebauthn.CreateAttestationResponse(testRelyingParty, attacker.authenticator,
		attacker.credential, virtualwebauthn.AttestationOptions{Challenge: loginOptions.Challenge, RelyingPartyID: testRelyingParty.ID})
	loginStateWithoutTicket := rewriteEnvelope(t, fixture.service, loginCeremony.State, func(envelope *ceremonyEnvelope) {
		envelope.TicketHash, envelope.Kind = "", credentialKindSecondFactor
		envelope.Session.CredParams = webauthn.CredentialParametersDefault()
	})
	for _, loginState := range [][]byte{loginCeremony.State, loginStateWithoutTicket} {
		err = fixture.service.FinishWebAuthnRegistration(ctx, fixture.grant.Token, loginState, testKeyName,
			strings.NewReader(attestationOverTheLoginChallenge))
		expectInvalidAssertion(t, err)
	}
	if valeurObtenue := len(fixture.storedCredentials(t)); valeurObtenue != 1 {
		t.Fatalf("valeurAttendue la seule clé enregistrée d'origine, valeurObtenue %d", valeurObtenue)
	}

	registration := fixture.beginRegistration(t, credentialKindSecondFactor)
	_, err = fixture.service.FinishWebAuthnLogin(ctx, ticket, registration.State, strings.NewReader("{}"), testClient)
	expectInvalidAssertion(t, err)
	fixture.passThrottleDelay()
	_, err = fixture.service.FinishPasskeyLogin(ctx, registration.State, strings.NewReader("{}"), testClient)
	expectInvalidAssertion(t, err)
}

func TestStateEnvelope_refusesAZeroOrPastExpiry(t *testing.T) {
	fixture := newWebAuthnFixture(t)
	key := newVirtualKey(virtualwebauthn.AuthenticatorOptions{})
	fixture.register(t, key, credentialKindPasskey)
	ctx := context.Background()

	ceremony, err := fixture.service.BeginPasskeyLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	body := key.assert(t, ceremony.Options, testRelyingParty)
	zeroExpiry := rewriteState(t, fixture.service, ceremony.State, func(session *webauthn.SessionData) { session.Expires = time.Time{} })
	_, err = fixture.service.FinishPasskeyLogin(ctx, zeroExpiry, strings.NewReader(body), testClient)
	expectInvalidAssertion(t, err)

	fixture.clock.advance(webAuthnCeremonyTimeout)
	_, err = fixture.service.FinishPasskeyLogin(ctx, ceremony.State, strings.NewReader(body), testClient)
	expectInvalidAssertion(t, err)

	fixture.passThrottleDelay()
	registration := fixture.beginRegistration(t, credentialKindSecondFactor)
	attestation := newVirtualKey(virtualwebauthn.AuthenticatorOptions{}).attest(t, registration.Options)
	fixture.clock.advance(webAuthnCeremonyTimeout)
	err = fixture.service.FinishWebAuthnRegistration(ctx, fixture.grant.Token, registration.State, testKeyName, strings.NewReader(attestation))
	expectInvalidAssertion(t, err)
}

func TestWebAuthnReplay_aCounterZeroKeyCannotReplayAState(t *testing.T) {
	fixture := newWebAuthnFixture(t)
	key := newVirtualKey(virtualwebauthn.AuthenticatorOptions{})
	key.counterStaysZero = true
	fixture.register(t, key, credentialKindPasskey)
	ctx := context.Background()
	ceremony, err := fixture.service.BeginPasskeyLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	body := key.assert(t, ceremony.Options, testRelyingParty)
	if _, err := fixture.service.FinishPasskeyLogin(ctx, ceremony.State, strings.NewReader(body), testClient); err != nil {
		t.Fatal(err)
	}
	sessionsBefore := fixture.sessionCount(t)

	_, err = fixture.service.FinishPasskeyLogin(ctx, ceremony.State, strings.NewReader(body), testClient)

	expectInvalidAssertion(t, err)
	if valeurObtenue := fixture.sessionCount(t); valeurObtenue != sessionsBefore {
		t.Fatalf("valeurAttendue %d sessions après le rejeu, valeurObtenue %d", sessionsBefore, valeurObtenue)
	}
	_, err = fixture.service.Login(ctx, testUsername, testPassword, testClient)
	var throttled *ThrottledError
	if !errors.As(err, &throttled) {
		t.Fatalf("valeurAttendue le rejeu compté comme échec, valeurObtenue %v", err)
	}
}

func TestExpiringEntries_isBoundedAndPurgesExpiredEntries(t *testing.T) {
	var entries expiringEntries[struct{}]
	now := time.Unix(1_800_000_000, 0)
	expiresAt := now.Add(time.Minute)
	for entryIndex := range maxExpiringEntries {
		if !entries.insertIfAbsent(strconv.Itoa(entryIndex), struct{}{}, expiresAt, now) {
			t.Fatalf("valeurAttendue l'entrée %d admise", entryIndex)
		}
	}

	duplicateAdmitted := entries.insertIfAbsent("0", struct{}{}, expiresAt, now)
	overflowAdmitted := entries.insertIfAbsent("overflow", struct{}{}, expiresAt, now)
	later := expiresAt
	admittedAfterExpiry := entries.insertIfAbsent("overflow", struct{}{}, later.Add(time.Minute), later)

	if duplicateAdmitted || overflowAdmitted || !admittedAfterExpiry {
		t.Fatalf("valeurAttendue doublon et débordement refusés puis admis après expiration, valeurObtenue %v %v %v",
			duplicateAdmitted, overflowAdmitted, admittedAfterExpiry)
	}
	if _, found := entries.peek("1", later); found {
		t.Fatal("valeurAttendue une entrée expirée introuvable")
	}
}
