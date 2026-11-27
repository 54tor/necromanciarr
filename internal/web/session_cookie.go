package web

import (
	"context"
	"math"
	"net/http"
	"strings"
	"time"

	"necromanciarr/internal/app"
)

// Cookies de l'authentification : la session (tout le site) et l'état d'avant session (routes /api/auth seulement).
// Le préfixe __Host- exige Path=/, d'où __Secure- pour le cookie d'avant session.
const (
	sessionCookieBase  = "necromanciarr-session"
	preAuthCookieBase  = "necromanciarr-preauth"
	hostCookiePrefix   = "__Host-"
	secureCookiePrefix = "__Secure-"
	sessionCookiePath  = "/"
	preAuthCookiePath  = "/api/auth"
	httpsOriginPrefix  = "https://"
)

// sessionTokenKey porte, dans le contexte d'une requête admise par protect, le jeton de session lu dans le cookie.
type sessionTokenKey struct{}

func withSessionToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, sessionTokenKey{}, token)
}

func sessionTokenOf(request *http.Request) string {
	token, _ := request.Context().Value(sessionTokenKey{}).(string)
	return token
}

// secureCookies suit publicUrl et non la requête : derrière un proxy HTTPS, l'application ne voit que du HTTP.
func (handler *Handler) secureCookies() bool {
	return strings.HasPrefix(handler.settings.PublicURL, httpsOriginPrefix)
}

func (handler *Handler) sessionCookieName() string {
	if handler.secureCookies() {
		return hostCookiePrefix + sessionCookieBase
	}
	return sessionCookieBase
}

func (handler *Handler) preAuthCookieName() string {
	if handler.secureCookies() {
		return secureCookiePrefix + preAuthCookieBase
	}
	return preAuthCookieBase
}

func (handler *Handler) baseCookie(name, value, path string) *http.Cookie {
	return &http.Cookie{ // #nosec G124 -- Secure suit publicUrl (https) ; false seulement sur http en boucle locale, voir 02-configuration
		Name: name, Value: value, Path: path, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: handler.secureCookies(),
	}
}

func (handler *Handler) setSessionCookie(writer http.ResponseWriter, grant app.SessionGrant) {
	cookie := handler.baseCookie(handler.sessionCookieName(), grant.Token, sessionCookiePath) // #nosec G124 -- Secure suit publicUrl (https) ; false seulement sur http en boucle locale, voir 02-configuration
	cookie.Expires = grant.ExpiresAt.UTC()
	cookie.MaxAge = wholeSecondsUntil(grant.ExpiresAt, handler.clock())
	http.SetCookie(writer, cookie)
}

func (handler *Handler) clearSessionCookie(writer http.ResponseWriter) {
	cookie := handler.baseCookie(handler.sessionCookieName(), "", sessionCookiePath) // #nosec G124 -- Secure suit publicUrl (https) ; false seulement sur http en boucle locale, voir 02-configuration
	cookie.MaxAge = -1
	http.SetCookie(writer, cookie)
}

func (handler *Handler) setPreAuthCookie(writer http.ResponseWriter, id string) {
	cookie := handler.baseCookie(handler.preAuthCookieName(), id, preAuthCookiePath) // #nosec G124 -- Secure suit publicUrl (https) ; false seulement sur http en boucle locale, voir 02-configuration
	cookie.MaxAge = int(preAuthLifetime / time.Second)
	http.SetCookie(writer, cookie)
}

func (handler *Handler) clearPreAuthCookie(writer http.ResponseWriter) {
	cookie := handler.baseCookie(handler.preAuthCookieName(), "", preAuthCookiePath) // #nosec G124 -- Secure suit publicUrl (https) ; false seulement sur http en boucle locale, voir 02-configuration
	cookie.MaxAge = -1
	http.SetCookie(writer, cookie)
}

// cookieValue lit un cookie par son nom exact ; un cookie vide compte comme absent.
func cookieValue(request *http.Request, name string) (string, bool) {
	cookie, err := request.Cookie(name)
	if err != nil || cookie.Value == "" {
		return "", false
	}
	return cookie.Value, true
}

// wholeSecondsUntil arrondit vers le haut, au moins une seconde (Max-Age=0 effacerait le cookie).
func wholeSecondsUntil(deadline, now time.Time) int {
	return max(1, int(math.Ceil(deadline.Sub(now).Seconds())))
}

func (handler *Handler) clock() time.Time {
	if handler.now == nil {
		return time.Now()
	}
	return handler.now()
}
