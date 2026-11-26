package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// FSProviderTypeLocal est le type d'un volume local nommé : des dossiers du disque local, sondés par volumes probe.
const FSProviderTypeLocal = "local"

// localProviderFile est la forme brute d'une entrée de type local.
type localProviderFile struct {
	Type  string   `json:"type"`
	Name  string   `json:"name"`
	Paths []string `json:"paths"`
}

func parseLocalProvider(rawProvider json.RawMessage) (FSProvider, error) {
	var rawFile localProviderFile
	decoder := json.NewDecoder(bytes.NewReader(rawProvider))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&rawFile); err != nil {
		return FSProvider{}, fmt.Errorf("type local: %w", err)
	}
	if !fsProviderNamePattern.MatchString(rawFile.Name) {
		return FSProvider{}, fmt.Errorf("name %q is required and must match %s", rawFile.Name, fsProviderNamePattern)
	}
	if len(rawFile.Paths) == 0 {
		return FSProvider{}, errors.New("type local: paths must list at least one absolute folder")
	}
	provider := FSProvider{Type: FSProviderTypeLocal, Name: rawFile.Name}
	for pathIndex, rawPath := range rawFile.Paths {
		cleanedPath, err := localProviderPath(rawPath)
		if err != nil {
			return FSProvider{}, fmt.Errorf("paths[%d]: %w", pathIndex, err)
		}
		provider.Paths = append(provider.Paths, cleanedPath)
	}
	return provider, nil
}

// localProviderPath exige un chemin absolu, sans « .. » ni octet nul, et le rend nettoyé.
func localProviderPath(rawPath string) (string, error) {
	hasParentSegment := slices.Contains(strings.Split(filepath.ToSlash(rawPath), "/"), "..")
	if !filepath.IsAbs(rawPath) || hasParentSegment || strings.ContainsRune(rawPath, 0) {
		return "", fmt.Errorf("folder %q must be absolute without \"..\"", rawPath)
	}
	return filepath.Clean(rawPath), nil
}

// ownedPaths rend les dossiers locaux qu'un fournisseur s'attribue : montages rclone et dossiers d'un volume local.
func ownedPaths(provider FSProvider) []string {
	paths := slices.Clone(provider.Paths)
	for _, mount := range provider.Mounts {
		paths = append(paths, mount.LocalPath)
	}
	return paths
}
