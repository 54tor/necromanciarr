package hashsource

import (
	"context"
	"errors"
	"os"
	"slices"
	"time"

	"necromanciarr/internal/linker"
	"necromanciarr/internal/store"
)

// ErrNoSampleFile : le montage ne montre aucun fichier d'essai, ses capacités restent inconnues.
var ErrNoSampleFile = errors.New("hashsource: no sample file to probe the mount")

// Capabilities dit ce qu'un montage permet, déterminé par essai réel et jamais déduit du nom du backend : MEGA S4
// (backend s3) a le MD5 et la date de modification, le backend mega classique n'a ni l'un ni l'autre.
type Capabilities struct {
	Hashes   []Algorithm
	ModTime  bool
	Symlinks bool
}

// Supports dit si le montage fournit cet algorithme.
func (capabilities Capabilities) Supports(algorithm Algorithm) bool {
	return slices.Contains(capabilities.Hashes, algorithm)
}

// ProbeCapabilities demande à source chaque algorithme de AllAlgorithms pour samplePath (un petit fichier réel du
// montage), lit sa date de modification et, si writableDir n'est pas vide, essaie un symlink dans ce dossier
// (linker.Probe y crée puis supprime des entrées d'essai).
func ProbeCapabilities(ctx context.Context, source HashSource, samplePath, writableDir string) (Capabilities, error) {
	if samplePath == "" {
		return Capabilities{}, ErrNoSampleFile
	}
	var capabilities Capabilities
	for _, algorithm := range AllAlgorithms {
		result, err := source.Hash(ctx, samplePath, algorithm)
		if err != nil {
			return Capabilities{}, err
		}
		if result.Available {
			capabilities.Hashes = append(capabilities.Hashes, algorithm)
		}
	}
	info, err := os.Stat(samplePath)
	if err != nil {
		return Capabilities{}, err
	}
	// Un montage sans date renvoie zéro ou l'époque Unix.
	capabilities.ModTime = !info.ModTime().IsZero() && info.ModTime().Unix() != 0
	if writableDir != "" {
		probed, err := linker.Probe(writableDir)
		if err != nil {
			return Capabilities{}, err
		}
		capabilities.Symlinks = probed.Symlink
	}
	return capabilities, nil
}

// CapabilityCache est la part de la base utilisée pour mémoriser les sondes ; implémenté par store.Store.
type CapabilityCache interface {
	LoadMountCapabilities(ctx context.Context, mountPath string) (store.MountCapabilities, bool, error)
	SaveMountCapabilities(ctx context.Context, capabilities store.MountCapabilities) error
}

// CachedCapabilities renvoie les capacités mémorisées du montage, sinon lance probe et mémorise son résultat. Un
// échec de la sonde n'est jamais mémorisé.
func CachedCapabilities(ctx context.Context, cache CapabilityCache, mountPath string, probe func(context.Context) (Capabilities, error)) (Capabilities, error) {
	stored, found, err := cache.LoadMountCapabilities(ctx, mountPath)
	if err != nil {
		return Capabilities{}, err
	}
	if found {
		return fromStored(stored), nil
	}
	probed, err := probe(ctx)
	if err != nil {
		return Capabilities{}, err
	}
	err = cache.SaveMountCapabilities(ctx, toStored(mountPath, probed))
	return probed, err
}

func fromStored(stored store.MountCapabilities) Capabilities {
	capabilities := Capabilities{ModTime: stored.ModTime, Symlinks: stored.Symlinks}
	for _, name := range stored.Hashes {
		capabilities.Hashes = append(capabilities.Hashes, Algorithm(name))
	}
	return capabilities
}

func toStored(mountPath string, capabilities Capabilities) store.MountCapabilities {
	stored := store.MountCapabilities{MountPath: mountPath, ModTime: capabilities.ModTime, Symlinks: capabilities.Symlinks, ProbedAt: time.Now().UTC()}
	for _, algorithm := range capabilities.Hashes {
		stored.Hashes = append(stored.Hashes, string(algorithm))
	}
	return stored
}
