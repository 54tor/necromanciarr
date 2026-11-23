package config

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"strconv"

	"necromanciarr/internal/crossseed"
)

const (
	envCrossSeedConfig   = "CROSS_SEED_CONFIG"
	defaultCrossSeedHost = "localhost"
	defaultCrossSeedPort = 2468
	sourceConfigFile     = "config.json"
	sourceCrossSeed      = "cross-seed"
	sourceNone           = "none"
)

// listenAllHosts sont les valeurs de host qui font écouter cross-seed partout : on le joint alors en local.
var listenAllHosts = []string{"", "0.0.0.0", "::"}

// CrossSeedImport résume, pour `config check`, ce qui a été repris du config.js de cross-seed : noms de clés seulement.
type CrossSeedImport struct {
	Configured   bool     `json:"configured"`
	ConfigPath   string   `json:"configPath"`
	ImportedKeys []string `json:"importedKeys"`
	IgnoredKeys  []string `json:"ignoredKeys"`
}

// Sources dit d'où vient chaque liste effective : config.json, cross-seed ou none.
type Sources struct {
	Radarr         string `json:"radarr"`
	Sonarr         string `json:"sonarr"`
	TorrentClients string `json:"torrentClients"`
	LinkDirs       string `json:"linkDirs"`
}

// CrossSeedDaemon est l'adresse du démon cross-seed ; BaseURL ne porte pas la clé.
type CrossSeedDaemon struct {
	BaseURL string
	APIKey  string
}

// crossSeedLayer est le résultat de l'import : fichier complété par les valeurs de cross-seed là où il se tait.
type crossSeedLayer struct {
	rawFile fileConfig
	status  CrossSeedImport
	sources Sources
	daemon  *CrossSeedDaemon
}

// layerCrossSeed glisse les valeurs du config.js de cross-seed sous celles de config.json, clé par clé : une liste
// non vide de config.json remplace en entier la liste importée. Sans chemin configuré, rien n'est importé.
func layerCrossSeed(options commandLine, rawFile fileConfig, lookup func(string) (string, bool)) (crossSeedLayer, error) {
	layer := crossSeedLayer{rawFile: rawFile}
	path := resolveCrossSeedPath(options, rawFile, lookup)
	var imported crossseed.Imported
	if path != "" {
		var err error
		// Load ne reçoit pas de contexte : le délai propre à l'évaluation borne l'appel.
		if imported, err = crossseed.LoadConfigJS(context.Background(), path, lookup); err != nil {
			return crossSeedLayer{}, err
		}
		layer.status = CrossSeedImport{Configured: true, ConfigPath: path, ImportedKeys: imported.ImportedKeys, IgnoredKeys: imported.IgnoredKeys}
	}
	if err := layer.mergeLists(imported); err != nil {
		return crossSeedLayer{}, fmt.Errorf("cross-seed config.js: %w", err)
	}
	layer.mergeScalars(imported)
	var err error
	if layer.daemon, err = crossSeedDaemonOf(rawFile.CrossSeed, imported); err != nil {
		return crossSeedLayer{}, err
	}
	return layer, nil
}

// resolveCrossSeedPath : --cross-seed-config > CROSS_SEED_CONFIG > crossSeedConfig de config.json.
func resolveCrossSeedPath(options commandLine, rawFile fileConfig, lookup func(string) (string, bool)) string {
	if options.set["cross-seed-config"] {
		return options.crossSeedConfig
	}
	if value, found := lookup(envCrossSeedConfig); found && value != "" {
		return value
	}
	return rawFile.CrossSeedConfig
}

// mergeLists ne valide que les listes importées réellement retenues : une liste invalide masquée par config.json
// ne bloque pas le chargement.
func (layer *crossSeedLayer) mergeLists(imported crossseed.Imported) error {
	var useImported bool
	layer.sources.Radarr, useImported = pickList(layer.rawFile.Radarr, imported.Radarr)
	if useImported {
		if _, err := parseArrServices("radarr", imported.Radarr); err != nil {
			return err
		}
		layer.rawFile.Radarr = imported.Radarr
	}
	layer.sources.Sonarr, useImported = pickList(layer.rawFile.Sonarr, imported.Sonarr)
	if useImported {
		if _, err := parseArrServices("sonarr", imported.Sonarr); err != nil {
			return err
		}
		layer.rawFile.Sonarr = imported.Sonarr
	}
	layer.sources.TorrentClients, useImported = pickList(layer.rawFile.TorrentClients, imported.TorrentClients)
	if useImported {
		if _, err := parseTorrentClients(imported.TorrentClients); err != nil {
			return err
		}
		layer.rawFile.TorrentClients = imported.TorrentClients
	}
	layer.sources.LinkDirs, useImported = pickList(layer.rawFile.LinkDirs, imported.LinkDirs)
	if useImported {
		layer.rawFile.LinkDirs = imported.LinkDirs
	}
	return nil
}

func pickList(fileList, importedList []string) (source string, useImported bool) {
	switch {
	case len(fileList) > 0:
		return sourceConfigFile, false
	case len(importedList) > 0:
		return sourceCrossSeed, true
	default:
		return sourceNone, false
	}
}

func (layer *crossSeedLayer) mergeScalars(imported crossseed.Imported) {
	if layer.rawFile.LinkType == "" {
		layer.rawFile.LinkType = imported.LinkType
	}
	if layer.rawFile.LinkCategory == "" {
		layer.rawFile.LinkCategory = imported.LinkCategory
	}
	if layer.rawFile.SkipRecheck == nil && slices.Contains(imported.ImportedKeys, "skipRecheck") {
		layer.rawFile.SkipRecheck = &imported.SkipRecheck
	}
}

// crossSeedDaemonOf : l'URL explicite crossSeed l'emporte (en Docker, le localhost du config.js désigne le
// conteneur de cross-seed) ; sinon host, port et apiKey importés ; nil si rien n'est connu.
func crossSeedDaemonOf(explicitURL string, imported crossseed.Imported) (*CrossSeedDaemon, error) {
	if explicitURL != "" {
		service, err := ParseArrService("crossSeed", explicitURL)
		if err != nil {
			return nil, err
		}
		return &CrossSeedDaemon{BaseURL: service.BaseURL, APIKey: service.APIKey}, nil
	}
	isDaemonKnown := slices.ContainsFunc([]string{"host", "port", "apiKey"}, func(key string) bool {
		return slices.Contains(imported.ImportedKeys, key)
	})
	if !isDaemonKnown {
		return nil, nil
	}
	host := imported.Host
	if slices.Contains(listenAllHosts, host) {
		host = defaultCrossSeedHost
	}
	port := imported.Port
	if port == 0 {
		port = defaultCrossSeedPort
	}
	return &CrossSeedDaemon{BaseURL: "http://" + net.JoinHostPort(host, strconv.Itoa(port)), APIKey: imported.APIKey}, nil
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
