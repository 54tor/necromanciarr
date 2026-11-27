package i18n

// Clés de l'authentification par compte (routes /api/auth) : erreurs et lignes du journal.
const (
	KeyErrorAuthBearerDisabled        Key = "error.auth.bearer_disabled"
	KeyErrorAuthSessionRequired       Key = "error.auth.session_required"
	KeyErrorAuthSetupTokenRequired    Key = "error.auth.setup_token_required"
	KeyErrorAuthStepExpired           Key = "error.auth.step_expired"
	KeyErrorAuthInvalidCredentials    Key = "error.auth.invalid_credentials"
	KeyErrorAuthThrottled             Key = "error.auth.throttled"
	KeyErrorAuthAccountExists         Key = "error.auth.account_exists"
	KeyErrorAuthInvalidUsername       Key = "error.auth.invalid_username"
	KeyErrorAuthPasswordTooShort      Key = "error.auth.password_too_short"
	KeyErrorAuthPasswordTooLong       Key = "error.auth.password_too_long"
	KeyErrorAuthWebAuthnUnavailable   Key = "error.auth.webauthn_unavailable"
	KeyErrorAuthInvalidAssertion      Key = "error.auth.invalid_assertion"
	KeyErrorAuthLoginTicketInvalid    Key = "error.auth.login_ticket_invalid"
	KeyErrorAuthInvalidCode           Key = "error.auth.invalid_code"
	KeyErrorAuthTOTPUnavailable       Key = "error.auth.totp_unavailable"
	KeyErrorAuthTOTPSecretUnreadable  Key = "error.auth.totp_secret_unreadable"
	KeyErrorAuthCredentialNotFound    Key = "error.auth.credential_not_found"
	KeyErrorAuthInvalidCredentialName Key = "error.auth.invalid_credential_name"
	KeyErrorAuthInvalidCredentialKind Key = "error.auth.invalid_credential_kind"

	KeyLogAPILoginFailed    Key = "log.api_login_failed"
	KeyLogAPILoginSucceeded Key = "log.api_login_succeeded"
)

var authKeys = []Key{
	KeyErrorAuthBearerDisabled, KeyErrorAuthSessionRequired, KeyErrorAuthSetupTokenRequired, KeyErrorAuthStepExpired,
	KeyErrorAuthInvalidCredentials, KeyErrorAuthThrottled, KeyErrorAuthAccountExists, KeyErrorAuthInvalidUsername,
	KeyErrorAuthPasswordTooShort, KeyErrorAuthPasswordTooLong, KeyErrorAuthWebAuthnUnavailable,
	KeyErrorAuthInvalidAssertion, KeyErrorAuthLoginTicketInvalid, KeyErrorAuthInvalidCode, KeyErrorAuthTOTPUnavailable,
	KeyErrorAuthTOTPSecretUnreadable, KeyErrorAuthCredentialNotFound, KeyErrorAuthInvalidCredentialName,
	KeyErrorAuthInvalidCredentialKind, KeyLogAPILoginFailed, KeyLogAPILoginSucceeded,
}
