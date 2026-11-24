package torznab

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

var (
	// ErrBadResponse signale une réponse illisible, trop grosse, tronquée ou contraire au format Torznab.
	ErrBadResponse = errors.New("torznab: unreadable response")
	// ErrHostileResponse signale une réponse refusée parce qu'elle tente une attaque : déclaration DOCTYPE ou ENTITY,
	// imbrication trop profonde, plus de maxItems éléments.
	ErrHostileResponse = errors.New("torznab: hostile response refused")
	// ErrAccessDenied signale une clé refusée : HTTP 401 ou 403, ou erreur Torznab 100 à 199.
	ErrAccessDenied = errors.New("torznab: access denied")
	// ErrUnavailable signale une panne réseau, un délai dépassé, un 5xx ou un quota atteint.
	ErrUnavailable = errors.New("torznab: unavailable")

	errInvalidEndpoint = errors.New("torznab: endpoint URL must be an http(s) URL without fragment nor userinfo, " +
		"whose only query parameter is apikey")
	errUnknownMode = errors.New("torznab: search mode must be search, movie or tvsearch")
)

const (
	firstAccessCode = 100
	lastAccessCode  = 199
)

// unavailableCodes sont les codes Torznab et Newznab d'un refus passager : quota de requêtes ou de téléchargements
// (500, 501, 429), service indisponible (503), erreur inconnue ou API coupée (900, 910).
var unavailableCodes = map[int]bool{429: true, 500: true, 501: true, 503: true, 900: true, 910: true}

// UnavailableError est renvoyée pour un HTTP 429 ou 503, ou un code Torznab de quota ; elle englobe ErrUnavailable.
type UnavailableError struct {
	// RetryAfter est le délai demandé par l'en-tête Retry-After, plafonné à une heure ; zéro s'il est absent.
	RetryAfter time.Duration
	reason     string
}

// Error décrit le refus sans URL, sans en-tête et sans texte venu de l'indexeur.
func (unavailable *UnavailableError) Error() string {
	return fmt.Sprintf("%s (%s, retry after %s)", ErrUnavailable.Error(), unavailable.reason, unavailable.RetryAfter)
}

// Unwrap permet errors.Is(err, ErrUnavailable).
func (unavailable *UnavailableError) Unwrap() error { return ErrUnavailable }

// statusError traduit un statut HTTP hors 2xx ; seul le code d'un élément <error> du corps est lu, jamais son texte.
func statusError(status int, body []byte, retryAfter time.Duration) error {
	isRedirect := status >= http.StatusMultipleChoices && status < http.StatusBadRequest
	switch {
	case isRedirect:
		return fmt.Errorf("%w: redirect refused (HTTP %d)", ErrBadResponse, status)
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return fmt.Errorf("%w (HTTP %d)", ErrAccessDenied, status)
	case status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable:
		return &UnavailableError{RetryAfter: retryAfter, reason: "HTTP " + strconv.Itoa(status)}
	case status >= http.StatusInternalServerError || status == http.StatusRequestTimeout:
		return fmt.Errorf("%w (HTTP %d)", ErrUnavailable, status)
	}
	if code, isErrorElement := errorElementCode(body); isErrorElement {
		return elementError(code, retryAfter)
	}
	return fmt.Errorf("%w (HTTP %d)", ErrBadResponse, status)
}

// elementError traduit le code d'un élément <error code="…" description="…"> ; la description n'est jamais recopiée,
// car un indexeur peut y renvoyer la clé.
func elementError(rawCode string, retryAfter time.Duration) error {
	code, err := strconv.Atoi(rawCode)
	if err != nil {
		return fmt.Errorf("%w: torznab error without a numeric code", ErrBadResponse)
	}
	if code >= firstAccessCode && code <= lastAccessCode {
		return fmt.Errorf("%w (torznab code %d)", ErrAccessDenied, code)
	}
	if unavailableCodes[code] {
		return &UnavailableError{RetryAfter: retryAfter, reason: "torznab code " + strconv.Itoa(code)}
	}
	return fmt.Errorf("%w (torznab code %d)", ErrBadResponse, code)
}
