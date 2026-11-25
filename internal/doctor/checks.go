package doctor

import (
	"context"
	"io"
	"path/filepath"
	"strconv"

	"necromanciarr/internal/config"
	"necromanciarr/internal/crossseed"
	"necromanciarr/internal/hashsource"
	"necromanciarr/internal/i18n"
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
	// CountTorrents lit une seule page : atLeast dit que le client en a au moins count.
	CountTorrents(ctx context.Context) (count int, atLeast bool, err error)
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
	// NewCapsReader crée le lecteur de capacités d'un indexeur Torznab ; NewIndexerLister celui de la liste des
	// indexeurs de Prowlarr (URL de l'API native, clé comprise).
	NewCapsReader    func(indexer config.TorznabIndexer) (CapsReader, error)
	NewIndexerLister func(rawURL string) (IndexerLister, error)
	// CapsCache réutilise le résultat des capacités d'un indexeur (nil : une requête à chaque diagnostic).
	CapsCache *CapsCache
	// SealedProbe lit les .torrent scellés en base pour tenter de les desceller (nil : contrôle de la clé seul).
	SealedProbe SealedProbe
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
	if deps.FilesystemKind == nil {
		deps.FilesystemKind = linker.FilesystemKindOf
	}
	if deps.SameVolume == nil {
		deps.SameVolume = sameVolume
	}
	if deps.NewCapsReader == nil {
		deps.NewCapsReader = newTorznabCapsReader
	}
	if deps.NewIndexerLister == nil {
		deps.NewIndexerLister = newProwlarrLister
	}
	if deps.DescribeMounts == nil {
		deps.DescribeMounts = describeMounts
	}
	return deps
}

// message construit un message à paramètres nommés (paires nom, valeur).
func message(key i18n.Key, nameValuePairs ...string) i18n.Message {
	params := make(map[string]string, len(nameValuePairs)/2)
	for pairIndex := 0; pairIndex+1 < len(nameValuePairs); pairIndex += 2 {
		params[nameValuePairs[pairIndex]] = nameValuePairs[pairIndex+1]
	}
	return i18n.Message{Key: key, Params: params}
}

func newCheck(id string, titleKey i18n.Key, run func(context.Context) Result) Check {
	return Check{ID: id, TitleKey: titleKey, Run: run}
}

// Build prépare les contrôles de la configuration, dans l'ordre d'exécution. Chaque contrôle masque d'abord les
// secrets connus de la configuration, si bien qu'aucun appelant ne peut oublier la redaction.
func Build(settings config.Config, deps Deps) []Check {
	deps = deps.withDefaults()
	checks := []Check{
		newCheck(IDConfig, i18n.KeyDoctorTitleConfig, func(context.Context) Result { return checkConfig(settings) }),
		newCheck(IDConfigDir, i18n.KeyDoctorTitleConfigDir, func(context.Context) Result { return checkConfigDir(settings, deps) }),
		newCheck(IDAuth, i18n.KeyDoctorTitleAuth, func(context.Context) Result { return checkAuth(settings) }),
		newCheck(IDLibraryRoots, i18n.KeyDoctorTitleLibraryRoots, func(ctx context.Context) Result { return checkLibraryRoots(ctx, settings) }),
		newCheck(IDLibraryAPI, i18n.KeyDoctorTitleLibraryAPI, func(ctx context.Context) Result { return checkLibraryAPI(ctx, settings, deps) }),
	}
	checks = append(checks, arrChecks(settings, deps)...)
	checks = append(checks, sourceChecks(settings, deps)...)
	checks = append(checks, secretKeyChecks(settings, deps)...)
	checks = append(checks,
		newCheck(IDQbittorrent, i18n.KeyDoctorTitleQbittorrent, func(ctx context.Context) Result { return checkQbittorrent(ctx, settings, deps) }),
		newCheck(IDLinks, i18n.KeyDoctorTitleLinks, func(context.Context) Result { return checkLinkFeasibility(settings, deps) }),
		newCheck(IDFSProviders, i18n.KeyDoctorTitleFSProviders, func(ctx context.Context) Result { return checkFSProviders(ctx, settings, deps) }),
		newCheck(IDCrossSeed, i18n.KeyDoctorTitleCrossSeed, func(ctx context.Context) Result { return checkCrossSeed(ctx, settings, deps) }),
		newCheck(IDPathMappings, i18n.KeyDoctorTitlePathMappings, func(context.Context) Result { return checkPathMappings(settings) }),
		newCheck(IDDedupe, i18n.KeyDoctorTitleDedupe, func(context.Context) Result { return checkDedupe(settings) }),
	)
	secrets := settings.KnownSecrets()
	for index := range checks {
		checks[index].Run = redactingRun(checks[index].Run, secrets)
	}
	return checks
}

func redactingRun(run func(context.Context) Result, secrets []string) func(context.Context) Result {
	redact := func(text string) string { return config.RedactText(text, secrets...) }
	return func(ctx context.Context) (result Result) {
		// La panique est récupérée ici pour que les secrets connus soient masqués dans son message.
		defer func() {
			if recovered := recover(); recovered != nil {
				result = panicResult(recovered, redact)
			}
		}()
		return redactResult(run(ctx), redact)
	}
}

// findings rassemble les constats d'un contrôle qui examine plusieurs éléments ; le statut est le pire constaté et le
// code celui du premier constat de ce statut.
type findings struct {
	status  Status
	code    string
	details []i18n.Message
	hints   []i18n.Message
}

func (collected *findings) add(status Status, code string, detail, hint i18n.Message) {
	if collected.code == "" || status.severity() > collected.status.severity() {
		collected.status, collected.code = status, code
	}
	collected.details = append(collected.details, detail)
	if hint.Key != "" {
		collected.hints = append(collected.hints, hint)
	}
}

func (collected *findings) result() Result {
	code := collected.code
	if code == "" {
		code = CodeOK
	}
	return Result{Status: collected.status, Code: code, Details: collected.details, Hints: collected.hints}
}

// checkConfig rend l'état de la configuration ; une variable de trackers trop courte pour être masquée partout ajoute
// un avertissement, à côté de celui du .env.
func checkConfig(settings config.Config) Result {
	base := configLoadResult(settings)
	shortNames := shortTrackerVariables(settings)
	if len(shortNames) == 0 {
		return base
	}
	var collected findings
	collected.add(base.Status, base.Code, base.Detail, base.Hint)
	for _, name := range shortNames {
		collected.add(Warn, CodeTrackerValueTooShort, message(i18n.KeyDoctorTrackerValueTooShort, "name", name),
			message(i18n.KeyDoctorTrackerValueTooShortHint))
	}
	return collected.result()
}

func shortTrackerVariables(settings config.Config) []string {
	var names []string
	for _, tracker := range settings.Trackers {
		names = append(names, tracker.ShortVariables...)
	}
	return names
}

func configLoadResult(settings config.Config) Result {
	sources := settings.Sources
	params := []string{
		"dir", settings.ConfigDir, "radarr", sources.Radarr, "sonarr", sources.Sonarr,
		"torrent_clients", sources.TorrentClients, "link_dirs", sources.LinkDirs,
	}
	if settings.WorkingDirDotEnv {
		return Result{
			Status: Warn, Code: CodeWorkingDirDotEnv,
			Detail: message(i18n.KeyDoctorConfigDotEnv, params...), Hint: message(i18n.KeyDoctorConfigDotEnvHint),
		}
	}
	return Result{Status: OK, Code: CodeOK, Detail: message(i18n.KeyDoctorConfigLoaded, params...)}
}

func checkConfigDir(settings config.Config, deps Deps) Result {
	if !isDirectory(settings.ConfigDir) {
		return Result{
			Status: Fail, Code: CodeConfigDirMissing,
			Detail: message(i18n.KeyDoctorConfigDirMissing, "path", settings.ConfigDir), Hint: message(i18n.KeyDoctorConfigDirMissingHint),
		}
	}
	databasePath := config.ResolveDBPath(settings)
	if refusal, refused := refuseDatabasePath(databasePath, settings.LibraryRoots); refused {
		return refusal
	}
	database, err := deps.OpenStore(databasePath)
	if err != nil {
		return Result{
			Status: Fail, Code: CodeDatabaseUnwritable,
			Detail: message(i18n.KeyDoctorDatabaseUnwritable, "error", err.Error()),
			Hint:   message(i18n.KeyDoctorDatabaseUnwritableHint, "dir", filepath.Dir(databasePath)),
		}
	}
	_ = database.Close()
	return Result{Status: OK, Code: CodeOK, Detail: message(i18n.KeyDoctorDatabaseOK, "path", databasePath)}
}

// refuseDatabasePath refuse d'ouvrir la base si son dossier ne se résout pas ou si, liens symboliques suivis (dossier
// et fichier lui-même), elle tombe dans une racine de bibliothèque.
func refuseDatabasePath(databasePath string, libraryRoots []string) (Result, bool) {
	resolvedDirectory, err := filepath.EvalSymlinks(filepath.Dir(databasePath))
	if err != nil {
		return Result{
			Status: Fail, Code: CodeDatabaseDirUnresolved,
			Detail: message(i18n.KeyDoctorDatabaseDirUnresolved, "path", filepath.Dir(databasePath)),
			Hint:   message(i18n.KeyDoctorDatabaseDirUnresolvedHint),
		}, true
	}
	directories := []string{resolvedDirectory}
	if resolvedFile, fileErr := filepath.EvalSymlinks(databasePath); fileErr == nil {
		directories = append(directories, filepath.Dir(resolvedFile))
	}
	for _, directory := range directories {
		if root, inside := insideLibraryRoot(directory, libraryRoots); inside {
			return Result{
				Status: Fail, Code: CodeDatabaseInLibrary,
				Detail: message(i18n.KeyDoctorDatabaseInLibrary, "root", root), Hint: message(i18n.KeyDoctorDatabaseInLibraryHint),
			}, true
		}
	}
	return Result{}, false
}

func checkAuth(settings config.Config) Result {
	if config.IsLoopbackAddress(settings.ListenAddr) {
		return Result{Status: OK, Code: CodeLoopback, Detail: message(i18n.KeyDoctorAuthLoopback, "address", settings.ListenAddr)}
	}
	if settings.AuthToken == "" {
		return Result{
			Status: Fail, Code: CodeNoAuthToken,
			Detail: message(i18n.KeyDoctorAuthNoToken, "address", settings.ListenAddr), Hint: message(i18n.KeyDoctorAuthNoTokenHint),
		}
	}
	return Result{Status: OK, Code: CodeAuthTokenOK, Detail: message(i18n.KeyDoctorAuthToken, "address", settings.ListenAddr)}
}

func checkPathMappings(settings config.Config) Result {
	if len(settings.PathRules) == 0 {
		return Result{Status: Skipped, Code: CodeNoPathMapping, Detail: message(i18n.KeyDoctorPathMappingsNone)}
	}
	var collected findings
	for ruleIndex, rule := range settings.PathRules {
		index := strconv.Itoa(ruleIndex)
		if !pathExists(rule.From) && !pathExists(rule.To) {
			collected.add(Warn, CodeRuleUnreachable, message(i18n.KeyDoctorPathRuleUnreachable, "index", index),
				message(i18n.KeyDoctorPathRuleUnreachableHint))
			continue
		}
		if len(settings.LibraryRoots) > 0 && !coversAnyRoot(rule, settings.LibraryRoots) {
			collected.add(Warn, CodeRuleNoLibraryRoot, message(i18n.KeyDoctorPathRuleNoRoot, "index", index),
				message(i18n.KeyDoctorPathRuleNoRootHint))
			continue
		}
		collected.add(OK, CodeRuleOK, message(i18n.KeyDoctorPathRuleOK, "index", index), i18n.Message{})
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
		return Result{Status: Skipped, Code: CodeDedupeInactive, Detail: message(i18n.KeyDoctorDedupeNone)}
	}
	var collected findings
	for rootIndex, torrentRoot := range settings.Dedupe.TorrentRoots {
		index := strconv.Itoa(rootIndex)
		if overlapped, found := overlappingTorrentRoot(torrentRoot, settings.LibraryRoots); found {
			collected.add(Fail, CodeTorrentRootOverlap, message(i18n.KeyDoctorDedupeOverlap, "index", index, "root", overlapped),
				message(i18n.KeyDoctorDedupeOverlapHint))
			continue
		}
		if !isDirectory(torrentRoot) {
			collected.add(Warn, CodeTorrentRootMissing, message(i18n.KeyDoctorDedupeMissing, "index", index), i18n.Message{})
			continue
		}
		collected.add(OK, CodeTorrentRootOK, message(i18n.KeyDoctorDedupeOK, "index", index), i18n.Message{})
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
