package web

import (
	"context"
	"io"
	"log/slog"
	"strconv"

	"necromanciarr/internal/config"
	"necromanciarr/internal/i18n"
)

// Codes stables du journal du serveur : l'attribut « code » de chaque ligne ne change jamais avec la langue.
const (
	LogCodeJobFailed          = "job_failed"
	LogCodeJobPanicked        = "job_panicked"
	LogCodeServerListening    = "server_listening"
	LogCodeNonLoopbackWarning = "non_loopback_warning"
	LogCodeHealthcheckFailed  = "healthcheck_failed"
	LogCodeAPIInternalError   = "api_internal_error"
	LogCodeAPIUnauthorized    = "api_unauthorized"
)

// NewLogger écrit des lignes texte clé=valeur sur writer.
func NewLogger(writer io.Writer) *slog.Logger { return slog.New(slog.NewTextHandler(writer, nil)) }

// LogLine écrit une ligne dont le message est rendu dans la langue du translator et dont l'attribut « code » est
// stable ; chaque valeur de paramètre est masquée (secrets connus, URL, segments longs) avant rendu.
func LogLine(ctx context.Context, logger *slog.Logger, translator i18n.Translator, level slog.Level, code string, key i18n.Key, params map[string]string, secrets ...string) {
	redacted := make(map[string]string, len(params))
	for name, value := range params {
		redacted[name] = config.RedactText(value, secrets...)
	}
	logger.LogAttrs(ctx, level, translator.Text(key, redacted), slog.String("code", code))
}

// UseLogger remplace le journal (stderr par défaut) ; à appeler avant de servir.
func (handler *Handler) UseLogger(logger *slog.Logger) { handler.logger = logger }

func (handler *Handler) logLine(level slog.Level, code string, key i18n.Key, params map[string]string) {
	LogLine(context.Background(), handler.logger, handler.translator, level, code, key, params, handler.secrets()...)
}

func jobParams(job *Job, detail string) map[string]string {
	return map[string]string{"id": strconv.FormatInt(job.ID, 10), "action": job.Action, "error": detail}
}
