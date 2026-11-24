package crossseed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"necromanciarr/internal/httpsafe"
)

const (
	webhookPath           = "/api/webhook"
	apiKeyHeader          = "X-Api-Key"
	defaultWebhookTimeout = 30 * time.Second
	maxResponseBytes      = 64 << 10
)

// infoHashPattern : empreinte SHA-1 hexadécimale (torrent v1), contrôlée avant toute requête.
var infoHashPattern = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

var (
	// ErrInvalidInfoHash signale une empreinte qui n'est pas 40 caractères hexadécimaux ; aucune requête n'est émise.
	ErrInvalidInfoHash = errors.New("cross-seed: invalid info hash")
	// ErrInvalidAPIKey signale une clé d'API vide ou contenant un caractère de contrôle ; la valeur n'est jamais citée.
	ErrInvalidAPIKey = errors.New("cross-seed: invalid api key")
	// ErrAccessDenied signale une clé d'API refusée (HTTP 401 ou 403).
	ErrAccessDenied = errors.New("cross-seed: access denied")
	// ErrNotFound signale une route absente ou désactivée côté cross-seed (HTTP 404).
	ErrNotFound = errors.New("cross-seed: webhook route not found")
	// ErrUnavailable signale une panne réseau, un délai dépassé, un 409, un 429 ou un 5xx.
	ErrUnavailable = errors.New("cross-seed: unavailable")
	// ErrUnexpectedStatus signale tout statut autre que 204, y compris un 2xx : seul 204 est documenté.
	ErrUnexpectedStatus = errors.New("cross-seed: unexpected status")
)

// Notifier prévient cross-seed qu'un torrent vient d'être ajouté, pour qu'il cherche ses correspondances.
type Notifier interface {
	Notify(ctx context.Context, infoHash string) error
}

// Noop est le notifieur inactif : cross-seed non configuré ou désactivé.
type Noop struct{}

// Notify ne fait rien et renvoie nil.
func (Noop) Notify(context.Context, string) error { return nil }

// Webhook notifie le démon cross-seed par POST /api/webhook. La recherche est asynchrone : 204 veut dire « reçu ».
type Webhook struct {
	endpoint   string
	apiKey     string
	httpClient *http.Client
}

// WebhookOption ajuste un Webhook.
type WebhookOption func(*Webhook)

// WithTimeout fixe le délai total d'une notification (30 s par défaut).
func WithTimeout(timeout time.Duration) WebhookOption {
	return func(webhook *Webhook) {
		if timeout > 0 {
			webhook.httpClient.Timeout = timeout
		}
	}
}

// NewWebhook valide l'URL de base (http ou https, sans identifiants, requête ni fragment) et exige une clé d'API.
func NewWebhook(baseURL, apiKey string, options ...WebhookOption) (*Webhook, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.Fragment != "" || parsed.RawQuery != "" || strings.Contains(baseURL, "#") {
		return nil, errors.New("cross-seed: base URL must be an http(s) URL without credentials, query or fragment")
	}
	if !isValidHeaderValue(apiKey) {
		return nil, ErrInvalidAPIKey
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + webhookPath
	webhook := &Webhook{endpoint: parsed.String(), apiKey: apiKey, httpClient: httpsafe.NewClient(defaultWebhookTimeout)}
	for _, option := range options {
		option(webhook)
	}
	return webhook, nil
}

// isValidHeaderValue refuse la chaîne vide et tout octet de contrôle (CR, LF, NUL…) : injection d'en-tête.
func isValidHeaderValue(value string) bool {
	if value == "" {
		return false
	}
	for index := range len(value) {
		if value[index] < 0x20 || value[index] == 0x7f {
			return false
		}
	}
	return true
}

// Notify demande à cross-seed de chercher le torrent d'empreinte infoHash. Les erreurs ne portent ni URL, ni clé, ni corps.
func (webhook *Webhook) Notify(ctx context.Context, infoHash string) error {
	if !infoHashPattern.MatchString(infoHash) {
		return ErrInvalidInfoHash
	}
	payload, err := json.Marshal(map[string]string{"infoHash": infoHash})
	if err != nil {
		return fmt.Errorf("%w: encoding failed", ErrUnexpectedStatus)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook.endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("%w: invalid request", ErrUnavailable)
	}
	request.Header.Set(apiKeyHeader, webhook.apiKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := webhook.httpClient.Do(request)
	if err != nil {
		return requestError(err)
	}
	defer func() { _ = response.Body.Close() }()
	// Le corps est vidé (borné) pour réutiliser la connexion, jamais exploité.
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
	return statusError(response.StatusCode)
}

// requestError garde la cause de contexte (annulation, délai) sans jamais citer l'erreur réseau, qui contient l'URL.
func requestError(err error) error {
	for _, contextErr := range []error{context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, contextErr) {
			return fmt.Errorf("%w: request failed: %w", ErrUnavailable, contextErr)
		}
	}
	return fmt.Errorf("%w: request failed", ErrUnavailable)
}

func statusError(status int) error {
	switch {
	case status == http.StatusNoContent:
		return nil
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return fmt.Errorf("%w (HTTP %d)", ErrAccessDenied, status)
	case status == http.StatusNotFound:
		return ErrNotFound
	case status == http.StatusConflict || status == http.StatusTooManyRequests || status >= http.StatusInternalServerError:
		return fmt.Errorf("%w (HTTP %d)", ErrUnavailable, status)
	}
	return fmt.Errorf("%w (HTTP %d)", ErrUnexpectedStatus, status)
}

// String décrit le client sans URL ni clé.
func (webhook Webhook) String() string { return "crossseed.Webhook{apiKey:" + redactedMark + "}" }

// GoString masque la clé et l'URL pour %#v.
func (webhook Webhook) GoString() string { return webhook.String() }

// LogValue masque la clé et l'URL dans les journaux slog.
func (webhook Webhook) LogValue() slog.Value { return slog.StringValue(webhook.String()) }
