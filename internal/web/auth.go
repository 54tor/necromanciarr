package web

import (
	"crypto/sha256"
	"crypto/subtle"
	"mime"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"necromanciarr/internal/config"
)

// contentSecurityPolicy interdit tout script, style ou ressource inline et externe : le build Vite ne charge que des
// fichiers de même origine (vérifié sur dist), Vue pose ses styles par le CSSOM, que style-src n'atteint pas.
const contentSecurityPolicy = "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; " +
	"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// Éléments du modèle de sécurité de l'API : corps borné, en-tête que seul un script de même origine pose sans
// prévol CORS (et jamais un formulaire), schéma d'authentification.
const (
	maxBodyBytes         = 64 << 10
	requestedWithHeader  = "X-Requested-With"
	requestedWithValue   = "necromanciarr"
	bearerPrefix         = "Bearer "
	jsonContentType      = "application/json"
	loopbackHostIPv4     = "127.0.0.1"
	loopbackHostIPv6     = "::1"
	loopbackHostName     = "localhost"
	errorMessageHost     = "unknown host header"
	errorMessageOrigin   = "cross-site request refused"
	errorMessageToken    = "missing or invalid bearer token"
	errorMessageMimeType = "content type must be application/json"
)

func setSecurityHeaders(header http.Header) {
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Frame-Options", "DENY")
	header.Set("Cross-Origin-Opener-Policy", "same-origin")
	header.Set("Content-Security-Policy", contentSecurityPolicy)
}

// allowedHosts est la liste des en-têtes Host admis ; nil = aucun contrôle (écoute hors boucle locale, le jeton
// protège alors l'API quel que soit le nom par lequel on y arrive, derrière un mandataire par exemple).
type allowedHosts []string

// hostsFor dérive la liste de l'adresse d'écoute en boucle locale : l'adresse elle-même et localhost, 127.0.0.1 et
// [::1] sur le même port. Une page d'un autre domaine qui se fait résoudre en 127.0.0.1 (rebinding DNS) envoie son
// propre nom dans Host et est refusée.
func hostsFor(listenAddr string) allowedHosts {
	if !config.IsLoopbackAddress(listenAddr) {
		return nil
	}
	listenHost, port, _ := net.SplitHostPort(listenAddr)
	hosts := allowedHosts{}
	for _, host := range []string{listenHost, loopbackHostName, loopbackHostIPv4, loopbackHostIPv6} {
		hosts = append(hosts, strings.ToLower(net.JoinHostPort(host, port)))
	}
	return hosts
}

func (hosts allowedHosts) admits(host string) bool {
	return hosts == nil || slices.Contains(hosts, strings.ToLower(host))
}

// protect applique, dans l'ordre, l'absence de cache, le contrôle de Host, le jeton, les contrôles anti-CSRF d'un
// POST et la borne du corps. Aucun en-tête CORS n'est jamais posé.
func (handler *Handler) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		if !handler.hosts.admits(request.Host) {
			handler.writeError(writer, newAPIError(http.StatusForbidden, errorCodeForbidden, errorMessageHost))
			return
		}
		if !handler.authorized(request) {
			writer.Header().Set("WWW-Authenticate", "Bearer")
			handler.writeError(writer, newAPIError(http.StatusUnauthorized, errorCodeUnauthorized, errorMessageToken))
			return
		}
		if refusal := handler.checkUnsafeMethod(request); refusal != nil {
			handler.writeError(writer, refusal)
			return
		}
		request.Body = http.MaxBytesReader(writer, request.Body, maxBodyBytes)
		next.ServeHTTP(writer, request)
	})
}

// authorized compare les empreintes SHA-256 en temps constant : ni le contenu ni la longueur du jeton ne fuient par
// la durée. Le jeton n'est lu que dans l'en-tête Authorization, jamais dans l'URL ni un cookie.
func (handler *Handler) authorized(request *http.Request) bool {
	if handler.settings.AuthToken == "" {
		return true
	}
	presented, hasBearer := strings.CutPrefix(request.Header.Get("Authorization"), bearerPrefix)
	presentedSum := sha256.Sum256([]byte(presented))
	expectedSum := sha256.Sum256([]byte(handler.settings.AuthToken))
	tokenMatches := subtle.ConstantTimeCompare(presentedSum[:], expectedSum[:]) == 1
	return hasBearer && tokenMatches
}

// checkUnsafeMethod refuse un POST qui n'est pas du JSON (un formulaire d'un autre site ne peut pas en envoyer sans
// prévol) ou dont l'Origin n'est pas la nôtre ; sans jeton, un POST sans Origin doit porter X-Requested-With.
func (handler *Handler) checkUnsafeMethod(request *http.Request) error {
	if request.Method == http.MethodGet || request.Method == http.MethodHead {
		return nil
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != jsonContentType {
		return newAPIError(http.StatusUnsupportedMediaType, errorCodeUsage, errorMessageMimeType)
	}
	origin := request.Header.Get("Origin")
	if origin != "" && !sameOrigin(origin, request.Host) {
		return newAPIError(http.StatusForbidden, errorCodeForbidden, errorMessageOrigin)
	}
	hasCustomHeader := request.Header.Get(requestedWithHeader) == requestedWithValue
	if origin == "" && handler.settings.AuthToken == "" && !hasCustomHeader {
		return newAPIError(http.StatusForbidden, errorCodeForbidden, errorMessageOrigin)
	}
	return nil
}

func sameOrigin(origin, host string) bool {
	parsedOrigin, err := url.Parse(origin)
	if err != nil {
		return false
	}
	isWebScheme := parsedOrigin.Scheme == "http" || parsedOrigin.Scheme == "https"
	return isWebScheme && strings.EqualFold(parsedOrigin.Host, host)
}
