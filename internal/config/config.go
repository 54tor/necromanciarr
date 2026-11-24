// Package config charge la configuration : fichier JSON, .env, variables d'environnement et options de ligne de commande.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"time"

	"necromanciarr/internal/crossseed"
)

const (
	defaultListenAddr         = "127.0.0.1:8787"
	defaultDBPath             = "necromanciarr.db"
	defaultReadBytesPerSecond = 20 * 1024 * 1024
	configFileName            = "config.json"
	redactedSecret            = "[redacted]"
	defaultDedupeMinFileAge   = 10 * time.Minute
	defaultLinkType           = "hardlink"

	envConfigDir            = "CONFIG_DIR"
	envLibraryAPI           = "LIBRARY_API"
	envAuthToken            = "NECROMANCIARR_AUTH_TOKEN"
	envLibraryProviderToken = "LIBRARY_PROVIDER_TOKEN"
	envCrossSeedMode        = "CROSS_SEED_MODE"
)

// PathRule traduit un préfixe de chemin entre deux vues du même stockage (hôte, conteneur, client torrent).
type PathRule struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Config est la configuration effective. Les jetons ne viennent que de l'environnement
// et sont exclus de toute sérialisation.
type Config struct {
	ConfigDir          string          `json:"configDir"`
	ListenAddr         string          `json:"listen"`
	DBPath             string          `json:"dbPath"`
	ReadBytesPerSecond int64           `json:"readBytesPerSecond"`
	LibraryAPI         string          `json:"libraryApi"`
	LibraryRoots       []string        `json:"libraryRoots"`
	PathRules          []PathRule      `json:"pathMappings"`
	Radarr             []ArrService    `json:"radarr"`
	Sonarr             []ArrService    `json:"sonarr"`
	TorrentClients     []TorrentClient `json:"torrentClients"`
	Dedupe             Dedupe          `json:"dedupe"`
	FSProviders        []FSProvider    `json:"fsProviders"`
	// LinkDirs, LinkType, SkipRecheck et LinkCategory reprennent les réglages de liens de cross-seed.
	LinkDirs        []string         `json:"linkDirs"`
	LinkType        string           `json:"linkType"`
	SkipRecheck     bool             `json:"skipRecheck"`
	LinkCategory    string           `json:"linkCategory"`
	CrossSeed       *CrossSeedDaemon `json:"crossSeed,omitempty"`
	CrossSeedImport CrossSeedImport  `json:"crossSeedImport"`
	// CrossSeedMode : auto (défaut), delegate, native ou off ; voir crossseed.ResolveMode.
	CrossSeedMode string  `json:"crossSeedMode"`
	Sources       Sources `json:"sources"`

	AuthToken            string `json:"-"`
	LibraryProviderToken string `json:"-"`
}

// fileConfig est le contenu brut de config.json, avant interpolation et analyse des chaînes de service.
type fileConfig struct {
	Listen             string            `json:"listen"`
	DBPath             string            `json:"dbPath"`
	ReadBytesPerSecond int64             `json:"readBytesPerSecond"`
	LibraryAPI         string            `json:"libraryApi"`
	LibraryRoots       []string          `json:"libraryRoots"`
	PathMappings       []PathRule        `json:"pathMappings"`
	Radarr             []string          `json:"radarr"`
	Sonarr             []string          `json:"sonarr"`
	TorrentClients     []string          `json:"torrentClients"`
	Dedupe             fileDedupe        `json:"dedupe"`
	FSProviders        []json.RawMessage `json:"fsProviders"`
	LinkDirs           []string          `json:"linkDirs"`
	LinkType           string            `json:"linkType"`
	SkipRecheck        *bool             `json:"skipRecheck"`
	LinkCategory       string            `json:"linkCategory"`
	CrossSeedConfig    string            `json:"crossSeedConfig"`
	CrossSeed          string            `json:"crossSeed"`
	CrossSeedMode      string            `json:"crossSeedMode"`
	// LegacyRclone n'existe que pour refuser l'ancienne section avec un message utile.
	LegacyRclone json.RawMessage `json:"rclone"`
}

// Dedupe règle le remplacement, côté torrent, des copies d'un fichier de la bibliothèque par des liens. TorrentRoots
// vide laisse la fonction inactive ; Kinds donne l'ordre de préférence parmi symlink, hardlink et reflink.
type Dedupe struct {
	TorrentRoots     []string      `json:"torrentRoots"`
	Kinds            []string      `json:"kinds"`
	RelativeSymlinks bool          `json:"relativeSymlinks"`
	MinFileAge       time.Duration `json:"minFileAge"`
	RecheckAfter     bool          `json:"recheckAfter"`
	// Verify : auto (défaut, empreinte du stockage si possible, sinon lecture complète), full, sampled ou hash.
	Verify string `json:"verify"`
}

// fileDedupe : les booléens sont des pointeurs pour distinguer une clé absente (défaut true) d'un false explicite.
type fileDedupe struct {
	TorrentRoots     []string `json:"torrentRoots"`
	Kinds            []string `json:"kinds"`
	RelativeSymlinks *bool    `json:"relativeSymlinks"`
	MinFileAge       string   `json:"minFileAge"`
	RecheckAfter     *bool    `json:"recheckAfter"`
	Verify           string   `json:"verify"`
}

var dedupeLinkKinds = []string{"symlink", "hardlink", "reflink"}

// commandLine regroupe les options reconnues ; set indique celles réellement passées.
type commandLine struct {
	listen, libraryAPI, configDir, dbPath string
	crossSeedConfig, crossSeedMode        string
	readBytesPerSecond                    int64
	set                                   map[string]bool
}

// ErrUsage signale une ligne de commande invalide (option inconnue, argument en trop, aide demandée).
var ErrUsage = errors.New("usage")

// ResolveConfigDir détermine le dossier de configuration sans charger le reste : --config-dir > CONFIG_DIR > défaut.
// Un dossier choisi explicitement doit exister ; le défaut est créé plus tard, à la demande.
func ResolveConfigDir(args []string, lookupEnv func(string) (string, bool), defaultConfigDir string) (string, error) {
	options, err := parseCommandLine(args)
	if err != nil {
		return "", err
	}
	return resolveConfigDir(options, lookupEnv, defaultConfigDir)
}

// Load construit la configuration. Priorité : ligne de commande > environnement > config.json > config.js de
// cross-seed (si crossSeedConfig est donné) > défauts.
// defaultConfigDir sert quand ni --config-dir ni CONFIG_DIR ne sont fournis. Les erreurs ne contiennent aucun secret.
func Load(args []string, lookupEnv func(string) (string, bool), defaultConfigDir string) (Config, error) {
	options, err := parseCommandLine(args)
	if err != nil {
		return Config{}, err
	}
	configDir, err := resolveConfigDir(options, lookupEnv, defaultConfigDir)
	if err != nil {
		return Config{}, err
	}
	lookup, err := loadDotEnvLookup(configDir, lookupEnv)
	if err != nil {
		return Config{}, err
	}
	rawFile, err := readConfigFile(filepath.Join(configDir, configFileName), lookup)
	if err != nil {
		return Config{}, err
	}
	layer, err := layerCrossSeed(options, rawFile, configDir, lookup)
	if err != nil {
		return Config{}, err
	}
	loadedConfig, err := buildConfig(layer.rawFile, lookup)
	if err != nil {
		return Config{}, err
	}
	loadedConfig.ConfigDir = configDir
	loadedConfig.CrossSeed, loadedConfig.CrossSeedImport, loadedConfig.Sources = layer.daemon, layer.status, layer.sources
	applyDefaults(&loadedConfig)
	applyOverrides(&loadedConfig, options, lookup)
	if loadedConfig.CrossSeedMode, err = crossseed.NormalizeMode(loadedConfig.CrossSeedMode); err != nil {
		return Config{}, err
	}
	if err := validate(loadedConfig); err != nil {
		return Config{}, err
	}
	return loadedConfig, nil
}

func parseCommandLine(args []string) (commandLine, error) {
	options := commandLine{set: map[string]bool{}}
	flagSet := flag.NewFlagSet("necromanciarr", flag.ContinueOnError)
	flagSet.SetOutput(io.Discard)
	flagSet.StringVar(&options.listen, "listen", "", "listen address")
	flagSet.StringVar(&options.libraryAPI, "library-api", "", "library provider base url")
	flagSet.StringVar(&options.configDir, "config-dir", "", "configuration directory")
	flagSet.StringVar(&options.dbPath, "db-path", "", "sqlite database path")
	flagSet.StringVar(&options.crossSeedConfig, "cross-seed-config", "", "cross-seed config.js path")
	flagSet.StringVar(&options.crossSeedMode, "cross-seed-mode", "", "cross-seed mode: auto, delegate, native or off")
	flagSet.Int64Var(&options.readBytesPerSecond, "read-bytes-per-second", 0, "read rate limit")
	if err := flagSet.Parse(args); err != nil {
		return commandLine{}, fmt.Errorf("%w: %w", ErrUsage, err)
	}
	if flagSet.NArg() > 0 {
		return commandLine{}, fmt.Errorf("%w: unexpected argument", ErrUsage)
	}
	flagSet.Visit(func(visited *flag.Flag) { options.set[visited.Name] = true })
	return options, nil
}

func resolveConfigDir(options commandLine, lookupEnv func(string) (string, bool), defaultConfigDir string) (string, error) {
	explicitDir := ""
	if options.set["config-dir"] {
		explicitDir = options.configDir
	} else if environmentDir, found := lookupEnv(envConfigDir); found {
		explicitDir = environmentDir
	}
	if explicitDir == "" {
		if defaultConfigDir == "" {
			return "", errors.New("config dir unknown: set --config-dir or " + envConfigDir)
		}
		return defaultConfigDir, nil
	}
	info, err := os.Stat(explicitDir)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("config dir %q does not exist or is not a directory", explicitDir)
	}
	return explicitDir, nil
}

// readConfigFile lit config.json en mode strict ; un fichier absent donne une configuration vide.
// Limite d'encoding/json : une clé dupliquée n'est pas détectée, la dernière l'emporte.
func readConfigFile(path string, lookup func(string) (string, bool)) (fileConfig, error) {
	var rawFile fileConfig
	content, err := os.ReadFile(path) // #nosec G304 -- chemin dérivé du dossier de configuration de l'opérateur
	if errors.Is(err, fs.ErrNotExist) {
		return rawFile, nil
	}
	if err != nil {
		return fileConfig{}, fmt.Errorf("read %s: %w", configFileName, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&rawFile); err != nil {
		return fileConfig{}, fmt.Errorf("parse %s: %w", configFileName, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fileConfig{}, fmt.Errorf("parse %s: unexpected data after the JSON object", configFileName)
	}
	if err := interpolateValue(reflect.ValueOf(&rawFile).Elem(), "", lookup); err != nil {
		return fileConfig{}, fmt.Errorf("%s: %w", configFileName, err)
	}
	return rawFile, nil
}

func buildConfig(rawFile fileConfig, lookup func(string) (string, bool)) (Config, error) {
	if len(rawFile.LegacyRclone) > 0 {
		return Config{}, errors.New(`"rclone" was renamed to "fsProviders" (type "rclone", with a "name")`)
	}
	loadedConfig := Config{
		ListenAddr:         rawFile.Listen,
		DBPath:             rawFile.DBPath,
		ReadBytesPerSecond: rawFile.ReadBytesPerSecond,
		LibraryAPI:         rawFile.LibraryAPI,
		LibraryRoots:       rawFile.LibraryRoots,
		PathRules:          rawFile.PathMappings,
		LinkDirs:           rawFile.LinkDirs,
		LinkType:           rawFile.LinkType,
		SkipRecheck:        rawFile.SkipRecheck == nil || *rawFile.SkipRecheck,
		LinkCategory:       rawFile.LinkCategory,
		CrossSeedMode:      rawFile.CrossSeedMode,
	}
	var err error
	if loadedConfig.Dedupe, err = parseDedupe(rawFile.Dedupe); err != nil {
		return Config{}, err
	}
	if loadedConfig.FSProviders, err = parseFSProviders(rawFile.FSProviders, lookup); err != nil {
		return Config{}, err
	}
	if loadedConfig.Radarr, err = parseArrServices("radarr", rawFile.Radarr); err != nil {
		return Config{}, err
	}
	if loadedConfig.Sonarr, err = parseArrServices("sonarr", rawFile.Sonarr); err != nil {
		return Config{}, err
	}
	if loadedConfig.TorrentClients, err = parseTorrentClients(rawFile.TorrentClients); err != nil {
		return Config{}, err
	}
	return loadedConfig, nil
}

func parseTorrentClients(rawClients []string) ([]TorrentClient, error) {
	var clients []TorrentClient
	for clientIndex, rawClient := range rawClients {
		client, err := ParseTorrentClient(rawClient)
		if err != nil {
			return nil, fmt.Errorf("torrentClients[%d]: %w", clientIndex, err)
		}
		clients = append(clients, client)
	}
	return clients, nil
}

func parseDedupe(rawDedupe fileDedupe) (Dedupe, error) {
	parsed := Dedupe{
		TorrentRoots:     rawDedupe.TorrentRoots,
		Kinds:            rawDedupe.Kinds,
		RelativeSymlinks: rawDedupe.RelativeSymlinks == nil || *rawDedupe.RelativeSymlinks,
		MinFileAge:       defaultDedupeMinFileAge,
		RecheckAfter:     rawDedupe.RecheckAfter == nil || *rawDedupe.RecheckAfter,
	}
	var verifyErr error
	if parsed.Verify, verifyErr = parseDedupeVerify(rawDedupe.Verify); verifyErr != nil {
		return Dedupe{}, verifyErr
	}
	if len(parsed.Kinds) == 0 {
		parsed.Kinds = []string{"symlink"}
	}
	for kindIndex, kind := range parsed.Kinds {
		if !slices.Contains(dedupeLinkKinds, kind) {
			return Dedupe{}, fmt.Errorf("dedupe.kinds[%d]: unknown link kind %q (allowed: %v)", kindIndex, kind, dedupeLinkKinds)
		}
	}
	if rawDedupe.MinFileAge != "" {
		minFileAge, err := time.ParseDuration(rawDedupe.MinFileAge)
		if err != nil || minFileAge < 0 {
			return Dedupe{}, fmt.Errorf("dedupe.minFileAge: %q is not a non-negative Go duration", rawDedupe.MinFileAge)
		}
		parsed.MinFileAge = minFileAge
	}
	return parsed, nil
}

func parseArrServices(kind string, rawServices []string) ([]ArrService, error) {
	var services []ArrService
	for serviceIndex, rawService := range rawServices {
		service, err := ParseArrService(kind, rawService)
		if err != nil {
			return nil, fmt.Errorf("%s[%d]: %w", kind, serviceIndex, err)
		}
		services = append(services, service)
	}
	return services, nil
}

func applyDefaults(target *Config) {
	if target.ListenAddr == "" {
		target.ListenAddr = defaultListenAddr
	}
	if target.DBPath == "" {
		target.DBPath = defaultDBPath
	}
	if target.ReadBytesPerSecond == 0 {
		target.ReadBytesPerSecond = defaultReadBytesPerSecond
	}
	if target.CrossSeedMode == "" {
		target.CrossSeedMode = crossseed.ModeAuto
	}
	if target.LinkType == "" {
		target.LinkType = defaultLinkType
	}
}

// applyOverrides applique l'environnement puis la ligne de commande par-dessus le fichier ; les jetons ne viennent que de l'environnement.
func applyOverrides(target *Config, options commandLine, lookup func(string) (string, bool)) {
	if value, found := lookup(envLibraryAPI); found && value != "" {
		target.LibraryAPI = value
	}
	if value, found := lookup(envCrossSeedMode); found && value != "" {
		target.CrossSeedMode = value
	}
	if options.set["cross-seed-mode"] {
		target.CrossSeedMode = options.crossSeedMode
	}
	target.AuthToken, _ = lookup(envAuthToken)
	target.LibraryProviderToken, _ = lookup(envLibraryProviderToken)
	if options.set["listen"] {
		target.ListenAddr = options.listen
	}
	if options.set["library-api"] {
		target.LibraryAPI = options.libraryAPI
	}
	if options.set["db-path"] {
		target.DBPath = options.dbPath
	}
	if options.set["read-bytes-per-second"] {
		target.ReadBytesPerSecond = options.readBytesPerSecond
	}
}

// Un jeton est exigé hors boucle locale : sans lui, l'interface serait ouverte au réseau sans authentification.
func validate(loadedConfig Config) error {
	if loadedConfig.ReadBytesPerSecond <= 0 {
		return errors.New("readBytesPerSecond must be positive")
	}
	if loadedConfig.LibraryAPI != "" {
		if _, err := parseHTTPURL(loadedConfig.LibraryAPI); err != nil {
			return errors.New("libraryApi must be an http(s) url without fragment")
		}
	}
	if !slices.Contains(dedupeLinkKinds, loadedConfig.LinkType) {
		return fmt.Errorf("linkType: unknown link kind (allowed: %v)", dedupeLinkKinds)
	}
	if err := validateDedupeRoots(loadedConfig); err != nil {
		return err
	}
	if err := validateLinkDirs(loadedConfig); err != nil {
		return err
	}
	if loadedConfig.AuthToken == "" && !isLoopbackAddress(loadedConfig.ListenAddr) {
		return fmt.Errorf("listen %q is not loopback: set %s", loadedConfig.ListenAddr, envAuthToken)
	}
	return nil
}

// validateDedupeRoots : une racine torrent qui contient la bibliothèque, ou y est contenue, ferait proposer des liens
// remplaçant des fichiers de la bibliothèque elle-même.
func validateDedupeRoots(loadedConfig Config) error {
	for torrentIndex, torrentRoot := range loadedConfig.Dedupe.TorrentRoots {
		for libraryIndex, libraryRoot := range loadedConfig.LibraryRoots {
			if PathsOverlap(torrentRoot, libraryRoot) {
				return fmt.Errorf("dedupe.torrentRoots[%d] %q overlaps libraryRoots[%d] %q", torrentIndex, torrentRoot,
					libraryIndex, libraryRoot)
			}
		}
	}
	return nil
}

// validateLinkDirs : un dossier de liens dans la bibliothèque (ou qui la contient) ferait écrire liens et annexes
// au milieu des fichiers de la bibliothèque. Contrôle lexical ; l'étape Link refait le contrôle sur chemins résolus.
func validateLinkDirs(loadedConfig Config) error {
	for linkIndex, linkDir := range loadedConfig.LinkDirs {
		for libraryIndex, libraryRoot := range loadedConfig.LibraryRoots {
			if PathsOverlap(linkDir, libraryRoot) {
				return fmt.Errorf("linkDirs[%d] %q overlaps libraryRoots[%d] %q", linkIndex, linkDir, libraryIndex, libraryRoot)
			}
		}
	}
	return nil
}

// PathsOverlap dit si deux chemins, une fois nettoyés, sont égaux ou si l'un contient l'autre, segment par segment
// (« /data/tv » ne contient pas « /data/tvshows »). Comparaison lexicale, sensible à la casse.
func PathsOverlap(firstPath, secondPath string) bool {
	return contains(firstPath, secondPath) || contains(secondPath, firstPath)
}

func contains(parentPath, childPath string) bool {
	relative, err := filepath.Rel(filepath.Clean(parentPath), filepath.Clean(childPath))
	return err == nil && (relative == "." || filepath.IsLocal(relative))
}

func isLoopbackAddress(listenAddr string) bool {
	host, _, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	parsedIP := net.ParseIP(host)
	return parsedIP != nil && parsedIP.IsLoopback()
}

// RedactedJSON sérialise la configuration effective, identifiants masqués (jetons exclus), pour `config check`.
func (loadedConfig Config) RedactedJSON() ([]byte, error) {
	return json.MarshalIndent(loadedConfig, "", "  ")
}

type plainConfig Config

// redacted renvoie une copie sans secret : jetons, identifiants et valeurs sensibles des URLs masqués.
func (loadedConfig Config) redacted() plainConfig {
	redactedCopy := loadedConfig
	redactedCopy.LibraryAPI = redactURL(loadedConfig.LibraryAPI)
	redactedCopy.FSProviders = slices.Clone(loadedConfig.FSProviders)
	for providerIndex := range redactedCopy.FSProviders {
		redactedCopy.FSProviders[providerIndex].RC = redactRCURL(redactedCopy.FSProviders[providerIndex].RC)
	}
	if redactedCopy.AuthToken != "" {
		redactedCopy.AuthToken = redactedSecret
	}
	if redactedCopy.LibraryProviderToken != "" {
		redactedCopy.LibraryProviderToken = redactedSecret
	}
	return plainConfig(redactedCopy)
}

// MarshalJSON masque les secrets : toute sérialisation est destinée à l'affichage.
func (loadedConfig Config) MarshalJSON() ([]byte, error) {
	return json.Marshal(loadedConfig.redacted())
}

// String masque identifiants d'URL et jetons pour %v et %+v.
func (loadedConfig Config) String() string {
	return fmt.Sprintf("%+v", loadedConfig.redacted())
}

// GoString masque les secrets pour %#v.
func (loadedConfig Config) GoString() string {
	return loadedConfig.String()
}

// LogValue masque les secrets dans les journaux slog.
func (loadedConfig Config) LogValue() slog.Value {
	return slog.StringValue(loadedConfig.String())
}
