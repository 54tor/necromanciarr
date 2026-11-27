package app

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/descope/virtualwebauthn"
)

func (set *expiringEntries[Value]) size() int {
	set.mutex.Lock()
	defer set.mutex.Unlock()
	return len(set.entries)
}

func splitSealedState(t *testing.T, state []byte) (payload, mac []byte) {
	t.Helper()
	encodedPayload, encodedMAC, found := bytes.Cut(state, []byte("."))
	if !found {
		t.Fatalf("valeurAttendue charge.mac, valeurObtenue %q", state)
	}
	payload, err := base64.RawURLEncoding.DecodeString(string(encodedPayload))
	if err != nil {
		t.Fatal(err)
	}
	mac, err = base64.RawURLEncoding.DecodeString(string(encodedMAC))
	if err != nil {
		t.Fatal(err)
	}
	return payload, mac
}

func joinSealedState(payload, mac []byte) []byte {
	return []byte(base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac))
}

func randomBytes(t *testing.T, length int) []byte {
	t.Helper()
	value := make([]byte, length)
	if _, err := rand.Read(value); err != nil {
		t.Fatal(err)
	}
	return value
}

func signedWithAnotherKey(t *testing.T, payload []byte) []byte {
	t.Helper()
	signer := hmac.New(sha256.New, randomBytes(t, stateSealKeyBytes))
	signer.Write([]byte(base64.RawURLEncoding.EncodeToString(payload)))
	return joinSealedState(payload, signer.Sum(nil))
}

func forgedEnvelope(t *testing.T, genuinePayload []byte) []byte {
	t.Helper()
	var envelope ceremonyEnvelope
	if err := json.Unmarshal(genuinePayload, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope.Nonce = randomBytes(t, ceremonyNonceBytes)
	payload, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return joinSealedState(payload, randomBytes(t, sha256.Size))
}

func TestSealedState_refusesATamperedOrForgedStateWithoutBurningANonce(t *testing.T) {
	fixture := newWebAuthnFixture(t)
	key := newVirtualKey(virtualwebauthn.AuthenticatorOptions{})
	fixture.register(t, key, credentialKindPasskey)
	ctx := context.Background()
	ceremony, err := fixture.service.BeginPasskeyLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	body := key.assert(t, ceremony.Options, testRelyingParty)
	payload, mac := splitSealedState(t, ceremony.State)
	flippedPayload := bytes.Clone(payload)
	flippedPayload[len(flippedPayload)/2] ^= 0x01
	forgedStates := map[string][]byte{
		"one flipped byte":      joinSealedState(flippedPayload, mac),
		"signed with other key": signedWithAnotherKey(t, payload),
		"truncated mac":         joinSealedState(payload, mac[:len(mac)-1]),
		"empty state":           {},
		"forged envelope":       forgedEnvelope(t, payload),
		"no separator":          []byte(base64.RawURLEncoding.EncodeToString(payload)),
	}
	burnedBefore := fixture.service.burnedNonces.size()

	for name, forgedState := range forgedStates {
		fixture.passThrottleDelay()
		_, err := fixture.service.FinishPasskeyLogin(ctx, forgedState, strings.NewReader(body), testClient)
		if !errors.Is(err, ErrInvalidAssertion) {
			t.Fatalf("%s : valeurAttendue ErrInvalidAssertion, valeurObtenue %v", name, err)
		}
		if valeurObtenue := fixture.service.burnedNonces.size(); valeurObtenue != burnedBefore {
			t.Fatalf("%s : valeurAttendue %d nonces brûlés, valeurObtenue %d", name, burnedBefore, valeurObtenue)
		}
	}

	fixture.passThrottleDelay()
	genuine, err := fixture.service.BeginPasskeyLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	genuineBody := key.assert(t, genuine.Options, testRelyingParty)
	if _, err := fixture.service.FinishPasskeyLogin(ctx, genuine.State, strings.NewReader(genuineBody), testClient); err != nil {
		t.Fatalf("valeurAttendue l'état authentique accepté une fois, valeurObtenue %v", err)
	}
	_, err = fixture.service.FinishPasskeyLogin(ctx, genuine.State, strings.NewReader(genuineBody), testClient)
	expectInvalidAssertion(t, err)
}

func TestSealedState_eachServiceSignsWithItsOwnKey(t *testing.T) {
	issuer := newWebAuthnFixture(t)
	verifier := newWebAuthnFixture(t)
	ceremony, err := issuer.service.BeginPasskeyLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	_, err = verifier.service.unsealState(ceremony.State)

	expectInvalidAssertion(t, err)
}

func TestLoginTicket_aFullSetEvictsTheOldestTicket(t *testing.T) {
	fixture, _ := newSecondFactorFixture(t)
	ctx := context.Background()
	oldest := fixture.loginTicket(t)
	for range maxExpiringEntries {
		if _, err := fixture.service.issueLoginTicket(ctx); err != nil {
			t.Fatal(err)
		}
	}

	fresh := fixture.loginTicket(t)

	if _, err := fixture.service.BeginWebAuthnLogin(ctx, fresh); err != nil {
		t.Fatalf("valeurAttendue le ticket récent accepté, valeurObtenue %v", err)
	}
	_, err := fixture.service.BeginWebAuthnLogin(ctx, oldest)
	expectTicketRefused(t, err)
}

func TestFinishWebAuthnRegistration_anInvalidTokenLeavesTheStateUsable(t *testing.T) {
	fixture := newWebAuthnFixture(t)
	ctx := context.Background()
	key := newVirtualKey(virtualwebauthn.AuthenticatorOptions{})
	ceremony := fixture.beginRegistration(t, credentialKindSecondFactor)
	body := key.attest(t, ceremony.Options)

	err := fixture.service.FinishWebAuthnRegistration(ctx, "forged-token", ceremony.State, testKeyName, strings.NewReader(body))
	if !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("valeurAttendue ErrSessionInvalid, valeurObtenue %v", err)
	}

	if err := fixture.service.FinishWebAuthnRegistration(ctx, fixture.grant.Token, ceremony.State, testKeyName, strings.NewReader(body)); err != nil {
		t.Fatalf("valeurAttendue l'état toujours utilisable, valeurObtenue %v", err)
	}
}
