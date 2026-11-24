package app

import (
	"context"
	"sync"
	"time"

	"necromanciarr/internal/config"
	"necromanciarr/internal/crossseed"
	"necromanciarr/internal/i18n"
)

// crossSeedResolutionTTL : un serve qui tourne des jours suit l'arrêt et le retour du démon cross-seed, sans sonder
// à chaque appel.
const crossSeedResolutionTTL = 30 * time.Second

// crossSeedProbeTimeout borne la sonde TCP, indépendante du contexte de l'appelant.
const crossSeedProbeTimeout = 2 * time.Second

// CrossSeedResolution est le mode cross-seed effectif à un instant : comportement, avertissement éventuel et
// notificateur (le webhook en Delegate, sinon aucun).
type CrossSeedResolution struct {
	Behavior crossseed.Behavior
	Warnings []i18n.Message
	Notifier crossseed.Notifier
}

// crossSeedResolver résout le mode (sonde TCP de 2 s) et garde le résultat crossSeedResolutionTTL.
type crossSeedResolver struct {
	settings  config.Config
	webhook   crossseed.Notifier
	reachable func(ctx context.Context, baseURL string) bool
	now       func() time.Time
	mutex     sync.Mutex
	cached    CrossSeedResolution
	expiresAt time.Time
}

func newCrossSeedResolver(settings config.Config, webhook crossseed.Notifier, reachable func(context.Context, string) bool, now func() time.Time) *crossSeedResolver {
	return &crossSeedResolver{settings: settings, webhook: webhook, reachable: reachable, now: now}
}

// resolve renvoie la résolution en cache, ou sonde le démon une fois le délai passé ; la sonde se fait sous le
// verrou pour que des appels simultanés n'en lancent qu'une. Un appelant déjà annulé ne fausse pas la sonde, mais son
// résultat n'est pas gardé.
func (resolver *crossSeedResolver) resolve(ctx context.Context) CrossSeedResolution {
	resolver.mutex.Lock()
	defer resolver.mutex.Unlock()
	if resolver.now().Before(resolver.expiresAt) {
		return resolver.cached
	}
	probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), crossSeedProbeTimeout)
	defer cancel()
	resolution := resolver.probe(probeCtx)
	if ctx.Err() != nil {
		return resolution
	}
	resolver.cached, resolver.expiresAt = resolution, resolver.now().Add(crossSeedResolutionTTL)
	return resolution
}

func (resolver *crossSeedResolver) probe(ctx context.Context) CrossSeedResolution {
	settings := resolver.settings
	daemonConfigured := settings.CrossSeed != nil && settings.CrossSeed.BaseURL != ""
	daemonReachable := daemonConfigured && resolver.reachable(ctx, settings.CrossSeed.BaseURL)
	// Le mode a été validé au chargement de la configuration : ResolveMode ne peut plus refuser.
	behavior, warning, _ := crossseed.ResolveMode(settings.CrossSeedMode, daemonConfigured, daemonReachable)
	var warnings []i18n.Message
	if warning.Key != "" {
		warnings = append(warnings, warning)
	}
	if len(settings.TorrentClients) > 1 {
		warnings = append(warnings, warningMultipleClients)
	}
	resolution := CrossSeedResolution{Behavior: behavior, Warnings: warnings, Notifier: crossseed.Noop{}}
	if behavior == crossseed.Delegate && resolver.webhook != nil {
		resolution.Notifier = resolver.webhook
	}
	return resolution
}

// crossSeed donne la résolution courante : celle du résolveur de production, sinon les valeurs fixes de Deps.
func (svc *service) crossSeed(ctx context.Context) CrossSeedResolution {
	if svc.deps.ResolveCrossSeed != nil {
		return svc.deps.ResolveCrossSeed(ctx)
	}
	return CrossSeedResolution{Behavior: svc.deps.CrossSeed, Warnings: svc.deps.CrossSeedWarnings, Notifier: svc.deps.Stages.Notifier}
}
