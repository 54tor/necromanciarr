package linker

import (
	"errors"
	"fmt"

	"necromanciarr/internal/fsys"
)

// ErrNoLinkDirOnSameDevice signale qu'aucun répertoire de liens n'est sur le système de fichiers de la source ;
// l'appelant choisit alors un lien symbolique ou renonce.
var ErrNoLinkDirOnSameDevice = errors.New("linker: no link directory on the source filesystem")

// ErrVolumeIdentityUnknown : le volume ne sait pas dire sur quel système de fichiers est un chemin ; rien ne prouve
// alors qu'un lien dur y passerait.
var ErrVolumeIdentityUnknown = errors.New("linker: filesystem identity unknown")

// volumeIdentity rend l'identité du système de fichiers d'une Location (VolumeID du volume).
type volumeIdentity func(location fsys.Location) (id string, known bool, err error)

// ChooseLinkDir renvoie le premier répertoire de linkDirs, dans l'ordre donné, situé sur le même volume que
// sourcePath et sur le même système de fichiers (VolumeID lu par registry : périphérique sous Unix, volume sous
// Windows, fsid à distance). Un répertoire d'un autre volume est écarté ; un répertoire illisible ou d'identité
// inconnue aussi, son erreur jointe à ErrNoLinkDirOnSameDevice.
func ChooseLinkDir(sourcePath fsys.Location, linkDirs []fsys.Location, registry *fsys.Registry) (fsys.Location, error) {
	return chooseLinkDir(sourcePath, linkDirs, registryIdentity(registry))
}

func registryIdentity(registry *fsys.Registry) volumeIdentity {
	return func(location fsys.Location) (string, bool, error) {
		readFS, name, err := registry.Read(location)
		if err != nil {
			return "", false, err
		}
		return readFS.VolumeID(name)
	}
}

func chooseLinkDir(sourcePath fsys.Location, linkDirs []fsys.Location, identityOf volumeIdentity) (fsys.Location, error) {
	sourceID, err := knownIdentity(sourcePath, identityOf)
	if err != nil {
		return fsys.Location{}, fmt.Errorf("linker: source device: %w", err)
	}
	skipped := []error{fmt.Errorf("%w: %s", ErrNoLinkDirOnSameDevice, sourcePath)}
	for _, linkDir := range linkDirs {
		if linkDir.Volume != sourcePath.Volume {
			continue
		}
		linkDirID, err := knownIdentity(linkDir, identityOf)
		if err != nil {
			skipped = append(skipped, err)
			continue
		}
		if linkDirID == sourceID {
			return linkDir, nil
		}
	}
	return fsys.Location{}, errors.Join(skipped...)
}

func knownIdentity(location fsys.Location, identityOf volumeIdentity) (string, error) {
	id, known, err := identityOf(location)
	if err != nil {
		return "", err
	}
	if !known {
		return "", fmt.Errorf("%w: %s", ErrVolumeIdentityUnknown, location)
	}
	return id, nil
}
