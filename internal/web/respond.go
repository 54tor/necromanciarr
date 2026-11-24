package web

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"necromanciarr/internal/app"
	"necromanciarr/internal/config"
	"necromanciarr/internal/i18n"
)

// Codes d'erreur propres au web, en plus de ceux d'app.Classify.
const (
	errorCodeUsage                = app.ErrorCodeUsage
	errorCodeNotFound             = app.ErrorCodeNotFound
	errorCodeStageRunning         = app.ErrorCodeStageRunning
	errorCodeUnauthorized         = "unauthorized"
	errorCodeForbidden            = "forbidden"
	errorCodeConfirmationRequired = "confirmation_required"
	errorCodeMethodNotAllowed     = "method_not_allowed"
	errorCodeShuttingDown         = "shutting_down"
)

// statusByErrorCode donne le statut HTTP de chaque code stable ; un code absent vaut 500.
var statusByErrorCode = map[string]int{
	app.ErrorCodeUsage:          http.StatusBadRequest,
	app.ErrorCodeStale:          http.StatusConflict,
	app.ErrorCodeRefused:        http.StatusUnprocessableEntity,
	app.ErrorCodeNotFound:       http.StatusNotFound,
	app.ErrorCodeNotConfigured:  http.StatusConflict,
	app.ErrorCodeStageRunning:   http.StatusConflict,
	app.ErrorCodeInspectionBusy: http.StatusConflict,
}

// ErrorBody est le corps stable d'une erreur : {"error": {"code", "message"}} dans une réponse, "error" d'un job.
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// apiError est une erreur du web lui-même (requête invalide, accès refusé), avec son statut et son message sûr ;
// allow est l'en-tête Allow d'un 405.
type apiError struct {
	status  int
	allow   string
	code    string
	message i18n.Message
}

func newAPIError(status int, code string, message i18n.Message) *apiError {
	return &apiError{status: status, code: code, message: message}
}

// named construit un message dont le gabarit porte un seul paramètre {name}.
func named(key i18n.Key, value string) i18n.Message {
	return i18n.Message{Key: key, Params: map[string]string{"name": value}}
}

func (failure *apiError) Error() string { return string(failure.message.Key) }

// describe donne le statut et le corps d'une erreur : le message est celui de l'erreur typée reconnue, rendu dans la
// langue du Service, jamais le texte brut de la chaîne (chemins, réponses de services) ; une erreur non typée devient
// l'erreur interne traduite. Le code ne dépend jamais de la langue.
func (handler *Handler) describe(err error) (int, ErrorBody) {
	if failure, isAPIError := errors.AsType[*apiError](err); isAPIError {
		return failure.status, ErrorBody{Code: failure.code, Message: handler.translator.Render(failure.message)}
	}
	code, _ := app.Classify(err)
	status, known := statusByErrorCode[code]
	if !known {
		status = http.StatusInternalServerError
	}
	return status, ErrorBody{Code: code, Message: handler.translator.Render(app.MessageOf(err))}
}

// secrets liste les secrets de la configuration, masqués dans chaque sortie (corps JSON et journal).
func (handler *Handler) secrets() []string { return handler.settings.KnownSecrets() }

// writeError écrit l'erreur ; une erreur interne part masquée dans le journal du serveur, pour l'opérateur, sans être renvoyée.
func (handler *Handler) writeError(writer http.ResponseWriter, err error) {
	status, body := handler.describe(err)
	if status == http.StatusInternalServerError {
		handler.logLine(slog.LevelError, LogCodeAPIInternalError, i18n.KeyLogAPIInternalError, map[string]string{"error": err.Error()})
	}
	if failure, isAPIError := errors.AsType[*apiError](err); isAPIError && failure.allow != "" {
		writer.Header().Set("Allow", failure.allow)
	}
	handler.writeJSON(writer, status, map[string]ErrorBody{"error": body})
}

func (handler *Handler) writeJSON(writer http.ResponseWriter, status int, value any) {
	encoded, err := config.RedactJSON(value, handler.secrets()...)
	if err != nil {
		status = http.StatusInternalServerError
		encoded, _ = json.Marshal(map[string]ErrorBody{"error": {Code: app.ErrorCodeFailure, Message: handler.translator.Text(i18n.KeyErrorInternal, nil)}})
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_, _ = writer.Write(encoded)
}

// endpointFunc traite une requête de l'API : valeur à encoder et statut de succès, ou erreur.
type endpointFunc func(handler *Handler, request *http.Request) (any, int, error)

func (handler *Handler) endpoint(handle endpointFunc) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		value, status, err := handle(handler, request)
		if err != nil {
			handler.writeError(writer, err)
			return
		}
		handler.writeJSON(writer, status, value)
	})
}

func unknownRoute(*Handler, *http.Request) (any, int, error) {
	return nil, 0, newAPIError(http.StatusNotFound, errorCodeNotFound, i18n.Message{Key: i18n.KeyErrorWebUnknownRoute})
}

// decodeBody lit un objet JSON strict (champ inconnu refusé) ; un corps vide garde les valeurs par défaut.
func decodeBody(request *http.Request, target any) error {
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(target)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
		return newAPIError(http.StatusRequestEntityTooLarge, errorCodeUsage, i18n.Message{Key: i18n.KeyErrorWebBodyTooLarge})
	}
	if err != nil || decoder.More() {
		return newAPIError(http.StatusBadRequest, errorCodeUsage, i18n.Message{Key: i18n.KeyErrorWebInvalidJSON})
	}
	return nil
}
