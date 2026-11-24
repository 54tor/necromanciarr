// Package prowlarr lit la liste des indexeurs de l'API native de Prowlarr (GET /api/v1/indexer, clé dans l'en-tête
// X-Api-Key), pour que doctor repère un indexeur Torznab désactivé ou supprimé. Jamais utilisé pour chercher. La réponse
// est une donnée hostile : corps plafonné avant analyse, nombre d'indexeurs borné, aucune redirection, erreurs sans URL.
package prowlarr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"necromanciarr/internal/httpsafe"
)

const (
	indexerPath      = "/api/v1/indexer"
	apiKeyHeader     = "X-Api-Key"
	apiKeyParameter  = "apikey"
	requestTimeout   = 15 * time.Second
	maxBodyBytes     = 16 << 20
	maxIndexers      = 2000
	maxNameRunes     = 128
	jsonMediaType    = "application/json"
	redactedLocation = "/***"
)

var (
	// ErrAccessDenied signale une clé refusée (HTTP 401 ou 403).
	ErrAccessDenied = errors.New("prowlarr: access denied")
	// ErrUnavailable signale une panne réseau, un délai dépassé, un 5xx ou un quota atteint.
	ErrUnavailable = errors.New("prowlarr: unavailable")
	// ErrBadResponse signale une réponse illisible, trop grosse, redirigée ou qui n'est pas la liste attendue.
	ErrBadResponse = errors.New("prowlarr: unreadable response")

	errInvalidURL = errors.New("prowlarr: URL must be an http(s) URL without fragment nor userinfo, " +
		"whose only query parameter is apikey")
)

// Indexer est un indexeur de Prowlarr ; ID est celui du chemin des flux Torznab (« /<id>/api »).
type Indexer struct {
	ID      int
	Name    string
	Enabled bool
}

// Client interroge l'API native de Prowlarr ; il porte la clé : ses formes texte la masquent.
type Client struct {
	base       url.URL
	apiKey     string
	httpClient *http.Client
}

// New crée un client depuis l'URL « https://hote:9696[/prefixe]?apikey=… » ; httpClient nil prend httpsafe.NewClient.
// Les redirections sont toujours refusées. L'erreur ne recopie jamais l'URL.
func New(rawURL string, httpClient *http.Client) (*Client, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.Fragment != "" || parsed.User != nil {
		return nil, errInvalidURL
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil || len(query) != 1 || len(query[apiKeyParameter]) != 1 || query.Get(apiKeyParameter) == "" {
		return nil, errInvalidURL
	}
	base := *parsed
	base.RawQuery, base.ForceQuery = "", false
	base.Path = strings.TrimSuffix(base.Path, "/") + indexerPath
	base.RawPath = ""
	if httpClient == nil {
		httpClient = httpsafe.NewClient(requestTimeout)
	} else {
		forced := *httpClient
		forced.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		httpClient = &forced
	}
	return &Client{base: base, apiKey: query.Get(apiKeyParameter), httpClient: httpClient}, nil
}

// String rend l'hôte sans chemin ni clé, pour %v, %+v et %s.
func (client *Client) String() string {
	return "prowlarr.Client{" + client.base.Scheme + "://" + client.base.Host + redactedLocation + "}"
}

// GoString masque la clé pour %#v.
func (client *Client) GoString() string { return client.String() }

// LogValue masque la clé dans les journaux slog.
func (client *Client) LogValue() slog.Value { return slog.StringValue(client.String()) }

type indexerResource struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Enable bool   `json:"enable"`
}

// Indexers liste les indexeurs de Prowlarr, activés ou non. La liste est lue en flux, un élément à la fois : une
// réponse hostile de millions d'éléments s'arrête au premier dépassement de maxIndexers.
func (client *Client) Indexers(ctx context.Context) ([]Indexer, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	body, err := client.get(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	return decodeIndexers(&cappedReader{reader: body, remaining: maxBodyBytes})
}

func decodeIndexers(reader io.Reader) ([]Indexer, error) {
	decoder := json.NewDecoder(reader)
	if opening, err := decoder.Token(); err != nil || opening != json.Delim('[') {
		return nil, fmt.Errorf("%w: not an indexer list", ErrBadResponse)
	}
	var indexers []Indexer
	for decoder.More() {
		if len(indexers) >= maxIndexers {
			return nil, fmt.Errorf("%w: too many indexers", ErrBadResponse)
		}
		var resource indexerResource
		if err := decoder.Decode(&resource); err != nil {
			return nil, fmt.Errorf("%w: not an indexer list", ErrBadResponse)
		}
		indexers = append(indexers, Indexer{ID: resource.ID, Name: cleanName(resource.Name), Enabled: resource.Enable})
	}
	if _, err := decoder.Token(); err != nil {
		return nil, fmt.Errorf("%w: not an indexer list", ErrBadResponse)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: data after the list", ErrBadResponse)
	}
	return indexers, nil
}

// cappedReader échoue en ErrBadResponse dès que la réponse dépasse sa limite : jamais de troncature silencieuse.
type cappedReader struct {
	reader    io.Reader
	remaining int
}

func (capped *cappedReader) Read(buffer []byte) (int, error) {
	count, err := capped.reader.Read(buffer[:min(len(buffer), capped.remaining+1)])
	capped.remaining -= count
	if capped.remaining < 0 {
		return 0, fmt.Errorf("%w: response larger than its limit", ErrBadResponse)
	}
	return count, err
}

// get renvoie le corps d'une réponse 2xx JSON ; l'appelant le ferme et garde le contexte vivant jusqu'à sa lecture.
func (client *Client) get(ctx context.Context) (io.ReadCloser, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.base.String(), nil)
	if err != nil {
		return nil, ErrBadResponse
	}
	request.Header.Set(apiKeyHeader, client.apiKey)
	request.Header.Set("Accept", jsonMediaType)
	response, err := client.httpClient.Do(request)
	if err != nil {
		// url.Error porte l'URL : seule la cause de contexte est conservée.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("%w: %w", ErrUnavailable, ctxErr)
		}
		return nil, fmt.Errorf("%w: request failed", ErrUnavailable)
	}
	if statusErr := statusError(response.StatusCode); statusErr != nil {
		_ = response.Body.Close()
		return nil, statusErr
	}
	if mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type")); mediaType != jsonMediaType {
		_ = response.Body.Close()
		return nil, fmt.Errorf("%w: not JSON", ErrBadResponse)
	}
	return response.Body, nil
}

func statusError(status int) error {
	switch {
	case status >= 200 && status < 300:
		return nil
	case status >= 300 && status < 400:
		return fmt.Errorf("%w: redirect refused (HTTP %d)", ErrBadResponse, status)
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return fmt.Errorf("%w (HTTP %d)", ErrAccessDenied, status)
	case status == http.StatusTooManyRequests || status == http.StatusRequestTimeout || status >= 500:
		return fmt.Errorf("%w (HTTP %d)", ErrUnavailable, status)
	}
	return fmt.Errorf("%w (HTTP %d)", ErrBadResponse, status)
}

// cleanName retire les caractères de contrôle et borne la longueur : le nom vient d'un serveur distant.
func cleanName(name string) string {
	cleaned := strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return -1
		}
		return character
	}, name)
	if runes := []rune(cleaned); len(runes) > maxNameRunes {
		return string(runes[:maxNameRunes])
	}
	return cleaned
}
