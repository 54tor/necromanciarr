package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"necromanciarr/internal/config"
	"necromanciarr/internal/httpsafe"
	"necromanciarr/internal/web"
)

const (
	// healthcheckTimeout borne la sonde du conteneur (HEALTHCHECK --timeout=5s laisse de la marge).
	healthcheckTimeout  = 3 * time.Second
	healthcheckFailure  = "healthcheck: unavailable"
	nonLoopbackWarning  = "warning: listening outside loopback, every api request needs the bearer token"
	healthcheckPath     = "/healthz"
	loopbackForWildcard = "127.0.0.1"
)

// errNoHealthProbe : le Service ouvert ne fournit pas de quoi sonder la base pour /healthz.
var errNoHealthProbe = errors.New("serve: the opened store cannot answer the health probe")

// listen ouvre l'écoute TCP de serve ; les tests le remplacent.
var listen = func(address string) (net.Listener, error) { return net.Listen("tcp", address) }

// runServe sert l'API et l'interface jusqu'au premier SIGINT ou SIGTERM (annulation de ctx), puis s'arrête proprement.
func runServe(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	out := newPrinter(stdout, stderr, args)
	settings, err := config.Load(args, lookupEnv, defaultConfigDir())
	if err != nil {
		return out.failure(err)
	}
	out.useSettings(settings)
	if err := config.ValidateServe(settings); err != nil {
		return out.failure(err)
	}
	service, closer, err := openService(ctx, settings)
	if err != nil {
		return out.failure(err)
	}
	defer func() { _ = closer.Close() }()
	health, canPing := closer.(web.HealthChecker)
	if !canPing {
		return out.failure(errNoHealthProbe)
	}
	listener, err := listen(settings.ListenAddr)
	if err != nil {
		return out.failure(err)
	}
	_, _ = fmt.Fprintf(stdout, "listening on http://%s\n", listener.Addr())
	if !config.IsLoopbackAddress(settings.ListenAddr) {
		_, _ = fmt.Fprintln(stderr, nonLoopbackWarning)
	}
	if err := web.Run(ctx, listener, web.NewHandler(ctx, service, health, settings)); err != nil {
		return out.failure(err)
	}
	return successExitCode
}

// runHealthcheck interroge /healthz sur la boucle locale ; code 0 seulement sur un 200. Aucun détail n'est affiché
// (la sortie d'une sonde Docker est visible par docker inspect).
func runHealthcheck(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	settings, err := config.Load(args, lookupEnv, defaultConfigDir())
	if err != nil {
		_, _ = fmt.Fprintln(stderr, healthcheckFailure)
		return failureExitCode
	}
	probeURL, err := healthcheckURL(settings.ListenAddr)
	if err != nil || !healthzAnswers(ctx, probeURL) {
		_, _ = fmt.Fprintln(stderr, healthcheckFailure)
		return failureExitCode
	}
	_, _ = fmt.Fprintln(stdout, "ok")
	return successExitCode
}

// healthcheckURL vise l'hôte d'écoute ; une écoute sur toutes les interfaces (0.0.0.0, [::], hôte vide) est sondée
// par 127.0.0.1, puisqu'une adresse non spécifiée ne se joint pas.
func healthcheckURL(listenAddr string) (string, error) {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return "", err
	}
	parsedIP := net.ParseIP(host)
	if host == "" || (parsedIP != nil && parsedIP.IsUnspecified()) {
		host = loopbackForWildcard
	}
	return "http://" + net.JoinHostPort(host, port) + healthcheckPath, nil
}

// healthzAnswers suit httpsafe : ni mandataire d'environnement, ni redirection (une redirection n'est pas un 200).
func healthzAnswers(ctx context.Context, probeURL string) bool {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, http.NoBody)
	if err != nil {
		return false
	}
	response, err := httpsafe.NewClient(healthcheckTimeout).Do(request)
	if err != nil {
		return false
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<10))
	return response.StatusCode == http.StatusOK
}
