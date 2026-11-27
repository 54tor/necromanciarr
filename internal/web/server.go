// Package web sert l'API JSON de revue (/api), la sonde /healthz et l'interface embarquée. Chaque action passe par
// app.Service : le web n'appelle jamais le store ni le pipeline directement.
package web

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"necromanciarr/internal/app"
	"necromanciarr/internal/config"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/web/ui"
)

// Bornes du serveur HTTP : les actions longues tournent en job, aucune requête n'a donc besoin de plus de 30 s de
// lecture. Pas de WriteTimeout : le diagnostic synchrone attend ses contrôles (chacun borné par son propre délai).
const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second
	idleTimeout       = 120 * time.Second
	maxHeaderBytes    = 16 << 10
	shutdownTimeout   = 10 * time.Second
	healthTimeout     = 2 * time.Second
)

// HealthChecker répond à la sonde /healthz ; *store.Store convient (Ping de la base SQLite).
type HealthChecker interface {
	Ping(ctx context.Context) error
}

// Handler est le handler HTTP complet : API protégée, sonde et interface. Wait attend la fin des jobs en cours.
type Handler struct {
	mux        *http.ServeMux
	service    app.Service
	health     HealthChecker
	settings   config.Config
	translator i18n.Translator
	jobs       *jobRunner
	logger     *slog.Logger
	hosts      allowedHosts
	// preAuth garde les seconds facteurs en attente (mot de passe vérifié), passkeyPreAuth les connexions par clé
	// d'accès (publiques, sans limite : une avalanche n'y évince que d'autres clés d'accès) ; ceremonies les états
	// d'enregistrement par session.
	preAuth        *heldStates
	passkeyPreAuth *heldStates
	ceremonies     *heldStates
	now            func() time.Time
}

// NewHandler sert l'interface embarquée (ui.Assets) ; ctx est le contexte du serveur : son annulation arrête les jobs.
func NewHandler(ctx context.Context, service app.Service, health HealthChecker, settings config.Config) *Handler {
	// fs.Sub n'échoue que sur un nom invalide, et « dist » est valide.
	dist, _ := fs.Sub(ui.Assets, "dist")
	return NewHandlerWithAssets(ctx, service, health, settings, dist)
}

// NewHandlerWithAssets sert assets (racine = contenu de dist) à la place de l'interface embarquée ; sert aux tests.
func NewHandlerWithAssets(ctx context.Context, service app.Service, health HealthChecker, settings config.Config, assets fs.FS) *Handler {
	handler := &Handler{
		mux: http.NewServeMux(), service: service, health: health, settings: settings,
		translator: service.Translator(), hosts: hostsFor(settings.ListenAddr, settings.PublicURL), logger: NewLogger(os.Stderr),
		preAuth: newHeldStates(preAuthLifetime, time.Now), passkeyPreAuth: newHeldStates(preAuthLifetime, time.Now), ceremonies: newHeldStates(ceremonyLifetime, time.Now), now: time.Now,
	}
	handler.jobs = newJobRunner(ctx, handler.describe, handler.logLine)
	handler.mux.HandleFunc("GET /healthz", handler.serveHealth)
	for _, apiRoute := range coreRoutes {
		handler.mux.Handle(apiRoute.pattern(), handler.protect(accessProtected, handler.endpoint(apiRoute.handle)))
	}
	for _, authRoute := range authRoutes {
		handler.mux.Handle(authRoute.pattern(), handler.protect(authRoute.access, handler.authEndpoint(authRoute.handle)))
	}
	for path, methods := range allowedMethods() {
		handler.mux.Handle(path, handler.protect(accessProtected, handler.endpoint(methodNotAllowed(methods))))
	}
	handler.mux.Handle("/api/", handler.protect(accessProtected, handler.endpoint(unknownRoute)))
	handler.mux.Handle("/", assetsHandler(assets, handler.translator))
	return handler
}

// ServeHTTP pose les en-têtes de sécurité sur chaque réponse, puis route la requête.
func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	setSecurityHeaders(writer.Header())
	handler.mux.ServeHTTP(writer, request)
}

// Wait attend la fin des jobs lancés (ils s'arrêtent quand le contexte du serveur est annulé).
func (handler *Handler) Wait() { handler.jobs.wait() }

// serveHealth ne renvoie que « ok » ou « unavailable » : ni configuration, ni version, ni texte d'erreur.
func (handler *Handler) serveHealth(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), healthTimeout)
	defer cancel()
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	if handler.health == nil || handler.health.Ping(ctx) != nil {
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte("unavailable"))
		return
	}
	_, _ = writer.Write([]byte("ok"))
}

// NewServer règle les délais et la taille des en-têtes du serveur ; ctx devient le contexte de base des requêtes.
func NewServer(ctx context.Context, handler http.Handler) *http.Server {
	server := &http.Server{
		Handler: handler, ReadHeaderTimeout: readHeaderTimeout, ReadTimeout: readTimeout, IdleTimeout: idleTimeout,
		MaxHeaderBytes: maxHeaderBytes, BaseContext: func(net.Listener) context.Context { return ctx },
	}
	if coded, isHandler := handler.(*Handler); isHandler {
		server.ErrorLog = log.New(serverErrorWriter{handler: coded}, "", 0)
	}
	return server
}

// Run sert handler sur listener jusqu'à l'annulation de ctx, puis arrête le serveur proprement (requêtes en cours
// terminées sous 10 s) et attend la fin des jobs. Hors boucle locale sans jeton, il refuse sans servir.
func Run(ctx context.Context, listener net.Listener, handler *Handler) error {
	if err := config.ValidateServe(handler.settings); err != nil {
		return err
	}
	server := NewServer(ctx, handler)
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	var serveErr error
	select {
	case serveErr = <-served:
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancel()
		serveErr = server.Shutdown(shutdownContext)
	}
	handler.Wait()
	if errors.Is(serveErr, http.ErrServerClosed) {
		return nil
	}
	return serveErr
}
