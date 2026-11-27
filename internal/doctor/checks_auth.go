package doctor

import (
	"strings"

	"necromanciarr/internal/config"
	"necromanciarr/internal/i18n"
)

func checkAuth(settings config.Config) Result {
	if hasHTTPSPublicURL(settings) && !webAuthnAvailable(settings) {
		return Result{
			Status: Warn, Code: CodeAuthPublicURLUnusable,
			Detail: message(i18n.KeyDoctorAuthPublicURLUnusable, "publicUrl", settings.PublicURL),
			Hint:   message(i18n.KeyDoctorAuthPublicURLUnusableHint),
		}
	}
	if config.IsLoopbackAddress(settings.ListenAddr) {
		return Result{Status: OK, Code: CodeLoopback, Detail: message(i18n.KeyDoctorAuthLoopback, "address", settings.ListenAddr)}
	}
	if settings.AuthToken == "" {
		return Result{
			Status: Fail, Code: CodeNoAuthToken,
			Detail: message(i18n.KeyDoctorAuthNoToken, "address", settings.ListenAddr), Hint: message(i18n.KeyDoctorAuthNoTokenHint),
		}
	}
	if !hasHTTPSPublicURL(settings) {
		return Result{
			Status: Warn, Code: CodeAuthPublicURLMissing,
			Detail: message(i18n.KeyDoctorAuthPublicURLMissing, "address", settings.ListenAddr),
			Hint:   message(i18n.KeyDoctorAuthPublicURLMissingHint),
		}
	}
	return Result{Status: OK, Code: CodeAuthTokenOK, Detail: message(i18n.KeyDoctorAuthToken, "address", settings.ListenAddr)}
}

func webAuthnAvailable(settings config.Config) bool {
	_, _, available := settings.WebAuthnOrigin()
	return available
}

func hasHTTPSPublicURL(settings config.Config) bool {
	return strings.HasPrefix(settings.PublicURL, "https://")
}
