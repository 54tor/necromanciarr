package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"strconv"
	"strings"

	"necromanciarr/internal/crossseed"
)

const (
	envCrossSeedConfig   = "CROSS_SEED_CONFIG"
	defaultCrossSeedHost = "localhost"
	defaultCrossSeedPort = 2468
)

// supportedClientTypes sont les clients torrent pris en charge ; knownClientTypes ceux que cross-seed connaît
// aussi et que l'avertissement peut nommer sans risque.
var (
	supportedClientTypes = []string{typeQbittorrent}
	knownClientTypes     = []string{typeQbittorrent, "deluge", "transmission", "rtorrent"}
)

// listenAllHosts sont les valeurs de host qui font écouter cross-seed partout : on le joint alors en local.
var listenAllHosts = []string{"", "0.0.0.0", "::"}

// CrossSeedImport résume, pour `config check`, ce qui a été repris du config.js de cross-seed : noms de clés seulement.
// UnsupportedTorrentClients liste, sans leur URL, les clients importés écartés faute d'adaptateur.
type CrossSeedImport struct {
	Configured                bool                `json:"configured"`
	ConfigPath                string              `json:"configPath"`
	ImportedKeys              []string            `json:"importedKeys"`
	IgnoredKeys               []string            `json:"ignoredKeys"`
	UnnamedIgnoredKeys        int                 `json:"unnamedIgnoredKeys"`
	UnsupportedTorrentClients []UnsupportedClient `json:"unsupportedTorrentClients"`
	// BlockedEnvironment nomme les variables de cet outil que config.js a tenté de lire : elles lui restent cachées.
	BlockedEnvironment []string `json:"blockedEnvironment"`
}

// UnsupportedClient désigne un client torrent importé et écarté : son rang dans torrentClients et son type
// (« unknown » hors des types connus de cross-seed).
type UnsupportedClient struct {
	Index int    `json:"index"`
	Type  string `json:"type"`
}

// CrossSeedDaemon est l'adresse du démon cross-seed ; BaseURL ne porte pas la clé.
type CrossSeedDaemon struct {
	BaseURL string
	APIKey  string
}

// crossSeedLayer est le résultat de l'import du config.js de cross-seed : valeurs lues et bilan pour config check.
type crossSeedLayer struct {
	imported crossseed.Imported
	status   CrossSeedImport
}

// importCrossSeed lit le config.js de cross-seed ; sans chemin configuré, rien n'est importé. Les clients torrent non
// pris en charge sont écartés de l'amont et signalés.
func importCrossSeed(options commandLine, lookup func(string) (string, bool)) (crossSeedLayer, error) {
	path := resolveCrossSeedPath(options, lookup)
	if path == "" {
		return crossSeedLayer{}, nil
	}
	// Load ne reçoit pas de contexte : le délai propre à l'évaluation borne l'appel.
	imported, err := crossseed.LoadConfigJS(context.Background(), path, lookup)
	if err != nil {
		return crossSeedLayer{}, err
	}
	supportedClients, unsupportedClients := splitSupportedClients(imported.TorrentClients)
	imported.TorrentClients = supportedClients
	status := CrossSeedImport{
		Configured: true, ConfigPath: path, ImportedKeys: imported.ImportedKeys,
		IgnoredKeys: imported.IgnoredKeys, UnnamedIgnoredKeys: imported.UnnamedIgnoredKeys,
		BlockedEnvironment: imported.BlockedEnvironment, UnsupportedTorrentClients: unsupportedClients,
	}
	return crossSeedLayer{imported: imported, status: status}, nil
}

// upstreamLists donne, par liste, les chaînes brutes importées qui servent d'amont aux surcharges de la base.
func (layer crossSeedLayer) upstreamLists() map[string][]string {
	return map[string][]string{
		listRadarr: layer.imported.Radarr, listSonarr: layer.imported.Sonarr,
		listTorrentClients: layer.imported.TorrentClients, listLinkDirs: layer.imported.LinkDirs,
	}
}

// resolveCrossSeedPath : --cross-seed-config > CROSS_SEED_CONFIG ; un chemin relatif part du dossier courant.
func resolveCrossSeedPath(options commandLine, lookup func(string) (string, bool)) string {
	if options.set["cross-seed-config"] {
		return options.crossSeedConfig
	}
	value, _ := lookup(envCrossSeedConfig)
	return value
}

// splitSupportedClients garde les clients pris en charge ; les autres ne sont décrits que par rang et type, jamais
// par leur URL. Le rang est celui de la liste de cross-seed.
func splitSupportedClients(rawClients []string) (supported []string, unsupported []UnsupportedClient) {
	for clientIndex, rawClient := range rawClients {
		clientType, _, _ := strings.Cut(rawClient, ":")
		if slices.Contains(supportedClientTypes, clientType) {
			supported = append(supported, rawClient)
			continue
		}
		if !slices.Contains(knownClientTypes, clientType) {
			clientType = "unknown"
		}
		unsupported = append(unsupported, UnsupportedClient{Index: clientIndex, Type: clientType})
	}
	return supported, unsupported
}

// mergeCrossSeedScalars complète rawFile par les réglages importés là où la base se tait ; rend les clés complétées.
func mergeCrossSeedScalars(rawFile *fileConfig, imported crossseed.Imported) []string {
	var filledKeys []string
	if rawFile.LinkType == "" && imported.LinkType != "" {
		rawFile.LinkType = imported.LinkType
		filledKeys = append(filledKeys, settingLinkType)
	}
	if rawFile.LinkCategory == "" && imported.LinkCategory != "" {
		rawFile.LinkCategory = imported.LinkCategory
		filledKeys = append(filledKeys, settingLinkCategory)
	}
	if rawFile.SkipRecheck == nil && slices.Contains(imported.ImportedKeys, settingSkipRecheck) {
		rawFile.SkipRecheck = &imported.SkipRecheck
		filledKeys = append(filledKeys, settingSkipRecheck)
	}
	return filledKeys
}

// crossSeedDaemonOf : crossSeedUrl l'emporte sur host et port importés (en Docker, le localhost du config.js désigne
// le conteneur de cross-seed), crossSeedApiKey sur l'apiKey importée, chacun indépendamment ; nil si rien n'est connu.
func crossSeedDaemonOf(explicitURL, explicitKey string, imported crossseed.Imported) (*CrossSeedDaemon, error) {
	baseURL, err := daemonBaseURL(explicitURL, imported)
	if err != nil {
		return nil, err
	}
	apiKey := explicitKey
	if apiKey == "" {
		apiKey = imported.APIKey
	}
	if baseURL == "" && apiKey == "" {
		return nil, nil
	}
	if baseURL == "" {
		return nil, errors.New(settingCrossSeedAPIKey + " needs " + settingCrossSeedURL + " or a host and port imported from the cross-seed config.js")
	}
	return &CrossSeedDaemon{BaseURL: baseURL, APIKey: apiKey}, nil
}

func daemonBaseURL(explicitURL string, imported crossseed.Imported) (string, error) {
	if explicitURL != "" {
		return validatedCrossSeedURL(explicitURL)
	}
	if !isDaemonImported(imported) {
		return "", nil
	}
	host := imported.Host
	if slices.Contains(listenAllHosts, host) {
		host = defaultCrossSeedHost
	}
	port := imported.Port
	if port == 0 {
		port = defaultCrossSeedPort
	}
	daemonURL := "http://" + net.JoinHostPort(host, strconv.Itoa(port))
	if _, err := parseHTTPURL(daemonURL); err != nil {
		return "", errors.New(`cross-seed config.js: key "host": not usable in a daemon url`)
	}
	return daemonURL, nil
}

func isDaemonImported(imported crossseed.Imported) bool {
	return slices.ContainsFunc([]string{"host", "port", "apiKey"}, func(key string) bool {
		return slices.Contains(imported.ImportedKeys, key)
	})
}

// validatedCrossSeedURL n'admet que http(s)://hôte[:port][/chemin] : la clé a son propre réglage.
func validatedCrossSeedURL(rawURL string) (string, error) {
	parsedURL, err := parseHTTPURL(rawURL)
	if err != nil {
		return "", fmt.Errorf(errInvalidURLFormat, settingCrossSeedURL)
	}
	if parsedURL.RawQuery != "" {
		return "", errors.New(settingCrossSeedURL + ": no query allowed; the API key goes in " + settingCrossSeedAPIKey)
	}
	if parsedURL.User != nil {
		return "", errors.New(settingCrossSeedURL + ": userinfo is not supported; the API key goes in " + settingCrossSeedAPIKey)
	}
	return baseURLOf(parsedURL), nil
}

// importedDaemonKeys nomme les réglages du démon dont la valeur effective vient du config.js importé.
func importedDaemonKeys(rawFile fileConfig, imported crossseed.Imported) []string {
	var keys []string
	if rawFile.CrossSeedURL == "" && isDaemonImported(imported) {
		keys = append(keys, settingCrossSeedURL)
	}
	if rawFile.CrossSeedAPIKey == "" && imported.APIKey != "" {
		keys = append(keys, settingCrossSeedAPIKey)
	}
	return keys
}

// redacted renvoie une copie dont la clé d'API est masquée.
func (daemon CrossSeedDaemon) redacted() CrossSeedDaemon {
	if daemon.APIKey != "" {
		daemon.APIKey = redactedMark
	}
	return daemon
}

type (
	plainCrossSeedDaemon CrossSeedDaemon
	jsonCrossSeedDaemon  struct {
		BaseURL string `json:"baseUrl"`
		APIKey  string `json:"apiKey"`
	}
)

// String masque la clé d'API pour %v, %+v et %s.
func (daemon CrossSeedDaemon) String() string {
	return fmt.Sprintf("%+v", plainCrossSeedDaemon(daemon.redacted()))
}

// GoString masque la clé d'API pour %#v.
func (daemon CrossSeedDaemon) GoString() string { return daemon.String() }

// LogValue masque la clé d'API dans les journaux slog.
func (daemon CrossSeedDaemon) LogValue() slog.Value { return slog.StringValue(daemon.String()) }

// MarshalJSON masque la clé d'API : toute sérialisation est destinée à l'affichage.
func (daemon CrossSeedDaemon) MarshalJSON() ([]byte, error) {
	return json.Marshal(jsonCrossSeedDaemon(daemon.redacted()))
}
