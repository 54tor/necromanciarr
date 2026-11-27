package doctor

import (
	"strings"

	"necromanciarr/internal/config"
	"necromanciarr/internal/i18n"
)

func checkAuth(settings config.Config) Result {
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

func hasHTTPSPublicURL(settings config.Config) bool {
	return strings.HasPrefix(settings.PublicURL, "https://")
}
