package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

const (
	listRadarr         = "radarr"
	listSonarr         = "sonarr"
	listTorrentClients = "torrentClients"
	listLinkDirs       = "linkDirs"
	listTrackers       = "trackers"
	listTorznab        = "torznab"
	listFSProviders    = "fsProviders"

	fieldURL      = "url"
	fieldAPIKey   = "apiKey"
	fieldUser     = "user"
	fieldPassword = "password"
	fieldType     = "type"
	fieldReadOnly = "readOnly"
	fieldPath     = "path"
	fieldKey      = "key"
	fieldAnnounce = "announce"

	reasonUnreadableRaw = "unreadable value (expected a valid url form)"
)

// ErrInvalidElement signale un élément illisible ; son message ne reprend jamais la valeur brute (elle porte la clé).
var ErrInvalidElement = errors.New("invalid list element")

func invalidElement(list, reason string) error {
	return fmt.Errorf("%w: %s: %s", ErrInvalidElement, list, reason)
}

// ElementFromRaw décompose la chaîne brute d'une liste en élément ; seules radarr, sonarr, torrentClients, linkDirs et
// torznab ont une forme chaîne.
func ElementFromRaw(list, raw string) (Element, error) {
	switch list {
	case listRadarr, listSonarr:
		return arrElementFromRaw(list, raw)
	case listTorrentClients:
		return clientElementFromRaw(raw)
	case listLinkDirs:
		return Element{fieldPath: raw}, nil
	case listTorznab:
		return torznabElementFromRaw(raw)
	}
	return nil, invalidElement(list, "no raw string form")
}

func arrElementFromRaw(list, raw string) (Element, error) {
	service, err := ParseArrService(list, raw)
	if err != nil {
		return nil, invalidElement(list, reasonUnreadableRaw)
	}
	plainURL, proxyKey := splitProxyKey(service.BaseURL)
	element := Element{fieldURL: plainURL, fieldAPIKey: service.APIKey}
	addCredentials(element, service.User, service.Password)
	addProxyKey(element, proxyKey)
	return element, nil
}

func clientElementFromRaw(raw string) (Element, error) {
	client, err := ParseTorrentClient(raw)
	if err != nil {
		return nil, invalidElement(listTorrentClients, reasonUnreadableRaw)
	}
	plainURL, proxyKey := splitProxyKey(client.BaseURL)
	element := Element{fieldType: client.Type, fieldReadOnly: client.ReadOnly, fieldURL: plainURL}
	addCredentials(element, client.User, client.Password)
	addProxyKey(element, proxyKey)
	return element, nil
}

func addCredentials(element Element, user, password string) {
	if user != "" {
		element[fieldUser] = user
	}
	if password != "" {
		element[fieldPassword] = password
	}
}

// RawFromElement reconstruit la forme que lit l'analyseur de la liste : une chaîne, ou du JSON pour trackers
// (objet à une clé, assemblable par TrackersSection), torznab et fsProviders.
func RawFromElement(list string, element Element) (any, error) {
	switch list {
	case listRadarr, listSonarr:
		return arrRawFromElement(list, element)
	case listTorrentClients:
		return clientRawFromElement(element)
	case listLinkDirs:
		return requiredString(list, element, fieldPath)
	case listTrackers:
		return trackerRawFromElement(element)
	case listTorznab:
		return torznabRawFromElement(element)
	case listFSProviders:
		return marshalElement(list, element)
	}
	return nil, invalidElement(list, "unknown list")
}

func requiredString(list string, element Element, field string) (string, error) {
	value, _ := element[field].(string)
	if value == "" {
		return "", invalidElement(list, "missing "+field)
	}
	return value, nil
}

func urlWithCredentials(list string, element Element) (*url.URL, error) {
	rawURL, err := requiredString(list, element, fieldURL)
	if err != nil {
		return nil, err
	}
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return nil, invalidElement(list, "unparsable url")
	}
	if err := joinProxyKey(list, element, parsedURL); err != nil {
		return nil, err
	}
	user, _ := element[fieldUser].(string)
	password, _ := element[fieldPassword].(string)
	switch {
	case password != "":
		parsedURL.User = url.UserPassword(user, password)
	case user != "":
		parsedURL.User = url.User(user)
	}
	return parsedURL, nil
}

func arrRawFromElement(list string, element Element) (string, error) {
	apiKey, err := requiredString(list, element, fieldAPIKey)
	if err != nil {
		return "", err
	}
	parsedURL, err := urlWithCredentials(list, element)
	if err != nil {
		return "", err
	}
	parsedURL.RawQuery = url.Values{apiKeyQueryParam: {apiKey}}.Encode()
	return parsedURL.String(), nil
}

func clientRawFromElement(element Element) (string, error) {
	clientType, err := requiredString(listTorrentClients, element, fieldType)
	if err != nil {
		return "", err
	}
	parsedURL, err := urlWithCredentials(listTorrentClients, element)
	if err != nil {
		return "", err
	}
	modifier := ""
	if readOnly, _ := element[fieldReadOnly].(bool); readOnly {
		modifier = readOnlyModifier
	}
	return clientType + ":" + modifier + parsedURL.String(), nil
}

func trackerRawFromElement(element Element) (json.RawMessage, error) {
	key, err := requiredString(listTrackers, element, fieldKey)
	if err != nil {
		return nil, err
	}
	entry := Element{}
	for field, value := range element {
		if field != fieldKey {
			entry[field] = value
		}
	}
	// L'analyseur lit announce comme une liste : une annonce seule devient une liste d'un élément.
	if singleAnnounce, isString := entry[fieldAnnounce].(string); isString {
		entry[fieldAnnounce] = []string{singleAnnounce}
	}
	return marshalElement(listTrackers, map[string]Element{key: entry})
}

func marshalElement(list string, value any) (json.RawMessage, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, invalidElement(list, "not encodable as JSON")
	}
	return encoded, nil
}

// TrackersSection assemble les objets à une clé de RawFromElement("trackers", …) en la section que lit parseTrackers.
func TrackersSection(rawTrackers []json.RawMessage) (json.RawMessage, error) {
	section := map[string]json.RawMessage{}
	for _, rawTracker := range rawTrackers {
		var singleEntry map[string]json.RawMessage
		if err := json.Unmarshal(rawTracker, &singleEntry); err != nil {
			return nil, invalidElement(listTrackers, "not a JSON object")
		}
		for key, entry := range singleEntry {
			if _, exists := section[key]; exists {
				return nil, invalidElement(listTrackers, "duplicate key "+strings.TrimSpace(key))
			}
			section[key] = entry
		}
	}
	return marshalElement(listTrackers, section)
}
