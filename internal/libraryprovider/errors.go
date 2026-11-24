package libraryprovider

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var (
	// ErrAccessDenied signale un refus d'accès (HTTP 401 ou 403).
	ErrAccessDenied = errors.New("library-provider: access denied")
	// ErrNotFound signale une release inconnue du fournisseur (HTTP 404).
	ErrNotFound = errors.New("library-provider: release not found")
	// ErrUnavailable signale une panne réseau, un délai dépassé, un 5xx, un 408 ou un 429.
	ErrUnavailable = errors.New("library-provider: unavailable")
	// ErrBadResponse signale une réponse illisible ou contraire au contrat.
	ErrBadResponse = errors.New("library-provider: unreadable response")
	// ErrResponseTooLarge précise un ErrBadResponse dont le corps dépasse le plafond (32 Mio pour un .torrent).
	ErrResponseTooLarge = errors.New("library-provider: response body exceeds the limit")
)

const (
	maxProblemBytes  = 64 << 10
	maxDetailRunes   = 200
	maxRetryAfter    = time.Hour
	redactedFragment = "[redacted]"
)

// RateLimitedError est renvoyée sur un HTTP 429 ; elle englobe ErrUnavailable.
type RateLimitedError struct {
	// RetryAfter est le délai demandé par le fournisseur, plafonné à une heure ; zéro s'il est absent.
	RetryAfter time.Duration
	detail     string
}

// Error décrit la limitation sans exposer l'URL ni les en-têtes.
func (rateLimited *RateLimitedError) Error() string {
	message := fmt.Sprintf("%s (HTTP 429, retry after %s)", ErrUnavailable.Error(), rateLimited.RetryAfter)
	if rateLimited.detail != "" {
		message += ": " + rateLimited.detail
	}
	return message
}

// Unwrap permet errors.Is(err, ErrUnavailable).
func (rateLimited *RateLimitedError) Unwrap() error { return ErrUnavailable }

// statusError traduit un statut HTTP en erreur typée ; response.Body n'est lu que pour le titre RFC 9457.
func (client *Client) statusError(response *http.Response) error {
	status := response.StatusCode
	if status < http.StatusBadRequest {
		return nil
	}
	detail := client.problemDetail(response.Body)
	switch {
	case status == http.StatusTooManyRequests:
		return &RateLimitedError{RetryAfter: parseRetryAfter(response.Header.Get("Retry-After"), time.Now()), detail: detail}
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return withDetail(fmt.Errorf("%w (HTTP %d)", ErrAccessDenied, status), detail)
	case status == http.StatusNotFound:
		return withDetail(ErrNotFound, detail)
	case status >= http.StatusInternalServerError || status == http.StatusRequestTimeout:
		return withDetail(fmt.Errorf("%w (HTTP %d)", ErrUnavailable, status), detail)
	}
	return withDetail(fmt.Errorf("%w (HTTP %d)", ErrBadResponse, status), detail)
}

func withDetail(base error, detail string) error {
	if detail == "" {
		return base
	}
	return fmt.Errorf("%w: %s", base, detail)
}

// problemDetail extrait title et detail du problem+json ; le corps brut n'est jamais renvoyé.
func (client *Client) problemDetail(body io.Reader) string {
	raw, err := io.ReadAll(io.LimitReader(body, maxProblemBytes))
	if err != nil {
		return ""
	}
	var problem struct {
		Title  string `json:"title"`
		Detail string `json:"detail"`
	}
	if json.Unmarshal(raw, &problem) != nil {
		return ""
	}
	text := strings.TrimSpace(strings.Join([]string{problem.Title, problem.Detail}, ": "))
	text = strings.Trim(text, ": ")
	// Un fournisseur hostile pourrait faire écho du jeton dans son message.
	text = strings.ReplaceAll(text, client.token, redactedFragment)
	return truncateRunes(stripControl(text), maxDetailRunes)
}

func stripControl(text string) string {
	return strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return -1
		}
		return character
	}, text)
}

func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

// parseRetryAfter lit des secondes ou une date HTTP ; valeur invalide ou passée : zéro.
func parseRetryAfter(header string, now time.Time) time.Duration {
	header = strings.TrimSpace(header)
	var delay time.Duration
	if seconds, err := strconv.ParseInt(header, 10, 64); err == nil {
		if seconds > int64(maxRetryAfter/time.Second) {
			return maxRetryAfter
		}
		delay = time.Duration(seconds) * time.Second
	} else if date, err := http.ParseTime(header); err == nil {
		delay = date.Sub(now)
	}
	return min(max(delay, 0), maxRetryAfter)
}
