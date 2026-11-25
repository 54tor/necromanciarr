package hashsource

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"

	"necromanciarr/internal/config"
)

// Deps regroupe ce que les fabriques de fournisseurs reçoivent de l'extérieur ; HTTPClient nil : client par défaut.
type Deps struct {
	HTTPClient *http.Client
}

// ProviderFactory construit la source d'empreintes d'un fournisseur de système de fichiers à partir de sa déclaration.
type ProviderFactory func(spec config.FSProvider, deps Deps) (HashSource, error)

// providerFactories associe chaque type connu à sa fabrique ; table explicite, sans enregistrement à l'init.
var providerFactories = map[string]ProviderFactory{
	config.FSProviderTypeRclone: newRcloneProvider,
}

func newRcloneProvider(spec config.FSProvider, deps Deps) (HashSource, error) {
	return NewRclone(RcloneOptions{RCURL: spec.RC, Binary: spec.Binary, Mounts: toMounts(spec.Mounts), Timeout: spec.Timeout, HTTPClient: deps.HTTPClient})
}

func toMounts(specMounts []config.FSMount) []Mount {
	mounts := make([]Mount, 0, len(specMounts))
	for _, specMount := range specMounts {
		mounts = append(mounts, Mount{LocalPath: specMount.LocalPath, Remote: specMount.Target})
	}
	return mounts
}

// registeredProvider est un fournisseur construit, avec sa déclaration pour la description des montages.
type registeredProvider struct {
	spec   config.FSProvider
	source HashSource
}

// Registry retrouve le fournisseur qui porte un chemin local.
type Registry struct {
	providers []registeredProvider
}

// NewFromConfig construit la source de chaque fournisseur déclaré. Un type sans fabrique est une erreur.
func NewFromConfig(providers []config.FSProvider, deps Deps) (Registry, error) {
	return newRegistry(providers, deps, providerFactories)
}

func newRegistry(providers []config.FSProvider, deps Deps, factories map[string]ProviderFactory) (Registry, error) {
	var registry Registry
	for _, spec := range providers {
		factory, known := factories[spec.Type]
		if !known {
			return Registry{}, fmt.Errorf("fs provider %q: unknown type %q", spec.Name, spec.Type)
		}
		source, err := factory(spec, deps)
		if err != nil {
			return Registry{}, fmt.Errorf("fs provider %q: %w", spec.Name, err)
		}
		registry.providers = append(registry.providers, registeredProvider{spec: spec, source: source})
	}
	return registry, nil
}

// ForPath renvoie la source du fournisseur dont le montage est le plus long préfixe de path (frontière de segment,
// tous fournisseurs confondus). Un chemin non sûr ou hors de tout montage donne false.
func (registry Registry) ForPath(path string) (HashSource, bool) {
	if checkSafePath(path) != nil {
		return nil, false
	}
	var best registeredProvider
	bestLength := -1
	for _, provider := range registry.providers {
		for _, mount := range provider.spec.Mounts {
			if len(mount.LocalPath) > bestLength && isStrictlyUnder(path, mount.LocalPath) {
				best, bestLength = provider, len(mount.LocalPath)
			}
		}
	}
	if bestLength < 0 {
		return nil, false
	}
	return best.source, true
}

// Hash fait du registre une HashSource (dedupe.Options.Backend, NewConfirmer) : la source du montage qui porte path
// répond ; un chemin hors de tout montage n'a pas d'empreinte (Available faux), ce qui fait relire le fichier.
func (registry Registry) Hash(ctx context.Context, path string, algorithm Algorithm) (Result, error) {
	source, found := registry.ForPath(path)
	if !found {
		return Result{}, nil
	}
	return source.Hash(ctx, path, algorithm)
}

func isStrictlyUnder(path, mountPath string) bool {
	prefix := strings.TrimRight(mountPath, string(filepath.Separator)) + string(filepath.Separator)
	return strings.HasPrefix(path, prefix) && len(path) > len(prefix)
}

// ProviderMountReport est le rapport d'un montage avec le nom du fournisseur qui le porte.
type ProviderMountReport struct {
	Provider string
	MountReport
}

// DescribeMounts sonde chaque montage de chaque fournisseur rclone (DescribeMountWith) ; sans rc ni binaire, le rapport
// le dit en conseil. Les types sans sonde sont sautés.
func (registry Registry) DescribeMounts(ctx context.Context) []ProviderMountReport {
	var reports []ProviderMountReport
	for _, provider := range registry.providers {
		if provider.spec.Type != config.FSProviderTypeRclone {
			continue
		}
		options := DescribeOptions{RCURL: provider.spec.RC, Binary: provider.spec.Binary, Timeout: provider.spec.Timeout}
		for _, mount := range toMounts(provider.spec.Mounts) {
			reports = append(reports, ProviderMountReport{Provider: provider.spec.Name, MountReport: DescribeMountWith(ctx, mount, options)})
		}
	}
	slices.SortStableFunc(reports, func(first, second ProviderMountReport) int {
		return strings.Compare(first.Mount.LocalPath, second.Mount.LocalPath)
	})
	return reports
}

// CachedHashAlgorithms renvoie, dans l'ordre d'AllAlgorithms, les types d'empreinte qu'au moins un montage rclone fournit :
// les capacités sont lues dans le cache (mount_capabilities) et un montage n'est sondé, sur un fichier d'essai, que
// s'il n'y figure pas encore. Un montage sans fichier d'essai ou dont la sonde échoue est ignoré (rien n'est mémorisé).
func (registry Registry) CachedHashAlgorithms(ctx context.Context, cache CapabilityCache) ([]Algorithm, error) {
	offered := map[Algorithm]bool{}
	for _, provider := range registry.providers {
		if provider.spec.Type != config.FSProviderTypeRclone {
			continue
		}
		for _, mount := range provider.spec.Mounts {
			probe := func(probeCtx context.Context) (Capabilities, error) {
				return ProbeCapabilities(probeCtx, provider.source, findSampleFile(probeCtx, mount.LocalPath), "", nil)
			}
			capabilities, err := CachedCapabilities(ctx, cache, provider.spec.Name, mount.LocalPath, probe)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if err != nil {
				continue
			}
			for _, algorithm := range capabilities.Hashes {
				offered[algorithm] = true
			}
		}
	}
	return slices.DeleteFunc(slices.Clone(AllAlgorithms), func(algorithm Algorithm) bool { return !offered[algorithm] }), nil
}
