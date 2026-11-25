package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"necromanciarr/internal/config"
	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/sftpfs"
)

// capabilityStore garde la dernière sonde d'un volume, pour l'affichage ; *store.Store convient.
type capabilityStore interface {
	SaveVolumeCapabilities(ctx context.Context, volume string, capabilities fsys.Capabilities, at time.Time) error
}

// sftpVolumes est l'ensemble des volumes sftp du registre ; Close les ferme tous (aucune connexion ne survit au
// service ni à une commande ponctuelle).
type sftpVolumes struct {
	volumes map[string]*sftpfs.Volume
	store   atomic.Pointer[capabilityStore]

	savedMutex sync.Mutex
	saved      map[string]fsys.Capabilities
}

// newSFTPVolume prépare le volume d'un fournisseur sans le joindre (sftpfs.NewLazy) : seuls ses chemins de
// bibliothèque lui sont donnés, et jamais le plafond global (option A de C10 : torrentcheck, dedupe et hashsource le
// paient déjà avant chaque lecture).
func newSFTPVolume(provider config.FSProvider, libraryRoots []fsys.Location, deps sftpfs.Deps) *sftpfs.Volume {
	deps.Limiter, deps.LibraryRoots = nil, nil
	for _, libraryRoot := range libraryRoots {
		if libraryRoot.Volume == provider.Name {
			deps.LibraryRoots = append(deps.LibraryRoots, libraryRoot.Path)
		}
	}
	return sftpfs.NewLazy(provider, deps)
}

// saveProbesTo enregistre désormais chaque nouvelle sonde dans capabilities.
func (remote *sftpVolumes) saveProbesTo(capabilities capabilityStore) {
	remote.store.Store(&capabilities)
}

// ensureProbed est le contrôle de chaque racine d'écriture du volume, posé dans le registre : avant qu'une étape ne
// reçoive la racine, ses capacités sont sondées dans cette même racine (une fois par session, gardées par sftpfs) et
// enregistrées. Un échec de la sonde refuse la racine : rien n'est écrit sur des capacités non prouvées.
func (remote *sftpVolumes) ensureProbed(ctx context.Context, name string) func(fsys.WriteRoot) error {
	volume := remote.volumes[name]
	return func(root fsys.WriteRoot) error {
		capabilities, err := volume.ProbeCapabilities(ctx, root)
		if err != nil {
			return fmt.Errorf("app: probe sftp volume %q: %w", name, err)
		}
		return remote.record(ctx, name, capabilities)
	}
}

// record n'écrit en base qu'une sonde différente de la dernière enregistrée par ce processus.
func (remote *sftpVolumes) record(ctx context.Context, name string, capabilities fsys.Capabilities) error {
	stored := remote.store.Load()
	if stored == nil {
		return nil
	}
	remote.savedMutex.Lock()
	defer remote.savedMutex.Unlock()
	if previous, known := remote.saved[name]; known && reflect.DeepEqual(previous, capabilities) {
		return nil
	}
	if err := (*stored).SaveVolumeCapabilities(ctx, name, capabilities, time.Now()); err != nil {
		return fmt.Errorf("app: save sftp volume %q capabilities: %w", name, err)
	}
	remote.saved[name] = capabilities
	return nil
}

// Close ferme chaque volume ; il ne se reconnecte plus.
func (remote *sftpVolumes) Close() error {
	var closeErrors []error
	for _, volume := range remote.volumes {
		closeErrors = append(closeErrors, volume.Close())
	}
	return errors.Join(closeErrors...)
}

// serviceCloser ferme les volumes sftp puis le store ; Ping reste la sonde de santé du store (web).
type serviceCloser struct {
	store interface {
		Close() error
		Ping(ctx context.Context) error
	}
	volumes interface{ Close() error }
}

func (closer serviceCloser) Close() error {
	return errors.Join(closer.volumes.Close(), closer.store.Close())
}

func (closer serviceCloser) Ping(ctx context.Context) error { return closer.store.Ping(ctx) }
