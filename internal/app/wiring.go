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
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/libraryprovider"
	"necromanciarr/internal/linker"
	"necromanciarr/internal/pipeline"
	"necromanciarr/internal/qbit"
	"necromanciarr/internal/sources"
	"necromanciarr/internal/store"
)

const (
	// readBurstBytes dépasse le plus gros bloc lu d'un coup (64 Kio) : sinon WaitN échoue.
	readBurstBytes = 1 << 20
	// hashSourceTimeout borne une requête vers un fournisseur d'empreintes (rclone rc).
	hashSourceTimeout = 30 * time.Second
)

// warningMultipleClients : seul le premier client torrent configuré reçoit les seeds.
var warningMultipleClients = i18n.Message{Key: i18n.KeyWarningMultipleClients}

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
	if err := clearSealedAtStart(ctx, openedStore, settings); err != nil {
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
		ReadBudget:  settings.TieBreakReadBytes, PartialMaxMissingPercent: &settings.PartialMaxMissingPercent,
		LinkDirs: settings.LinkDirs, LinkKinds: linkKinds, Category: settings.LinkCategory,
		Tags: settings.SeedTags, SkipRecheck: settings.SkipRecheck,
	}
	if err := wireProvider(settings, &stages); err != nil {
		return Deps{}, err
	}
	if err := wireSealer(settings, &stages); err != nil {
		return Deps{}, err
	}
	deps := Deps{Store: openedStore, Config: settings, Translator: i18n.New(settings.Language, func(text string) string {
		return config.RedactText(text, settings.KnownSecrets()...)
	})}
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
	deps.DoctorDeps.SealedProbe = openStoreProbe{store: openedStore}
	return deps, nil
}

// wireSealer pose le déscellement des .torrent d'indexeur quand NECROMANCIARR_SECRET_KEY est définie ; sans elle, Seed
// refuse un candidat scellé (secret_key_missing). L'annonce (Deps.Announces) est posée par RunStage, depuis la
// configuration courante.
func wireSealer(settings config.Config, stages *pipeline.Deps) error {
	if len(settings.SecretKey) == 0 {
		return nil
	}
	sealer, err := sources.NewSealer(settings.SecretKey)
	if err != nil {
		return fmt.Errorf("app: secret key: %w", err)
	}
	stages.Sealer = sealer
	return nil
}

// clearSealedAtStart efface, quand le service s'ouvre, les .torrent scellés qu'une entrée trackers couvre désormais.
func clearSealedAtStart(ctx context.Context, openedStore *store.Store, settings config.Config) error {
	candidates, err := openedStore.ListCandidatesByState(ctx)
	if err != nil {
		return fmt.Errorf("app: sealed torrents: %w", err)
	}
	if err := clearCoveredSealed(ctx, openedStore, sources.NewResolver(settings.Trackers), candidates); err != nil {
		return fmt.Errorf("app: sealed torrents: %w", err)
	}
	return nil
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

// wireCrossSeed pose le résolveur du mode cross-seed (démon joignable en TCP, 2 s, résultat gardé 30 s) et fait une
// première résolution, pour que Deps porte un mode même sans appel. Seul Delegate notifie le démon après un seed.
func wireCrossSeed(ctx context.Context, settings config.Config, stages *pipeline.Deps, deps *Deps) error {
	if _, _, err := crossseed.ResolveMode(settings.CrossSeedMode, false, false); err != nil {
		return fmt.Errorf("app: cross-seed mode: %w", err)
	}
	var webhook crossseed.Notifier
	daemonConfigured := settings.CrossSeed != nil && settings.CrossSeed.BaseURL != ""
	mayDelegate := settings.CrossSeedMode == crossseed.ModeAuto || settings.CrossSeedMode == crossseed.ModeDelegate
	if daemonConfigured && mayDelegate {
		built, err := crossseed.NewWebhook(settings.CrossSeed.BaseURL, settings.CrossSeed.APIKey)
		if err != nil {
			return fmt.Errorf("app: cross-seed webhook: %w", err)
		}
		webhook = built
	}
	reachable := func(probeCtx context.Context, baseURL string) bool { return crossseed.Reachable(probeCtx, baseURL, 0) }
	resolver := newCrossSeedResolver(settings, webhook, reachable, time.Now)
	initial := resolver.resolve(ctx)
	stages.CrossSeed, stages.CrossSeedWarnings, stages.Notifier = initial.Behavior, initial.Warnings, initial.Notifier
	deps.CrossSeed, deps.CrossSeedWarnings, deps.ResolveCrossSeed = initial.Behavior, initial.Warnings, resolver.resolve
	return nil
}
