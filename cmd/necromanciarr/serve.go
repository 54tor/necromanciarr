package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"necromanciarr/internal/config"
	"necromanciarr/internal/httpsafe"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/web"
)

const (
	// healthcheckTimeout borne la sonde du conteneur (HEALTHCHECK --timeout=5s laisse de la marge).
	healthcheckTimeout  = 3 * time.Second
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
	if exitCode, failed := out.bootstrapSecrets(args); failed {
		return exitCode
	}
	settings, err := loadConfig(args)
	if err != nil {
		return out.failure(err)
	}
	out.useSettings(settings)
	if err := config.ValidateServe(settings); err != nil {
		return out.failure(err)
	}
	service, closer, err := openService(ctx, settings, storedConfigLoader(args))
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
	web.LogLine(ctx, web.NewLogger(stdout), out.translator, slog.LevelInfo, web.LogCodeServerListening, i18n.KeyLogServerListening,
		map[string]string{"url": "http://" + listener.Addr().String()}, out.secrets...)
	errorLog := web.NewLogger(stderr)
	if !config.IsLoopbackAddress(settings.ListenAddr) {
		web.LogLine(ctx, errorLog, out.translator, slog.LevelWarn, web.LogCodeNonLoopbackWarning, i18n.KeyLogNonLoopbackWarning, nil)
	}
	handler := web.NewHandler(ctx, service, health, settings)
	handler.UseLogger(errorLog)
	if err := web.Run(ctx, listener, handler); err != nil {
		return out.failure(err)
	}
	return successExitCode
}

// bootstrapSecrets génère les secrets absents au premier lancement ; le jeton neuf n'est écrit que sur stderr, une fois,
// hors de web.LogLine dont le masquage l'effacerait.
func (out printer) bootstrapSecrets(args []string) (int, bool) {
	result, err := config.EnsureBootstrap(args, lookupEnv, defaultConfigDir())
	if notWritable, isNotWritable := errors.AsType[*config.NotWritableError](err); isNotWritable {
		_, _ = fmt.Fprintln(out.stderr, "error:", out.translator.Text(i18n.KeyCLIBootstrapNotWritable, textParams("dir", notWritable.Dir)))
		return failureExitCode, true
	}
	if errors.Is(err, config.ErrBootstrapWouldOrphanSecrets) {
		_, _ = fmt.Fprintln(out.stderr, "error:", out.translator.Text(i18n.KeyCLIBootstrapOrphanSecrets, nil))
		return failureExitCode, true
	}
	if err != nil {
		return 0, false
	}
	if len(result.Created) > 0 {
		_, _ = fmt.Fprintln(out.stderr, out.translator.Text(i18n.KeyCLIBootstrapCreated, textParams("names", strings.Join(result.Created, ", "))))
	}
	if result.AuthToken != "" {
		_, _ = fmt.Fprintln(out.stderr, out.translator.Text(i18n.KeyCLIBootstrapToken, textParams("token", result.AuthToken)))
	}
	return 0, false
}

// runHealthcheck interroge /healthz sur la boucle locale ; code 0 seulement sur un 200. Aucun détail n'est affiché
// (la sortie d'une sonde Docker est visible par docker inspect).
func runHealthcheck(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	out := newPrinter(stdout, stderr, args)
	settings, err := loadConfig(args)
	if err != nil {
		return out.healthcheckFailed(ctx)
	}
	out.useSettings(settings)
	probeURL, err := healthcheckURL(settings.ListenAddr)
	if err != nil || !healthzAnswers(ctx, probeURL) {
		return out.healthcheckFailed(ctx)
	}
	_, _ = fmt.Fprintln(stdout, out.translator.Text(i18n.KeyCLIHealthcheckOK, nil))
	return successExitCode
}

// healthcheckFailed écrit l'unique ligne d'échec, sans détail, avec son code stable.
func (out printer) healthcheckFailed(ctx context.Context) int {
	web.LogLine(ctx, web.NewLogger(out.stderr), out.translator, slog.LevelError, web.LogCodeHealthcheckFailed, i18n.KeyLogHealthcheckFailed, nil)
	return failureExitCode
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
