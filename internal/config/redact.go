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
	// escapedSchemeSeparator est « :// » échappé pour JSON.
	escapedSchemeSeparator = `:\/\/`
)

var (
	// Expressions RE2 : temps linéaire en la taille du texte, aucun retour arrière possible.
	// urlInText reconnaît aussi une URL échappée pour JSON (« https:\/\/ »), telle qu'un corps PHP json_encode la rend.
	urlInText    = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*:(?://|\\/\\/)[^\s"'<>]+`)
	secretAssign = regexp.MustCompile(`(?i)((?:api[_-]?key|token|password|passwd|secret|passkey|authkey|torrent_pass)=)[^&\s"']+`)
	// secretParameterNames nomme les paramètres de passkey des trackers, masqués quelle que soit la longueur.
	secretParameterNames = []string{"passkey", "pk", "authkey", "torrent_pass", "key", "uid", "apikey", "secret", "token"}
)

// urlOnlySecretPrefix marque, dans une liste de secrets connus, une valeur trop courte pour le remplacement littéral
// du texte entier : elle n'est comparée, à l'identique, qu'aux segments et valeurs des URL. Un octet NUL ne peut pas
// figurer dans une variable d'environnement, donc jamais dans un vrai secret.
const urlOnlySecretPrefix = "\x00url-only:"

// urlOnlySecret marque value comme secret à ne masquer que dans les URL.
func urlOnlySecret(value string) string { return urlOnlySecretPrefix + value }

// splitSecrets sépare les secrets à remplacer partout de ceux qui ne valent que dans une URL.
func splitSecrets(knownSecrets []string) (literal, urlOnly []string) {
	for _, secret := range knownSecrets {
		if value, isURLOnly := strings.CutPrefix(secret, urlOnlySecretPrefix); isURLOnly {
			if value != "" {
				urlOnly = append(urlOnly, value)
			}
			continue
		}
		literal = append(literal, secret)
	}
	return literal, urlOnly
}

// RedactText masque dans un texte libre les secrets connus, puis dans chaque URL les identifiants, les valeurs des
// paramètres à nom sensible, le segment « /proxy/<clé> », tout segment ou valeur qui contient un mot de 16 caractères
// base64url ou plus (après décodage, même répété) ou qui égale un secret réservé aux URL, un mot d'au moins 16
// caractères avec un chiffre dans l'hôte, ainsi que les affectations « apikey=… ». Une URL échappée pour JSON
// (« https:\/\/… ») est masquée en entier. Hors d'une URL, seule la liste des secrets connus protège une passkey nue.
// Sert aux rapports destinés à l'affichage.
func RedactText(text string, knownSecrets ...string) string {
	literal, urlOnly := splitSecrets(knownSecrets)
	// Les plus longs d'abord : un secret court inclus dans un long ne doit pas en laisser un reste.
	slices.SortStableFunc(literal, func(first, second string) int { return len(second) - len(first) })
	for _, secret := range literal {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, redactedMark)
		}
	}
	// Les affectations d'abord : masquées après, elles changeraient les bornes d'une URL déjà traitée (un « < » ou un
	// « # » avalé) et une seconde passe ne rendrait plus le même texte.
	text = secretAssign.ReplaceAllString(text, "${1}"+redactedMark)
	masker := urlMasker{urlOnlySecrets: urlOnly}
	return urlInText.ReplaceAllStringFunc(text, masker.redactURLInText)
}

// urlMasker masque la structure d'une URL ; urlOnlySecrets sont les valeurs courtes comparées à l'identique.
type urlMasker struct{ urlOnlySecrets []string }

// redactURLInText masque une URL trouvée dans un texte ; illisible ou échappée pour JSON, elle est masquée en entier.
func (masker urlMasker) redactURLInText(rawURL string) string {
	if strings.Contains(rawURL, escapedSchemeSeparator) {
		return redactedMark
	}
	if _, err := url.Parse(rawURL); err != nil {
		return redactedMark
	}
	return masker.maskURLStructure(redactProxyKey(rawURL))
}

// maskURLStructure travaille sur le texte brut de l'URL (jamais réencodé) : identifiants, hôte, segments de chemin,
// paramètres de requête et de fragment, séparés par « & » ou « ; ».
func (masker urlMasker) maskURLStructure(rawURL string) string {
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
	masked.WriteString(maskHostWords(authority))
	masked.WriteString(masker.maskPath(path))
	if hasQuery {
		masked.WriteString("?" + masker.maskParameters(query))
	}
	if hasFragment {
		masked.WriteString("#" + masker.maskParameters(fragment))
	}
	return masked.String()
}

// maskHostWords remplace, dans un hôte, chaque mot base64url d'au moins 16 caractères qui contient un chiffre : un
// jeton en sous-domaine. L'exigence d'un chiffre épargne les noms d'hôte longs ordinaires (« seedbox-provider-eu »).
func maskHostWords(host string) string {
	var masked strings.Builder
	maskedAWord := false
	for start := 0; start < len(host); {
		end := start
		for end < len(host) && isBase64URLByte(host[end]) {
			end++
		}
		if end == start {
			masked.WriteByte(host[start])
			start++
			continue
		}
		word := host[start:end]
		if len(word) >= minSecretSegmentLength && strings.ContainsAny(word, "0123456789") {
			word, maskedAWord = redactedMark, true
		}
		masked.WriteString(word)
		start = end
	}
	// Un mot coupé au milieu d'un « %XX » laisserait un échappement invalide : l'hôte entier est alors masqué.
	if maskedAWord && strings.Contains(host, "%") {
		return redactedMark
	}
	return masked.String()
}

// equalsURLOnlySecret compare une valeur décodée aux secrets réservés aux URL, casse ignorée (hexadécimal en capitales).
func (masker urlMasker) equalsURLOnlySecret(decoded string) bool {
	return slices.ContainsFunc(masker.urlOnlySecrets, func(secret string) bool { return strings.EqualFold(secret, decoded) })
}

// maskPath masque un segment qui révèle un secret ou égale un secret réservé aux URL, et le segment qui suit un nom de
// passkey (« /passkey/<valeur> »).
func (masker urlMasker) maskPath(path string) string {
	segments := strings.Split(path, "/")
	followsSecretName := false
	for segmentIndex, segment := range segments {
		decoded := decodeRepeatedly(segment)
		if (followsSecretName && segment != "") || revealsSecret(decoded) || masker.equalsURLOnlySecret(decoded) {
			segments[segmentIndex] = redactedMark
		}
		followsSecretName = slices.Contains(secretParameterNames, strings.ToLower(decoded))
	}
	return strings.Join(segments, "/")
}

// maskParameters masque chaque paramètre « nom=valeur » séparé par « & » ou « ; », séparateurs gardés.
func (masker urlMasker) maskParameters(rawParameters string) string {
	var masked strings.Builder
	for rawParameters != "" {
		separatorIndex := strings.IndexAny(rawParameters, "&;")
		if separatorIndex < 0 {
			masked.WriteString(masker.maskParameter(rawParameters))
			break
		}
		masked.WriteString(masker.maskParameter(rawParameters[:separatorIndex]))
		masked.WriteByte(rawParameters[separatorIndex])
		rawParameters = rawParameters[separatorIndex+1:]
	}
	return masked.String()
}

func (masker urlMasker) maskParameter(parameter string) string {
	name, value, hasValue := strings.Cut(parameter, "=")
	decodedName := decodeRepeatedly(name)
	if revealsSecret(decodedName) || masker.equalsURLOnlySecret(decodedName) {
		return redactedMark
	}
	if !hasValue || value == "" {
		return parameter
	}
	decodedValue := decodeRepeatedly(value)
	if isSecretParameterName(decodedName) || revealsSecret(decodedValue) || masker.equalsURLOnlySecret(decodedValue) {
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

func isBase64URLByte(character byte) bool { return isBase64URLRune(rune(character)) }

// isBase64URLRune reconnaît une lettre ASCII, un chiffre, « - » ou « _ » ; tout autre caractère, non ASCII compris, est
// un séparateur.
func isBase64URLRune(character rune) bool {
	isLetter := (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z')
	return isLetter || (character >= '0' && character <= '9') || character == '-' || character == '_'
}

// base64URLWords découpe un texte en suites maximales de caractères base64url.
func base64URLWords(text string) []string {
	return strings.FieldsFunc(text, func(character rune) bool { return !isBase64URLRune(character) })
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
// Les formes échappées des secrets (celles qui diffèrent du secret brut) sont aussi remplacées littéralement dans le
// texte encodé, en défense en profondeur ; jamais la forme brute, qui toucherait les nombres et la structure.
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
// redaction (URL, « clé=valeur ») mangeraient une barre oblique d'échappement et casseraient le JSON. Seules les formes
// échappées sont cherchées (elles contiennent une barre oblique inverse, donc n'apparaissent que dans une chaîne) ; si
// un remplacement rendait malgré tout le JSON invalide, le texte déjà masqué par redactTree est gardé.
func replaceSecretsLiterally(encoded string, knownSecrets []string) string {
	replaced := encoded
	forms := escapedSecrets(knownSecrets)
	slices.SortStableFunc(forms, func(first, second string) int { return len(second) - len(first) })
	for _, form := range forms {
		if form != "" {
			replaced = strings.ReplaceAll(replaced, form, redactedMark)
		}
	}
	if !json.Valid([]byte(replaced)) {
		return encoded
	}
	return replaced
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

// escapedSecrets donne les formes échappées par JSON (avec et sans échappement HTML) qui diffèrent du secret brut.
func escapedSecrets(knownSecrets []string) []string {
	var forms []string
	literal, _ := splitSecrets(knownSecrets)
	for _, secret := range literal {
		if secret == "" {
			continue
		}
		var plain bytes.Buffer
		encoder := json.NewEncoder(&plain)
		encoder.SetEscapeHTML(false)
		_ = encoder.Encode(secret)
		htmlEscaped, _ := json.Marshal(secret)
		for _, quoted := range []string{strings.TrimSpace(plain.String()), string(htmlEscaped)} {
			if form := strings.TrimSuffix(strings.TrimPrefix(quoted, `"`), `"`); form != secret {
				forms = append(forms, form)
			}
		}
	}
	return forms
}
