// Package torznab est le client durci d'un indexeur Torznab (flux Prowlarr, Jackett ou indexeur natif) : capacités
// et recherche. Context7 ne connaît que la spécification Torznab, aucune bibliothèque Go cliente maintenue ; le format
// est un RSS 2.0 étendu de quelques torznab:attr, lu par encoding/xml de la bibliothèque standard, qui ne résout ni
// DTD ni entité externe. Les réponses sont des données hostiles : corps plafonné avant analyse, directives refusées,
// profondeur et nombre d'éléments bornés, aucune redirection, erreurs sans URL ni texte venu de l'indexeur.
package torznab

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"necromanciarr/internal/httpsafe"
)

const (
	defaultMinInterval = 3 * time.Second
	defaultTimeout     = 30 * time.Second
	apiKeyParameter    = "apikey"
	acceptXML          = "application/rss+xml, application/xml, text/xml"
	redactedPath       = "/***"
)

// Endpoint décrit un indexeur Torznab.
type Endpoint struct {
	// Name nomme l'indexeur dans les messages.
	Name string
	// URL est l'URL de l'API Torznab ; elle porte la clé dans le paramètre apikey : c'est un secret.
	URL string
	// MinInterval est l'intervalle minimal entre deux requêtes à cet indexeur (3 s si zéro).
	MinInterval time.Duration
	// Timeout borne chaque requête (30 s si zéro).
	Timeout time.Duration
}

// String masque la clé pour %v, %+v et %s : l'URL est rendue en schéma et hôte seulement.
func (endpoint Endpoint) String() string {
	return fmt.Sprintf("torznab.Endpoint{Name: %q, URL: %s, MinInterval: %s, Timeout: %s}",
		endpoint.Name, redactedRawURL(endpoint.URL), endpoint.MinInterval, endpoint.Timeout)
}

// GoString masque la clé pour %#v.
func (endpoint Endpoint) GoString() string { return endpoint.String() }

// LogValue masque la clé dans les journaux slog.
func (endpoint Endpoint) LogValue() slog.Value { return slog.StringValue(endpoint.String()) }

// MarshalJSON masque la clé : toute sérialisation est destinée à l'affichage.
func (endpoint Endpoint) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Name        string `json:"name"`
		URL         string `json:"url"`
		MinInterval string `json:"minInterval"`
		Timeout     string `json:"timeout"`
	}{endpoint.Name, redactedRawURL(endpoint.URL), endpoint.MinInterval.String(), endpoint.Timeout.String()})
}

// Deps porte les dépendances injectables d'un Client.
type Deps struct {
	// HTTP est le client HTTP (httpsafe.NewClient si nil) ; ses redirections sont toujours refusées.
	HTTP *http.Client
	// Now donne l'heure du limiteur et du cache (time.Now si nil).
	Now func() time.Time
}

// Client interroge un indexeur ; il est sûr pour un usage concurrent et a son propre limiteur.
type Client struct {
	name       string
	base       *url.URL
	apiKey     string
	timeout    time.Duration
	httpClient *http.Client
	now        func() time.Time
	limiter    *rate.Limiter
	// wait attend la fin d'un délai du limiteur ; remplacé dans les tests par une horloge factice.
	wait func(ctx context.Context, delay time.Duration) error

	capsMutex     sync.Mutex
	cachedCaps    *Caps
	capsFetchedAt time.Time
}

// New crée un client ; l'URL doit être en http(s), sans fragment ni identifiants, avec apikey pour seul paramètre.
// L'erreur ne recopie jamais l'URL.
func New(endpoint Endpoint, deps Deps) (*Client, error) {
	base, apiKey, err := splitEndpointURL(endpoint.URL)
	if err != nil {
		return nil, err
	}
	timeout := valueOrDefault(endpoint.Timeout, defaultTimeout)
	minInterval := valueOrDefault(endpoint.MinInterval, defaultMinInterval)
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	return &Client{
		name:       endpoint.Name,
		base:       base,
		apiKey:     apiKey,
		timeout:    timeout,
		httpClient: withoutRedirects(deps.HTTP, timeout),
		now:        now,
		limiter:    rate.NewLimiter(rate.Every(minInterval), 1),
		wait:       sleepContext,
	}, nil
}

func splitEndpointURL(rawURL string) (*url.URL, string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, "", errInvalidEndpoint
	}
	isHTTP := parsed.Scheme == "http" || parsed.Scheme == "https"
	if !isHTTP || parsed.Host == "" || parsed.Fragment != "" || parsed.User != nil {
		return nil, "", errInvalidEndpoint
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil || len(query[apiKeyParameter]) > 1 {
		return nil, "", errInvalidEndpoint
	}
	for name := range query {
		if name != apiKeyParameter {
			return nil, "", errInvalidEndpoint
		}
	}
	base := *parsed
	base.RawQuery = ""
	base.ForceQuery = false
	return &base, query.Get(apiKeyParameter), nil
}

func valueOrDefault(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}

// withoutRedirects copie le client fourni pour lui interdire toute redirection : elle ferait suivre la clé ailleurs.
func withoutRedirects(provided *http.Client, timeout time.Duration) *http.Client {
	if provided == nil {
		return httpsafe.NewClient(timeout)
	}
	hardened := *provided
	hardened.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &hardened
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// String décrit le client par son nom, son schéma et son hôte : la clé n'apparaît jamais.
func (client *Client) String() string {
	return fmt.Sprintf("torznab.Client{name: %q, url: %s}", client.name, redactedOrigin(client.base))
}

// GoString décrit le client sans secret pour %#v.
func (client *Client) GoString() string { return client.String() }

// LogValue décrit le client sans secret dans les journaux slog.
func (client *Client) LogValue() slog.Value { return slog.StringValue(client.String()) }

func redactedRawURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return redactedPath
	}
	return redactedOrigin(parsed)
}

func redactedOrigin(parsed *url.URL) string {
	return parsed.Scheme + "://" + parsed.Host + redactedPath
}

// throttle impose MinInterval ; le jeton réservé est rendu si l'attente est abandonnée.
func (client *Client) throttle(ctx context.Context) error {
	reservedAt := client.now()
	reservation := client.limiter.ReserveN(reservedAt, 1)
	delay := reservation.DelayFrom(reservedAt)
	if delay <= 0 {
		return nil
	}
	if err := client.wait(ctx, delay); err != nil {
		reservation.CancelAt(client.now())
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return nil
}

// get envoie une requête de l'API et rend le corps XML plafonné et le délai Retry-After ; l'erreur d'un appel HTTP
// est reconstruite sans l'URL, qui porte la clé.
func (client *Client) get(ctx context.Context, parameters url.Values) ([]byte, time.Duration, error) {
	if err := client.throttle(ctx); err != nil {
		return nil, 0, err
	}
	requestContext, cancel := context.WithTimeout(ctx, client.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, client.target(parameters), http.NoBody)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: invalid request", ErrBadResponse)
	}
	request.Header.Set("Accept", acceptXML)
	response, err := client.httpClient.Do(request)
	if err != nil {
		if contextErr := requestContext.Err(); contextErr != nil {
			return nil, 0, fmt.Errorf("%w: %w", ErrUnavailable, contextErr)
		}
		return nil, 0, fmt.Errorf("%w: request failed", ErrUnavailable)
	}
	defer func() { _ = response.Body.Close() }()
	retryAfter := httpsafe.ParseRetryAfter(response.Header.Get("Retry-After"), client.now())
	isSuccess := response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices
	if !isSuccess {
		content, _ := readCapped(response.Body)
		return nil, 0, statusError(response.StatusCode, content, retryAfter)
	}
	if !isXMLContentType(response.Header.Get("Content-Type")) {
		return nil, 0, fmt.Errorf("%w: content type is not XML", ErrBadResponse)
	}
	content, err := readCapped(response.Body)
	if err != nil {
		return nil, 0, err
	}
	return content, retryAfter, nil
}

func (client *Client) target(parameters url.Values) string {
	if client.apiKey != "" {
		parameters.Set(apiKeyParameter, client.apiKey)
	}
	target := *client.base
	target.RawQuery = parameters.Encode()
	return target.String()
}
