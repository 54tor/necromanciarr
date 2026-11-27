package web

import (
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"

	"necromanciarr/internal/app"
	"necromanciarr/internal/auth"
	"necromanciarr/internal/i18n"
)

// Codes d'erreur stables de l'authentification par compte.
const (
	errorCodeBearerDisabled        = "bearer_disabled"
	errorCodeSessionRequired       = "session_required"
	errorCodeSetupTokenRequired    = "setup_token_required"
	errorCodeStepExpired           = "step_expired"
	errorCodeInvalidCredentials    = "invalid_credentials"
	errorCodeThrottled             = "throttled"
	errorCodeAccountExists         = "account_exists"
	errorCodeInvalidUsername       = "invalid_username"
	errorCodePasswordTooShort      = "password_too_short"
	errorCodePasswordTooLong       = "password_too_long"
	errorCodeWebAuthnUnavailable   = "webauthn_unavailable"
	errorCodeInvalidAssertion      = "invalid_assertion"
	errorCodeLoginTicketInvalid    = "login_ticket_invalid"
	errorCodeInvalidCode           = "invalid_code"
	errorCodeTOTPUnavailable       = "totp_unavailable"
	errorCodeTOTPSecretUnreadable  = "totp_secret_unreadable"
	errorCodeCredentialNotFound    = "credential_not_found"
	errorCodeInvalidCredentialName = "invalid_credential_name"
)

// Raisons courtes d'une ligne api_login_failed : jamais l'identifiant, le mot de passe, le code ni un état.
const (
	loginReasonPassword    = "password"
	loginReasonWebAuthn    = "webauthn"
	loginReasonTOTP        = "totp"
	loginReasonPasskey     = "passkey"
	loginReasonStepExpired = "step_expired"
	loginReasonThrottled   = "throttled"
	loginReasonInternal    = "internal"
)

// authErrorClass donne le statut, le code et le message d'une sentinelle du Service ou du paquet auth.
type authErrorClass struct {
	sentinel error
	status   int
	code     string
	key      i18n.Key
}

var authErrorClasses = []authErrorClass{
	{app.ErrInvalidCredentials, http.StatusUnauthorized, errorCodeInvalidCredentials, i18n.KeyErrorAuthInvalidCredentials},
	{app.ErrSessionInvalid, http.StatusUnauthorized, errorCodeSessionRequired, i18n.KeyErrorAuthSessionRequired},
	{app.ErrAccountExists, http.StatusConflict, errorCodeAccountExists, i18n.KeyErrorAuthAccountExists},
	{app.ErrInvalidUsername, http.StatusBadRequest, errorCodeInvalidUsername, i18n.KeyErrorAuthInvalidUsername},
	{auth.ErrPasswordTooShort, http.StatusBadRequest, errorCodePasswordTooShort, i18n.KeyErrorAuthPasswordTooShort},
	{auth.ErrPasswordTooLong, http.StatusBadRequest, errorCodePasswordTooLong, i18n.KeyErrorAuthPasswordTooLong},
	{app.ErrLoginTicketInvalid, http.StatusUnauthorized, errorCodeLoginTicketInvalid, i18n.KeyErrorAuthLoginTicketInvalid},
	{app.ErrInvalidAssertion, http.StatusUnauthorized, errorCodeInvalidAssertion, i18n.KeyErrorAuthInvalidAssertion},
	{app.ErrWebAuthnUnavailable, http.StatusServiceUnavailable, errorCodeWebAuthnUnavailable, i18n.KeyErrorAuthWebAuthnUnavailable},
	{app.ErrCredentialNotFound, http.StatusNotFound, errorCodeCredentialNotFound, i18n.KeyErrorAuthCredentialNotFound},
	{app.ErrInvalidCredentialName, http.StatusBadRequest, errorCodeInvalidCredentialName, i18n.KeyErrorAuthInvalidCredentialName},
	{app.ErrInvalidCredentialKind, http.StatusBadRequest, errorCodeUsage, i18n.KeyErrorAuthInvalidCredentialKind},
	{app.ErrTOTPUnavailable, http.StatusServiceUnavailable, errorCodeTOTPUnavailable, i18n.KeyErrorAuthTOTPUnavailable},
	{app.ErrInvalidCode, http.StatusUnauthorized, errorCodeInvalidCode, i18n.KeyErrorAuthInvalidCode},
	{app.ErrTOTPSecretUnreadable, http.StatusInternalServerError, errorCodeTOTPSecretUnreadable, i18n.KeyErrorAuthTOTPSecretUnreadable},
}

// authError change une erreur du Service en erreur de l'API, avec Retry-After pour le limiteur ; une erreur sans
// sentinelle reste telle quelle (erreur interne masquée).
func authError(err error) error {
	if throttled, isThrottled := errors.AsType[*app.ThrottledError](err); isThrottled {
		return throttledError(throttled)
	}
	for _, class := range authErrorClasses {
		if errors.Is(err, class.sentinel) {
			return newAPIError(class.status, class.code, i18n.Message{Key: class.key})
		}
	}
	return err
}

func throttledError(throttled *app.ThrottledError) *apiError {
	seconds := strconv.Itoa(max(1, int(math.Ceil(throttled.RetryAfter.Seconds()))))
	failure := newAPIError(http.StatusTooManyRequests, errorCodeThrottled, i18n.Message{Key: i18n.KeyErrorAuthThrottled, Params: map[string]string{"seconds": seconds}})
	failure.headers = map[string]string{"Retry-After": seconds}
	return failure
}

func authRefusal(status int, code string, key i18n.Key) *apiError {
	return newAPIError(status, code, i18n.Message{Key: key})
}

func stepExpired() *apiError {
	return authRefusal(http.StatusUnauthorized, errorCodeStepExpired, i18n.KeyErrorAuthStepExpired)
}

func invalidCredentials() *apiError {
	return authRefusal(http.StatusUnauthorized, errorCodeInvalidCredentials, i18n.KeyErrorAuthInvalidCredentials)
}

// loginFailureReason nomme l'échec d'une tentative de connexion pour le journal, sans rien de saisi.
func loginFailureReason(err error, stepReason string) string {
	if errors.Is(err, app.ErrThrottled) {
		return loginReasonThrottled
	}
	apiFailure, isAPIError := errors.AsType[*apiError](authError(err))
	switch {
	case !isAPIError || apiFailure.status >= http.StatusInternalServerError:
		return loginReasonInternal
	case apiFailure.code == errorCodeStepExpired:
		return loginReasonStepExpired
	}
	return stepReason
}

// logLoginFailed écrit une ligne par tentative refusée : l'adresse et une raison courte, rien d'autre.
func (handler *Handler) logLoginFailed(request *http.Request, reason string) {
	level := slog.LevelWarn
	if reason == loginReasonInternal {
		level = slog.LevelError
	}
	handler.logLine(level, LogCodeAPILoginFailed, i18n.KeyLogAPILoginFailed, map[string]string{"address": remoteAddress(request), "reason": reason})
}

func (handler *Handler) logLoginSucceeded(request *http.Request) {
	handler.logLine(slog.LevelInfo, LogCodeAPILoginSucceeded, i18n.KeyLogAPILoginSucceeded, map[string]string{"address": remoteAddress(request)})
}
