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
	proxyKeyPlaceholder        = "{proxyKey}"
	escapedProxyKeyPlaceholder = "%7BproxyKey%7D"
	reasonMissingProxyKey      = "missing proxyKey for the {proxyKey} placeholder of url"
)

// splitProxyKey remplace le segment qui suit le premier /proxy/ par {proxyKey} et rend la clé décodée.
func splitProxyKey(baseURL string) (string, string) {
	beforeMarker, afterMarker, found := strings.Cut(baseURL, proxyPathMarker)
	if !found {
		return baseURL, ""
	}
	escapedKey, remainingPath, hasRemainingPath := strings.Cut(afterMarker, "/")
	proxyKey, err := url.PathUnescape(escapedKey)
	if err != nil {
		proxyKey = escapedKey
	}
	placeholderURL := beforeMarker + proxyPathMarker + proxyKeyPlaceholder
	if hasRemainingPath {
		placeholderURL += "/" + remainingPath
	}
	return placeholderURL, proxyKey
}

func addProxyKey(element Element, proxyKey string) {
	if proxyKey != "" {
		element[fieldProxyKey] = proxyKey
	}
}

// joinProxyKey remet dans l'url la clé de proxy à la place de {proxyKey}, échappée pour un chemin.
func joinProxyKey(list string, element Element, rawURL string) (string, error) {
	hasPlaceholder := strings.Contains(rawURL, proxyKeyPlaceholder) || strings.Contains(rawURL, escapedProxyKeyPlaceholder)
	if !hasPlaceholder {
		return rawURL, nil
	}
	proxyKey, _ := element[fieldProxyKey].(string)
	if proxyKey == "" {
		return "", invalidElement(list, reasonMissingProxyKey)
	}
	escapedKey := url.PathEscape(proxyKey)
	joined := strings.ReplaceAll(rawURL, proxyKeyPlaceholder, escapedKey)
	return strings.ReplaceAll(joined, escapedProxyKeyPlaceholder, escapedKey), nil
}

// RejectSecretInURL refuse l'url d'un élément qui porte un secret en clair ; le message ne cite jamais l'url.
func RejectSecretInURL(list string, element map[string]any) error {
	if !slices.Contains(endpointLists, list) {
		return nil
	}
	rawURL, _ := element[fieldURL].(string)
	if !urlCarriesSecret(rawURL) {
		return nil
	}
	return usageError{message: fmt.Sprintf(
		"%s.url must not carry a secret: put it in apiKey, user and password, or proxyKey with the %s placeholder",
		list, proxyKeyPlaceholder)}
}

func urlCarriesSecret(rawURL string) bool {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return parsedURL.User != nil || hasAPIKeyParameter(parsedURL) || hasClearProxyKey(parsedURL.Path)
}

func hasAPIKeyParameter(parsedURL *url.URL) bool {
	for parameterName := range parsedURL.Query() {
		if strings.EqualFold(parameterName, apiKeyQueryParam) {
			return true
		}
	}
	return false
}

func hasClearProxyKey(path string) bool {
	_, afterMarker, found := strings.Cut(path, proxyPathMarker)
	if !found {
		return false
	}
	segment, _, _ := strings.Cut(afterMarker, "/")
	return segment != proxyKeyPlaceholder
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
