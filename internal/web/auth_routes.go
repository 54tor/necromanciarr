package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"unicode/utf8"

	"necromanciarr/internal/app"
	"necromanciarr/internal/auth"
	"necromanciarr/internal/i18n"
)

// authRoute est une route de /api/auth : sans action du registre (la CLI a sa commande account), avec sa politique.
type authRoute struct {
	method        string
	path          string
	justification string
	access        accessPolicy
	handle        authEndpointFunc
}

func (apiRoute authRoute) pattern() string { return apiRoute.method + " " + apiRoute.path }

const authJustification = "operator account sign-in, sessions and second factors, driven by the CLI through the account command"

var authRoutes = []authRoute{
	{http.MethodGet, "/api/auth/session", authJustification, accessPublic, getAuthSession},
	{http.MethodPost, "/api/auth/setup", authJustification, accessSetupBearer, setupAccount},
	{http.MethodPost, "/api/auth/login", authJustification, accessPublic, loginWithPassword},
	{http.MethodPost, "/api/auth/webauthn/login/finish", authJustification, accessPublic, finishWebAuthnLogin},
	{http.MethodPost, "/api/auth/totp/login", authJustification, accessPublic, loginWithTOTP},
	{http.MethodPost, "/api/auth/passkey/login/begin", authJustification, accessPublic, beginPasskeyLogin},
	{http.MethodPost, "/api/auth/passkey/login/finish", authJustification, accessPublic, finishPasskeyLogin},
	{http.MethodPost, "/api/auth/logout", authJustification, accessProtected, logout},
	{http.MethodPost, "/api/auth/logout-all", authJustification, accessProtected, logoutAll},
	{http.MethodPost, "/api/auth/password", authJustification, accessProtected, changePassword},
	{http.MethodGet, "/api/auth/sessions", authJustification, accessProtected, listSessions},
	{http.MethodPost, "/api/auth/webauthn/register/begin", authJustification, accessProtected, beginRegistration},
	{http.MethodPost, "/api/auth/webauthn/register/finish", authJustification, accessProtected, finishRegistration},
	{http.MethodGet, "/api/auth/webauthn/credentials", authJustification, accessProtected, listCredentials},
	{http.MethodPatch, "/api/auth/webauthn/credentials/{id}", authJustification, accessProtected, renameCredential},
	{http.MethodDelete, "/api/auth/webauthn/credentials/{id}", authJustification, accessProtected, deleteCredential},
	{http.MethodPost, "/api/auth/totp/setup/begin", authJustification, accessProtected, beginTOTPSetup},
	{http.MethodPost, "/api/auth/totp/setup/finish", authJustification, accessProtected, finishTOTPSetup},
	{http.MethodDelete, "/api/auth/totp", authJustification, accessProtected, removeTOTP},
}

// routesOf donne la forme commune de la table (fichier d'or, en-tête Allow) ; handle n'y sert pas.
func routesOf(table []authRoute) []route {
	routes := make([]route, 0, len(table))
	for _, apiRoute := range table {
		routes = append(routes, route{method: apiRoute.method, path: apiRoute.path, justification: apiRoute.justification})
	}
	return routes
}

// Bornes des champs, contrôlées avant tout appel au Service (aucune chaîne énorme n'atteint argon2).
const (
	maxUsernameRunes       = 64
	maxCredentialNameRunes = 64
	maxCodeBytes           = 16
	maxCredentialIDBytes   = 256
)

// États rendus par les routes d'authentification.
const (
	stateNoAccount            = "no_account"
	stateLoggedOut            = "logged_out"
	stateAuthenticated        = "authenticated"
	stateSecondFactorRequired = "second_factor_required"
)

type stateBody struct {
	State string `json:"state"`
}

// loginBody : Factors et WebAuthn seulement quand un second facteur est attendu.
type loginBody struct {
	State    string          `json:"state"`
	Factors  []string        `json:"factors,omitempty"`
	WebAuthn json.RawMessage `json:"webauthn,omitempty"`
}

type factorCounts struct {
	Keys     int  `json:"keys"`
	Passkeys int  `json:"passkeys"`
	TOTP     bool `json:"totp"`
}

// sessionStateBody : Username et SecondFactors seulement pour une session ouverte.
type sessionStateBody struct {
	State             string        `json:"state"`
	WebAuthnAvailable bool          `json:"webauthnAvailable"`
	Username          string        `json:"username,omitempty"`
	SecondFactors     *factorCounts `json:"secondFactors,omitempty"`
}

type credentialsBody struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type assertionBody struct {
	Credential json.RawMessage `json:"credential"`
}

type codeBody struct {
	Code string `json:"code"`
}

func tooManyRunes(value string, maxRunes int) bool { return utf8.RuneCountInString(value) > maxRunes }

func clientOf(request *http.Request) app.ClientInfo {
	return app.ClientInfo{Address: remoteAddress(request), UserAgent: request.UserAgent()}
}

func getAuthSession(handler *Handler, _ http.ResponseWriter, request *http.Request) (any, int, error) {
	status, err := handler.service.AuthStatus(request.Context())
	if err != nil {
		return nil, 0, err
	}
	body := sessionStateBody{State: stateNoAccount, WebAuthnAvailable: status.WebAuthnAvailable}
	if !status.AccountExists {
		return body, http.StatusOK, nil
	}
	body.State = stateLoggedOut
	token, found := cookieValue(request, handler.sessionCookieName())
	if !found {
		return body, http.StatusOK, nil
	}
	view, err := handler.service.ResumeSession(request.Context(), token)
	if errors.Is(err, app.ErrSessionInvalid) {
		return body, http.StatusOK, nil
	}
	if err != nil {
		return nil, 0, err
	}
	summary, err := handler.service.AccountSummary(request.Context())
	if err != nil {
		return nil, 0, err
	}
	body.State, body.Username = stateAuthenticated, view.Username
	body.SecondFactors = &factorCounts{Keys: summary.SecondFactorKeys, Passkeys: summary.Passkeys, TOTP: summary.TOTP}
	return body, http.StatusOK, nil
}

func setupAccount(handler *Handler, writer http.ResponseWriter, request *http.Request) (any, int, error) {
	var body credentialsBody
	if err := decodeBody(request, &body); err != nil {
		return nil, 0, err
	}
	if tooManyRunes(body.Username, maxUsernameRunes) {
		return nil, 0, app.ErrInvalidUsername
	}
	if len(body.Password) > auth.MaxPasswordBytes {
		return nil, 0, auth.ErrPasswordTooLong
	}
	grant, err := handler.service.SetupAccount(request.Context(), body.Username, body.Password, clientOf(request))
	if err != nil {
		return nil, 0, err
	}
	handler.setSessionCookie(writer, grant)
	return stateBody{State: stateAuthenticated}, http.StatusCreated, nil
}

// loginWithPassword rend la même réponse pour un identifiant inconnu, un mauvais mot de passe et une faute interne de
// l'étape (hachage abîmé…), consignée seule au journal.
func loginWithPassword(handler *Handler, writer http.ResponseWriter, request *http.Request) (any, int, error) {
	var body credentialsBody
	if err := decodeBody(request, &body); err != nil {
		return handler.loginStepFailed(request, err, loginReasonPassword)
	}
	if tooManyRunes(body.Username, maxUsernameRunes) || len(body.Password) > auth.MaxPasswordBytes {
		handler.logLoginFailed(request, loginReasonPassword)
		return nil, 0, invalidCredentials()
	}
	result, err := handler.service.Login(request.Context(), body.Username, body.Password, clientOf(request))
	if err != nil {
		return nil, 0, handler.passwordStepFailure(request, err)
	}
	if result.Session != nil {
		return handler.signedIn(writer, request, *result.Session)
	}
	pending, err := handler.secondFactorRequired(writer, request, result)
	if err != nil {
		return handler.loginStepFailed(request, err, loginReasonPassword)
	}
	return pending, http.StatusOK, nil
}

func (handler *Handler) passwordStepFailure(request *http.Request, err error) error {
	reason := loginFailureReason(err, loginReasonPassword)
	handler.logLoginFailed(request, reason)
	if errors.Is(err, app.ErrThrottled) {
		return err
	}
	if reason == loginReasonInternal {
		handler.logLine(slog.LevelError, LogCodeAPIInternalError, i18n.KeyLogAPIInternalError, map[string]string{"error": err.Error()})
	}
	return invalidCredentials()
}

func (handler *Handler) signedIn(writer http.ResponseWriter, request *http.Request, grant app.SessionGrant) (any, int, error) {
	handler.setSessionCookie(writer, grant)
	handler.logLoginSucceeded(request)
	return stateBody{State: stateAuthenticated}, http.StatusOK, nil
}

// secondFactorRequired garde le ticket (et l'état WebAuthn) côté serveur, lié au navigateur par le cookie d'avant
// session ; aucune session n'est émise. Sans WebAuthn utilisable, seul le TOTP reste proposé.
func (handler *Handler) secondFactorRequired(writer http.ResponseWriter, request *http.Request, result app.LoginResult) (loginBody, error) {
	held := heldState{purpose: purposeLogin, ticket: result.Ticket}
	body := loginBody{State: stateSecondFactorRequired}
	for _, factor := range result.SecondFactors {
		if factor != app.SecondFactorWebAuthn {
			body.Factors = append(body.Factors, factor)
			continue
		}
		ceremony, err := handler.service.BeginWebAuthnLogin(request.Context(), result.Ticket)
		if errors.Is(err, app.ErrWebAuthnUnavailable) {
			continue
		}
		if err != nil {
			return loginBody{}, err
		}
		held.state, body.WebAuthn = ceremony.State, ceremony.Options
		body.Factors = append(body.Factors, factor)
	}
	if len(body.Factors) == 0 {
		return loginBody{}, app.ErrWebAuthnUnavailable
	}
	id := newPreAuthID()
	handler.preAuth.put(id, held)
	handler.setPreAuthCookie(writer, id)
	return body, nil
}

// takePreAuth consomme l'état d'avant session du cookie dans les deux tables, quelle que soit l'issue de l'étape ou
// la route qui le présente, et efface le cookie.
func (handler *Handler) takePreAuth(writer http.ResponseWriter, request *http.Request, purpose string) (heldState, bool) {
	id, found := cookieValue(request, handler.preAuthCookieName())
	if !found {
		return heldState{}, false
	}
	handler.clearPreAuthCookie(writer)
	target, other := handler.preAuth, handler.passkeyPreAuth
	if purpose == purposePasskey {
		target, other = other, target
	}
	other.forget(id)
	return target.take(id, purpose)
}

func decodeAssertion(request *http.Request) (*bytes.Reader, error) {
	var body assertionBody
	if err := decodeBody(request, &body); err != nil {
		return nil, err
	}
	return requireCredential(body.Credential)
}

// requireCredential rend l'objet de PublicKeyCredential.toJSON() tel quel, pour que le Service le lise.
func requireCredential(credential json.RawMessage) (*bytes.Reader, error) {
	if len(credential) == 0 || bytes.Equal(credential, []byte("null")) {
		return nil, newAPIError(http.StatusBadRequest, errorCodeUsage, named(i18n.KeyErrorWebFieldRequired, "credential"))
	}
	return bytes.NewReader(credential), nil
}

func finishWebAuthnLogin(handler *Handler, writer http.ResponseWriter, request *http.Request) (any, int, error) {
	held, found := handler.takePreAuth(writer, request, purposeLogin)
	if !found || len(held.state) == 0 {
		return handler.loginStepFailed(request, stepExpired(), loginReasonWebAuthn)
	}
	assertion, err := decodeAssertion(request)
	if err != nil {
		return handler.loginStepFailed(request, err, loginReasonWebAuthn)
	}
	grant, err := handler.service.FinishWebAuthnLogin(request.Context(), held.ticket, held.state, assertion, clientOf(request))
	if err != nil {
		return handler.loginStepFailed(request, err, loginReasonWebAuthn)
	}
	return handler.signedIn(writer, request, grant)
}

func loginWithTOTP(handler *Handler, writer http.ResponseWriter, request *http.Request) (any, int, error) {
	held, found := handler.takePreAuth(writer, request, purposeLogin)
	if !found {
		return handler.loginStepFailed(request, stepExpired(), loginReasonTOTP)
	}
	var body codeBody
	if err := decodeBody(request, &body); err != nil {
		return handler.loginStepFailed(request, err, loginReasonTOTP)
	}
	if len(body.Code) > maxCodeBytes {
		return handler.loginStepFailed(request, fieldTooLong("code"), loginReasonTOTP)
	}
	grant, err := handler.service.LoginWithTOTP(request.Context(), held.ticket, body.Code, clientOf(request))
	if err != nil {
		return handler.loginStepFailed(request, err, loginReasonTOTP)
	}
	return handler.signedIn(writer, request, grant)
}

// beginPasskeyLogin : une avalanche d'appels n'évince que d'autres clés d'accès en attente, risque accepté en v1 (le
// Service limite les finish).
func beginPasskeyLogin(handler *Handler, writer http.ResponseWriter, request *http.Request) (any, int, error) {
	if err := decodeBody(request, &struct{}{}); err != nil {
		return nil, 0, err
	}
	ceremony, err := handler.service.BeginPasskeyLogin(request.Context())
	if err != nil {
		return nil, 0, err
	}
	id := newPreAuthID()
	handler.passkeyPreAuth.put(id, heldState{purpose: purposePasskey, state: ceremony.State})
	handler.setPreAuthCookie(writer, id)
	return loginBody{State: stateLoggedOut, WebAuthn: ceremony.Options}, http.StatusOK, nil
}

func finishPasskeyLogin(handler *Handler, writer http.ResponseWriter, request *http.Request) (any, int, error) {
	held, found := handler.takePreAuth(writer, request, purposePasskey)
	if !found {
		return handler.loginStepFailed(request, stepExpired(), loginReasonPasskey)
	}
	assertion, err := decodeAssertion(request)
	if err != nil {
		return handler.loginStepFailed(request, err, loginReasonPasskey)
	}
	grant, err := handler.service.FinishPasskeyLogin(request.Context(), held.state, assertion, clientOf(request))
	if err != nil {
		return handler.loginStepFailed(request, err, loginReasonPasskey)
	}
	return handler.signedIn(writer, request, grant)
}

func (handler *Handler) loginStepFailed(request *http.Request, err error, stepReason string) (any, int, error) {
	handler.logLoginFailed(request, loginFailureReason(err, stepReason))
	return nil, 0, err
}
