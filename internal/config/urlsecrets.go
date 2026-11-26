package config

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
)

const (
	fieldProxyKey = "proxyKey"
	// proxyKeyPlaceholder marque, dans une url en clair, le segment de chemin qui porte la clé de proxy.
	proxyKeyPlaceholder   = "{proxyKey}"
	reasonMissingProxyKey = "missing proxyKey for the {proxyKey} placeholder of url"
)

// proxyLayout décrit les segments proxy d'un chemin : au plus un, suivi du seul segment {proxyKey} admis.
type proxyLayout struct {
	isValid, hasPlaceholder bool
}

const proxySegmentName = "proxy"

// proxyLayoutOf vérifie que chaque segment proxy (casse ignorée) est suivi de {proxyKey}, que {proxyKey} ne suit
// qu'un segment proxy, et qu'il n'y en a qu'un.
func proxyLayoutOf(escapedPath string) proxyLayout {
	segments := decodedSegments(escapedPath)
	proxyCount, placeholderCount := 0, 0
	for position, segment := range segments {
		if strings.EqualFold(segment, proxySegmentName) {
			proxyCount++
			isFollowed := position+1 < len(segments) && segments[position+1] == proxyKeyPlaceholder
			if !isFollowed {
				return proxyLayout{}
			}
		}
		if segment == proxyKeyPlaceholder {
			placeholderCount++
			isAfterProxy := position > 0 && strings.EqualFold(segments[position-1], proxySegmentName)
			if !isAfterProxy {
				return proxyLayout{}
			}
		}
	}
	if proxyCount > 1 || placeholderCount > 1 {
		return proxyLayout{}
	}
	return proxyLayout{isValid: true, hasPlaceholder: placeholderCount == 1}
}

func decodedSegments(escapedPath string) []string {
	segments := strings.Split(escapedPath, "/")
	for position, segment := range segments {
		if decoded, err := url.PathUnescape(segment); err == nil {
			segments[position] = decoded
		}
	}
	return segments
}

// HasProxyPlaceholder dit si l'url porte le segment {proxyKey} à sa place, juste après /proxy/.
func HasProxyPlaceholder(rawURL string) bool {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	layout := proxyLayoutOf(parsedURL.EscapedPath())
	return layout.isValid && layout.hasPlaceholder
}

// splitProxyKey remplace le segment qui suit le premier segment proxy (casse ignorée) par {proxyKey} et rend la clé
// décodée ; un second segment proxy reste et sera refusé à la jonction.
func splitProxyKey(baseURL string) (string, string) {
	parsedURL, err := url.Parse(baseURL)
	if err != nil {
		return baseURL, ""
	}
	segments := strings.Split(parsedURL.EscapedPath(), "/")
	for position, segment := range segments {
		isProxyWithKey := strings.EqualFold(segment, proxySegmentName) && position+1 < len(segments)
		if !isProxyWithKey {
			continue
		}
		proxyKey, unescapeErr := url.PathUnescape(segments[position+1])
		if unescapeErr != nil {
			proxyKey = segments[position+1]
		}
		segments[position+1] = proxyKeyPlaceholder
		return parsedURL.Scheme + "://" + parsedURL.Host + strings.Join(segments, "/"), proxyKey
	}
	return baseURL, ""
}

func addProxyKey(element Element, proxyKey string) {
	if proxyKey != "" {
		element[fieldProxyKey] = proxyKey
	}
}

// joinProxyKey remet dans le chemin la clé de proxy à la place de {proxyKey}, échappée pour un chemin ; une clé sans
// segment {proxyKey}, ou un chemin mal formé, est refusé.
func joinProxyKey(list string, element Element, parsedURL *url.URL) error {
	if err := RejectSecretInURL(list, Element{fieldURL: parsedURL.String()}); err != nil {
		return err
	}
	proxyKey, _ := element[fieldProxyKey].(string)
	if !proxyLayoutOf(parsedURL.EscapedPath()).hasPlaceholder {
		return requireNoProxyKey(list, proxyKey)
	}
	if proxyKey == "" {
		return invalidElement(list, reasonMissingProxyKey)
	}
	segments := strings.Split(parsedURL.EscapedPath(), "/")
	for position, segment := range segments {
		if decoded, _ := url.PathUnescape(segment); decoded == proxyKeyPlaceholder {
			segments[position] = escapeProxyKey(proxyKey)
		}
	}
	return setEscapedPath(parsedURL, strings.Join(segments, "/"))
}

func requireNoProxyKey(list, proxyKey string) error {
	if proxyKey == "" {
		return nil
	}
	return usageError{message: fmt.Sprintf("%s.%s is set but url has no %s segment", list, fieldProxyKey, proxyKeyPlaceholder)}
}

// escapeProxyKey échappe la clé pour un segment ; « @ », laissé par PathEscape, trahirait un userinfo pour parseHTTPURL.
func escapeProxyKey(proxyKey string) string {
	return strings.ReplaceAll(url.PathEscape(proxyKey), "@", "%40")
}

func setEscapedPath(parsedURL *url.URL, escapedPath string) error {
	decodedPath, err := url.PathUnescape(escapedPath)
	if err != nil {
		return err
	}
	parsedURL.Path, parsedURL.RawPath = decodedPath, escapedPath
	return nil
}

// RejectSecretInURL refuse l'url d'un élément qui porte un secret en clair, une requête, ou un segment proxy hors de la
// forme /proxy/{proxyKey} ; une url illisible est refusée aussi. Le message ne cite jamais l'url.
func RejectSecretInURL(list string, element map[string]any) error {
	if !slices.Contains(endpointLists, list) {
		return nil
	}
	rawURL, _ := element[fieldURL].(string)
	if !urlCarriesSecret(rawURL) {
		return nil
	}
	return usageError{message: fmt.Sprintf(
		"%s.url must not carry a secret nor a query: put the keys in apiKey, user and password, or proxyKey with a %s "+
			"segment right after /proxy/", list, proxyKeyPlaceholder)}
}

func urlCarriesSecret(rawURL string) bool {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return true
	}
	hasQuery := parsedURL.RawQuery != "" || parsedURL.ForceQuery
	return parsedURL.User != nil || hasQuery || !proxyLayoutOf(parsedURL.EscapedPath()).isValid
}

// torznabRawFromElement rend l'objet que lit parseTorznabEntry : la clé d'API rejoint l'url en paramètre apikey.
func torznabRawFromElement(element Element) (any, error) {
	rawURL, err := requiredString(listTorznab, element, fieldURL)
	if err != nil {
		return nil, err
	}
	apiKey, err := requiredString(listTorznab, element, fieldAPIKey)
	if err != nil {
		return nil, err
	}
	entry := Element{}
	for field, value := range element {
		if field != fieldAPIKey {
			entry[field] = value
		}
	}
	separator := "?"
	if strings.Contains(rawURL, "?") {
		separator = "&"
	}
	entry[fieldURL] = rawURL + separator + url.Values{apiKeyQueryParam: {apiKey}}.Encode()
	return marshalElement(listTorznab, entry)
}

// torznabElementFromRaw sort le paramètre apikey de l'url d'un indexeur vers le champ apiKey.
func torznabElementFromRaw(raw string) (Element, error) {
	parsedURL, err := url.Parse(raw)
	if err != nil {
		return nil, invalidElement(listTorznab, reasonUnreadableRaw)
	}
	query := parsedURL.Query()
	apiKey := query.Get(apiKeyQueryParam)
	query.Del(apiKeyQueryParam)
	parsedURL.RawQuery = query.Encode()
	element := Element{fieldURL: parsedURL.String()}
	if apiKey != "" {
		element[fieldAPIKey] = apiKey
	}
	return element, nil
}
