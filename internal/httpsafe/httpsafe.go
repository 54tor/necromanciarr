// Package httpsafe construit les clients HTTP durcis des appels sortants qui portent un secret.
package httpsafe

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// NewClient renvoie un client sans proxy d'environnement (un secret ne doit pas lui être confié), qui refuse toute
// redirection (elle ferait suivre les en-têtes d'authentification) et dont le délai total est timeout.
func NewClient(timeout time.Duration) *http.Client {
	client := &http.Client{
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	if defaultTransport, isTransport := http.DefaultTransport.(*http.Transport); isTransport {
		transport := defaultTransport.Clone()
		transport.Proxy = nil
		client.Transport = transport
	}
	return client
}

// maxRetryAfter plafonne le délai demandé par un serveur : au-delà, une valeur hostile figerait une étape.
const maxRetryAfter = time.Hour

// ParseRetryAfter lit l'en-tête Retry-After en secondes ou en date HTTP, plafonné à une heure ; une valeur invalide
// ou passée donne zéro.
func ParseRetryAfter(header string, now time.Time) time.Duration {
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
