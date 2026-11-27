package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/descope/virtualwebauthn"
	"github.com/go-webauthn/webauthn/webauthn"

	"necromanciarr/internal/config"
	"necromanciarr/internal/store"
)

const testKeyName = "Desk key"

var testRelyingParty = virtualwebauthn.RelyingParty{
	Name: webAuthnDisplayName, ID: "example.com", Origin: "https://example.com",
}

var testWebAuthnConfig = config.Config{PublicURL: testRelyingParty.Origin}

// virtualKey est un authentificateur virtuel portant une seule credential.
type virtualKey struct {
	authenticator virtualwebauthn.Authenticator
	credential    virtualwebauthn.Credential
}

func newVirtualKey(options virtualwebauthn.AuthenticatorOptions) *virtualKey {
	return &virtualKey{
		authenticator: virtualwebauthn.NewAuthenticatorWithOptions(options),
		credential:    virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2),
	}
}

func (key *virtualKey) attest(t *testing.T, options json.RawMessage) string {
	t.Helper()
	return key.attestFor(t, options, testRelyingParty)
}

func (key *virtualKey) attestFor(t *testing.T, options json.RawMessage, relyingParty virtualwebauthn.RelyingParty) string {
	t.Helper()
	parsedOptions, err := virtualwebauthn.ParseAttestationOptions(string(options))
	if err != nil {
		t.Fatal(err)
	}
	key.authenticator.Options.UserHandle = []byte(parsedOptions.UserID)
	return virtualwebauthn.CreateAttestationResponse(relyingParty, key.authenticator, key.credential, *parsedOptions)
}

// assert signe une assertion comme un vrai authentificateur : le compteur avance à chaque usage.
func (key *virtualKey) assert(t *testing.T, options json.RawMessage, relyingParty virtualwebauthn.RelyingParty) string {
	t.Helper()
	parsedOptions, err := virtualwebauthn.ParseAssertionOptions(string(options))
	if err != nil {
		t.Fatal(err)
	}
	key.credential.Counter++
	return virtualwebauthn.CreateAssertionResponse(relyingParty, key.authenticator, key.credential, *parsedOptions)
}

func (key *virtualKey) encodedID() string {
	return base64.RawURLEncoding.EncodeToString(key.credential.ID)
}

type webAuthnFixture struct {
	*accountFixture
	grant SessionGrant
}

func newWebAuthnFixture(t *testing.T) *webAuthnFixture {
	t.Helper()
	fixture := newAccountFixture(t)
	fixture.service.deps.Config = testWebAuthnConfig
	return &webAuthnFixture{accountFixture: fixture, grant: fixture.setup(t)}
}

func (fixture *webAuthnFixture) beginRegistration(t *testing.T, kind string) WebAuthnCeremony {
	t.Helper()
	ceremony, err := fixture.service.BeginWebAuthnRegistration(context.Background(), fixture.grant.Token, testPassword, kind)
	if err != nil {
		t.Fatal(err)
	}
	return ceremony
}

func (fixture *webAuthnFixture) tryRegister(t *testing.T, key *virtualKey, kind string) error {
	t.Helper()
	ceremony := fixture.beginRegistration(t, kind)
	body := key.attest(t, ceremony.Options)
	return fixture.service.FinishWebAuthnRegistration(context.Background(), fixture.grant.Token, ceremony.State, testKeyName, strings.NewReader(body))
}

func (fixture *webAuthnFixture) register(t *testing.T, key *virtualKey, kind string) {
	t.Helper()
	if err := fixture.tryRegister(t, key, kind); err != nil {
		t.Fatal(err)
	}
}

func (fixture *webAuthnFixture) secondFactorLogin(t *testing.T, key *virtualKey) (SessionGrant, error) {
	t.Helper()
	ceremony, err := fixture.service.BeginWebAuthnLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	body := key.assert(t, ceremony.Options, testRelyingParty)
	return fixture.service.FinishWebAuthnLogin(context.Background(), ceremony.State, strings.NewReader(body), testClient)
}

func (fixture *webAuthnFixture) passkeyLogin(t *testing.T, key *virtualKey) (SessionGrant, error) {
	t.Helper()
	ceremony, err := fixture.service.BeginPasskeyLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	body := key.assert(t, ceremony.Options, testRelyingParty)
	return fixture.service.FinishPasskeyLogin(context.Background(), ceremony.State, strings.NewReader(body), testClient)
}

func (fixture *webAuthnFixture) sessionCount(t *testing.T) int {
	t.Helper()
	sessions, err := fixture.store.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return len(sessions)
}

func (fixture *webAuthnFixture) storedCredentials(t *testing.T) []store.WebAuthnCredential {
	t.Helper()
	credentials, err := fixture.store.Credentials(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return credentials
}

func expectInvalidAssertion(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrInvalidAssertion) || err.Error() != ErrInvalidAssertion.Error() {
		t.Fatalf("valeurAttendue exactement ErrInvalidAssertion, valeurObtenue %v", err)
	}
}

func rewriteState(t *testing.T, state []byte, change func(session *webauthn.SessionData)) []byte {
	t.Helper()
	var session webauthn.SessionData
	if err := json.Unmarshal(state, &session); err != nil {
		t.Fatal(err)
	}
	change(&session)
	rewritten, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	return rewritten
}

func TestWebAuthnRegistration_storesTheCredentialForTheAccount(t *testing.T) {
	fixture := newWebAuthnFixture(t)
	key := newVirtualKey(virtualwebauthn.AuthenticatorOptions{})

	fixture.register(t, key, credentialKindSecondFactor)

	views, err := fixture.service.ListCredentials(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	valeurAttendue := CredentialView{ID: key.encodedID(), Name: testKeyName, Kind: credentialKindSecondFactor, CreatedAt: fixture.clock.now()}
	if len(views) != 1 || views[0] != valeurAttendue {
		t.Fatalf("valeurAttendue [%+v], valeurObtenue %+v", valeurAttendue, views)
	}
	if stored := fixture.storedCredentials(t); len(stored) != 1 || len(stored[0].Record) == 0 {
		t.Fatalf("valeurAttendue une credential avec son enregistrement, valeurObtenue %+v", stored)
	}
}

func TestWebAuthnRegistration_requiresASessionAndThePassword(t *testing.T) {
	fixture := newWebAuthnFixture(t)
	ctx := context.Background()

	_, err := fixture.service.BeginWebAuthnRegistration(ctx, "forged-token", testPassword, credentialKindSecondFactor)
	if !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("valeurAttendue ErrSessionInvalid, valeurObtenue %v", err)
	}
	_, err = fixture.service.BeginWebAuthnRegistration(ctx, fixture.grant.Token, testWrong, credentialKindSecondFactor)
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("valeurAttendue ErrInvalidCredentials, valeurObtenue %v", err)
	}
	_, err = fixture.service.BeginWebAuthnRegistration(ctx, fixture.grant.Token, testPassword, "unknown")
	if !errors.Is(err, ErrInvalidCredentialKind) {
		t.Fatalf("valeurAttendue ErrInvalidCredentialKind, valeurObtenue %v", err)
	}
	fixture.passThrottleDelay()
	ceremony := fixture.beginRegistration(t, credentialKindSecondFactor)
	body := newVirtualKey(virtualwebauthn.AuthenticatorOptions{}).attest(t, ceremony.Options)
	if err := fixture.service.FinishWebAuthnRegistration(ctx, fixture.grant.Token, ceremony.State, " \x07 ", strings.NewReader(body)); !errors.Is(err, ErrInvalidCredentialName) {
		t.Fatalf("valeurAttendue ErrInvalidCredentialName, valeurObtenue %v", err)
	}
	if err := fixture.service.Logout(ctx, fixture.grant.Token); err != nil {
		t.Fatal(err)
	}

	err = fixture.service.FinishWebAuthnRegistration(ctx, fixture.grant.Token, ceremony.State, testKeyName, strings.NewReader(body))

	if !errors.Is(err, ErrSessionInvalid) || len(fixture.storedCredentials(t)) != 0 {
		t.Fatalf("valeurAttendue ErrSessionInvalid sans rien stocker, valeurObtenue %v", err)
	}
}

func TestWebAuthnLogin_createsASessionOnlyAfterAValidAssertion(t *testing.T) {
	fixture := newWebAuthnFixture(t)
	key := newVirtualKey(virtualwebauthn.AuthenticatorOptions{})
	fixture.register(t, key, credentialKindSecondFactor)
	sessionsBefore := fixture.sessionCount(t)

	_, err := fixture.secondFactorLogin(t, newVirtualKey(virtualwebauthn.AuthenticatorOptions{}))

	expectInvalidAssertion(t, err)
	if valeurObtenue := fixture.sessionCount(t); valeurObtenue != sessionsBefore {
		t.Fatalf("valeurAttendue %d sessions, valeurObtenue %d", sessionsBefore, valeurObtenue)
	}
	fixture.passThrottleDelay()
	grant, err := fixture.secondFactorLogin(t, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.ResumeSession(context.Background(), grant.Token); err != nil {
		t.Fatalf("valeurAttendue une session valide, valeurObtenue %v", err)
	}
}

func TestWebAuthnLogin_refusesAForgedChallengeAndAWrongOrigin(t *testing.T) {
	fixture := newWebAuthnFixture(t)
	key := newVirtualKey(virtualwebauthn.AuthenticatorOptions{})
	fixture.register(t, key, credentialKindSecondFactor)
	ctx := context.Background()
	sessionsBefore := fixture.sessionCount(t)

	genuine, err := fixture.service.BeginWebAuthnLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	other, err := fixture.service.BeginWebAuthnLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	forgedChallenge := key.assert(t, other.Options, testRelyingParty)
	_, err = fixture.service.FinishWebAuthnLogin(ctx, genuine.State, strings.NewReader(forgedChallenge), testClient)
	expectInvalidAssertion(t, err)

	fixture.passThrottleDelay()
	wrongOrigin := testRelyingParty
	wrongOrigin.Origin = "https://evil.example.com"
	wrongOriginBody := key.assert(t, genuine.Options, wrongOrigin)
	_, err = fixture.service.FinishWebAuthnLogin(ctx, genuine.State, strings.NewReader(wrongOriginBody), testClient)
	expectInvalidAssertion(t, err)

	if valeurObtenue := fixture.sessionCount(t); valeurObtenue != sessionsBefore {
		t.Fatalf("valeurAttendue %d sessions, valeurObtenue %d", sessionsBefore, valeurObtenue)
	}
}

func storedSignCount(t *testing.T, credential store.WebAuthnCredential) uint32 {
	t.Helper()
	var record webauthn.Credential
	if err := json.Unmarshal(credential.Record, &record); err != nil {
		t.Fatal(err)
	}
	return record.Authenticator.SignCount
}

func TestWebAuthnLogin_updatesTheSignCountAndRefusesARollback(t *testing.T) {
	fixture := newWebAuthnFixture(t)
	key := newVirtualKey(virtualwebauthn.AuthenticatorOptions{})
	key.credential.Counter = 5
	fixture.register(t, key, credentialKindSecondFactor)
	fixture.clock.advance(time.Minute)

	if _, err := fixture.secondFactorLogin(t, key); err != nil {
		t.Fatal(err)
	}

	stored := fixture.storedCredentials(t)[0]
	valeurAttendue := uint32(6)
	if valeurObtenue := storedSignCount(t, stored); valeurObtenue != valeurAttendue {
		t.Fatalf("valeurAttendue compteur %d, valeurObtenue %d", valeurAttendue, valeurObtenue)
	}
	if stored.LastUsedAt == nil || !stored.LastUsedAt.Equal(fixture.clock.now()) {
		t.Fatalf("valeurAttendue dernier usage %v, valeurObtenue %v", fixture.clock.now(), stored.LastUsedAt)
	}
	for _, rolledBackCounter := range []uint32{5, 2} {
		fixture.passThrottleDelay()
		sessionsBefore := fixture.sessionCount(t)
		key.credential.Counter = rolledBackCounter - 1
		_, err := fixture.secondFactorLogin(t, key)
		expectInvalidAssertion(t, err)
		if fixture.sessionCount(t) != sessionsBefore || storedSignCount(t, fixture.storedCredentials(t)[0]) != valeurAttendue {
			t.Fatalf("valeurAttendue ni session ni compteur changé pour le compteur %d", rolledBackCounter)
		}
	}
}

func TestWebAuthn_isUnavailableOverPlainHttp(t *testing.T) {
	fixture := newAccountFixture(t)
	fixture.service.deps.Config = config.Config{PublicURL: "http://example.com", ListenAddr: "0.0.0.0:8787"}
	grant := fixture.setup(t)
	ctx := context.Background()
	body := func() *strings.Reader { return strings.NewReader("{}") }
	_, beginRegistrationErr := fixture.service.BeginWebAuthnRegistration(ctx, grant.Token, testPassword, credentialKindPasskey)
	_, beginLoginErr := fixture.service.BeginWebAuthnLogin(ctx)
	_, beginPasskeyErr := fixture.service.BeginPasskeyLogin(ctx)
	_, finishLoginErr := fixture.service.FinishWebAuthnLogin(ctx, []byte("{}"), body(), testClient)
	_, finishPasskeyErr := fixture.service.FinishPasskeyLogin(ctx, []byte("{}"), body(), testClient)
	finishRegistrationErr := fixture.service.FinishWebAuthnRegistration(ctx, grant.Token, []byte("{}"), testKeyName, body())

	for _, err := range []error{beginRegistrationErr, beginLoginErr, beginPasskeyErr, finishLoginErr, finishPasskeyErr, finishRegistrationErr} {
		if !errors.Is(err, ErrWebAuthnUnavailable) {
			t.Fatalf("valeurAttendue ErrWebAuthnUnavailable, valeurObtenue %v", err)
		}
	}
}

func TestDeleteCredential_requiresThePassword(t *testing.T) {
	fixture := newWebAuthnFixture(t)
	key := newVirtualKey(virtualwebauthn.AuthenticatorOptions{})
	fixture.register(t, key, credentialKindSecondFactor)
	ctx := context.Background()

	if err := fixture.service.DeleteCredential(ctx, "forged-token", key.encodedID(), testPassword); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("valeurAttendue ErrSessionInvalid, valeurObtenue %v", err)
	}
	if err := fixture.service.DeleteCredential(ctx, fixture.grant.Token, key.encodedID(), testWrong); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("valeurAttendue ErrInvalidCredentials, valeurObtenue %v", err)
	}
	if valeurObtenue := len(fixture.storedCredentials(t)); valeurObtenue != 1 {
		t.Fatalf("valeurAttendue la clé gardée, valeurObtenue %d clés", valeurObtenue)
	}
	fixture.passThrottleDelay()
	if err := fixture.service.DeleteCredential(ctx, fixture.grant.Token, "AAAA", testPassword); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("valeurAttendue ErrCredentialNotFound, valeurObtenue %v", err)
	}

	if err := fixture.service.DeleteCredential(ctx, fixture.grant.Token, key.encodedID(), testPassword); err != nil {
		t.Fatal(err)
	}

	if valeurObtenue := len(fixture.storedCredentials(t)); valeurObtenue != 0 {
		t.Fatalf("valeurAttendue aucune clé, valeurObtenue %d", valeurObtenue)
	}
}

func TestDeleteCredential_lastKeyReturnsLoginToPasswordOnly(t *testing.T) {
	fixture := newWebAuthnFixture(t)
	key := newVirtualKey(virtualwebauthn.AuthenticatorOptions{})
	fixture.register(t, key, credentialKindSecondFactor)
	ctx := context.Background()
	if err := fixture.service.DeleteCredential(ctx, fixture.grant.Token, key.encodedID(), testPassword); err != nil {
		t.Fatal(err)
	}

	result, err := fixture.service.Login(ctx, testUsername, testPassword, testClient)

	if err != nil || result.Session == nil || len(result.SecondFactors) != 0 {
		t.Fatalf("valeurAttendue une session au mot de passe seul, valeurObtenue %+v err=%v", result, err)
	}
}

func TestRenameCredential_validatesTheNameAndReportsAMissingKey(t *testing.T) {
	fixture := newWebAuthnFixture(t)
	key := newVirtualKey(virtualwebauthn.AuthenticatorOptions{})
	fixture.register(t, key, credentialKindSecondFactor)
	ctx := context.Background()

	if err := fixture.service.RenameCredential(ctx, key.encodedID(), strings.Repeat("x", 65)); !errors.Is(err, ErrInvalidCredentialName) {
		t.Fatalf("valeurAttendue ErrInvalidCredentialName, valeurObtenue %v", err)
	}
	if err := fixture.service.RenameCredential(ctx, "AAAA", "Spare key"); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("valeurAttendue ErrCredentialNotFound, valeurObtenue %v", err)
	}
	if err := fixture.service.RenameCredential(ctx, key.encodedID(), "  Spare key  "); err != nil {
		t.Fatal(err)
	}

	valeurAttendue := "Spare key"
	if valeurObtenue := fixture.storedCredentials(t)[0].Name; valeurObtenue != valeurAttendue {
		t.Fatalf("valeurAttendue %q, valeurObtenue %q", valeurAttendue, valeurObtenue)
	}
}

func TestLogin_neverIssuesASessionWhileASecondFactorExists(t *testing.T) {
	fixture := newWebAuthnFixture(t)
	fixture.register(t, newVirtualKey(virtualwebauthn.AuthenticatorOptions{}), credentialKindPasskey)
	fixture.register(t, newVirtualKey(virtualwebauthn.AuthenticatorOptions{}), credentialKindSecondFactor)
	sessionsBefore := fixture.sessionCount(t)

	result, err := fixture.service.Login(context.Background(), testUsername, testPassword, testClient)
	if err != nil {
		t.Fatal(err)
	}

	if result.Session != nil || len(result.SecondFactors) != 1 || result.SecondFactors[0] != SecondFactorWebAuthn {
		t.Fatalf("valeurAttendue le facteur webauthn sans session, valeurObtenue %+v", result)
	}
	if valeurObtenue := fixture.sessionCount(t); valeurObtenue != sessionsBefore {
		t.Fatalf("valeurAttendue %d sessions, valeurObtenue %d", sessionsBefore, valeurObtenue)
	}
}

func TestPasskeyLogin_createsASessionWithoutAPassword(t *testing.T) {
	fixture := newWebAuthnFixture(t)
	key := newVirtualKey(virtualwebauthn.AuthenticatorOptions{})
	fixture.register(t, key, credentialKindPasskey)

	grant, err := fixture.passkeyLogin(t, key)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := fixture.service.ResumeSession(context.Background(), grant.Token); err != nil {
		t.Fatalf("valeurAttendue une session valide, valeurObtenue %v", err)
	}
	if fixture.storedCredentials(t)[0].LastUsedAt == nil {
		t.Fatal("valeurAttendue le dernier usage daté")
	}
}

func TestPasskeyLogin_refusesAnAssertionWithoutUserVerification(t *testing.T) {
	fixture := newWebAuthnFixture(t)
	key := newVirtualKey(virtualwebauthn.AuthenticatorOptions{})
	fixture.register(t, key, credentialKindPasskey)
	key.authenticator.Options.UserNotVerified = true
	ctx := context.Background()
	sessionsBefore := fixture.sessionCount(t)

	_, err := fixture.passkeyLogin(t, key)
	expectInvalidAssertion(t, err)

	fixture.passThrottleDelay()
	ceremony, err := fixture.service.BeginPasskeyLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	relaxedState := rewriteState(t, ceremony.State, func(session *webauthn.SessionData) { session.UserVerification = "preferred" })
	body := key.assert(t, ceremony.Options, testRelyingParty)
	_, err = fixture.service.FinishPasskeyLogin(ctx, relaxedState, strings.NewReader(body), testClient)
	expectInvalidAssertion(t, err)

	if valeurObtenue := fixture.sessionCount(t); valeurObtenue != sessionsBefore {
		t.Fatalf("valeurAttendue %d sessions, valeurObtenue %d", sessionsBefore, valeurObtenue)
	}
}

type creationOptionsSelection struct {
	PublicKey struct {
		AuthenticatorSelection struct {
			ResidentKey        string `json:"residentKey"`
			RequireResidentKey bool   `json:"requireResidentKey"`
			UserVerification   string `json:"userVerification"`
		} `json:"authenticatorSelection"`
	} `json:"publicKey"`
}

func TestPasskeyRegistration_requiresAResidentKeyAndUserVerification(t *testing.T) {
	fixture := newWebAuthnFixture(t)
	cases := map[string]struct{ residentKey, userVerification string }{
		credentialKindPasskey:      {"required", "required"},
		credentialKindSecondFactor: {"discouraged", "preferred"},
	}
	for kind, valeurAttendue := range cases {
		var options creationOptionsSelection
		if err := json.Unmarshal(fixture.beginRegistration(t, kind).Options, &options); err != nil {
			t.Fatal(err)
		}
		selection := options.PublicKey.AuthenticatorSelection
		requiresResidentKey := valeurAttendue.residentKey == "required"
		if selection.ResidentKey != valeurAttendue.residentKey || selection.RequireResidentKey != requiresResidentKey ||
			selection.UserVerification != valeurAttendue.userVerification {
			t.Fatalf("%s : valeurAttendue %+v, valeurObtenue %+v", kind, valeurAttendue, selection)
		}
	}

	unverified := newVirtualKey(virtualwebauthn.AuthenticatorOptions{UserNotVerified: true})
	expectInvalidAssertion(t, fixture.tryRegister(t, unverified, credentialKindPasskey))
	notResident := newVirtualKey(virtualwebauthn.AuthenticatorOptions{
		ClientExtensionResults: map[string]any{"credProps": map[string]any{"rk": false}},
	})
	expectInvalidAssertion(t, fixture.tryRegister(t, notResident, credentialKindPasskey))

	if valeurObtenue := len(fixture.storedCredentials(t)); valeurObtenue != 0 {
		t.Fatalf("valeurAttendue aucune clé stockée, valeurObtenue %d", valeurObtenue)
	}
}

func TestPasskeyLogin_failuresGoThroughTheSharedThrottle(t *testing.T) {
	fixture := newWebAuthnFixture(t)
	key := newVirtualKey(virtualwebauthn.AuthenticatorOptions{})
	fixture.register(t, key, credentialKindPasskey)
	ctx := context.Background()

	_, err := fixture.passkeyLogin(t, newVirtualKey(virtualwebauthn.AuthenticatorOptions{UserHandle: []byte("someone")}))
	expectInvalidAssertion(t, err)

	_, err = fixture.service.Login(ctx, testUsername, testPassword, testClient)
	var throttled *ThrottledError
	if !errors.As(err, &throttled) {
		t.Fatalf("valeurAttendue ThrottledError sur le mot de passe, valeurObtenue %v", err)
	}
	if _, err := fixture.passkeyLogin(t, key); !errors.As(err, &throttled) {
		t.Fatalf("valeurAttendue ThrottledError sur la clé d'accès, valeurObtenue %v", err)
	}
	fixture.passThrottleDelay()
	if _, err := fixture.passkeyLogin(t, key); err != nil {
		t.Fatalf("valeurAttendue une session après le délai, valeurObtenue %v", err)
	}
}

func TestSecondFactorKey_cannotSignInAlone(t *testing.T) {
	fixture := newWebAuthnFixture(t)
	key := newVirtualKey(virtualwebauthn.AuthenticatorOptions{})
	fixture.register(t, key, credentialKindSecondFactor)
	sessionsBefore := fixture.sessionCount(t)

	_, err := fixture.passkeyLogin(t, key)

	expectInvalidAssertion(t, err)
	if valeurObtenue := fixture.sessionCount(t); valeurObtenue != sessionsBefore {
		t.Fatalf("valeurAttendue %d sessions, valeurObtenue %d", sessionsBefore, valeurObtenue)
	}
}

type requestOptionsAllowList struct {
	PublicKey struct {
		AllowCredentials []struct {
			ID string `json:"id"`
		} `json:"allowCredentials"`
	} `json:"publicKey"`
}

func TestPasskeyCredential_cannotSatisfyTheSecondFactorStep(t *testing.T) {
	fixture := newWebAuthnFixture(t)
	securityKey := newVirtualKey(virtualwebauthn.AuthenticatorOptions{})
	passkey := newVirtualKey(virtualwebauthn.AuthenticatorOptions{})
	fixture.register(t, securityKey, credentialKindSecondFactor)
	fixture.register(t, passkey, credentialKindPasskey)
	ctx := context.Background()

	ceremony, err := fixture.service.BeginWebAuthnLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var options requestOptionsAllowList
	if err := json.Unmarshal(ceremony.Options, &options); err != nil {
		t.Fatal(err)
	}
	allowed := options.PublicKey.AllowCredentials
	if len(allowed) != 1 || allowed[0].ID != securityKey.encodedID() {
		t.Fatalf("valeurAttendue la seule clé second facteur %s, valeurObtenue %+v", securityKey.encodedID(), allowed)
	}
	sessionsBefore := fixture.sessionCount(t)

	body := passkey.assert(t, ceremony.Options, testRelyingParty)
	_, err = fixture.service.FinishWebAuthnLogin(ctx, ceremony.State, strings.NewReader(body), testClient)

	expectInvalidAssertion(t, err)
	if valeurObtenue := fixture.sessionCount(t); valeurObtenue != sessionsBefore {
		t.Fatalf("valeurAttendue %d sessions, valeurObtenue %d", sessionsBefore, valeurObtenue)
	}
}

func TestWebAuthnLogin_isRefusedWithoutAnySecondFactorKey(t *testing.T) {
	fixture := newWebAuthnFixture(t)
	passkey := newVirtualKey(virtualwebauthn.AuthenticatorOptions{})
	fixture.register(t, passkey, credentialKindPasskey)
	ctx := context.Background()

	if _, err := fixture.service.BeginWebAuthnLogin(ctx); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("valeurAttendue ErrCredentialNotFound, valeurObtenue %v", err)
	}
	passkeyCeremony, err := fixture.service.BeginPasskeyLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	body := passkey.assert(t, passkeyCeremony.Options, testRelyingParty)
	_, err = fixture.service.FinishWebAuthnLogin(ctx, passkeyCeremony.State, strings.NewReader(body), testClient)

	expectInvalidAssertion(t, err)
}

func TestWebAuthn_aWrongOriginOrReplayedStateFailsWithoutStoringAnything(t *testing.T) {
	fixture := newWebAuthnFixture(t)
	ctx := context.Background()
	key := newVirtualKey(virtualwebauthn.AuthenticatorOptions{})
	ceremony := fixture.beginRegistration(t, credentialKindSecondFactor)
	evilOrigin := testRelyingParty
	evilOrigin.Origin = "https://evil.example.com"
	evilRelyingParty := virtualwebauthn.RelyingParty{Name: webAuthnDisplayName, ID: "evil.example.com", Origin: "https://evil.example.com"}
	tamperedCeremonies := []struct {
		state []byte
		body  string
	}{
		{
			rewriteState(t, ceremony.State, func(session *webauthn.SessionData) { session.Origin = evilOrigin.Origin }),
			key.attestFor(t, ceremony.Options, evilOrigin),
		},
		{
			rewriteState(t, ceremony.State, func(session *webauthn.SessionData) { session.RelyingPartyID = evilRelyingParty.ID }),
			key.attestFor(t, ceremony.Options, evilRelyingParty),
		},
		{[]byte("not json"), key.attest(t, ceremony.Options)},
	}
	for _, tampered := range tamperedCeremonies {
		err := fixture.service.FinishWebAuthnRegistration(ctx, fixture.grant.Token, tampered.state, testKeyName, strings.NewReader(tampered.body))
		expectInvalidAssertion(t, err)
	}
	if valeurObtenue := len(fixture.storedCredentials(t)); valeurObtenue != 0 {
		t.Fatalf("valeurAttendue aucune clé après un état falsifié, valeurObtenue %d", valeurObtenue)
	}
	body := key.attest(t, ceremony.Options)
	if err := fixture.service.FinishWebAuthnRegistration(ctx, fixture.grant.Token, ceremony.State, testKeyName, strings.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	replayErr := fixture.service.FinishWebAuthnRegistration(ctx, fixture.grant.Token, ceremony.State, "Replayed", strings.NewReader(body))
	expectInvalidAssertion(t, replayErr)
	if valeurObtenue := len(fixture.storedCredentials(t)); valeurObtenue != 1 {
		t.Fatalf("valeurAttendue une seule clé, valeurObtenue %d", valeurObtenue)
	}

	loginCeremony, err := fixture.service.BeginWebAuthnLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertion := key.assert(t, loginCeremony.Options, testRelyingParty)
	if _, err := fixture.service.FinishWebAuthnLogin(ctx, loginCeremony.State, strings.NewReader(assertion), testClient); err != nil {
		t.Fatal(err)
	}
	sessionsBefore := fixture.sessionCount(t)
	_, err = fixture.service.FinishWebAuthnLogin(ctx, loginCeremony.State, strings.NewReader(assertion), testClient)
	expectInvalidAssertion(t, err)
	if valeurObtenue := fixture.sessionCount(t); valeurObtenue != sessionsBefore {
		t.Fatalf("valeurAttendue %d sessions après le rejeu, valeurObtenue %d", sessionsBefore, valeurObtenue)
	}
}

func TestNew_minimalDepsRunAWebAuthnRegistrationAndLogin(t *testing.T) {
	service := New(Deps{Store: openTestStore(t), Config: testWebAuthnConfig})
	ctx := context.Background()
	grant, err := service.SetupAccount(ctx, testUsername, testPassword, testClient)
	if err != nil {
		t.Fatal(err)
	}
	key := newVirtualKey(virtualwebauthn.AuthenticatorOptions{})
	registration, err := service.BeginWebAuthnRegistration(ctx, grant.Token, testPassword, credentialKindPasskey)
	if err != nil {
		t.Fatal(err)
	}
	attestation := key.attest(t, registration.Options)
	if err := service.FinishWebAuthnRegistration(ctx, grant.Token, registration.State, testKeyName, strings.NewReader(attestation)); err != nil {
		t.Fatal(err)
	}

	login, err := service.BeginPasskeyLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertion := key.assert(t, login.Options, testRelyingParty)
	session, err := service.FinishPasskeyLogin(ctx, login.State, strings.NewReader(assertion), testClient)

	if err != nil || session.Token == "" {
		t.Fatalf("valeurAttendue une session, valeurObtenue %+v err=%v", session, err)
	}
}
