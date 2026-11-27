package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/descope/virtualwebauthn"
	"github.com/pquerna/otp/totp"

	"necromanciarr/internal/app"
	"necromanciarr/internal/config"
)

// Parcours de bout en bout sur le vrai câblage (app.NewFromConfig, base temporaire, httptest.Server) : une dépendance
// nil dans le handler ou le Service ferait échouer ces tests, ce que les tests sur faux service ne voient pas.

const (
	e2eToken       = "e2e-bootstrap-token"
	e2eUsername    = "alice"
	e2ePassword    = "correct horse battery staple"
	e2eKeyName     = "Desk key"
	e2eTOTPPeriod  = 30 * time.Second
	e2eSecretValue = 0x07
)

// e2eServer sert le vrai handler sur 127.0.0.1, appelé par le nom localhost : WebAuthn y est disponible sans publicUrl.
type e2eServer struct {
	service      app.Service
	baseURL      string
	relyingParty virtualwebauthn.RelyingParty
	client       *http.Client
}

func newE2EServer(t *testing.T) *e2eServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	settings := config.Config{
		ConfigDir: t.TempDir(), DBPath: "necromanciarr.db", ListenAddr: "127.0.0.1:" + port, AuthToken: e2eToken, LinkType: "hardlink",
		CrossSeedMode: "off", SecretKey: bytes.Repeat([]byte{e2eSecretValue}, config.SecretKeySize),
	}
	serverContext, cancel := context.WithCancel(context.Background())
	service, closer, err := app.NewFromConfig(serverContext, settings, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	handler := NewHandlerWithAssets(serverContext, service, &fakeHealth{}, settings, builtAssets())
	handler.UseLogger(NewLogger(io.Discard))
	server := httptest.NewUnstartedServer(handler)
	_ = server.Listener.Close()
	server.Listener = listener
	server.Start()
	t.Cleanup(func() {
		server.Close()
		cancel()
		handler.Wait()
		_ = closer.Close()
	})
	origin := "http://localhost:" + port
	dialer := &net.Dialer{}
	transport := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, listener.Addr().String())
	}}
	return &e2eServer{
		service: service, baseURL: origin, client: &http.Client{Transport: transport},
		relyingParty: virtualwebauthn.RelyingParty{Name: "Necromanciarr", ID: "localhost", Origin: origin},
	}
}

// browser garde ses cookies comme un navigateur : posés, remplacés, effacés par Max-Age négatif.
type browser struct {
	server  *e2eServer
	cookies map[string]string
	bearer  bool
}

func (server *e2eServer) newBrowser() *browser {
	return &browser{server: server, cookies: map[string]string{}}
}

type e2eAnswer struct {
	status int
	body   []byte
	header http.Header
}

func (answer e2eAnswer) field(t *testing.T, name string) json.RawMessage {
	t.Helper()
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(answer.body, &decoded); err != nil {
		t.Fatalf("valeurObtenue %s, valeurAttendue a JSON object: %v", answer.body, err)
	}
	return decoded[name]
}

func (answer e2eAnswer) text(t *testing.T, name string) string {
	t.Helper()
	var value string
	_ = json.Unmarshal(answer.field(t, name), &value)
	return value
}

func (answer e2eAnswer) errorCode(t *testing.T) string {
	t.Helper()
	var failure ErrorBody
	_ = json.Unmarshal(answer.field(t, "error"), &failure)
	return failure.Code
}

func (client *browser) call(t *testing.T, method, path, body string) e2eAnswer {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), method, client.server.baseURL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodGet {
		request.Header.Set("Content-Type", jsonType)
		request.Header.Set("Origin", client.server.baseURL)
	}
	if client.bearer {
		request.Header.Set("Authorization", "Bearer "+e2eToken)
	}
	for name, value := range client.cookies {
		request.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	response, err := client.server.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	read, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, cookie := range response.Cookies() {
		if cookie.MaxAge < 0 {
			delete(client.cookies, cookie.Name)
			continue
		}
		client.cookies[cookie.Name] = cookie.Value
	}
	return e2eAnswer{status: response.StatusCode, body: read, header: response.Header}
}

func expectStatus(t *testing.T, step string, answer e2eAnswer, valeurAttendue int) {
	t.Helper()
	if answer.status != valeurAttendue {
		t.Fatalf("%s: valeurObtenue %d %s, valeurAttendue %d", step, answer.status, answer.body, valeurAttendue)
	}
}

// e2eKey est un authentificateur virtuel à une credential ; le compteur avance à chaque assertion.
type e2eKey struct {
	authenticator virtualwebauthn.Authenticator
	credential    virtualwebauthn.Credential
}

func newE2EKey() *e2eKey {
	return &e2eKey{
		authenticator: virtualwebauthn.NewAuthenticatorWithOptions(virtualwebauthn.AuthenticatorOptions{}),
		credential:    virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2),
	}
}

func (key *e2eKey) attest(t *testing.T, relyingParty virtualwebauthn.RelyingParty, options json.RawMessage) string {
	t.Helper()
	parsed, err := virtualwebauthn.ParseAttestationOptions(string(options))
	if err != nil {
		t.Fatal(err)
	}
	key.authenticator.Options.UserHandle = []byte(parsed.UserID)
	return virtualwebauthn.CreateAttestationResponse(relyingParty, key.authenticator, key.credential, *parsed)
}

func (key *e2eKey) assert(t *testing.T, relyingParty virtualwebauthn.RelyingParty, options json.RawMessage) string {
	t.Helper()
	parsed, err := virtualwebauthn.ParseAssertionOptions(string(options))
	if err != nil {
		t.Fatal(err)
	}
	key.credential.Counter++
	return virtualwebauthn.CreateAssertionResponse(relyingParty, key.authenticator, key.credential, *parsed)
}

func setupJSON() string {
	return `{"username":"` + e2eUsername + `","password":"` + e2ePassword + `"}`
}

func passwordJSON() string { return `{"password":"` + e2ePassword + `"}` }

// createAccount crée le compte avec le jeton d'amorçage et rend le navigateur connecté.
func (server *e2eServer) createAccount(t *testing.T) *browser {
	t.Helper()
	client := server.newBrowser()
	client.bearer = true
	expectStatus(t, "setup", client.call(t, http.MethodPost, "/api/auth/setup", setupJSON()), http.StatusCreated)
	client.bearer = false
	return client
}

func (client *browser) registerKey(t *testing.T, key *e2eKey, kind string) {
	t.Helper()
	begin := client.call(t, http.MethodPost, "/api/auth/webauthn/register/begin", `{"password":"`+e2ePassword+`","kind":"`+kind+`"}`)
	expectStatus(t, "register begin", begin, http.StatusOK)
	attestation := key.attest(t, client.server.relyingParty, begin.field(t, "webauthn"))
	finish := client.call(t, http.MethodPost, "/api/auth/webauthn/register/finish", `{"name":"`+e2eKeyName+`","credential":`+attestation+`}`)
	expectStatus(t, "register finish", finish, http.StatusCreated)
}

// enrolTOTP enregistre le TOTP et rend son secret.
func (client *browser) enrolTOTP(t *testing.T) string {
	t.Helper()
	begin := client.call(t, http.MethodPost, "/api/auth/totp/setup/begin", passwordJSON())
	expectStatus(t, "totp begin", begin, http.StatusOK)
	secret := begin.text(t, "secret")
	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	expectStatus(t, "totp finish", client.call(t, http.MethodPost, "/api/auth/totp/setup/finish", `{"code":"`+code+`"}`), http.StatusCreated)
	return secret
}

func (client *browser) passwordLogin(t *testing.T) e2eAnswer {
	t.Helper()
	answer := client.call(t, http.MethodPost, "/api/auth/login", setupJSON())
	expectStatus(t, "login", answer, http.StatusOK)
	return answer
}

func TestE2E_fullJourneyWithASecurityKeyAndTOTP(t *testing.T) {
	server := newE2EServer(t)
	bearerOnly := server.newBrowser()
	bearerOnly.bearer = true
	expectStatus(t, "bearer before setup", bearerOnly.call(t, http.MethodGet, "/api/jobs", ""), http.StatusOK)

	operator := server.createAccount(t)
	session := operator.call(t, http.MethodGet, "/api/auth/session", "")
	if session.text(t, "state") != stateAuthenticated || session.text(t, "username") != e2eUsername {
		t.Fatalf("valeurObtenue %s, valeurAttendue an authenticated session for the new account", session.body)
	}
	if refused := bearerOnly.call(t, http.MethodGet, "/api/jobs", ""); refused.errorCode(t) != errorCodeBearerDisabled {
		t.Fatalf("valeurObtenue %s, valeurAttendue the bearer refused once the account exists", refused.body)
	}
	key := newE2EKey()
	operator.registerKey(t, key, "second_factor")
	secret := operator.enrolTOTP(t)
	expectStatus(t, "logout", operator.call(t, http.MethodPost, "/api/auth/logout", `{}`), http.StatusOK)
	expectStatus(t, "after logout", operator.call(t, http.MethodGet, "/api/jobs", ""), http.StatusUnauthorized)

	login := operator.passwordLogin(t)
	if login.text(t, "state") != stateSecondFactorRequired || string(login.field(t, "factors")) != `["webauthn","totp"]` {
		t.Fatalf("valeurObtenue %s, valeurAttendue both second factors without a session", login.body)
	}
	expectStatus(t, "no session before the second factor", operator.call(t, http.MethodGet, "/api/jobs", ""), http.StatusUnauthorized)
	assertion := key.assert(t, server.relyingParty, login.field(t, "webauthn"))
	expectStatus(t, "webauthn finish", operator.call(t, http.MethodPost, "/api/auth/webauthn/login/finish", `{"credential":`+assertion+`}`), http.StatusOK)
	expectStatus(t, "signed in by key", operator.call(t, http.MethodGet, "/api/jobs", ""), http.StatusOK)

	expectStatus(t, "logout again", operator.call(t, http.MethodPost, "/api/auth/logout", `{}`), http.StatusOK)
	operator.passwordLogin(t)
	nextStepCode, err := totp.GenerateCode(secret, time.Now().Add(e2eTOTPPeriod))
	if err != nil {
		t.Fatal(err)
	}
	expectStatus(t, "totp login", operator.call(t, http.MethodPost, "/api/auth/totp/login", `{"code":"`+nextStepCode+`"}`), http.StatusOK)

	staleCookies := map[string]string{}
	for name, value := range operator.cookies {
		staleCookies[name] = value
	}
	expectStatus(t, "logout-all", operator.call(t, http.MethodPost, "/api/auth/logout-all", `{}`), http.StatusOK)
	operator.cookies = staleCookies
	if afterLogoutAll := operator.call(t, http.MethodGet, "/api/jobs", ""); afterLogoutAll.errorCode(t) != errorCodeSessionRequired {
		t.Fatalf("valeurObtenue %s, valeurAttendue the old session cookie refused after logout-all", afterLogoutAll.body)
	}
}

func TestE2E_passkeyAloneOpensASessionAndItsStateCannotBeReplayed(t *testing.T) {
	server := newE2EServer(t)
	operator := server.createAccount(t)
	passkey := newE2EKey()
	operator.registerKey(t, passkey, "passkey")
	expectStatus(t, "logout", operator.call(t, http.MethodPost, "/api/auth/logout", `{}`), http.StatusOK)

	begin := operator.call(t, http.MethodPost, "/api/auth/passkey/login/begin", `{}`)
	expectStatus(t, "passkey begin", begin, http.StatusOK)
	preAuthCookies := map[string]string{preAuthCookieBase: operator.cookies[preAuthCookieBase]}
	assertion := passkey.assert(t, server.relyingParty, begin.field(t, "webauthn"))
	finishBody := `{"credential":` + assertion + `}`
	expectStatus(t, "passkey finish", operator.call(t, http.MethodPost, "/api/auth/passkey/login/finish", finishBody), http.StatusOK)
	expectStatus(t, "signed in by passkey", operator.call(t, http.MethodGet, "/api/jobs", ""), http.StatusOK)

	replayer := server.newBrowser()
	replayer.cookies = preAuthCookies
	if replayed := replayer.call(t, http.MethodPost, "/api/auth/passkey/login/finish", finishBody); replayed.errorCode(t) != errorCodeStepExpired {
		t.Fatalf("valeurObtenue %s, valeurAttendue the replayed pre-auth cookie refused", replayed.body)
	}
}

func TestE2E_resetAccountReopensTheBearerWithoutRestart(t *testing.T) {
	server := newE2EServer(t)
	operator := server.createAccount(t)
	bearerOnly := server.newBrowser()
	bearerOnly.bearer = true
	expectStatus(t, "bearer refused", bearerOnly.call(t, http.MethodGet, "/api/jobs", ""), http.StatusUnauthorized)

	if err := server.service.ResetAccount(context.Background()); err != nil {
		t.Fatal(err)
	}

	expectStatus(t, "bearer reopened", bearerOnly.call(t, http.MethodGet, "/api/jobs", ""), http.StatusOK)
	if state := operator.call(t, http.MethodGet, "/api/auth/session", "").text(t, "state"); state != stateNoAccount {
		t.Fatalf("valeurObtenue %s, valeurAttendue %s after the reset", state, stateNoAccount)
	}
}

func TestE2E_concurrentSetupsCreateExactlyOneAccount(t *testing.T) {
	server := newE2EServer(t)
	statuses := make([]int, 2)
	failures := make([]error, 2)
	var group sync.WaitGroup
	for attemptIndex := range statuses {
		group.Go(func() { statuses[attemptIndex], failures[attemptIndex] = server.setupStatus() })
	}
	group.Wait()
	if err := errors.Join(failures...); err != nil {
		t.Fatal(err)
	}

	created, conflicts := 0, 0
	for _, status := range statuses {
		switch status {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflicts++
		}
	}
	if created != 1 || conflicts != 1 {
		t.Fatalf("valeurObtenue %v, valeurAttendue one 201 and one 409", statuses)
	}
}

// setupStatus envoie un setup sans *testing.T : il tourne hors de la goroutine du test.
func (server *e2eServer) setupStatus() (int, error) {
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.baseURL+"/api/auth/setup", strings.NewReader(setupJSON()))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", jsonType)
	request.Header.Set("Origin", server.baseURL)
	request.Header.Set("Authorization", "Bearer "+e2eToken)
	response, err := server.client.Do(request)
	if err != nil {
		return 0, err
	}
	defer func() { _ = response.Body.Close() }()
	return response.StatusCode, nil
}

func TestLogin_unknownUserAndWrongPasswordGiveTheSameBodyAndStatus(t *testing.T) {
	server := newE2EServer(t)
	server.createAccount(t)
	client := server.newBrowser()

	unknownUser := client.call(t, http.MethodPost, "/api/auth/login", `{"username":"bob","password":"`+e2ePassword+`"}`)
	time.Sleep(1200 * time.Millisecond)
	wrongPassword := client.call(t, http.MethodPost, "/api/auth/login", `{"username":"`+e2eUsername+`","password":"wrong password value"}`)

	if unknownUser.status != http.StatusUnauthorized || unknownUser.status != wrongPassword.status || !bytes.Equal(unknownUser.body, wrongPassword.body) {
		t.Fatalf("valeurObtenue %d %s and %d %s, valeurAttendue the same 401 body", unknownUser.status, unknownUser.body, wrongPassword.status, wrongPassword.body)
	}
}

func TestPasskeyLogin_neverReusesTheSessionOfAPasswordLogin(t *testing.T) {
	server := newE2EServer(t)
	operator := server.createAccount(t)
	passkey, securityKey := newE2EKey(), newE2EKey()
	operator.registerKey(t, passkey, "passkey")
	operator.registerKey(t, securityKey, "second_factor")
	expectStatus(t, "logout", operator.call(t, http.MethodPost, "/api/auth/logout", `{}`), http.StatusOK)

	passwordBrowser, passkeyBrowser := server.newBrowser(), server.newBrowser()
	passwordBrowser.passwordLogin(t)
	passkeyBegin := passkeyBrowser.call(t, http.MethodPost, "/api/auth/passkey/login/begin", `{}`)
	passkeyAssertion := `{"credential":` + passkey.assert(t, server.relyingParty, passkeyBegin.field(t, "webauthn")) + `}`
	passwordBrowser.cookies[preAuthCookieBase], passkeyBrowser.cookies[preAuthCookieBase] =
		passkeyBrowser.cookies[preAuthCookieBase], passwordBrowser.cookies[preAuthCookieBase]
	loginStateAtPasskeyFinish := passkeyBrowser.call(t, http.MethodPost, "/api/auth/passkey/login/finish", passkeyAssertion)
	passkeyStateAtKeyFinish := passwordBrowser.call(t, http.MethodPost, "/api/auth/webauthn/login/finish", passkeyAssertion)
	if loginStateAtPasskeyFinish.errorCode(t) != errorCodeStepExpired || passkeyStateAtKeyFinish.errorCode(t) != errorCodeStepExpired {
		t.Fatalf("valeurObtenue %s and %s, valeurAttendue step_expired for each swapped state", loginStateAtPasskeyFinish.body, passkeyStateAtKeyFinish.body)
	}

	login := operator.passwordLogin(t)
	keyAssertion := securityKey.assert(t, server.relyingParty, login.field(t, "webauthn"))
	expectStatus(t, "key finish", operator.call(t, http.MethodPost, "/api/auth/webauthn/login/finish", `{"credential":`+keyAssertion+`}`), http.StatusOK)
	passwordSession := operator.cookies[sessionCookieBase]
	begin := operator.call(t, http.MethodPost, "/api/auth/passkey/login/begin", `{}`)
	finish := `{"credential":` + passkey.assert(t, server.relyingParty, begin.field(t, "webauthn")) + `}`
	expectStatus(t, "passkey finish", operator.call(t, http.MethodPost, "/api/auth/passkey/login/finish", finish), http.StatusOK)

	if passkeySession := operator.cookies[sessionCookieBase]; passkeySession == "" || passkeySession == passwordSession {
		t.Fatalf("valeurObtenue the same session %q for both logins, valeurAttendue a new session for the passkey", passkeySession)
	}
}
