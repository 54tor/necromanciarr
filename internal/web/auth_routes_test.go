package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"necromanciarr/internal/app"
	"necromanciarr/internal/config"
)

const (
	testPassword      = "correct horse battery"
	testTOTPCode      = "123456"
	testCeremonyState = "sealed-ceremony-state"
	testPublicURL     = "https://necro.example.com"
)

func errorCodeOf(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error ErrorBody `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("valeurObtenue %q, valeurAttendue an error body: %v", recorder.Body.String(), err)
	}
	return body.Error.Code
}

func cookieNamed(recorder *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

func withBearer(request *http.Request) *http.Request {
	request.Header.Set("Authorization", "Bearer "+testToken)
	return request
}

func withCookie(request *http.Request, name, value string) *http.Request {
	request.AddCookie(&http.Cookie{Name: name, Value: value})
	return request
}

func sessionRequest(method, target, body string) *http.Request {
	return withCookie(apiRequest(method, target, body), sessionCookieBase, fakeSessionToken)
}

func loopbackTokenSettings() config.Config {
	return config.Config{ListenAddr: testListen, AuthToken: testToken}
}

const credentialsJSON = `{"username":"` + fakeUsername + `","password":"` + testPassword + `"}`

func TestProtect_noAccountKeepsTheBearerBehaviour(t *testing.T) {
	tokenHandler := newTestHandler(t, &fakeService{}, tokenSettings())
	openHandler := newTestHandler(t, &fakeService{}, loopbackSettings())

	withoutBearer := serve(tokenHandler, apiRequest(http.MethodGet, "/api/status", ""))
	withValidBearer := serve(tokenHandler, withBearer(apiRequest(http.MethodGet, "/api/status", "")))
	openLoopback := serve(openHandler, apiRequest(http.MethodGet, "/api/status", ""))

	isExpected := withoutBearer.Code == http.StatusUnauthorized && withoutBearer.Header().Get("WWW-Authenticate") == bearerScheme &&
		errorCodeOf(t, withoutBearer) == errorCodeUnauthorized && withValidBearer.Code == http.StatusOK && openLoopback.Code == http.StatusOK
	if !isExpected {
		t.Fatalf("valeurObtenue %d %d %d, valeurAttendue 401 with WWW-Authenticate, then 200 and 200", withoutBearer.Code, withValidBearer.Code, openLoopback.Code)
	}
}

func TestProtect_accountModeRefusesTheBearerWithBearerDisabled(t *testing.T) {
	handler := newTestHandler(t, (&fakeService{}).withAccount(), tokenSettings())

	answered := serve(handler, withBearer(apiRequest(http.MethodGet, "/api/status", "")))

	valeurAttendue := errorCodeBearerDisabled
	valeurObtenue := errorCodeOf(t, answered)
	if answered.Code != http.StatusUnauthorized || valeurObtenue != valeurAttendue || answered.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("valeurObtenue %d %s, valeurAttendue 401 %s without WWW-Authenticate", answered.Code, valeurObtenue, valeurAttendue)
	}
}

func TestProtect_accountModeRequiresTheSessionCookie(t *testing.T) {
	handler := newTestHandler(t, (&fakeService{}).withAccount(), loopbackSettings())

	anonymous := serve(handler, apiRequest(http.MethodGet, "/api/status", ""))
	unknownSession := serve(handler, withCookie(apiRequest(http.MethodGet, "/api/status", ""), sessionCookieBase, "unknown"))
	signedIn := serve(handler, sessionRequest(http.MethodGet, "/api/status", ""))

	isExpected := anonymous.Code == http.StatusUnauthorized && errorCodeOf(t, anonymous) == errorCodeSessionRequired &&
		unknownSession.Code == http.StatusUnauthorized && signedIn.Code == http.StatusOK
	if !isExpected {
		t.Fatalf("valeurObtenue %d %d %d, valeurAttendue 401 session_required, 401, 200", anonymous.Code, unknownSession.Code, signedIn.Code)
	}
}

func TestSessionTokenIsOnlyReadFromTheCookie(t *testing.T) {
	handler := newTestHandler(t, (&fakeService{}).withAccount(), loopbackSettings())
	byHeader := apiRequest(http.MethodGet, "/api/status", "")
	byHeader.Header.Set("Authorization", "Bearer "+fakeSessionToken)

	headerAnswer := serve(handler, byHeader)
	queryAnswer := serve(handler, apiRequest(http.MethodGet, "/api/status?session="+fakeSessionToken, ""))
	otherCookieAnswer := serve(handler, withCookie(apiRequest(http.MethodGet, "/api/status", ""), "session", fakeSessionToken))

	isExpected := errorCodeOf(t, headerAnswer) == errorCodeBearerDisabled && errorCodeOf(t, queryAnswer) == errorCodeSessionRequired &&
		errorCodeOf(t, otherCookieAnswer) == errorCodeSessionRequired
	if !isExpected {
		t.Fatalf("valeurObtenue %d %d %d, valeurAttendue every non-cookie token refused", headerAnswer.Code, queryAnswer.Code, otherCookieAnswer.Code)
	}
}

func TestSetup_requiresAConfiguredValidBearerEvenOnLoopback(t *testing.T) {
	withoutToken := newTestHandler(t, &fakeService{}, loopbackSettings())
	fake := &fakeService{}
	withToken := newTestHandler(t, fake, loopbackTokenSettings())

	noTokenConfigured := serve(withoutToken, apiRequest(http.MethodPost, "/api/auth/setup", credentialsJSON))
	noBearer := serve(withToken, apiRequest(http.MethodPost, "/api/auth/setup", credentialsJSON))
	created := serve(withToken, withBearer(apiRequest(http.MethodPost, "/api/auth/setup", credentialsJSON)))

	isExpected := noTokenConfigured.Code == http.StatusForbidden && errorCodeOf(t, noTokenConfigured) == errorCodeSetupTokenRequired &&
		noBearer.Code == http.StatusUnauthorized && created.Code == http.StatusCreated &&
		cookieNamed(created, sessionCookieBase) != nil && fake.callCount("SetupAccount") == 1
	if !isExpected {
		t.Fatalf("valeurObtenue %d %d %d calls %v, valeurAttendue 403, 401, 201 with a session cookie", noTokenConfigured.Code, noBearer.Code, created.Code, fake.calls)
	}
}

func TestSetup_secondCallReturns409(t *testing.T) {
	handler := newTestHandler(t, &fakeService{}, loopbackTokenSettings())

	first := serve(handler, withBearer(apiRequest(http.MethodPost, "/api/auth/setup", credentialsJSON)))
	second := serve(handler, withBearer(apiRequest(http.MethodPost, "/api/auth/setup", credentialsJSON)))

	if first.Code != http.StatusCreated || second.Code != http.StatusConflict || errorCodeOf(t, second) != errorCodeAccountExists {
		t.Fatalf("valeurObtenue %d then %d, valeurAttendue 201 then 409 account_exists", first.Code, second.Code)
	}
}

func TestLogin_aCorruptedHashStillReturnsTheGenericResponse(t *testing.T) {
	wrongPassword := &fakeService{errors: map[string]error{"Login": app.ErrInvalidCredentials}}
	corruptedHash := &fakeService{errors: map[string]error{"Login": fmt.Errorf("verify password: %w", errors.New("malformed hash"))}}
	var logged bytes.Buffer
	corruptedHandler := newTestHandler(t, corruptedHash.withAccount(), loopbackSettings())
	corruptedHandler.UseLogger(NewLogger(&logged))

	valeurAttendue := serve(newTestHandler(t, wrongPassword.withAccount(), loopbackSettings()), apiRequest(http.MethodPost, "/api/auth/login", credentialsJSON))
	valeurObtenue := serve(corruptedHandler, apiRequest(http.MethodPost, "/api/auth/login", credentialsJSON))

	if valeurObtenue.Code != valeurAttendue.Code || valeurObtenue.Body.String() != valeurAttendue.Body.String() || valeurObtenue.Code != http.StatusUnauthorized {
		t.Fatalf("valeurObtenue %d %q, valeurAttendue %d %q", valeurObtenue.Code, valeurObtenue.Body.String(), valeurAttendue.Code, valeurAttendue.Body.String())
	}
	if !strings.Contains(logged.String(), "level=ERROR") || strings.Contains(valeurObtenue.Body.String(), "malformed") {
		t.Fatalf("valeurObtenue log %q, valeurAttendue an error line and no detail in the body", logged.String())
	}
}

func TestLogin_throttledReturns429WithRetryAfter(t *testing.T) {
	fake := &fakeService{errors: map[string]error{"Login": &app.ThrottledError{RetryAfter: 1500 * time.Millisecond}}}
	handler := newTestHandler(t, fake.withAccount(), loopbackSettings())

	answered := serve(handler, apiRequest(http.MethodPost, "/api/auth/login", credentialsJSON))

	valeurAttendue := "2"
	valeurObtenue := answered.Header().Get("Retry-After")
	if answered.Code != http.StatusTooManyRequests || valeurObtenue != valeurAttendue || errorCodeOf(t, answered) != errorCodeThrottled {
		t.Fatalf("valeurObtenue %d Retry-After %q, valeurAttendue 429 Retry-After %q", answered.Code, valeurObtenue, valeurAttendue)
	}
}

func TestLogin_refusesAMultiMegabytePasswordBeforeCallingTheService(t *testing.T) {
	fake := (&fakeService{}).withAccount()
	handler := newTestHandler(t, fake, loopbackSettings())
	hugeBody := `{"username":"alice","password":"` + strings.Repeat("x", 2<<20) + `"}`
	longBody := `{"username":"alice","password":"` + strings.Repeat("x", 4096) + `"}`

	huge := serve(handler, apiRequest(http.MethodPost, "/api/auth/login", hugeBody))
	long := serve(handler, apiRequest(http.MethodPost, "/api/auth/login", longBody))

	valeurAttendue := 0
	valeurObtenue := fake.callCount("Login")
	if valeurObtenue != valeurAttendue || huge.Code != http.StatusRequestEntityTooLarge || errorCodeOf(t, long) != errorCodeInvalidCredentials {
		t.Fatalf("valeurObtenue %d calls, %d and %d, valeurAttendue %d calls, 413 and 401", valeurObtenue, huge.Code, long.Code, valeurAttendue)
	}
}

func cookieAttributesMatch(cookie *http.Cookie, path string, secure bool) bool {
	return cookie != nil && cookie.HttpOnly && cookie.SameSite == http.SameSiteStrictMode && cookie.Path == path && cookie.Secure == secure
}

func TestSessionCookie_attributesFollowThePublicUrlScheme(t *testing.T) {
	scenarios := []struct {
		publicURL, sessionName, preAuthName string
		secure                              bool
	}{
		{"", sessionCookieBase, preAuthCookieBase, false},
		{"http://necro.example.com", sessionCookieBase, preAuthCookieBase, false},
		{testPublicURL, "__Host-" + sessionCookieBase, "__Secure-" + preAuthCookieBase, true},
	}
	for _, scenario := range scenarios {
		settings := config.Config{ListenAddr: testListen, PublicURL: scenario.publicURL}
		handler := newTestHandler(t, (&fakeService{}).withAccount(), settings)
		passkeyFake := (&fakeService{}).withAccount()
		passkeyFake.account.ceremony = app.WebAuthnCeremony{Options: json.RawMessage(`{}`), State: []byte(testCeremonyState)}
		passkeyHandler := newTestHandler(t, passkeyFake, settings)

		login := serve(handler, apiRequest(http.MethodPost, "/api/auth/login", credentialsJSON))
		begin := serve(passkeyHandler, apiRequest(http.MethodPost, "/api/auth/passkey/login/begin", `{}`))

		session := cookieNamed(login, scenario.sessionName)
		preAuth := cookieNamed(begin, scenario.preAuthName)
		maxAgeInRange := session != nil && session.MaxAge > 3500 && session.MaxAge <= 3600 && preAuth != nil && preAuth.MaxAge == 300
		if !cookieAttributesMatch(session, "/", scenario.secure) || !cookieAttributesMatch(preAuth, "/api/auth", scenario.secure) || !maxAgeInRange {
			t.Fatalf("%q: valeurObtenue %+v and %+v, valeurAttendue %s and %s, secure=%v", scenario.publicURL, session, preAuth, scenario.sessionName, scenario.preAuthName, scenario.secure)
		}
	}
}

func TestOrigin_isPinnedToThePublicUrlWhenSet(t *testing.T) {
	handler := newTestHandler(t, (&fakeService{}).withAccount(), config.Config{ListenAddr: testListen, PublicURL: testPublicURL})
	fromHost := apiRequest(http.MethodPost, "/api/auth/login", credentialsJSON)
	fromHost.Header.Set("Origin", "http://"+testHost)
	fromPublicURL := apiRequest(http.MethodPost, "/api/auth/login", credentialsJSON)
	fromPublicURL.Header.Set("Origin", testPublicURL)

	refused := serve(handler, fromHost)
	admitted := serve(handler, fromPublicURL)

	if refused.Code != http.StatusForbidden || admitted.Code != http.StatusOK {
		t.Fatalf("valeurObtenue %d and %d, valeurAttendue 403 for the Host origin and 200 for the public url", refused.Code, admitted.Code)
	}
}

func TestAuthRoutes_stillRefuseCrossSitePosts(t *testing.T) {
	handler := newTestHandler(t, (&fakeService{}).withAccount(), loopbackSettings())
	crossSiteFetch := apiRequest(http.MethodPost, "/api/auth/login", credentialsJSON)
	crossSiteFetch.Header.Set("Sec-Fetch-Site", "cross-site")
	noCustomHeader := apiRequest(http.MethodPost, "/api/auth/login", credentialsJSON)
	noCustomHeader.Header.Del("X-Requested-With")
	foreignOrigin := sessionRequest(http.MethodPost, "/api/auth/logout", `{}`)
	foreignOrigin.Header.Set("Origin", "https://evil.example.com")
	formPost := apiRequest(http.MethodPost, "/api/auth/setup", credentialsJSON)
	formPost.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	answers := []int{
		serve(handler, crossSiteFetch).Code, serve(handler, noCustomHeader).Code,
		serve(handler, foreignOrigin).Code, serve(newTestHandler(t, &fakeService{}, loopbackTokenSettings()), withBearer(formPost)).Code,
	}

	valeurAttendue := []int{http.StatusForbidden, http.StatusForbidden, http.StatusForbidden, http.StatusUnsupportedMediaType}
	if fmt.Sprint(answers) != fmt.Sprint(valeurAttendue) {
		t.Fatalf("valeurObtenue %v, valeurAttendue %v", answers, valeurAttendue)
	}
}

func TestHealthz_staysUnauthenticated(t *testing.T) {
	handler := newTestHandler(t, (&fakeService{}).withAccount(), tokenSettings())
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	answered := serve(handler, request)

	if answered.Code != http.StatusOK || readBody(t, answered) != "ok" {
		t.Fatalf("valeurObtenue %d, valeurAttendue 200 ok without a session", answered.Code)
	}
}

func TestSession_stateNeverRevealsTheUsernameOrFactorsToAnAnonymousCaller(t *testing.T) {
	fake := (&fakeService{}).withAccount()
	fake.account.summary = app.AccountSummary{Exists: true, Username: fakeUsername, SecondFactorKeys: 1, TOTP: true}
	handler := newTestHandler(t, fake, loopbackSettings())

	anonymous := readBody(t, serve(handler, apiRequest(http.MethodGet, "/api/auth/session", "")))
	expiredCookie := readBody(t, serve(handler, withCookie(apiRequest(http.MethodGet, "/api/auth/session", ""), sessionCookieBase, "stale")))
	signedIn := readBody(t, serve(handler, sessionRequest(http.MethodGet, "/api/auth/session", "")))

	valeurAttendue := `{"state":"logged_out","webauthnAvailable":false}`
	if anonymous != valeurAttendue || expiredCookie != valeurAttendue {
		t.Fatalf("valeurObtenue %s and %s, valeurAttendue %s", anonymous, expiredCookie, valeurAttendue)
	}
	if !strings.Contains(signedIn, `"username":"alice"`) || !strings.Contains(signedIn, `"secondFactors":{"keys":1,"passkeys":0,"totp":true}`) {
		t.Fatalf("valeurObtenue %s, valeurAttendue the username and factors for the session", signedIn)
	}
}

func TestLogoutAll_clearsTheCookie(t *testing.T) {
	fake := (&fakeService{}).withAccount()
	handler := newTestHandler(t, fake, loopbackSettings())

	answered := serve(handler, sessionRequest(http.MethodPost, "/api/auth/logout-all", `{}`))
	afterwards := serve(handler, sessionRequest(http.MethodGet, "/api/status", ""))

	cleared := cookieNamed(answered, sessionCookieBase)
	if answered.Code != http.StatusOK || cleared == nil || cleared.MaxAge >= 0 || !fake.called("LogoutAll") || afterwards.Code != http.StatusUnauthorized {
		t.Fatalf("valeurObtenue %d cookie %+v then %d, valeurAttendue 200, a cleared cookie, then 401", answered.Code, cleared, afterwards.Code)
	}
}
