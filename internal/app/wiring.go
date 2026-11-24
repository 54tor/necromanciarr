package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"golang.org/x/time/rate"

	"necromanciarr/internal/arr"
	"necromanciarr/internal/config"
	"necromanciarr/internal/crossseed"
	"necromanciarr/internal/hashsource"
	"necromanciarr/internal/httpsafe"
	"necromanciarr/internal/libraryprovider"
	"necromanciarr/internal/linker"
	"necromanciarr/internal/pipeline"
	"necromanciarr/internal/qbit"
	"necromanciarr/internal/store"
)

const (
	// readBurstBytes dépasse le plus gros bloc lu d'un coup (64 Kio) : sinon WaitN échoue.
	readBurstBytes = 1 << 20
	// hashSourceTimeout borne une requête vers un fournisseur d'empreintes (rclone rc).
	hashSourceTimeout = 30 * time.Second
	// defaultTorrentTag permet à qui et à cross-seed de cibler nos torrents ; la configuration n'a pas encore de clé tags.
	defaultTorrentTag = "necromanciarr"
	// multipleClientsWarning : seul le premier client torrent configuré reçoit les seeds.
	multipleClientsWarning = "several torrent clients are configured: only the first one is used"
)

// NewFromConfig construit le Service de production : store (config.ResolveDBPath), fournisseur de bibliothèque,
// instances Radarr et Sonarr, premier client torrent, registre d'empreintes, mode et notificateur cross-seed, mapper
// de chemins et limiteur de lecture. Le Closer ferme le store. Une erreur nomme le composant, jamais un secret.
func NewFromConfig(ctx context.Context, settings config.Config) (Service, io.Closer, error) {
	openedStore, err := store.Open(config.ResolveDBPath(settings))
	if err != nil {
		return nil, nil, fmt.Errorf("app: store: %w", err)
	}
	deps, err := buildDeps(ctx, settings, openedStore)
	if err != nil {
		return nil, nil, errors.Join(err, openedStore.Close())
	}
	return New(deps), openedStore, nil
}

func buildDeps(ctx context.Context, settings config.Config, openedStore *store.Store) (Deps, error) {
	mapper := linker.NewMapper(settings.PathRules)
	linkKinds, err := linkKindsOf(settings.LinkType)
	if err != nil {
		return Deps{}, err
	}
	stages := pipeline.Deps{
		Store: openedStore, ScanRoot: pipeline.LibraryScanner(openedStore), LibraryRoots: settings.LibraryRoots,
		ArrSources: arrSources(settings), Mapper: mapper, ClientMapper: mapper,
		ReadLimiter: rate.NewLimiter(rate.Limit(settings.ReadBytesPerSecond), readBurstBytes),
		LinkDirs:    settings.LinkDirs, LinkKinds: linkKinds, Category: settings.LinkCategory,
		Tags: []string{defaultTorrentTag}, SkipRecheck: settings.SkipRecheck,
	}
	if err := wireProvider(settings, &stages); err != nil {
		return Deps{}, err
	}
	deps := Deps{Store: openedStore, Config: settings}
	if err := wireTorrentClient(settings, &stages, &deps); err != nil {
		return Deps{}, err
	}
	if err := wireCrossSeed(ctx, settings, &stages, &deps); err != nil {
		return Deps{}, err
	}
	registry, err := hashsource.NewFromConfig(settings.FSProviders, hashsource.Deps{HTTPClient: httpsafe.NewClient(hashSourceTimeout)})
	if err != nil {
		return Deps{}, fmt.Errorf("app: fs providers: %w", err)
	}
	deps.Stages, deps.HashSources = stages, registry
	return deps, nil
}

// linkKindByName relie linkType (cross-seed : une seule sorte, sans repli implicite) à la sorte du linker.
var linkKindByName = map[string]linker.Kind{"hardlink": linker.Hardlink, "reflink": linker.Reflink, "symlink": linker.Symlink}

func linkKindsOf(linkType string) ([]linker.Kind, error) {
	kind, known := linkKindByName[linkType]
	if !known {
		return nil, fmt.Errorf("app: linkType %q is not a link kind", linkType)
	}
	return []linker.Kind{kind}, nil
}

func arrSources(settings config.Config) []pipeline.ItemsSource {
	var sources []pipeline.ItemsSource
	for _, service := range slices.Concat(settings.Radarr, settings.Sonarr) {
		sources = append(sources, arr.New(service.Kind, service.BaseURL, service.APIKey, arr.WithBasicAuth(service.User, service.Password)))
	}
	return sources
}

// wireProvider : sans libraryApi, Match et Verify refusent avec ErrNotConfigured (les autres étapes restent utilisables).
func wireProvider(settings config.Config, stages *pipeline.Deps) error {
	if settings.LibraryAPI == "" {
		return nil
	}
	provider, err := libraryprovider.NewFromConfig(settings)
	if err != nil {
		return fmt.Errorf("app: library provider: %w", err)
	}
	stages.Provider, stages.Fetcher = provider, provider
	return nil
}

func wireTorrentClient(settings config.Config, stages *pipeline.Deps, deps *Deps) error {
	if len(settings.TorrentClients) == 0 {
		return nil
	}
	client, err := qbit.NewFromConfig(settings.TorrentClients[0])
	if err != nil {
		return fmt.Errorf("app: torrent client: %w", err)
	}
	stages.Client, deps.Client, deps.Maintenance = client, client, client
	return nil
}

// wireCrossSeed résout le mode (démon joignable en TCP, 2 s) ; seul Delegate notifie le démon après un seed.
func wireCrossSeed(ctx context.Context, settings config.Config, stages *pipeline.Deps, deps *Deps) error {
	daemonConfigured := settings.CrossSeed != nil && settings.CrossSeed.BaseURL != ""
	daemonReachable := daemonConfigured && crossseed.Reachable(ctx, settings.CrossSeed.BaseURL, 0)
	behavior, warning, err := crossseed.ResolveMode(settings.CrossSeedMode, daemonConfigured, daemonReachable)
	if err != nil {
		return fmt.Errorf("app: cross-seed mode: %w", err)
	}
	if len(settings.TorrentClients) > 1 {
		warning = joinWarnings(warning, multipleClientsWarning)
	}
	stages.CrossSeed, stages.CrossSeedWarning, stages.Notifier = behavior, warning, crossseed.Noop{}
	deps.CrossSeed, deps.CrossSeedWarning = behavior, warning
	if behavior != crossseed.Delegate {
		return nil
	}
	webhook, err := crossseed.NewWebhook(settings.CrossSeed.BaseURL, settings.CrossSeed.APIKey)
	if err != nil {
		return fmt.Errorf("app: cross-seed webhook: %w", err)
	}
	stages.Notifier = webhook
	return nil
}

func joinWarnings(first, second string) string {
	if first == "" {
		return second
	}
	return first + "; " + second
}
