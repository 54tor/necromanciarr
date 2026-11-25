package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"necromanciarr/internal/fsys"
)

// filesystemProviderTypes liste les types de fournisseurs qui exposent un système de fichiers lisible par un
// chemin sftp://<fournisseur>/… ; rclone n'y est pas (fournisseur d'empreintes), le type sftp y entrera avec son
// implémentation.
var filesystemProviderTypes = []string{}

// validateLocations vérifie chaque racine (libraryRoots, linkDirs, dedupe.torrentRoots) : un chemin local absolu ou
// une adresse sftp://<fournisseur>/<chemin absolu> dont le fournisseur est déclaré, de type système de fichiers ; et
// chaque pathMappings dont from est une adresse sftp:// (to reste un chemin vu par le client). Les erreurs nomment
// la clé, l'indice et le fournisseur, jamais le chemin brut ni un secret.
func validateLocations(loadedConfig Config, filesystemTypes []string) error {
	lists := []struct {
		key   string
		roots []string
	}{
		{"libraryRoots", loadedConfig.LibraryRoots},
		{"linkDirs", loadedConfig.LinkDirs},
		{"dedupe.torrentRoots", loadedConfig.Dedupe.TorrentRoots},
	}
	for _, list := range lists {
		for rootIndex, root := range list.roots {
			if err := validateLocation(loadedConfig, filesystemTypes, root); err != nil {
				return fmt.Errorf("%w: %s[%d]: %w", ErrUsage, list.key, rootIndex, err)
			}
		}
	}
	return validateRemotePathRules(loadedConfig, filesystemTypes)
}

// remoteRuleScheme repère un côté de règle distant, « sftp:/x » mal formé compris.
const remoteRuleScheme = "sftp:"

// errRemoteRuleTarget : le côté to d'une règle est la vue du client torrent, jamais une adresse sftp://.
var errRemoteRuleTarget = errors.New("to is the path seen by the torrent client, not an sftp:// location")

func validateRemotePathRules(loadedConfig Config, filesystemTypes []string) error {
	for ruleIndex, rule := range loadedConfig.PathRules {
		if strings.HasPrefix(rule.To, remoteRuleScheme) {
			return fmt.Errorf("%w: pathMappings[%d].to: %w", ErrUsage, ruleIndex, errRemoteRuleTarget)
		}
		if !strings.HasPrefix(rule.From, remoteRuleScheme) {
			continue
		}
		if err := validateLocation(loadedConfig, filesystemTypes, rule.From); err != nil {
			return fmt.Errorf("%w: pathMappings[%d].from: %w", ErrUsage, ruleIndex, err)
		}
	}
	return nil
}

func validateLocation(loadedConfig Config, filesystemTypes []string, root string) error {
	location, err := fsys.ParseLocation(root)
	if err != nil {
		return err
	}
	if location.Volume == "" {
		return nil
	}
	providerIndex := slices.IndexFunc(loadedConfig.FSProviders, func(provider FSProvider) bool {
		return provider.Name == location.Volume
	})
	if providerIndex < 0 {
		return fmt.Errorf("provider %q is not declared in fsProviders", location.Volume)
	}
	providerType := loadedConfig.FSProviders[providerIndex].Type
	if !slices.Contains(filesystemTypes, providerType) {
		return fmt.Errorf("provider %q has type %q, which does not expose a filesystem", location.Volume, providerType)
	}
	return nil
}
