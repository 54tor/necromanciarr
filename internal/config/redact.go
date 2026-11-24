package config

import (
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

var (
	urlInText    = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s"'<>]+`)
	secretAssign = regexp.MustCompile(`(?i)((?:api[_-]?key|token|password|passwd|secret)=)[^&\s"']+`)
)

// RedactText masque dans un texte libre les secrets connus, les identifiants d'URL, les valeurs de requête sensibles,
// le segment « /proxy/<clé> » et les affectations « apikey=… » ; sert aux rapports destinés à l'affichage.
func RedactText(text string, knownSecrets ...string) string {
	// Les plus longs d'abord : un secret court inclus dans un long ne doit pas en laisser un reste.
	ordered := slices.Clone(knownSecrets)
	slices.SortStableFunc(ordered, func(first, second string) int { return len(second) - len(first) })
	for _, secret := range ordered {
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
	secrets := append([]string{loadedConfig.AuthToken, loadedConfig.LibraryProviderToken}, urlSecrets(loadedConfig.LibraryAPI)...)
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

// urlSecrets extrait d'une URL le mot de passe, la clé de proxy et les valeurs de requête à nom sensible.
func urlSecrets(rawURL string) []string {
	secrets := append(rcCredentials(rawURL), proxyKeyOf(rawURL))
	if parsedURL, err := url.Parse(rawURL); err == nil {
		for parameterName, values := range parsedURL.Query() {
			if looksLikeSecretName(parameterName) {
				secrets = append(secrets, values...)
			}
		}
	}
	return secrets
}

// ResolveDBPath donne le chemin effectif de la base : un chemin relatif se lit depuis le dossier de configuration.
// Doctor et le service doivent passer par cette fonction pour viser le même fichier.
func ResolveDBPath(settings Config) string {
	if filepath.IsAbs(settings.DBPath) {
		return settings.DBPath
	}
	return filepath.Join(settings.ConfigDir, settings.DBPath)
}
