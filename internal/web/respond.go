package web

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"

	"necromanciarr/internal/app"
	"necromanciarr/internal/config"
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
	// internalErrorMessage remplace le texte d'une erreur non typée : il pourrait porter un chemin ou un secret.
	internalErrorMessage = "internal error"
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
	status int
	allow  string
	body   ErrorBody
}

func newAPIError(status int, code, message string) *apiError {
	return &apiError{status: status, body: ErrorBody{Code: code, Message: message}}
}

func (failure *apiError) Error() string { return failure.body.Message }

// describe donne le statut et le corps d'une erreur : le message est celui de l'erreur typée reconnue, jamais le
// texte brut de la chaîne (chemins, réponses de services) ; une erreur non typée devient « internal error ».
func describe(err error) (int, ErrorBody) {
	if failure, isAPIError := errors.AsType[*apiError](err); isAPIError {
		return failure.status, failure.body
	}
	code, typed := app.Classify(err)
	status, known := statusByErrorCode[code]
	if !known {
		status = http.StatusInternalServerError
	}
	switch {
	case typed == nil:
		return status, ErrorBody{Code: code, Message: internalErrorMessage}
	case errors.Is(typed, app.ErrStaleProposal):
		return status, ErrorBody{Code: code, Message: app.StaleProposalMessage}
	}
	return status, ErrorBody{Code: code, Message: typed.Error()}
}

// secrets liste les secrets de la configuration, masqués dans chaque sortie (corps JSON et journal).
func (handler *Handler) secrets() []string { return handler.settings.KnownSecrets() }

// writeError écrit l'erreur ; une erreur interne part masquée dans le journal du serveur, pour l'opérateur, sans être renvoyée.
func (handler *Handler) writeError(writer http.ResponseWriter, err error) {
	status, body := describe(err)
	if status == http.StatusInternalServerError {
		log.Printf("api: %s", config.RedactText(err.Error(), handler.secrets()...))
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
		encoded, _ = json.Marshal(map[string]ErrorBody{"error": {Code: app.ErrorCodeFailure, Message: internalErrorMessage}})
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
	return nil, 0, newAPIError(http.StatusNotFound, errorCodeNotFound, "unknown api route")
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
		return newAPIError(http.StatusRequestEntityTooLarge, errorCodeUsage, "request body too large")
	}
	if err != nil || decoder.More() {
		return newAPIError(http.StatusBadRequest, errorCodeUsage, "invalid json body")
	}
	return nil
}
