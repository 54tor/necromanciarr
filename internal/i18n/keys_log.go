package i18n

// Clés du journal du serveur : chaque ligne porte aussi un attribut « code » stable, jamais traduit.
const (
	KeyLogJobFailed          Key = "log.job_failed"
	KeyLogJobPanicked        Key = "log.job_panicked"
	KeyLogServerListening    Key = "log.server_listening"
	KeyLogNonLoopbackWarning Key = "log.non_loopback_warning"
	KeyLogHealthcheckFailed  Key = "log.healthcheck_failed"
	KeyLogAPIInternalError   Key = "log.api_internal_error"
	KeyLogAPIUnauthorized    Key = "log.api_unauthorized"
	KeyLogAPIPanicked        Key = "log.api_panicked"
	KeyLogHTTPServerError    Key = "log.http_server_error"
)

var logKeys = []Key{
	KeyLogJobFailed, KeyLogJobPanicked, KeyLogServerListening, KeyLogNonLoopbackWarning,
	KeyLogHealthcheckFailed, KeyLogAPIInternalError, KeyLogAPIUnauthorized, KeyLogAPIPanicked, KeyLogHTTPServerError,
}
