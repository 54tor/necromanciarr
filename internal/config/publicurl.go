package config

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

const (
	settingPublicURL  = "publicUrl"
	localhostName     = "localhost"
	schemeHTTP        = "http"
	schemeHTTPS       = "https"
	defaultHTTPPort   = "80"
	defaultHTTPSPort  = "443"
	errPublicURLShape = settingPublicURL + ": expected http(s)://host[:port] without path, query, fragment or credentials"
)

// normalizePublicURL rend l'origine http(s)://hôte[:port] en minuscules, sans barre finale ni port par défaut.
func normalizePublicURL(rawURL string) (string, error) {
	if rawURL == "" {
		return "", nil
	}
	parsedURL, err := parseHTTPURL(rawURL)
	if err != nil {
		return "", errors.New(errPublicURLShape)
	}
	hasExtraParts := parsedURL.User != nil || parsedURL.RawQuery != "" || parsedURL.ForceQuery ||
		(parsedURL.Path != "" && parsedURL.Path != "/")
	if hasExtraParts || parsedURL.Hostname() == "" {
		return "", errors.New(errPublicURLShape)
	}
	return originOfURL(parsedURL), nil
}

func originOfURL(parsedURL *url.URL) string {
	scheme := strings.ToLower(parsedURL.Scheme)
	host := strings.ToLower(parsedURL.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	port := parsedURL.Port()
	isDefaultPort := (scheme == schemeHTTP && port == defaultHTTPPort) || (scheme == schemeHTTPS && port == defaultHTTPSPort)
	if port == "" || isDefaultPort {
		return scheme + "://" + host
	}
	return scheme + "://" + host + ":" + port
}

// WebAuthnOrigin rend l'origine et le RP ID de WebAuthn. Sans publicUrl, une écoute en boucle locale garde la
// première utilisation possible sur http://localhost:<port>. Une adresse IP ne peut pas servir de RP ID.
func (loadedConfig Config) WebAuthnOrigin() (origin string, rpID string, available bool) {
	if loadedConfig.PublicURL == "" {
		return loopbackListenOrigin(loadedConfig.ListenAddr)
	}
	parsedURL, err := url.Parse(loadedConfig.PublicURL)
	if err != nil {
		return "", "", false
	}
	host := parsedURL.Hostname()
	isIPAddress := net.ParseIP(host) != nil
	if isIPAddress || (parsedURL.Scheme != schemeHTTPS && host != localhostName) {
		return "", "", false
	}
	return loadedConfig.PublicURL, host, true
}

func loopbackListenOrigin(listenAddr string) (origin string, rpID string, available bool) {
	if !isLoopbackAddress(listenAddr) {
		return "", "", false
	}
	_, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return "", "", false
	}
	return schemeHTTP + "://" + net.JoinHostPort(localhostName, port), localhostName, true
}
