// Package libraryprovider est le client du contrat fournisseur de bibliothèque (docs/contract/openapi.yaml).
package libraryprovider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"necromanciarr/internal/config"
	"necromanciarr/internal/httpsafe"
)

const (
	httpTimeout        = 30 * time.Second
	defaultConcurrency = 4
	maxJSONBytes       = 16 << 20
	maxTorrentBytes    = 32 << 20

	acceptJSON    = "application/json, application/problem+json"
	acceptTorrent = "application/x-bittorrent, application/problem+json"

	segmentVersion  = "v1"
	segmentReleases = "releases"
	segmentTorrent  = "torrent"
	segmentNfos     = "nfos"
)

// Client interroge un fournisseur ; il est sûr pour un usage concurrent.
type Client struct {
	// MaxInFlight borne les requêtes simultanées (4 par défaut) ; à fixer avant le premier appel.
	MaxInFlight int

	base          *url.URL
	token         string
	httpClient    *http.Client
	cache         *cache
	semaphore     chan struct{}
	semaphoreOnce sync.Once
}

// New crée un client. L'URL de base peut porter un préfixe de chemin ; le jeton Bearer est obligatoire.
func New(baseURL, token string) (*Client, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Fragment != "" {
		return nil, errors.New("library-provider: base URL must be an http(s) URL without fragment")
	}
	if token == "" {
		return nil, errors.New("library-provider: token is required")
	}
	return &Client{
		MaxInFlight: defaultConcurrency,
		base:        parsed,
		token:       token,
		httpClient:  httpsafe.NewClient(httpTimeout),
		cache:       newCache(),
	}, nil
}

// NewFromConfig crée le client depuis libraryApi / LIBRARY_API et LIBRARY_PROVIDER_TOKEN.
func NewFromConfig(settings config.Config) (*Client, error) {
	return New(settings.LibraryAPI, settings.LibraryProviderToken)
}

func (client *Client) acquire(ctx context.Context) (func(), error) {
	client.semaphoreOnce.Do(func() {
		limit := client.MaxInFlight
		if limit < 1 {
			limit = defaultConcurrency
		}
		client.semaphore = make(chan struct{}, limit)
	})
	select {
	case client.semaphore <- struct{}{}:
		return func() { <-client.semaphore }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, ctx.Err())
	}
}

// endpoint construit l'URL d'une route du contrat sous la base configurée.
func (client *Client) endpoint(query url.Values, segments ...string) string {
	escaped := []string{segmentVersion}
	for _, segment := range segments {
		escaped = append(escaped, url.PathEscape(segment))
	}
	target := *client.base
	target.Path = strings.TrimRight(target.Path, "/") + "/" + strings.Join(escaped, "/")
	target.RawPath = ""
	target.RawQuery = query.Encode()
	return target.String()
}

// get lit au plus limit octets ; un corps plus long est une erreur, jamais une troncature silencieuse.
func (client *Client) get(ctx context.Context, target, accept string, limit int64) ([]byte, error) {
	release, err := client.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid request", ErrBadResponse)
	}
	request.Header.Set("Authorization", "Bearer "+client.token)
	request.Header.Set("Accept", accept)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: request failed", ErrUnavailable)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode >= http.StatusMultipleChoices {
		return nil, client.statusError(response)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%w: body read failed", ErrUnavailable)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%w: body exceeds %d bytes", ErrBadResponse, limit)
	}
	return body, nil
}

func (client *Client) getJSON(ctx context.Context, target string) ([]byte, error) {
	return client.get(ctx, target, acceptJSON, maxJSONBytes)
}

func trackerQuery(tracker string) url.Values {
	query := url.Values{}
	if tracker != "" {
		query.Set("tracker", tracker)
	}
	return query
}
