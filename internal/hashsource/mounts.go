package hashsource

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// Mount associe un dossier local (point de montage rclone) à la partie « remote:sousChemin » qu'il expose.
type Mount struct {
	LocalPath string
	Remote    string
}

// mountTable est triée du plus long LocalPath au plus court : le premier montage qui contient un chemin est le plus
// spécifique.
type mountTable []Mount

func newMountTable(mounts []Mount) (mountTable, error) {
	table := slices.Clone(mounts)
	for _, mount := range table {
		if err := checkSafePath(mount.LocalPath); err != nil {
			return nil, fmt.Errorf("mount local path: %w", err)
		}
		if !strings.Contains(mount.Remote, ":") || strings.HasPrefix(mount.Remote, "-") || strings.ContainsRune(mount.Remote, 0) {
			return nil, fmt.Errorf("mount remote for %s: %w", mount.LocalPath, ErrNoMountForPath)
		}
	}
	slices.SortStableFunc(table, func(first, second Mount) int { return len(second.LocalPath) - len(first.LocalPath) })
	return table, nil
}

// checkSafePath n'accepte qu'un chemin absolu, déjà nettoyé, sans octet nul ni segment « .. » : rien d'ambigu n'est
// traduit en argument ou en requête.
func checkSafePath(path string) error {
	hasParentSegment := slices.Contains(strings.Split(filepath.ToSlash(path), "/"), "..")
	if path == "" || strings.ContainsRune(path, 0) || hasParentSegment || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrUnsafePath
	}
	return nil
}

// resolve renvoie le montage contenant path (frontière de segment, plus long préfixe) et le sous-chemin relatif, en
// barres obliques.
func (table mountTable) resolve(path string) (mount Mount, relative string, err error) {
	if err := checkSafePath(path); err != nil {
		return Mount{}, "", err
	}
	for _, candidate := range table {
		prefix := strings.TrimRight(candidate.LocalPath, string(filepath.Separator)) + string(filepath.Separator)
		if strings.HasPrefix(path, prefix) && len(path) > len(prefix) {
			return candidate, filepath.ToSlash(path[len(prefix):]), nil
		}
	}
	return Mount{}, "", ErrNoMountForPath
}
