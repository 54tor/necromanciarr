// Package httpsafe construit les clients HTTP durcis des appels sortants qui portent un secret.
package httpsafe

import (
	"net/http"
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
