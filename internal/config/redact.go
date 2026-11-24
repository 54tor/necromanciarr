package config

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

const (
	// minSecretSegmentLength : une passkey fait 32 caractères hexadécimaux ou plus ; 16 laisse de la marge sans
	// toucher aux segments courts d'une URL ordinaire.
	minSecretSegmentLength = 16
	// maxDecodeRounds borne le décodage d'une valeur encodée plusieurs fois (« %252F ») : temps linéaire garanti.
	maxDecodeRounds = 4
)

var (
	// Expressions RE2 : temps linéaire en la taille du texte, aucun retour arrière possible.
	urlInText    = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s"'<>]+`)
	secretAssign = regexp.MustCompile(`(?i)((?:api[_-]?key|token|password|passwd|secret|passkey|authkey|torrent_pass)=)[^&\s"']+`)
	// secretParameterNames nomme les paramètres de passkey des trackers, masqués quelle que soit la longueur.
	secretParameterNames = []string{"passkey", "pk", "authkey", "torrent_pass", "key", "uid", "apikey", "secret", "token"}
)

// RedactText masque dans un texte libre les secrets connus, puis dans chaque URL les identifiants, les valeurs des
// paramètres à nom sensible, le segment « /proxy/<clé> », tout segment ou valeur qui contient un mot de 16 caractères
// base64url ou plus (après décodage, même répété), ainsi que les affectations « apikey=… ». Hors d'une URL, seule la
// liste des secrets connus protège une passkey nue. Sert aux rapports destinés à l'affichage.
func RedactText(text string, knownSecrets ...string) string {
	// Les plus longs d'abord : un secret court inclus dans un long ne doit pas en laisser un reste.
	ordered := slices.Clone(knownSecrets)
	slices.SortStableFunc(ordered, func(first, second string) int { return len(second) - len(first) })
	for _, secret := range ordered {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, redactedMark)
		}
	}
	// Les affectations d'abord : masquées après, elles changeraient les bornes d'une URL déjà traitée (un « < » ou un
	// « # » avalé) et une seconde passe ne rendrait plus le même texte.
	text = secretAssign.ReplaceAllString(text, "${1}"+redactedMark)
	return urlInText.ReplaceAllStringFunc(text, redactURLInText)
}

// redactURLInText masque une URL trouvée dans un texte ; illisible, elle est masquée en entier.
func redactURLInText(rawURL string) string {
	if _, err := url.Parse(rawURL); err != nil {
		return redactedMark
	}
	return maskURLStructure(redactProxyKey(rawURL))
}

// maskURLStructure travaille sur le texte brut de l'URL (jamais réencodé) : identifiants, segments de chemin,
// paramètres de requête et de fragment, séparés par « & » ou « ; ».
func maskURLStructure(rawURL string) string {
	schemeEnd := strings.Index(rawURL, "://") + len("://")
	beforeFragment, fragment, hasFragment := strings.Cut(rawURL[schemeEnd:], "#")
	beforeQuery, query, hasQuery := strings.Cut(beforeFragment, "?")
	authority, path := beforeQuery, ""
	if slashIndex := strings.IndexByte(beforeQuery, '/'); slashIndex >= 0 {
		authority, path = beforeQuery[:slashIndex], beforeQuery[slashIndex:]
	}
	var masked strings.Builder
	masked.WriteString(rawURL[:schemeEnd])
	if atIndex := strings.LastIndexByte(authority, '@'); atIndex >= 0 {
		authority = redactedMark + authority[atIndex:]
	}
	masked.WriteString(authority)
	masked.WriteString(maskPath(path))
	if hasQuery {
		masked.WriteString("?" + maskParameters(query))
	}
	if hasFragment {
		masked.WriteString("#" + maskParameters(fragment))
	}
	return masked.String()
}

// maskPath masque un segment qui révèle un secret, et le segment qui suit un nom de passkey (« /passkey/<valeur> »).
func maskPath(path string) string {
	segments := strings.Split(path, "/")
	followsSecretName := false
	for segmentIndex, segment := range segments {
		decoded := decodeRepeatedly(segment)
		if (followsSecretName && segment != "") || revealsSecret(decoded) {
			segments[segmentIndex] = redactedMark
		}
		followsSecretName = slices.Contains(secretParameterNames, strings.ToLower(decoded))
	}
	return strings.Join(segments, "/")
}

// maskParameters masque chaque paramètre « nom=valeur » séparé par « & » ou « ; », séparateurs gardés.
func maskParameters(rawParameters string) string {
	var masked strings.Builder
	for rawParameters != "" {
		separatorIndex := strings.IndexAny(rawParameters, "&;")
		if separatorIndex < 0 {
			masked.WriteString(maskParameter(rawParameters))
			break
		}
		masked.WriteString(maskParameter(rawParameters[:separatorIndex]))
		masked.WriteByte(rawParameters[separatorIndex])
		rawParameters = rawParameters[separatorIndex+1:]
	}
	return masked.String()
}

func maskParameter(parameter string) string {
	name, value, hasValue := strings.Cut(parameter, "=")
	decodedName := decodeRepeatedly(name)
	if revealsSecret(decodedName) {
		return redactedMark
	}
	if !hasValue || value == "" {
		return parameter
	}
	if isSecretParameterName(decodedName) || revealsSecret(decodeRepeatedly(value)) {
		return name + "=" + redactedMark
	}
	return parameter
}

// isSecretParameterName reconnaît un nom de passkey ou un nom typique d'un secret (clé, jeton, mot de passe).
func isSecretParameterName(name string) bool {
	lowered := strings.ToLower(name)
	return slices.Contains(secretParameterNames, lowered) || looksLikeSecretName(lowered)
}

// revealsSecret dit si un texte décodé contient un mot base64url d'au moins 16 caractères, ou un nom de passkey
// suivi de « = » ou « / » et d'une valeur (séparateurs encodés déjà décodés).
func revealsSecret(decoded string) bool {
	previousWord, previousEnd := "", -1
	for start := 0; start < len(decoded); {
		if !isBase64URLByte(decoded[start]) {
			start++
			continue
		}
		end := start
		for end < len(decoded) && isBase64URLByte(decoded[end]) {
			end++
		}
		word := decoded[start:end]
		if len(word) >= minSecretSegmentLength {
			return true
		}
		isAdjacent := previousEnd >= 0 && start == previousEnd+1
		if isAdjacent && namesSecretBefore(previousWord, decoded[previousEnd]) {
			return true
		}
		previousWord, previousEnd, start = word, end, end
	}
	return false
}

func namesSecretBefore(name string, separator byte) bool {
	switch separator {
	case '=':
		return isSecretParameterName(name)
	case '/':
		return slices.Contains(secretParameterNames, strings.ToLower(name))
	default:
		return false
	}
}

func isBase64URLByte(character byte) bool {
	isLetter := (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z')
	return isLetter || (character >= '0' && character <= '9') || character == '-' || character == '_'
}

// base64URLWords découpe un texte en suites maximales de caractères base64url.
func base64URLWords(text string) []string {
	return strings.FieldsFunc(text, func(character rune) bool { return character > 0x7f || !isBase64URLByte(byte(character)) })
}

// decodeRepeatedly décode les séquences « %XX » jusqu'à maxDecodeRounds fois ; une séquence invalide reste telle
// quelle au lieu d'arrêter le décodage (url.PathUnescape échouerait en entier et laisserait la passkey encodée).
func decodeRepeatedly(text string) string {
	for range maxDecodeRounds {
		decoded := unescapeLeniently(text)
		if decoded == text {
			break
		}
		text = decoded
	}
	return text
}

func unescapeLeniently(text string) string {
	if !strings.Contains(text, "%") {
		return text
	}
	var decoded strings.Builder
	for index := 0; index < len(text); index++ {
		if text[index] == '%' && index+2 < len(text) {
			if decodedByte, err := hex.DecodeString(text[index+1 : index+3]); err == nil {
				decoded.Write(decodedByte)
				index += 2
				continue
			}
		}
		decoded.WriteByte(text[index])
	}
	return decoded.String()
}

// KnownSecrets liste les valeurs secrètes de la configuration (jetons, clés, mots de passe, clé de proxy qui suit
// « /proxy/ », identifiants rc, clé de scellement sous ses écritures probables, valeurs interpolées dans trackers)
// pour que RedactText les masque même hors d'une URL.
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
	for _, tracker := range loadedConfig.Trackers {
		secrets = append(secrets, tracker.secrets...)
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
