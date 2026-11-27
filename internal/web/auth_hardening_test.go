package web

import (
	"bytes"
	"net/http"
	"slices"
	"strings"
	"testing"

	"necromanciarr/internal/app"
	"necromanciarr/internal/config"
)

func TestPreAuth_aPasskeyFloodNeverEvictsAPendingSecondFactor(t *testing.T) {
	fake := secondFactorFake(app.SecondFactorWebAuthn)
	handler := newTestHandler(t, fake, loopbackSettings())
	loginID := preAuthCookieOf(t, serve(handler, apiRequest(http.MethodPost, "/api/auth/login", credentialsJSON)))

	for range heldStatesCapacity + 1 {
		serve(handler, apiRequest(http.MethodPost, "/api/auth/passkey/login/begin", `{}`))
	}
	finished := serve(handler, withPreAuth(apiRequest(http.MethodPost, "/api/auth/webauthn/login/finish", testAssertionJSON), loginID))

	valeurAttendue := http.StatusOK
	if valeurObtenue := finished.Code; valeurObtenue != valeurAttendue {
		t.Fatalf("valeurObtenue %d %s, valeurAttendue %d after the passkey flood", valeurObtenue, readBody(t, finished), valeurAttendue)
	}
}

func TestPreAuth_aStateIsBurnedEvenWhenPresentedToTheWrongFinish(t *testing.T) {
	fake := secondFactorFake(app.SecondFactorWebAuthn)
	handler := newTestHandler(t, fake, loopbackSettings())
	loginID := preAuthCookieOf(t, serve(handler, apiRequest(http.MethodPost, "/api/auth/login", credentialsJSON)))

	wrongFinish := serve(handler, withPreAuth(apiRequest(http.MethodPost, "/api/auth/passkey/login/finish", testAssertionJSON), loginID))
	rightFinish := serve(handler, withPreAuth(apiRequest(http.MethodPost, "/api/auth/webauthn/login/finish", testAssertionJSON), loginID))

	if errorCodeOf(t, wrongFinish) != errorCodeStepExpired || errorCodeOf(t, rightFinish) != errorCodeStepExpired {
		t.Fatalf("valeurObtenue %s then %s, valeurAttendue step_expired twice", readBody(t, wrongFinish), readBody(t, rightFinish))
	}
}

func failedLoginLines(output string) int {
	return strings.Count(output, "code="+LogCodeAPILoginFailed)
}

func TestLoginLogs_everyFailedStepWritesExactlyOneLine(t *testing.T) {
	scenarios := []struct {
		name    string
		fake    *fakeService
		attempt func(handler *Handler) int
	}{
		{"undecodable finish after the state is consumed", secondFactorFake(app.SecondFactorWebAuthn), func(handler *Handler) int {
			loginID := preAuthCookieOf(t, serve(handler, apiRequest(http.MethodPost, "/api/auth/login", credentialsJSON)))
			return serve(handler, withPreAuth(apiRequest(http.MethodPost, "/api/auth/webauthn/login/finish", `{"unknown":1}`), loginID)).Code
		}},
		{"undecodable totp login", secondFactorFake(app.SecondFactorTOTP), func(handler *Handler) int {
			loginID := preAuthCookieOf(t, serve(handler, apiRequest(http.MethodPost, "/api/auth/login", credentialsJSON)))
			return serve(handler, withPreAuth(apiRequest(http.MethodPost, "/api/auth/totp/login", `not json`), loginID)).Code
		}},
		{"undecodable passkey finish", secondFactorFake(), func(handler *Handler) int {
			passkeyID := preAuthCookieOf(t, serve(handler, apiRequest(http.MethodPost, "/api/auth/passkey/login/begin", `{}`)))
			return serve(handler, withPreAuth(apiRequest(http.MethodPost, "/api/auth/passkey/login/finish", `{"credential":null}`), passkeyID)).Code
		}},
		{"webauthn unavailable after the password", unavailableKeyFake(), func(handler *Handler) int {
			return serve(handler, apiRequest(http.MethodPost, "/api/auth/login", credentialsJSON)).Code
		}},
	}
	for _, scenario := range scenarios {
		var logged bytes.Buffer
		handler := newTestHandler(t, scenario.fake, loopbackSettings())
		handler.UseLogger(NewLogger(&logged))

		status := scenario.attempt(handler)

		output := logged.String()
		secretLogged := slices.ContainsFunc([]string{fakeUsername, testPassword, fakeTicket, testCeremonyState}, func(secret string) bool {
			return strings.Contains(output, secret)
		})
		if valeurObtenue := failedLoginLines(output); valeurObtenue != 1 || status < http.StatusBadRequest || secretLogged {
			t.Fatalf("%s: valeurObtenue %d lines, status %d, log %s, valeurAttendue exactly one line without secret", scenario.name, valeurObtenue, status, output)
		}
	}
}

func unavailableKeyFake() *fakeService {
	fake := secondFactorFake(app.SecondFactorWebAuthn)
	fake.errors = map[string]error{"BeginWebAuthnLogin": app.ErrWebAuthnUnavailable}
	return fake
}

func TestProtect_accountModeRefusesABearerEvenOnPublicRoutes(t *testing.T) {
	accountHandler := newTestHandler(t, (&fakeService{}).withAccount(), tokenSettings())
	noAccountHandler := newTestHandler(t, &fakeService{}, tokenSettings())

	accountLogin := serve(accountHandler, withBearer(apiRequest(http.MethodPost, "/api/auth/login", credentialsJSON)))
	accountSession := serve(accountHandler, withBearer(apiRequest(http.MethodGet, "/api/auth/session", "")))
	noAccountSession := serve(noAccountHandler, withBearer(apiRequest(http.MethodGet, "/api/auth/session", "")))

	isExpected := errorCodeOf(t, accountLogin) == errorCodeBearerDisabled && errorCodeOf(t, accountSession) == errorCodeBearerDisabled &&
		noAccountSession.Code == http.StatusOK
	if !isExpected {
		t.Fatalf("valeurObtenue %s, %s, %d, valeurAttendue bearer_disabled twice, then 200 without an account", readBody(t, accountLogin), readBody(t, accountSession), noAccountSession.Code)
	}
}

func TestProtect_aValidSessionCookieWithABearerIsStillRefused(t *testing.T) {
	handler := newTestHandler(t, (&fakeService{}).withAccount(), tokenSettings())

	answered := serve(handler, withBearer(sessionRequest(http.MethodGet, "/api/status", "")))

	if errorCodeOf(t, answered) != errorCodeBearerDisabled {
		t.Fatalf("valeurObtenue %d %s, valeurAttendue bearer_disabled", answered.Code, readBody(t, answered))
	}
}

func TestHosts_admitThePublicURLHostOnALoopbackListen(t *testing.T) {
	withPublicURL := hostsFor(testListen, "https://necro.example.com:8443")
	withoutPublicURL := hostsFor(testListen, "")
	nonLoopback := hostsFor("0.0.0.0:8787", "https://necro.example.com")
	handler := newTestHandler(t, &fakeService{}, config.Config{ListenAddr: testListen, PublicURL: "https://necro.example.com"})
	proxied := apiRequest(http.MethodGet, "/api/auth/session", "")
	proxied.Host = "necro.example.com"

	isExpected := withPublicURL.admits("necro.example.com:8443") && !withPublicURL.admits("necro.example.com") &&
		withPublicURL.admits(testHost) && !withoutPublicURL.admits("necro.example.com:8443") && nonLoopback == nil &&
		serve(handler, proxied).Code == http.StatusOK
	if !isExpected {
		t.Fatalf("valeurObtenue %v / %v / %v, valeurAttendue the public host added on loopback only", withPublicURL, withoutPublicURL, nonLoopback)
	}
}

func TestFieldCaps_refuseOverlongValuesBeforeCallingTheService(t *testing.T) {
	fake := secondFactorFake(app.SecondFactorTOTP)
	handler := newTestHandler(t, fake, loopbackTokenSettings())
	setupBody := `{"username":"` + strings.Repeat("u", 65) + `","password":"` + testPassword + `"}`
	loginID := preAuthCookieOf(t, serve(handler, apiRequest(http.MethodPost, "/api/auth/login", credentialsJSON)))
	callsBefore := len(fake.calls)

	answers := []int{
		serve(handler, withBearer(apiRequest(http.MethodPost, "/api/auth/setup", setupBody))).Code,
		serve(handler, sessionRequest(http.MethodPatch, "/api/auth/webauthn/credentials/key", `{"name":"`+strings.Repeat("n", 65)+`"}`)).Code,
		serve(handler, withPreAuth(apiRequest(http.MethodPost, "/api/auth/totp/login", `{"code":"`+strings.Repeat("1", 17)+`"}`), loginID)).Code,
		serve(handler, sessionRequest(http.MethodDelete, "/api/auth/webauthn/credentials/"+strings.Repeat("k", 257), `{"password":"`+testPassword+`"}`)).Code,
	}

	valeurAttendue := []int{http.StatusBadRequest, http.StatusBadRequest, http.StatusBadRequest, http.StatusBadRequest}
	if !slices.Equal(answers, valeurAttendue) || len(fake.calls) != callsBefore {
		t.Fatalf("valeurObtenue %v calls %v, valeurAttendue %v and no new call", answers, fake.calls[callsBefore:], valeurAttendue)
	}
}

func TestCeremonies_anotherSessionCannotFinishThem(t *testing.T) {
	const otherSessionToken = "other-session-token"
	fake := secondFactorFake()
	fake.account.sessions[otherSessionToken] = true
	handler := newTestHandler(t, fake, loopbackSettings())
	otherSession := func(method, target, body string) *http.Request {
		return withCookie(apiRequest(method, target, body), sessionCookieBase, otherSessionToken)
	}

	serve(handler, sessionRequest(http.MethodPost, "/api/auth/webauthn/register/begin", `{"password":"`+testPassword+`","kind":"passkey"}`))
	serve(handler, sessionRequest(http.MethodPost, "/api/auth/totp/setup/begin", `{"password":"`+testPassword+`"}`))
	foreignRegistration := serve(handler, otherSession(http.MethodPost, "/api/auth/webauthn/register/finish", `{"name":"Desk key","credential":{"id":"key"}}`))
	foreignTOTP := serve(handler, otherSession(http.MethodPost, "/api/auth/totp/setup/finish", `{"code":"`+testTOTPCode+`"}`))
	ownRegistration := serve(handler, sessionRequest(http.MethodPost, "/api/auth/webauthn/register/finish", `{"name":"Desk key","credential":{"id":"key"}}`))

	isExpected := errorCodeOf(t, foreignRegistration) == errorCodeStepExpired && errorCodeOf(t, foreignTOTP) == errorCodeStepExpired &&
		ownRegistration.Code == http.StatusCreated && fake.callCount("FinishTOTPSetup") == 0 && fake.callCount("FinishWebAuthnRegistration") == 1
	if !isExpected {
		t.Fatalf("valeurObtenue %d %d %d calls %v, valeurAttendue the other session refused and the owner served", foreignRegistration.Code, foreignTOTP.Code, ownRegistration.Code, fake.calls)
	}
}
