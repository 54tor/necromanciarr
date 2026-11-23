package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
)

const (
	redactedMark        = "***"
	apiKeyQueryParam    = "apikey"
	typeQbittorrent     = "qbittorrent"
	readOnlyModifier    = "readonly:"
	errInvalidURLFormat = "%s: invalid url (expected http(s)://host[:port])"
)

// ArrService décrit une instance Radarr ou Sonarr ; BaseURL ne contient plus la clé d'API.
type ArrService struct {
	Kind    string
	BaseURL string
	APIKey  string
}

// TorrentClient décrit un client torrent ; BaseURL ne contient plus les identifiants.
type TorrentClient struct {
	Type     string
	ReadOnly bool
	BaseURL  string
	User     string
	Password string
}

// ParseArrService lit une chaîne `http://hote:7878?apikey=…` ; la clé est extraite de l'URL.
// Les erreurs ne reprennent jamais la chaîne brute, qui porte la clé.
func ParseArrService(kind, raw string) (ArrService, error) {
	parsedURL, err := parseHTTPURL(raw)
	if err != nil {
		return ArrService{}, fmt.Errorf(errInvalidURLFormat, kind)
	}
	query := parsedURL.Query()
	apiKey := query.Get(apiKeyQueryParam)
	if apiKey == "" {
		return ArrService{}, fmt.Errorf("%s: missing %s query parameter", kind, apiKeyQueryParam)
	}
	query.Del(apiKeyQueryParam)
	parsedURL.RawQuery = query.Encode()
	return ArrService{Kind: kind, BaseURL: parsedURL.String(), APIKey: apiKey}, nil
}

// ParseTorrentClient lit `type[:readonly]:http://user:pass@hote:port` ; seul qbittorrent existe en v1.
func ParseTorrentClient(raw string) (TorrentClient, error) {
	clientType, remainder, found := strings.Cut(raw, ":")
	if !found {
		return TorrentClient{}, errors.New("torrent client: missing type prefix (expected type:url)")
	}
	if clientType != typeQbittorrent {
		return TorrentClient{}, fmt.Errorf("torrent client: unknown type %q", clientType)
	}
	readOnly := strings.HasPrefix(remainder, readOnlyModifier)
	remainder = strings.TrimPrefix(remainder, readOnlyModifier)
	parsedURL, err := parseHTTPURL(remainder)
	if err != nil {
		return TorrentClient{}, fmt.Errorf(errInvalidURLFormat, "torrent client "+clientType)
	}
	password, _ := parsedURL.User.Password()
	user := parsedURL.User.Username()
	parsedURL.User = nil
	return TorrentClient{Type: clientType, ReadOnly: readOnly, BaseURL: parsedURL.String(), User: user, Password: password}, nil
}

func parseHTTPURL(raw string) (*url.URL, error) {
	parsedURL, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("unparsable url")
	}
	if (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		return nil, errors.New("not an http(s) url")
	}
	return parsedURL, nil
}

// redacted renvoie une copie dont la clé d'API est masquée.
func (service ArrService) redacted() ArrService {
	if service.APIKey != "" {
		service.APIKey = redactedMark
	}
	return service
}

// redacted renvoie une copie dont le mot de passe est masqué.
func (client TorrentClient) redacted() TorrentClient {
	if client.Password != "" {
		client.Password = redactedMark
	}
	return client
}

type (
	plainArrService    ArrService
	plainTorrentClient TorrentClient
	jsonArrService     struct {
		Kind    string `json:"kind"`
		BaseURL string `json:"baseUrl"`
		APIKey  string `json:"apiKey"`
	}
	jsonTorrentClient struct {
		Type     string `json:"type"`
		ReadOnly bool   `json:"readOnly"`
		BaseURL  string `json:"baseUrl"`
		User     string `json:"user"`
		Password string `json:"password"`
	}
)

// String masque la clé d'API pour %v, %+v et %s.
func (service ArrService) String() string {
	return fmt.Sprintf("%+v", plainArrService(service.redacted()))
}

// GoString masque la clé d'API pour %#v.
func (service ArrService) GoString() string { return service.String() }

// LogValue masque la clé d'API dans les journaux slog.
func (service ArrService) LogValue() slog.Value { return slog.StringValue(service.String()) }

// MarshalJSON masque la clé d'API : toute sérialisation est destinée à l'affichage.
func (service ArrService) MarshalJSON() ([]byte, error) {
	return json.Marshal(jsonArrService(service.redacted()))
}

// String masque le mot de passe pour %v, %+v et %s.
func (client TorrentClient) String() string {
	return fmt.Sprintf("%+v", plainTorrentClient(client.redacted()))
}

// GoString masque le mot de passe pour %#v.
func (client TorrentClient) GoString() string { return client.String() }

// LogValue masque le mot de passe dans les journaux slog.
func (client TorrentClient) LogValue() slog.Value { return slog.StringValue(client.String()) }

// MarshalJSON masque le mot de passe : toute sérialisation est destinée à l'affichage.
func (client TorrentClient) MarshalJSON() ([]byte, error) {
	return json.Marshal(jsonTorrentClient(client.redacted()))
}
