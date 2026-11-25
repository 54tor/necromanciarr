package linker

import (
	"errors"
	"fmt"

	"necromanciarr/internal/fsys/local"
)

// ErrNoLinkDirOnSameDevice signale qu'aucun répertoire de liens n'est sur le système de fichiers de la source ;
// l'appelant choisit alors un lien symbolique ou renonce.
var ErrNoLinkDirOnSameDevice = errors.New("linker: no link directory on the source filesystem")

// ChooseLinkDir renvoie le premier répertoire de linkDirs, dans l'ordre donné, situé sur le même système de fichiers
// que sourcePath (périphérique sous Unix, volume sous Windows). Un répertoire illisible est ignoré.
func ChooseLinkDir(sourcePath string, linkDirs []string) (string, error) {
	return chooseLinkDir(sourcePath, linkDirs, local.DeviceKey)
}

func chooseLinkDir(sourcePath string, linkDirs []string, deviceOf func(string) (string, error)) (string, error) {
	sourceDevice, err := deviceOf(sourcePath)
	if err != nil {
		return "", fmt.Errorf("linker: source device: %w", err)
	}
	skipped := []error{fmt.Errorf("%w: %s", ErrNoLinkDirOnSameDevice, sourcePath)}
	for _, linkDir := range linkDirs {
		linkDirDevice, err := deviceOf(linkDir)
		if err != nil {
			skipped = append(skipped, err)
			continue
		}
		if linkDirDevice == sourceDevice {
			return linkDir, nil
		}
	}
	return "", errors.Join(skipped...)
}
