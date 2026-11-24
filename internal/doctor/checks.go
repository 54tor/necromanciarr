package doctor

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"necromanciarr/internal/config"
	"necromanciarr/internal/crossseed"
	"necromanciarr/internal/hashsource"
	"necromanciarr/internal/libraryprovider"
	"necromanciarr/internal/linker"
	"necromanciarr/internal/qbit"
	"necromanciarr/internal/store"
)

// Identifiants stables des contrôles ; ceux des instances Radarr et Sonarr sont « arr:<kind>:<rang> ».
const (
	IDConfig        = "config"
	IDConfigDir     = "config-dir"
	IDAuth          = "auth"
	IDLibraryRoots  = "library-roots"
	IDLibraryAPI    = "library-api"
	IDQbittorrent   = "qbittorrent"
	IDLinks         = "link-feasibility"
	IDFSProviders   = "fs-providers"
	IDCrossSeed     = "cross-seed"
	IDPathMappings  = "path-mappings"
	IDDedupe        = "dedupe"
	separatorDetail = "; "
)

// Pinger vérifie qu'un service répond et accepte ses identifiants.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Searcher interroge le library provider.
type Searcher interface {
	Search(ctx context.Context, query libraryprovider.Query) ([]libraryprovider.Release, error)
}

// TorrentClient est la partie d'un client torrent que le diagnostic utilise.
type TorrentClient interface {
	Version(ctx context.Context) (string, error)
	Torrents(ctx context.Context) ([]qbit.TorrentInfo, error)
}

// Deps permet de remplacer chaque accès extérieur ; un champ nil prend la valeur de production.
type Deps struct {
	NewSearcher      func(settings config.Config) (Searcher, error)
	NewPinger        func(service config.ArrService) Pinger
	NewTorrentClient func(client config.TorrentClient) (TorrentClient, error)
	OpenStore        func(path string) (io.Closer, error)
	Reachable        func(ctx context.Context, baseURL string) bool
	Probe            func(directory string) (linker.Capabilities, error)
	FilesystemKind   func(path string) (linker.FilesystemKind, error)
	SameVolume       func(firstPath, secondPath string) (bool, error)
	DescribeMounts   func(ctx context.Context, providers []config.FSProvider) ([]hashsource.ProviderMountReport, error)
}

func (deps Deps) withDefaults() Deps {
	if deps.NewSearcher == nil {
		deps.NewSearcher = func(settings config.Config) (Searcher, error) { return libraryprovider.NewFromConfig(settings) }
	}
	if deps.NewPinger == nil {
		deps.NewPinger = newArrPinger
	}
	if deps.NewTorrentClient == nil {
		deps.NewTorrentClient = func(client config.TorrentClient) (TorrentClient, error) { return qbit.NewFromConfig(client) }
	}
	if deps.OpenStore == nil {
		deps.OpenStore = func(path string) (io.Closer, error) { return store.Open(path) }
	}
	if deps.Reachable == nil {
		deps.Reachable = func(ctx context.Context, baseURL string) bool { return crossseed.Reachable(ctx, baseURL, 0) }
	}
	if deps.Probe == nil {
		deps.Probe = linker.Probe
	}
	if deps.FilesystemKind == nil {
		deps.FilesystemKind = linker.FilesystemKindOf
	}
	if deps.SameVolume == nil {
		deps.SameVolume = sameVolume
	}
	if deps.DescribeMounts == nil {
		deps.DescribeMounts = describeMounts
	}
	return deps
}

// Build prépare les contrôles de la configuration, dans l'ordre d'exécution. Chaque contrôle masque d'abord les
// secrets connus de la configuration, si bien qu'aucun appelant ne peut oublier la redaction.
func Build(settings config.Config, deps Deps) []Check {
	deps = deps.withDefaults()
	checks := []Check{
		{IDConfig, "Configuration", func(context.Context) Result { return checkConfig(settings) }},
		{IDConfigDir, "Configuration directory and database", func(context.Context) Result { return checkConfigDir(settings, deps) }},
		{IDAuth, "Authentication", func(context.Context) Result { return checkAuth(settings) }},
		{IDLibraryRoots, "Library roots", func(ctx context.Context) Result { return checkLibraryRoots(ctx, settings) }},
		{IDLibraryAPI, "Library provider API", func(ctx context.Context) Result { return checkLibraryAPI(ctx, settings, deps) }},
	}
	checks = append(checks, arrChecks(settings, deps)...)
	checks = append(checks,
		Check{IDQbittorrent, "qBittorrent", func(ctx context.Context) Result { return checkQbittorrent(ctx, settings, deps) }},
		Check{IDLinks, "Link feasibility", func(context.Context) Result { return checkLinkFeasibility(settings, deps) }},
		Check{IDFSProviders, "Filesystem providers", func(ctx context.Context) Result { return checkFSProviders(ctx, settings, deps) }},
		Check{IDCrossSeed, "cross-seed", func(ctx context.Context) Result { return checkCrossSeed(ctx, settings, deps) }},
		Check{IDPathMappings, "Path mappings", func(context.Context) Result { return checkPathMappings(settings) }},
		Check{IDDedupe, "Deduplication roots", func(context.Context) Result { return checkDedupe(settings) }},
	)
	secrets := settings.KnownSecrets()
	for index := range checks {
		checks[index].Run = redactingRun(checks[index].Run, secrets)
	}
	return checks
}

func redactingRun(run func(context.Context) Result, secrets []string) func(context.Context) Result {
	return func(ctx context.Context) (result Result) {
		// La panique est récupérée ici pour que les secrets connus soient masqués dans son message.
		defer func() {
			if recovered := recover(); recovered != nil {
				result = panicResult(recovered, func(text string) string { return config.RedactText(text, secrets...) })
			}
		}()
		result = run(ctx)
		result.Detail, result.Hint = config.RedactText(result.Detail, secrets...), config.RedactText(result.Hint, secrets...)
		return result
	}
}

// findings rassemble les constats d'un contrôle qui examine plusieurs éléments ; le statut est le pire constaté.
type findings struct {
	status Status
	details,
	hints []string
}

func (collected *findings) add(status Status, detail, hint string) {
	if status.severity() > collected.status.severity() {
		collected.status = status
	}
	collected.details = append(collected.details, detail)
	if hint != "" {
		collected.hints = append(collected.hints, hint)
	}
}

func (collected *findings) result() Result {
	return Result{
		Status: collected.status, Detail: strings.Join(collected.details, separatorDetail),
		Hint: strings.Join(collected.hints, separatorDetail),
	}
}

func checkConfig(settings config.Config) Result {
	sources := settings.Sources
	detail := fmt.Sprintf("loaded from %s; sources: radarr=%s, sonarr=%s, torrentClients=%s, linkDirs=%s",
		settings.ConfigDir, sources.Radarr, sources.Sonarr, sources.TorrentClients, sources.LinkDirs)
	if settings.WorkingDirDotEnv {
		return Result{Status: Warn, Detail: detail + "; variables read from the .env of the working directory",
			Hint: "move that .env into the configuration directory, or delete it if it belongs to another tool"}
	}
	return Result{Status: OK, Detail: detail}
}

func checkConfigDir(settings config.Config, deps Deps) Result {
	if !isDirectory(settings.ConfigDir) {
		return Result{
			Status: Fail, Detail: "configuration directory not found: " + settings.ConfigDir,
			Hint: "create it or pass --config-dir",
		}
	}
	databasePath := config.ResolveDBPath(settings)
	if refusal, refused := refuseDatabasePath(databasePath, settings.LibraryRoots); refused {
		return refusal
	}
	database, err := deps.OpenStore(databasePath)
	if err != nil {
		return Result{
			Status: Fail, Detail: "database cannot be opened read-write: " + err.Error(),
			Hint: "check that " + filepath.Dir(databasePath) + " is writable and has free space",
		}
	}
	_ = database.Close()
	return Result{Status: OK, Detail: "database opens read-write: " + databasePath}
}

// refuseDatabasePath refuse d'ouvrir la base si son dossier ne se résout pas ou si, liens symboliques suivis (dossier
// et fichier lui-même), elle tombe dans une racine de bibliothèque.
func refuseDatabasePath(databasePath string, libraryRoots []string) (Result, bool) {
	resolvedDirectory, err := filepath.EvalSymlinks(filepath.Dir(databasePath))
	if err != nil {
		return Result{
			Status: Fail, Detail: "le dossier de la base ne peut pas être résolu : " + filepath.Dir(databasePath),
			Hint: "créer le dossier ou corriger dbPath",
		}, true
	}
	directories := []string{resolvedDirectory}
	if resolvedFile, fileErr := filepath.EvalSymlinks(databasePath); fileErr == nil {
		directories = append(directories, filepath.Dir(resolvedFile))
	}
	for _, directory := range directories {
		if root, inside := insideLibraryRoot(directory, libraryRoots); inside {
			return Result{
				Status: Fail, Detail: "la base de données est dans la racine de bibliothèque " + root,
				Hint: "placer dbPath hors de la bibliothèque : elle n'est jamais écrite",
			}, true
		}
	}
	return Result{}, false
}

func checkAuth(settings config.Config) Result {
	if config.IsLoopbackAddress(settings.ListenAddr) {
		return Result{Status: OK, Detail: "listening on loopback: " + settings.ListenAddr}
	}
	if settings.AuthToken == "" {
		return Result{
			Status: Fail, Detail: "listening on " + settings.ListenAddr + " without an auth token",
			Hint: "set NECROMANCIARR_AUTH_TOKEN or listen on 127.0.0.1",
		}
	}
	return Result{Status: OK, Detail: "listening on " + settings.ListenAddr + " with an auth token"}
}

func checkPathMappings(settings config.Config) Result {
	if len(settings.PathRules) == 0 {
		return Result{Status: Skipped, Detail: "no path mapping configured"}
	}
	var collected findings
	for ruleIndex, rule := range settings.PathRules {
		label := fmt.Sprintf("pathMappings[%d]", ruleIndex)
		if !pathExists(rule.From) && !pathExists(rule.To) {
			collected.add(Warn, label+": neither from nor to exists on this machine",
				"fix the rule, or ignore it if both sides only exist in other containers")
			continue
		}
		if len(settings.LibraryRoots) > 0 && !coversAnyRoot(rule, settings.LibraryRoots) {
			collected.add(Warn, label+": covers no library root", "check from/to against libraryRoots")
			continue
		}
		collected.add(OK, label+": ok", "")
	}
	return collected.result()
}

func coversAnyRoot(rule config.PathRule, roots []string) bool {
	for _, root := range roots {
		if config.PathsOverlap(rule.From, root) || config.PathsOverlap(rule.To, root) {
			return true
		}
	}
	return false
}

func checkDedupe(settings config.Config) Result {
	if len(settings.Dedupe.TorrentRoots) == 0 {
		return Result{Status: Skipped, Detail: "dedupe.torrentRoots is empty: deduplication is inactive"}
	}
	var collected findings
	for rootIndex, torrentRoot := range settings.Dedupe.TorrentRoots {
		label := fmt.Sprintf("dedupe.torrentRoots[%d]", rootIndex)
		if overlapped, found := overlappingTorrentRoot(torrentRoot, settings.LibraryRoots); found {
			collected.add(Fail, fmt.Sprintf("%s overlaps library root %s", label, overlapped),
				"torrent roots must be outside the library, or links would replace library files")
			continue
		}
		if !isDirectory(torrentRoot) {
			collected.add(Warn, label+": directory not found on this machine", "")
			continue
		}
		collected.add(OK, label+": ok", "")
	}
	return collected.result()
}

func overlappingRoot(candidate string, roots []string) (string, bool) {
	for _, root := range roots {
		if config.PathsOverlap(candidate, root) {
			return root, true
		}
	}
	return "", false
}

// overlappingTorrentRoot cumule la comparaison lexicale et celle des chemins résolus : un lien symbolique ne doit pas
// ranger une racine torrent dans la bibliothèque sans que le contrôle le voie.
func overlappingTorrentRoot(torrentRoot string, libraryRoots []string) (string, bool) {
	if root, found := overlappingRoot(torrentRoot, libraryRoots); found {
		return root, true
	}
	return insideLibraryRoot(torrentRoot, libraryRoots)
}
