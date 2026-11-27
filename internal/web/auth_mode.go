package web

import (
	"errors"
	"log/slog"
	"net/http"

	"necromanciarr/internal/app"
	"necromanciarr/internal/i18n"
)

// accessPolicy dit comment une route s'authentifie.
type accessPolicy int

const (
	// accessProtected : sans compte, le jeton porteur comme avant ; avec un compte, la session seule.
	accessProtected accessPolicy = iota
	// accessPublic : ni jeton ni session (état, étapes de connexion) ; la route contrôle elle-même ses cookies.
	accessPublic
	// accessSetupBearer : jeton porteur configuré et présenté, même en boucle locale (création du compte).
	accessSetupBearer
)

// admittedRequest est la requête admise, avec le jeton de session dans son contexte ; byBearer dit qu'un jeton
// porteur valable l'a authentifiée (aucun cookie ambiant n'entre alors en jeu).
type admittedRequest struct {
	request  *http.Request
	byBearer bool
}

// authenticate choisit le mode à chaque requête, sans cache : un account reset lancé par la CLI rouvre le jeton
// porteur aussitôt.
func (handler *Handler) authenticate(request *http.Request, access accessPolicy) (admittedRequest, error) {
	switch access {
	case accessPublic:
		return admittedRequest{request: request}, handler.refuseBearerOnceAnAccountExists(request)
	case accessSetupBearer:
		return admittedRequest{request: request, byBearer: true}, handler.requireSetupBearer(request)
	}
	status, err := handler.service.AuthStatus(request.Context())
	if err != nil {
		return admittedRequest{}, err
	}
	if status.AccountExists {
		return handler.requireSession(request)
	}
	if !handler.authorized(request) {
		return admittedRequest{}, handler.unauthorizedBearer(request)
	}
	return admittedRequest{request: request, byBearer: handler.settings.AuthToken != ""}, nil
}

// refuseBearerOnceAnAccountExists : une route publique n'ignore pas un jeton porteur en mode compte, elle le refuse.
func (handler *Handler) refuseBearerOnceAnAccountExists(request *http.Request) error {
	if request.Header.Get("Authorization") == "" {
		return nil
	}
	status, err := handler.service.AuthStatus(request.Context())
	if err != nil || !status.AccountExists {
		return err
	}
	return handler.bearerDisabled(request)
}

func (handler *Handler) bearerDisabled(request *http.Request) *apiError {
	handler.logUnauthorized(request)
	return authRefusal(http.StatusUnauthorized, errorCodeBearerDisabled, i18n.KeyErrorAuthBearerDisabled)
}

func (handler *Handler) requireSetupBearer(request *http.Request) error {
	if handler.settings.AuthToken == "" {
		return authRefusal(http.StatusForbidden, errorCodeSetupTokenRequired, i18n.KeyErrorAuthSetupTokenRequired)
	}
	if !handler.bearerMatches(request) {
		return handler.unauthorizedBearer(request)
	}
	return nil
}

// requireSession refuse tout en-tête Authorization (le jeton porteur ne vaut plus rien) et ne lit le jeton de session
// que dans son cookie.
func (handler *Handler) requireSession(request *http.Request) (admittedRequest, error) {
	if request.Header.Get("Authorization") != "" {
		return admittedRequest{}, handler.bearerDisabled(request)
	}
	token, found := cookieValue(request, handler.sessionCookieName())
	if !found {
		handler.logUnauthorized(request)
		return admittedRequest{}, sessionRequired()
	}
	if _, err := handler.service.ResumeSession(request.Context(), token); err != nil {
		if !errors.Is(err, app.ErrSessionInvalid) {
			return admittedRequest{}, err
		}
		handler.logUnauthorized(request)
		return admittedRequest{}, sessionRequired()
	}
	return admittedRequest{request: request.WithContext(withSessionToken(request.Context(), token))}, nil
}

func sessionRequired() *apiError {
	return authRefusal(http.StatusUnauthorized, errorCodeSessionRequired, i18n.KeyErrorAuthSessionRequired)
}

// unauthorizedBearer : une ligne par échec, sans limite de débit (risque accepté en v1) ; ni jeton ni en-tête n'y
// figurent.
func (handler *Handler) unauthorizedBearer(request *http.Request) *apiError {
	handler.logUnauthorized(request)
	failure := newAPIError(http.StatusUnauthorized, errorCodeUnauthorized, i18n.Message{Key: i18n.KeyErrorWebBearerToken})
	failure.headers = map[string]string{"WWW-Authenticate": bearerScheme}
	return failure
}

func (handler *Handler) logUnauthorized(request *http.Request) {
	handler.logLine(slog.LevelWarn, LogCodeAPIUnauthorized, i18n.KeyLogAPIUnauthorized, map[string]string{"address": remoteAddress(request)})
}
