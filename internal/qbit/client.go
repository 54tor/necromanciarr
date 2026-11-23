// Package qbit est le client de l'API Web v2 de qBittorrent (versions 4 et 5).
package qbit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"necromanciarr/internal/config"
	"necromanciarr/internal/dedupe"
)

const (
	requestTimeout      = 30 * time.Second
	defaultPollInterval = time.Second
	// maxListBytes borne les listes et l'export d'un .torrent ; maxSmallBytes borne les autres réponses.
	maxListBytes       = 8 << 20
	maxSmallBytes      = 64 << 10
	maxRedirects       = 10
	clientTypeQbit     = "qbittorrent"
	apiPathPrefix      = "/api/v2/"
	loginSuccessBody   = "Ok."
	formContentType    = "application/x-www-form-urlencoded"
	endpointLogin      = "auth/login"
	endpointAPIVersion = "app/webapiVersion"
)

// Erreurs typées du client, à tester avec errors.Is.
var (
	ErrAuthFailed = errors.New("qbit: authentication failed")
	ErrNotFound   = errors.New("qbit: torrent not found")
	// ErrUnsupported signale un point d'API absent de la version de qBittorrent interrogée.
	ErrUnsupported = errors.New("qbit: not supported by this qBittorrent version")
	// ErrIncompleteAfterRecheck : le torrent n'est pas complet après recontrôle et reste en pause.
	ErrIncompleteAfterRecheck = errors.New("qbit: torrent incomplete after recheck, left paused")
	// ErrRecheckTimeout : le recontrôle n'a pas abouti dans le délai ; le torrent reste en pause.
	ErrRecheckTimeout = errors.New("qbit: recheck not finished before the timeout, left paused")
	ErrBadResponse    = errors.New("qbit: unexpected response")
	// ErrReadOnly : client déclaré readonly dans la configuration, toute écriture est refusée.
	ErrReadOnly = errors.New("qbit: client is read-only")
	// ErrInvalidHash refuse tout ce qui n'est pas un seul info-hash hexadécimal (« all » ou « a|b » viseraient d'autres torrents).
	ErrInvalidHash = errors.New("qbit: invalid info hash")
)

var errRedirectRefused = fmt.Errorf("%w: redirect to another host refused", ErrBadResponse)

var _ dedupe.Rechecker = (*Client)(nil)

// Client parle à une instance qBittorrent ; il garde le cookie de session et la version d'API sondée une fois.
type Client struct {
	baseURL      *url.URL
	origin       string
	user         string
	password     string
	readOnly     bool
	pollInterval time.Duration
	httpClient   *http.Client

	versionMutex sync.Mutex
	version      *apiVersion
}

// Option règle un Client à sa création.
type Option func(client *Client)

// WithReadOnly refuse toutes les méthodes qui modifient l'état du client torrent.
func WithReadOnly() Option { return func(client *Client) { client.readOnly = true } }

// WithPollInterval règle l'intervalle d'interrogation pendant un recontrôle.
func WithPollInterval(interval time.Duration) Option {
	return func(client *Client) { client.pollInterval = interval }
}

// New crée un client ; baseURL peut porter un préfixe de chemin (proxy inverse, proxy de qui). Sans identifiants,
// aucune connexion n'est tentée. Les identifiants ne voyagent que dans le corps du formulaire de connexion.
func New(baseURL, user, password string, options ...Option) (*Client, error) {
	parsedURL, err := parseBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("qbit: cookie jar: %w", err)
	}
	client := &Client{
		baseURL:      parsedURL,
		origin:       parsedURL.Scheme + "://" + parsedURL.Host,
		user:         user,
		password:     password,
		pollInterval: defaultPollInterval,
	}
	client.httpClient = &http.Client{Timeout: requestTimeout, Jar: jar, Transport: newTransport(), CheckRedirect: client.checkRedirect}
	for _, option := range options {
		option(client)
	}
	return client, nil
}

// NewFromConfig crée le client d'une entrée de configuration de type qbittorrent, readonly compris.
func NewFromConfig(torrentClient config.TorrentClient) (*Client, error) {
	if torrentClient.Type != clientTypeQbit {
		return nil, fmt.Errorf("qbit: unsupported torrent client type %q", torrentClient.Type)
	}
	var options []Option
	if torrentClient.ReadOnly {
		options = append(options, WithReadOnly())
	}
	return New(torrentClient.BaseURL, torrentClient.User, torrentClient.Password, options...)
}

// parseBaseURL renvoie des erreurs à message fixe : l'URL brute peut contenir un mot de passe.
func parseBaseURL(raw string) (*url.URL, error) {
	parsedURL, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("qbit: invalid base url")
	}
	isHTTP := parsedURL.Scheme == "http" || parsedURL.Scheme == "https"
	if !isHTTP || parsedURL.Host == "" {
		return nil, errors.New("qbit: invalid base url (expected http(s)://host[:port][/path])")
	}
	if parsedURL.User != nil || parsedURL.RawQuery != "" || parsedURL.Fragment != "" || strings.Contains(raw, "#") {
		return nil, errors.New("qbit: base url must not carry credentials, query or fragment")
	}
	parsedURL.Path = strings.TrimRight(parsedURL.Path, "/")
	parsedURL.RawPath = ""
	return parsedURL, nil
}

// newTransport n'utilise pas le proxy d'environnement : HTTP_PROXY recevrait sinon le formulaire de connexion.
func newTransport() http.RoundTripper {
	defaultTransport, isTransport := http.DefaultTransport.(*http.Transport)
	if !isTransport {
		return http.DefaultTransport
	}
	transport := defaultTransport.Clone()
	transport.Proxy = nil
	return transport
}

// checkRedirect n'accepte qu'une redirection vers le même schéma et le même hôte : le cookie ne part jamais ailleurs.
func (client *Client) checkRedirect(request *http.Request, previous []*http.Request) error {
	sameOrigin := request.URL.Scheme == client.baseURL.Scheme && request.URL.Host == client.baseURL.Host
	if !sameOrigin || len(previous) >= maxRedirects {
		return errRedirectRefused
	}
	return nil
}

// String décrit le client sans mot de passe ni cookie.
func (client *Client) String() string {
	return fmt.Sprintf("qbit.Client{baseURL: %s, user: %q, readOnly: %t}", client.baseURL, client.user, client.readOnly)
}

// GoString décrit le client sans mot de passe ni cookie pour %#v.
func (client *Client) GoString() string { return client.String() }

func (client *Client) hasCredentials() bool { return client.user != "" || client.password != "" }

func (client *Client) refuseIfReadOnly() error {
	if client.readOnly {
		return ErrReadOnly
	}
	return nil
}

// requestBody est rejouable : la requête est renvoyée telle quelle après une reconnexion.
type requestBody struct {
	contentType string
	data        []byte
}

func formBody(values url.Values) *requestBody {
	return &requestBody{contentType: formContentType, data: []byte(values.Encode())}
}

// call envoie la requête ; sur 403 avec identifiants, il se reconnecte une fois puis rejoue une fois.
func (client *Client) call(ctx context.Context, method, endpoint string, query url.Values, body *requestBody, limit int64) ([]byte, error) {
	status, payload, err := client.send(ctx, method, endpoint, query, body, limit)
	if err != nil {
		return nil, err
	}
	if status == http.StatusForbidden && client.hasCredentials() {
		if err := client.login(ctx); err != nil {
			return nil, err
		}
		status, payload, err = client.send(ctx, method, endpoint, query, body, limit)
		if err != nil {
			return nil, err
		}
	}
	if err := statusError(endpoint, status); err != nil {
		return nil, err
	}
	return payload, nil
}

// statusError ne reprend jamais le corps de la réponse.
func statusError(endpoint string, status int) error {
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == http.StatusForbidden:
		return fmt.Errorf("%w: %s returned HTTP 403", ErrAuthFailed, endpoint)
	case status == http.StatusNotFound:
		return fmt.Errorf("%w: %s returned HTTP 404", ErrNotFound, endpoint)
	case status == http.StatusMethodNotAllowed:
		return fmt.Errorf("%w: %s returned HTTP 405", ErrUnsupported, endpoint)
	default:
		return fmt.Errorf("%w: %s returned HTTP %d", ErrBadResponse, endpoint, status)
	}
}

func (client *Client) send(ctx context.Context, method, endpoint string, query url.Values, body *requestBody, limit int64) (int, []byte, error) {
	request, err := client.newRequest(ctx, method, endpoint, query, body)
	if err != nil {
		return 0, nil, err
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return 0, nil, fmt.Errorf("qbit: %s: %w", endpoint, err)
	}
	defer func() { _ = response.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %s: reading body failed", ErrBadResponse, endpoint)
	}
	if int64(len(payload)) > limit {
		return 0, nil, fmt.Errorf("%w: %s: response larger than %d bytes", ErrBadResponse, endpoint, limit)
	}
	return response.StatusCode, payload, nil
}

// newRequest place Referer et Origin sur l'URL de base : la protection CSRF de qBittorrent les compare à l'hôte.
func (client *Client) newRequest(ctx context.Context, method, endpoint string, query url.Values, body *requestBody) (*http.Request, error) {
	// endpoint est toujours une constante du paquet, jamais une entrée.
	target := *client.baseURL
	target.Path = client.baseURL.Path + apiPathPrefix + endpoint
	target.RawQuery = query.Encode()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body.data)
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return nil, fmt.Errorf("qbit: %s: building request failed", endpoint)
	}
	if body != nil {
		request.Header.Set("Content-Type", body.contentType)
	}
	request.Header.Set("Referer", client.baseURL.String())
	request.Header.Set("Origin", client.origin)
	return request, nil
}

// login poste le formulaire ; qBittorrent répond 200 « Fails. » sur de mauvais identifiants et 403 sur une IP bannie.
func (client *Client) login(ctx context.Context) error {
	credentials := url.Values{"username": {client.user}, "password": {client.password}}
	status, payload, err := client.send(ctx, http.MethodPost, endpointLogin, nil, formBody(credentials), maxSmallBytes)
	if err != nil {
		return err
	}
	if status == http.StatusForbidden {
		return fmt.Errorf("%w: login refused (HTTP 403, IP possibly banned)", ErrAuthFailed)
	}
	if err := statusError(endpointLogin, status); err != nil {
		return err
	}
	if strings.TrimSpace(string(payload)) != loginSuccessBody {
		return fmt.Errorf("%w: credentials rejected", ErrAuthFailed)
	}
	return nil
}
