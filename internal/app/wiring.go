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
	"necromanciarr/internal/torznab"
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
	if err := wireSealer(settings, &stages); err != nil {
		return Deps{}, err
	}
	if err := wireSources(settings, &stages, openedStore); err != nil {
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

// wireSources bâtit la chaîne de sources : le fournisseur (libraryApi) d'abord, puis les indexeurs Torznab dans l'ordre
// de sourceOrder, un seul client par indexeur pour tout le processus. Sans libraryApi il n'y a pas de chaîne : Match et
// Verify refusent avec ErrNotConfigured (les autres étapes restent utilisables), même si des indexeurs sont configurés.
func wireSources(settings config.Config, stages *pipeline.Deps, usage sources.UsageStore) error {
	if settings.LibraryAPI == "" {
		return nil
	}
	provider, err := libraryprovider.NewFromConfig(settings)
	if err != nil {
		return fmt.Errorf("app: library provider: %w", err)
	}
	order, indexers, limits, err := indexerSources(settings)
	if err != nil {
		return err
	}
	chain, err := sources.NewChain(provider, indexers, order, limits, usage, stages.Sealer, time.Now)
	if err != nil {
		return fmt.Errorf("app: source chain: %w", err)
	}
	chain.SetFallbackTrackers(indexerTrackers(settings))
	chain.SetSearchCategories(indexerCategories(settings))
	if searchStore, hasStore := usage.(sources.SearchStore); hasStore {
		chain.SetSearchStore(searchStore)
	}
	stages.Sources = chain
	return nil
}

// sourceOrderOf rend l'ordre de la chaîne : sourceOrder, ou à défaut le fournisseur puis tous les indexeurs dans
// l'ordre de la configuration.
func sourceOrderOf(settings config.Config) []sources.ID {
	order := []sources.ID{sources.ProviderID}
	if len(settings.SourceOrder) == 0 {
		for _, indexer := range settings.Torznab {
			order = append(order, indexerID(indexer))
		}
		return order
	}
	order = order[:0]
	for _, name := range settings.SourceOrder {
		order = append(order, sources.ID(name))
	}
	return order
}

// indexerSources construit les clients Torznab cités par l'ordre de la chaîne (sourceOrderOf) avec leurs limites.
func indexerSources(settings config.Config) ([]sources.ID, map[sources.ID]sources.IndexerSource, map[sources.ID]sources.Limits, error) {
	order := sourceOrderOf(settings)
	indexers, limits := map[sources.ID]sources.IndexerSource{}, map[sources.ID]sources.Limits{}
	for _, indexer := range settings.Torznab {
		identifier := sources.ID(config.SourceTorznabPrefix + indexer.Name)
		if !slices.Contains(order, identifier) {
			continue
		}
		client, err := torznab.New(torznab.Endpoint{Name: indexer.Name, URL: indexer.URL, MinInterval: indexer.MinInterval, Timeout: indexer.Timeout}, torznab.Deps{})
		if err != nil {
			return nil, nil, nil, fmt.Errorf("app: torznab indexer %q: %w", indexer.Name, err)
		}
		indexers[identifier] = client
		limits[identifier] = sources.Limits{
			MinInterval: indexer.MinInterval, MaxQueriesPerRun: indexer.MaxQueriesPerRun, MaxDownloadsPerRun: indexer.MaxDownloadsPerRun,
			MaxQueriesPerDay: indexer.MaxQueriesPerDay, MaxDownloadsPerDay: indexer.MaxDownloadsPerDay,
		}
	}
	return order, indexers, limits, nil
}

// indexerTrackers relie chaque indexeur à son tracker déclaré (torznab[].tracker) : seul ce lien ouvre le secours du
// .torrent dans Verify.
func indexerTrackers(settings config.Config) map[sources.ID]string {
	trackers := map[sources.ID]string{}
	for _, indexer := range settings.Torznab {
		if indexer.Tracker != "" {
			trackers[sources.ID(config.SourceTorznabPrefix+indexer.Name)] = indexer.Tracker
		}
	}
	return trackers
}

// indexerCategories relie chaque indexeur à ses catégories configurées (torznab[].categories) ; vide : celles de ses
// capacités, selon le genre du média.
func indexerCategories(settings config.Config) map[sources.ID][]int {
	categories := map[sources.ID][]int{}
	for _, indexer := range settings.Torznab {
		if len(indexer.Categories) > 0 {
			categories[sources.ID(config.SourceTorznabPrefix+indexer.Name)] = indexer.Categories
		}
	}
	return categories
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
