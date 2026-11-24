package web

import (
	"crypto/sha256"
	"crypto/subtle"
	"log"
	"mime"
	"net"
	"net/http"
	"net/netip"
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
	bearerScheme         = "Bearer"
	defaultHTTPPort      = "80"
	crossSiteFetch       = "cross-site"
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
// protège alors l'API quel que soit le nom par lequel on y arrive, derrière un mandataire notamment).
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
		// Un navigateur omet le port par défaut dans Host : « http://localhost/ » envoie « localhost ».
		if port == defaultHTTPPort {
			hosts = append(hosts, strings.ToLower(bareHost(host)))
		}
	}
	return hosts
}

// bareHost met une adresse IPv6 entre crochets, comme dans un en-tête Host sans port.
func bareHost(host string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

func (hosts allowedHosts) admits(host string) bool {
	return hosts == nil || slices.Contains(hosts, strings.ToLower(host))
}

// protect applique, dans l'ordre, l'absence de cache, le contrôle de Host, le refus d'une lecture d'un autre site
// (Sec-Fetch-Site, posé par les navigateurs), le jeton, les contrôles anti-CSRF d'un
// POST et la borne du corps. Aucun en-tête CORS n'est jamais posé.
func (handler *Handler) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		if !handler.hosts.admits(request.Host) {
			handler.writeError(writer, newAPIError(http.StatusForbidden, errorCodeForbidden, errorMessageHost))
			return
		}
		if request.Header.Get("Sec-Fetch-Site") == crossSiteFetch {
			handler.writeError(writer, newAPIError(http.StatusForbidden, errorCodeForbidden, errorMessageOrigin))
			return
		}
		if !handler.authorized(request) {
			// Une ligne par échec, sans limite de débit (risque accepté en v1) ; ni jeton ni en-tête n'y figurent.
			log.Printf("api: unauthorized request from %s", remoteAddress(request)) // #nosec G706 -- adresse analysée par netip, sans texte libre
			writer.Header().Set("WWW-Authenticate", bearerScheme)
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
// la durée. Le jeton n'est lu que dans l'en-tête Authorization (schéma Bearer, casse ignorée comme le veut HTTP),
// jamais dans l'URL ni un cookie.
func (handler *Handler) authorized(request *http.Request) bool {
	if handler.settings.AuthToken == "" {
		return true
	}
	scheme, presented, _ := strings.Cut(request.Header.Get("Authorization"), " ")
	hasBearer := strings.EqualFold(scheme, bearerScheme)
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

// remoteAddress ne garde de RemoteAddr qu'une adresse analysée : aucun texte arbitraire n'entre dans le journal.
func remoteAddress(request *http.Request) string {
	address, err := netip.ParseAddrPort(request.RemoteAddr)
	if err != nil {
		return "unknown"
	}
	return address.String()
}

func sameOrigin(origin, host string) bool {
	parsedOrigin, err := url.Parse(origin)
	if err != nil {
		return false
	}
	isWebScheme := parsedOrigin.Scheme == "http" || parsedOrigin.Scheme == "https"
	return isWebScheme && strings.EqualFold(parsedOrigin.Host, host)
}
