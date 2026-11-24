package config

import (
	"net/url"
	"regexp"
	"strings"
)

var (
	urlInText    = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s"'<>]+`)
	secretAssign = regexp.MustCompile(`(?i)((?:api[_-]?key|token|password|passwd|secret)=)[^&\s"']+`)
)

// RedactText masque dans un texte libre les secrets connus, les identifiants d'URL, les valeurs de requête sensibles,
// le segment « /proxy/<clé> » et les affectations « apikey=… » ; sert aux rapports destinés à l'affichage.
func RedactText(text string, knownSecrets ...string) string {
	for _, secret := range knownSecrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, redactedMark)
		}
	}
	text = urlInText.ReplaceAllStringFunc(text, func(rawURL string) string {
		return redactRCURL(redactURL(redactProxyKey(rawURL)))
	})
	return secretAssign.ReplaceAllString(text, "${1}"+redactedMark)
}

// KnownSecrets liste les valeurs secrètes de la configuration (jetons, clés, mots de passe, clé de proxy qui suit
// « /proxy/ », identifiants rc) pour que RedactText les masque même hors d'une URL.
func (loadedConfig Config) KnownSecrets() []string {
	secrets := []string{loadedConfig.AuthToken, loadedConfig.LibraryProviderToken}
	for _, service := range append(append([]ArrService{}, loadedConfig.Radarr...), loadedConfig.Sonarr...) {
		secrets = append(secrets, service.APIKey, service.Password, proxyKeyOf(service.BaseURL))
	}
	for _, client := range loadedConfig.TorrentClients {
		secrets = append(secrets, client.Password, proxyKeyOf(client.BaseURL))
	}
	if loadedConfig.CrossSeed != nil {
		secrets = append(secrets, loadedConfig.CrossSeed.APIKey, proxyKeyOf(loadedConfig.CrossSeed.BaseURL))
	}
	for _, provider := range loadedConfig.FSProviders {
		secrets = append(secrets, rcCredentials(provider.RC)...)
	}
	return secrets
}

func proxyKeyOf(baseURL string) string {
	_, afterMarker, found := strings.Cut(baseURL, proxyPathMarker)
	if !found {
		return ""
	}
	segment, _, _ := strings.Cut(afterMarker, "/")
	return segment
}

// rcCredentials ne renvoie que le mot de passe : un nom d'utilisateur court masquerait des mots ordinaires.
func rcCredentials(rawURL string) []string {
	parsedURL, err := url.Parse(rawURL)
	if err != nil || parsedURL.User == nil {
		return nil
	}
	password, _ := parsedURL.User.Password()
	return []string{password}
}

// IsLoopbackAddress dit si une adresse d'écoute host:port désigne la boucle locale.
func IsLoopbackAddress(listenAddr string) bool { return isLoopbackAddress(listenAddr) }
