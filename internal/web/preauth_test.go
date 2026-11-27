package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"necromanciarr/internal/app"
)

// testClock est une horloge réglable pour les états gardés.
type testClock struct{ moment time.Time }

func (clock *testClock) now() time.Time { return clock.moment }

const testAssertionJSON = `{"credential":{"id":"key","type":"public-key"}}`

func secondFactorFake(factors ...string) *fakeService {
	fake := (&fakeService{}).withAccount()
	fake.account.loginResult = app.LoginResult{SecondFactors: factors, Ticket: fakeTicket}
	fake.account.ceremony = app.WebAuthnCeremony{Options: json.RawMessage(`{"publicKey":{"challenge":"abc"}}`), State: []byte(testCeremonyState)}
	return fake
}

// preAuthCookieOf rend la valeur du cookie d'avant session posé par la réponse.
func preAuthCookieOf(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	cookie := cookieNamed(recorder, preAuthCookieBase)
	if cookie == nil || cookie.Value == "" {
		t.Fatalf("valeurObtenue no pre-auth cookie in %v, valeurAttendue one", recorder.Result().Cookies())
	}
	return cookie.Value
}

func withPreAuth(request *http.Request, id string) *http.Request {
	return withCookie(request, preAuthCookieBase, id)
}

func TestPreAuth_expiresAfterFiveMinutes(t *testing.T) {
	clock := &testClock{moment: time.Unix(1_000_000, 0)}
	states := newHeldStates(preAuthLifetime, clock.now)
	states.put("fresh", heldState{purpose: purposeLogin, ticket: fakeTicket})
	states.put("stale", heldState{purpose: purposeLogin, ticket: fakeTicket})

	clock.moment = clock.moment.Add(preAuthLifetime - time.Second)
	_, freshFound := states.take("fresh", purposeLogin)
	clock.moment = clock.moment.Add(time.Second)
	_, staleFound := states.take("stale", purposeLogin)

	if !freshFound || staleFound {
		t.Fatalf("valeurObtenue fresh %v stale %v, valeurAttendue usable before five minutes and refused at five", freshFound, staleFound)
	}
}

func TestPreAuth_boundedWithOldestEviction(t *testing.T) {
	clock := &testClock{moment: time.Unix(1_000_000, 0)}
	states := newHeldStates(preAuthLifetime, clock.now)
	for entryIndex := range heldStatesCapacity + 1 {
		clock.moment = clock.moment.Add(time.Millisecond)
		states.put("entry-"+strconv.Itoa(entryIndex), heldState{purpose: purposeLogin})
	}

	_, oldestFound := states.take("entry-0", purposeLogin)
	_, secondFound := states.take("entry-1", purposeLogin)

	valeurAttendue := heldStatesCapacity - 1
	if valeurObtenue := states.count(); valeurObtenue != valeurAttendue || oldestFound || !secondFound {
		t.Fatalf("valeurObtenue %d entries, oldest %v, second %v, valeurAttendue %d and only the oldest evicted", valeurObtenue, oldestFound, secondFound, valeurAttendue)
	}
}

func TestPreAuth_zeroValueIsUsable(t *testing.T) {
	var states heldStates
	states.put("key", heldState{purpose: purposePasskey})

	held, found := states.take("key", purposePasskey)

	if !found || held.purpose != purposePasskey {
		t.Fatalf("valeurObtenue %+v %v, valeurAttendue the stored state", held, found)
	}
}

func TestPreAuth_stateIsCookieBoundSingleUseAndBurnedOnTheFirstFailure(t *testing.T) {
	fake := secondFactorFake(app.SecondFactorWebAuthn)
	fake.errors = map[string]error{"FinishWebAuthnLogin": app.ErrInvalidAssertion}
	handler := newTestHandler(t, fake, loopbackSettings())

	login := serve(handler, apiRequest(http.MethodPost, "/api/auth/login", credentialsJSON))
	preAuthID := preAuthCookieOf(t, login)
	withoutCookie := serve(handler, apiRequest(http.MethodPost, "/api/auth/webauthn/login/finish", testAssertionJSON))
	forgedCookie := serve(handler, withPreAuth(apiRequest(http.MethodPost, "/api/auth/webauthn/login/finish", testAssertionJSON), "forged"))
	failed := serve(handler, withPreAuth(apiRequest(http.MethodPost, "/api/auth/webauthn/login/finish", testAssertionJSON), preAuthID))
	replayed := serve(handler, withPreAuth(apiRequest(http.MethodPost, "/api/auth/webauthn/login/finish", testAssertionJSON), preAuthID))

	isExpected := login.Code == http.StatusOK && cookieNamed(login, sessionCookieBase) == nil &&
		errorCodeOf(t, withoutCookie) == errorCodeStepExpired && errorCodeOf(t, forgedCookie) == errorCodeStepExpired &&
		errorCodeOf(t, failed) == errorCodeInvalidAssertion && errorCodeOf(t, replayed) == errorCodeStepExpired &&
		fake.callCount("FinishWebAuthnLogin") == 1 && string(fake.account.lastState) == testCeremonyState &&
		fake.account.lastTicket == fakeTicket && fake.account.lastBody == `{"id":"key","type":"public-key"}`
	if !isExpected {
		t.Fatalf("valeurObtenue %d %s %s %s %s calls %v, valeurAttendue one finish with the held state, then step_expired",
			login.Code, readBody(t, withoutCookie), readBody(t, forgedCookie), readBody(t, failed), readBody(t, replayed), fake.calls)
	}
}

func TestLogin_secondFactorResponseCarriesTheFactorsAndOptionsWithoutASession(t *testing.T) {
	handler := newTestHandler(t, secondFactorFake(app.SecondFactorWebAuthn, app.SecondFactorTOTP), loopbackSettings())

	login := serve(handler, apiRequest(http.MethodPost, "/api/auth/login", credentialsJSON))

	valeurAttendue := `{"state":"second_factor_required","factors":["webauthn","totp"],"webauthn":{"publicKey":{"challenge":"abc"}}}`
	valeurObtenue := readBody(t, login)
	if valeurObtenue != valeurAttendue || cookieNamed(login, sessionCookieBase) != nil {
		t.Fatalf("valeurObtenue %s, valeurAttendue %s without a session cookie", valeurObtenue, valeurAttendue)
	}
}

func TestLogin_webAuthnUnavailableLeavesOnlyTOTPOrAClearError(t *testing.T) {
	withTOTP := secondFactorFake(app.SecondFactorWebAuthn, app.SecondFactorTOTP)
	withTOTP.errors = map[string]error{"BeginWebAuthnLogin": app.ErrWebAuthnUnavailable}
	keyOnly := secondFactorFake(app.SecondFactorWebAuthn)
	keyOnly.errors = map[string]error{"BeginWebAuthnLogin": app.ErrWebAuthnUnavailable}

	totpOnly := readBody(t, serve(newTestHandler(t, withTOTP, loopbackSettings()), apiRequest(http.MethodPost, "/api/auth/login", credentialsJSON)))
	unavailable := serve(newTestHandler(t, keyOnly, loopbackSettings()), apiRequest(http.MethodPost, "/api/auth/login", credentialsJSON))

	if totpOnly != `{"state":"second_factor_required","factors":["totp"]}` || errorCodeOf(t, unavailable) != errorCodeWebAuthnUnavailable {
		t.Fatalf("valeurObtenue %s and %s, valeurAttendue totp alone, then webauthn_unavailable", totpOnly, readBody(t, unavailable))
	}
}

func TestTOTPLogin_requiresThePreAuthCookie(t *testing.T) {
	fake := secondFactorFake(app.SecondFactorTOTP)
	handler := newTestHandler(t, fake, loopbackSettings())

	withoutCookie := serve(handler, apiRequest(http.MethodPost, "/api/auth/totp/login", `{"code":"`+testTOTPCode+`"}`))
	preAuthID := preAuthCookieOf(t, serve(handler, apiRequest(http.MethodPost, "/api/auth/login", credentialsJSON)))
	signedIn := serve(handler, withPreAuth(apiRequest(http.MethodPost, "/api/auth/totp/login", `{"code":"`+testTOTPCode+`"}`), preAuthID))

	clearedPreAuth := cookieNamed(signedIn, preAuthCookieBase)
	isExpected := errorCodeOf(t, withoutCookie) == errorCodeStepExpired && signedIn.Code == http.StatusOK &&
		cookieNamed(signedIn, sessionCookieBase) != nil && clearedPreAuth != nil && clearedPreAuth.MaxAge < 0 &&
		fake.callCount("LoginWithTOTP") == 1 && fake.account.lastTicket == fakeTicket
	if !isExpected {
		t.Fatalf("valeurObtenue %d then %d calls %v, valeurAttendue step_expired, then a session", withoutCookie.Code, signedIn.Code, fake.calls)
	}
}

func TestPasskeyLogin_isPublicButNeedsItsOwnStateCookie(t *testing.T) {
	fake := secondFactorFake(app.SecondFactorTOTP)
	handler := newTestHandler(t, fake, loopbackSettings())

	begin := serve(handler, apiRequest(http.MethodPost, "/api/auth/passkey/login/begin", `{}`))
	passkeyID := preAuthCookieOf(t, begin)
	loginID := preAuthCookieOf(t, serve(handler, apiRequest(http.MethodPost, "/api/auth/login", credentialsJSON)))
	wrongPurpose := serve(handler, withPreAuth(apiRequest(http.MethodPost, "/api/auth/passkey/login/finish", testAssertionJSON), loginID))
	signedIn := serve(handler, withPreAuth(apiRequest(http.MethodPost, "/api/auth/passkey/login/finish", testAssertionJSON), passkeyID))

	isExpected := begin.Code == http.StatusOK && strings.Contains(readBody(t, begin), `"webauthn":{"publicKey"`) &&
		errorCodeOf(t, wrongPurpose) == errorCodeStepExpired && signedIn.Code == http.StatusOK &&
		cookieNamed(signedIn, sessionCookieBase) != nil && fake.callCount("FinishPasskeyLogin") == 1 &&
		string(fake.account.lastState) == testCeremonyState
	if !isExpected {
		t.Fatalf("valeurObtenue %d %d %d calls %v, valeurAttendue begin, purpose refused, then a session", begin.Code, wrongPurpose.Code, signedIn.Code, fake.calls)
	}
}

func TestRegistration_stateIsHeldPerSessionAndUsedOnce(t *testing.T) {
	fake := secondFactorFake()
	handler := newTestHandler(t, fake, loopbackSettings())
	finishBody := `{"name":"Desk key","credential":{"id":"key"}}`

	begin := serve(handler, sessionRequest(http.MethodPost, "/api/auth/webauthn/register/begin", `{"password":"`+testPassword+`","kind":"second_factor"}`))
	finished := serve(handler, sessionRequest(http.MethodPost, "/api/auth/webauthn/register/finish", finishBody))
	replayed := serve(handler, sessionRequest(http.MethodPost, "/api/auth/webauthn/register/finish", finishBody))

	isExpected := begin.Code == http.StatusOK && finished.Code == http.StatusCreated && errorCodeOf(t, replayed) == errorCodeStepExpired &&
		fake.callCount("FinishWebAuthnRegistration") == 1 && string(fake.account.lastState) == testCeremonyState &&
		fake.account.lastBody == `{"id":"key"}`
	if !isExpected {
		t.Fatalf("valeurObtenue %d %d %d calls %v, valeurAttendue one finish with the held state", begin.Code, finished.Code, replayed.Code, fake.calls)
	}
}

func TestTOTPSetup_returnsTheEnrolmentUnmaskedAndHoldsTheState(t *testing.T) {
	fake := secondFactorFake()
	fake.account.enrolment = app.TOTPEnrolment{
		OtpauthURI: "otpauth://totp/Necromanciarr:alice?secret=JBSWY3DPEHPK3PXP", SecretBase32: "JBSWY3DPEHPK3PXP",
		QRPNG: []byte{1, 2, 3}, State: []byte(testCeremonyState),
	}
	handler := newTestHandler(t, fake, loopbackSettings())

	begin := readBody(t, serve(handler, sessionRequest(http.MethodPost, "/api/auth/totp/setup/begin", `{"password":"`+testPassword+`"}`)))
	finished := serve(handler, sessionRequest(http.MethodPost, "/api/auth/totp/setup/finish", `{"code":"`+testTOTPCode+`"}`))

	valeurAttendue := `{"otpauthUri":"otpauth://totp/Necromanciarr:alice?secret=JBSWY3DPEHPK3PXP","secret":"JBSWY3DPEHPK3PXP","qrPngBase64":"AQID"}`
	if begin != valeurAttendue || finished.Code != http.StatusCreated || string(fake.account.lastState) != testCeremonyState {
		t.Fatalf("valeurObtenue %s then %d, valeurAttendue %s then 201", begin, finished.Code, valeurAttendue)
	}
}

func TestLoginLogs_neverContainSecrets(t *testing.T) {
	var logged bytes.Buffer
	fake := secondFactorFake(app.SecondFactorTOTP)
	handler := newTestHandler(t, fake, loopbackSettings())
	handler.UseLogger(NewLogger(&logged))

	preAuthID := preAuthCookieOf(t, serve(handler, apiRequest(http.MethodPost, "/api/auth/login", credentialsJSON)))
	serve(handler, withPreAuth(apiRequest(http.MethodPost, "/api/auth/totp/login", `{"code":"`+testTOTPCode+`"}`), preAuthID))
	serve(handler, withPreAuth(apiRequest(http.MethodPost, "/api/auth/totp/login", `{"code":"`+testTOTPCode+`"}`), preAuthID))
	fake.errors = map[string]error{"Login": app.ErrInvalidCredentials}
	serve(handler, apiRequest(http.MethodPost, "/api/auth/login", credentialsJSON))
	fake.errors = map[string]error{"Login": &app.ThrottledError{RetryAfter: time.Second}}
	serve(handler, apiRequest(http.MethodPost, "/api/auth/login", credentialsJSON))

	output := logged.String()
	failedLines := strings.Count(output, "code="+LogCodeAPILoginFailed)
	for _, secret := range []string{fakeUsername, testPassword, testTOTPCode, fakeTicket, fakeSessionToken, preAuthID} {
		if strings.Contains(output, secret) {
			t.Fatalf("valeurObtenue a log carrying %q: %s", secret, output)
		}
	}
	if failedLines != 3 || !strings.Contains(output, "code="+LogCodeAPILoginSucceeded) {
		t.Fatalf("valeurObtenue %d failure lines in %s, valeurAttendue 3 and one success", failedLines, output)
	}
}
