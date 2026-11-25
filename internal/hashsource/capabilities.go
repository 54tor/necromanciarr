package hashsource

import (
	"context"
	"errors"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"time"

	"necromanciarr/internal/fsys"
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
// montage), lit sa date de modification et, si writableDir n'est pas vide, essaie un symlink dans ce dossier, ouvert
// par registry (linker.ProbeDirectory y crée puis supprime des entrées d'essai) ; registry nil refuse cette sonde
// (fsys.ErrNoLibraryRoots), la lecture des empreintes n'en a pas besoin.
func ProbeCapabilities(ctx context.Context, source HashSource, samplePath, writableDir string, registry *fsys.Registry) (Capabilities, error) {
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
	if _, err := os.Stat(samplePath); err != nil {
		return Capabilities{}, err
	}
	if writableDir != "" {
		probed, err := probeWritableDir(registry, writableDir)
		if err != nil {
			return Capabilities{}, err
		}
		capabilities.Symlinks = probed.Symlink
		capabilities.ModTime, err = probeModTime(registry, writableDir)
		if err != nil {
			return Capabilities{}, err
		}
	}
	return capabilities, nil
}

// probeWritableDir n'ouvre writableDir que par le registre, qui refuse un dossier recouvrant la bibliothèque.
func probeWritableDir(registry *fsys.Registry, writableDir string) (linker.Capabilities, error) {
	if registry == nil {
		return linker.Capabilities{}, fsys.ErrNoLibraryRoots
	}
	return linker.ProbeDirectory(registry, fsys.Location{Path: writableDir})
}

// CapabilityCache est la part de la base utilisée pour mémoriser les sondes ; implémenté par store.Store.
type CapabilityCache interface {
	LoadMountCapabilities(ctx context.Context, mountPath string) (store.MountCapabilities, bool, error)
	SaveMountCapabilities(ctx context.Context, capabilities store.MountCapabilities) error
}

// CachedCapabilities (providerName : fournisseur qui porte le montage) renvoie les capacités mémorisées du montage, sinon lance probe et mémorise son résultat. Un
// échec de la sonde n'est jamais mémorisé.
func CachedCapabilities(ctx context.Context, cache CapabilityCache, providerName, mountPath string, probe func(context.Context) (Capabilities, error)) (Capabilities, error) {
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
	err = cache.SaveMountCapabilities(ctx, toStored(providerName, mountPath, probed))
	return probed, err
}

func fromStored(stored store.MountCapabilities) Capabilities {
	capabilities := Capabilities{ModTime: stored.ModTime, Symlinks: stored.Symlinks}
	for _, name := range stored.Hashes {
		capabilities.Hashes = append(capabilities.Hashes, Algorithm(name))
	}
	return capabilities
}

func toStored(providerName, mountPath string, capabilities Capabilities) store.MountCapabilities {
	stored := store.MountCapabilities{MountPath: mountPath, Provider: providerName, ModTime: capabilities.ModTime, Symlinks: capabilities.Symlinks, ProbedAt: time.Now().UTC()}
	for _, algorithm := range capabilities.Hashes {
		stored.Hashes = append(stored.Hashes, string(algorithm))
	}
	return stored
}

// probeModTime crée une entrée d'essai dans la racine d'écriture de directory (ouverte par le registre), lui fixe une
// date ancienne puis la relit : un stockage qui garde l'heure d'envoi (backend mega classique) ne la restitue pas. Une
// racine sans réglage des dates (fsys.TimesSetter) ou qui le refuse laisse la date inconnue (false).
func probeModTime(registry *fsys.Registry, directory string) (bool, error) {
	if registry == nil {
		return false, fsys.ErrNoLibraryRoots
	}
	root, err := registry.WriteRoot(fsys.Location{Path: directory})
	if err != nil {
		return false, err
	}
	defer func() { _ = root.Close() }()
	probeName, err := createProbeEntry(root)
	if err != nil {
		return false, err
	}
	defer func() { _ = root.Remove(probeName) }()
	oldTime := time.Unix(probeModTimeSeconds, 0)
	if err := setModTime(root, probeName, oldTime, oldTime); err != nil {
		return false, nil //nolint:nilerr // un montage qui refuse Chtimes n'a pas de date réglable
	}
	info, err := root.Lstat(probeName)
	if err != nil {
		return false, err
	}
	return info.ModTime().Unix() == probeModTimeSeconds, nil
}

// probeEntryPrefix nomme l'entrée d'essai ; un suffixe aléatoire de 64 bits évite la collision entre deux sondes.
const probeEntryPrefix = ".necromanciarr-probe-mtime-"

func createProbeEntry(root fsys.WriteRoot) (string, error) {
	probeName := probeEntryPrefix + strconv.FormatUint(rand.Uint64(), 36) // #nosec G404 -- nom d'essai, pas un secret
	probeFile, err := root.CreateExclusive(probeName, 0o600)
	if err != nil {
		return "", err
	}
	if err := probeFile.Close(); err != nil {
		return "", errors.Join(err, root.Remove(probeName))
	}
	return probeName, nil
}

// setTimesIn fixe les dates par l'option de la racine ; sans elle, le réglage est refusé.
func setTimesIn(root fsys.WriteRoot, relative string, accessTime, modTime time.Time) error {
	setter, supported := root.(fsys.TimesSetter)
	if !supported {
		return errors.ErrUnsupported
	}
	return setter.Chtimes(relative, accessTime, modTime)
}

// probeModTimeSeconds : 2001-09-09, loin de toute heure d'envoi plausible.
const probeModTimeSeconds = 1000000000

// setModTime est remplaçable dans les tests pour simuler un stockage qui garde l'heure d'envoi.
var setModTime = setTimesIn
