package config

import (
	"bytes"
	"encoding/json"
	"net/url"
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
// « /proxy/ », identifiants rc, clé de scellement sous ses écritures probables) pour que RedactText les masque même
// hors d'une URL.
func (loadedConfig Config) KnownSecrets() []string {
	secrets := append([]string{loadedConfig.AuthToken, loadedConfig.LibraryProviderToken}, urlSecrets(loadedConfig.LibraryAPI)...)
	secrets = append(secrets, secretKeyForms(loadedConfig.SecretKey)...)
	for _, service := range append(append([]ArrService{}, loadedConfig.Radarr...), loadedConfig.Sonarr...) {
		secrets = append(secrets, service.APIKey, service.Password, proxyKeyOf(service.BaseURL))
	}
	for _, client := range loadedConfig.TorrentClients {
		secrets = append(secrets, client.Password, proxyKeyOf(client.BaseURL))
	}
	if loadedConfig.CrossSeed != nil {
		secrets = append(secrets, loadedConfig.CrossSeed.APIKey, proxyKeyOf(loadedConfig.CrossSeed.BaseURL))
	}
	for _, indexer := range loadedConfig.Torznab {
		secrets = append(secrets, torznabSecrets(indexer.URL)...)
	}
	secrets = append(secrets, torznabSecrets(loadedConfig.Prowlarr)...)
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

// RedactJSON encode value en JSON indenté, chaque chaîne (valeurs et clés) masquée par RedactText avant l'encodage :
// un secret qui contient « & », « < », « " », « \ » ou un caractère non ASCII n'a plus la même forme une fois échappé.
// Les formes échappées des secrets sont aussi remplacées littéralement dans le texte encodé, en défense en profondeur.
// Les clés des objets sortent triées. Sert à la CLI (--json) et à l'API web.
func RedactJSON(value any, knownSecrets ...string) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var tree any
	if err := decoder.Decode(&tree); err != nil {
		return nil, err
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(redactTree(tree, knownSecrets)); err != nil {
		return nil, err
	}
	return []byte(replaceSecretsLiterally(encoded.String(), knownSecrets)), nil
}

// replaceSecretsLiterally ne fait que des remplacements littéraux, les plus longs d'abord : les expressions de la
// redaction (URL, « clé=valeur ») mangeraient une barre oblique d'échappement et casseraient le JSON.
func replaceSecretsLiterally(encoded string, knownSecrets []string) string {
	forms := escapedSecrets(knownSecrets)
	slices.SortStableFunc(forms, func(first, second string) int { return len(second) - len(first) })
	for _, form := range forms {
		if form != "" {
			encoded = strings.ReplaceAll(encoded, form, redactedMark)
		}
	}
	return encoded
}

// redactTree masque chaque chaîne (valeurs et clés) d'un arbre JSON décodé.
func redactTree(node any, knownSecrets []string) any {
	switch typed := node.(type) {
	case string:
		return RedactText(typed, knownSecrets...)
	case []any:
		for index, element := range typed {
			typed[index] = redactTree(element, knownSecrets)
		}
		return typed
	case map[string]any:
		redacted := make(map[string]any, len(typed))
		for key, element := range typed {
			redacted[RedactText(key, knownSecrets...)] = redactTree(element, knownSecrets)
		}
		return redacted
	}
	return node
}

// escapedSecrets donne chaque secret tel quel et sous ses formes échappées par JSON (avec et sans échappement HTML).
func escapedSecrets(knownSecrets []string) []string {
	forms := slices.Clone(knownSecrets)
	for _, secret := range knownSecrets {
		if secret == "" {
			continue
		}
		var plain bytes.Buffer
		encoder := json.NewEncoder(&plain)
		encoder.SetEscapeHTML(false)
		_ = encoder.Encode(secret)
		htmlEscaped, _ := json.Marshal(secret)
		for _, quoted := range []string{strings.TrimSpace(plain.String()), string(htmlEscaped)} {
			forms = append(forms, strings.TrimSuffix(strings.TrimPrefix(quoted, `"`), `"`))
		}
	}
	return forms
}
