// Package qbit s'appuie sur github.com/autobrr/go-qbittorrent : garde de recontrôle, mode lecture seule, validation
// des hashes, erreurs typées sans URL ni secret, transport durci (sans proxy d'environnement, sans redirection vers
// un autre hôte, corps plafonnés, aucune nouvelle tentative automatique).
package qbit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	qbittorrent "github.com/autobrr/go-qbittorrent"

	"necromanciarr/internal/config"
	"necromanciarr/internal/dedupe"
)

const (
	requestTimeout        = 30 * time.Second
	requestTimeoutSeconds = 30
	// singleAttempt : une requête d'écriture rejouée (ajout, recontrôle, pause) est un risque ; seul le rejeu après
	// un 403 (requête refusée, donc non appliquée) est fait, une fois, par Client.run.
	singleAttempt       = 1
	maxRedirects        = 10
	defaultPollInterval = time.Second
	clientTypeQbit      = "qbittorrent"
	redactedPath        = "/***"
)

// Erreurs typées du client, à tester avec errors.Is ; leurs messages ne portent ni URL, ni corps, ni secret.
var (
	ErrAuthFailed = errors.New("qbit: authentication failed")
	ErrNotFound   = errors.New("qbit: torrent not found")
	// ErrUnsupported signale un point d'API absent de la version de qBittorrent interrogée.
	ErrUnsupported = errors.New("qbit: not supported by this qBittorrent version")
	// ErrIncompleteAfterRecheck : le torrent n'est pas complet après recontrôle et reste en pause.
	ErrIncompleteAfterRecheck = errors.New("qbit: torrent incomplete after recheck, left paused")
	// ErrRecheckTimeout : le recontrôle n'a pas commencé ou pas abouti dans le délai ; le torrent reste en pause.
	ErrRecheckTimeout = errors.New("qbit: recheck not finished before the timeout, left paused")
	ErrBadResponse    = errors.New("qbit: unexpected response")
	// ErrResponseTooLarge : la réponse dépasse son plafond (64 Mio pour la liste des torrents) ; rien n'est tronqué.
	ErrResponseTooLarge = errors.New("qbit: response larger than its limit")
	// ErrReadOnly : client déclaré readonly dans la configuration, toute écriture est refusée.
	ErrReadOnly = errors.New("qbit: client is read-only")
	// ErrInvalidHash refuse tout ce qui n'est pas un seul info-hash hexadécimal (« all » ou « a|b » viseraient d'autres torrents).
	ErrInvalidHash = errors.New("qbit: invalid info hash")
)

var _ dedupe.Rechecker = (*Client)(nil)

// Client parle à une instance qBittorrent au travers de la bibliothèque go-qbittorrent.
type Client struct {
	library           *qbittorrent.Client
	baseURL           *url.URL
	readOnly          bool
	pollInterval      time.Duration
	checkStartTimeout time.Duration
	recheckTimeout    time.Duration
}

// Option règle un Client à sa création.
type Option func(client *Client)

// WithReadOnly refuse toutes les méthodes qui modifient l'état du client torrent.
func WithReadOnly() Option { return func(client *Client) { client.readOnly = true } }

// WithPollInterval règle l'intervalle d'interrogation pendant un recontrôle.
func WithPollInterval(interval time.Duration) Option {
	return func(client *Client) { client.pollInterval = interval }
}

// WithCheckStartTimeout règle le délai dans lequel un état checking doit apparaître après la demande de recontrôle.
func WithCheckStartTimeout(timeout time.Duration) Option {
	return func(client *Client) { client.checkStartTimeout = timeout }
}

// WithRecheckTimeout règle le délai global de recontrôle utilisé par RecheckPath.
func WithRecheckTimeout(timeout time.Duration) Option {
	return func(client *Client) { client.recheckTimeout = timeout }
}

// New crée un client ; baseURL peut porter un préfixe de chemin (proxy inverse, proxy de qui). Sans identifiants,
// aucune connexion n'est tentée. Les identifiants ne voyagent que dans le corps du formulaire de connexion.
func New(baseURL, user, password string, options ...Option) (*Client, error) {
	parsedURL, err := parseBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	client := &Client{
		baseURL:           parsedURL,
		pollInterval:      defaultPollInterval,
		checkStartTimeout: defaultCheckStartTimeout,
		recheckTimeout:    defaultRecheckTimeout,
	}
	for _, option := range options {
		option(client)
	}
	library := qbittorrent.NewClient(qbittorrent.Config{
		Host:          parsedURL.String(),
		Username:      user,
		Password:      password,
		Timeout:       requestTimeoutSeconds,
		RetryAttempts: singleAttempt,
	})
	client.library = library.WithHTTPClient(client.newHTTPClient())
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

// parseBaseURL renvoie des erreurs à message fixe : l'URL brute peut contenir un mot de passe ou une clé de proxy.
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

// newHTTPClient remplace celui de la bibliothèque, qui suivrait HTTP_PROXY et toute redirection.
func (client *Client) newHTTPClient() *http.Client {
	transport := &guardTransport{
		base:    newBaseTransport(),
		referer: client.baseURL.String(),
		origin:  client.baseURL.Scheme + "://" + client.baseURL.Host,
	}
	return &http.Client{Timeout: requestTimeout, Transport: transport, CheckRedirect: client.checkRedirect}
}

// newBaseTransport n'utilise pas le proxy d'environnement : il recevrait sinon le formulaire de connexion.
func newBaseTransport() http.RoundTripper {
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
		probeFrom(request.Context()).redirectRefused = true
		return errors.New("redirect refused")
	}
	return nil
}

// String décrit le client par schéma et hôte seulement : le chemin peut porter la clé du proxy de qui.
func (client *Client) String() string {
	shownURL := client.baseURL.Scheme + "://" + client.baseURL.Host
	if client.baseURL.Path != "" {
		shownURL += redactedPath
	}
	return fmt.Sprintf("qbit.Client{baseURL: %s, readOnly: %t}", shownURL, client.readOnly)
}

// GoString décrit le client sans secret pour %#v.
func (client *Client) GoString() string { return client.String() }

// ReadOnly dit si le client est déclaré en lecture seule : aucun ajout, pause, recontrôle ni reprise.
func (client *Client) ReadOnly() bool { return client.readOnly }

func (client *Client) refuseIfReadOnly() error {
	if client.readOnly {
		return ErrReadOnly
	}
	return nil
}

// run exécute un appel de la bibliothèque ; après un 403 suivi d'une reconnexion réussie (faite par la
// bibliothèque), l'appel est rejoué une fois : la requête refusée n'a rien modifié.
func (client *Client) run(ctx context.Context, operation string, call func(ctx context.Context) error) (*callProbe, error) {
	probe := &callProbe{}
	err := call(withProbe(ctx, probe))
	if err != nil && probe.forbidden && !probe.loginRejected {
		probe = &callProbe{}
		err = call(withProbe(ctx, probe))
	}
	return probe, translateError(ctx, operation, err, probe)
}

// translateError ne reprend jamais le message de la bibliothèque, qui contient l'URL (et la clé du proxy).
func translateError(ctx context.Context, operation string, err error, probe *callProbe) error {
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("qbit: %s: %w", operation, ctxErr)
	}
	switch {
	case probe.loginRejected || errors.Is(err, qbittorrent.ErrBadCredentials) || errors.Is(err, qbittorrent.ErrIPBanned):
		return fmt.Errorf("%w: %s: login rejected", ErrAuthFailed, operation)
	case probe.forbidden:
		return fmt.Errorf("%w: %s: HTTP 403", ErrAuthFailed, operation)
	case probe.redirectRefused:
		return fmt.Errorf("%w: %s: redirect to another host refused", ErrBadResponse, operation)
	case probe.tooLarge:
		return fmt.Errorf("%w: %w: %s", ErrBadResponse, ErrResponseTooLarge, operation)
	case errors.Is(err, qbittorrent.ErrTorrentNotFound) || probe.lastStatus == http.StatusNotFound:
		return fmt.Errorf("%w: %s", ErrNotFound, operation)
	case errors.Is(err, qbittorrent.ErrUnsupportedVersion) || probe.lastStatus == http.StatusMethodNotAllowed:
		return fmt.Errorf("%w: %s", ErrUnsupported, operation)
	case probe.lastStatus != 0:
		return fmt.Errorf("%w: %s: HTTP %d", ErrBadResponse, operation, probe.lastStatus)
	default:
		return fmt.Errorf("%w: %s: request failed", ErrBadResponse, operation)
	}
}
