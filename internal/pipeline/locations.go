package pipeline

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"necromanciarr/internal/fsys"
)

// relativeSeparator : une WriteRoot reçoit toujours ses chemins relatifs en « / », en local comme à distance.
const relativeSeparator = "/"

// pathsOf rend la syntaxe des chemins du volume : filepath en local, POSIX à distance.
func pathsOf(location fsys.Location) fsys.PathSyntax {
	if location.Volume == "" {
		return fsys.LocalPaths()
	}
	return fsys.RemotePaths()
}

// storedIn rend la forme enregistrée (inventaire, liens, étapes) du chemin name du volume : brute en local,
// sftp://<fournisseur>/<chemin> à distance.
func storedIn(volume, name string) string {
	return fsys.Location{Volume: volume, Path: name}.String()
}

// joinLocation joint des segments à base, dans la syntaxe de son volume.
func joinLocation(base fsys.Location, segments ...string) fsys.Location {
	return fsys.Location{Volume: base.Volume, Path: pathsOf(base).Join(append([]string{base.Path}, segments...)...)}
}

// absoluteOf rend le chemin du volume d'un chemin relatif en « / » sous base.
func absoluteOf(base fsys.Location, relative string) string {
	if base.Volume == "" {
		relative = filepath.FromSlash(relative)
	}
	return pathsOf(base).Join(base.Path, relative)
}

// errOutsideBase : un chemin hors de la base (autre volume, « .. ») n'a pas de forme relative sous elle.
var errOutsideBase = errors.New("pipeline: path is not below its base")

// relativeIn rend target relatif à base, en « / » ; refus hors de base ou sur un autre volume. La WriteRoot refuse
// de toute façon tout échappement : ce contrôle évite seulement de lui passer un chemin absurde.
func relativeIn(base, target fsys.Location) (string, error) {
	if base.Volume != target.Volume {
		return "", errOutsideBase
	}
	relative, err := pathsOf(base).Rel(base.Path, target.Path)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errOutsideBase, err)
	}
	if base.Volume == "" {
		relative = filepath.ToSlash(relative)
	}
	isEscape := relative == "." || relative == ".." || strings.HasPrefix(relative, "../")
	if isEscape {
		return "", errOutsideBase
	}
	return relative, nil
}

// storedLocations relit des chemins de la configuration (linkDirs) ; une adresse invalide, refusée au chargement de
// la configuration, est ignorée.
func storedLocations(stored []string) []fsys.Location {
	locations := make([]fsys.Location, 0, len(stored))
	for _, entry := range stored {
		if location, err := locationOfStoredPath(entry); err == nil {
			locations = append(locations, location)
		}
	}
	return locations
}

// statStored lit, par le registre, l'information d'un chemin enregistré (en suivant les liens symboliques).
func statStored(registry *fsys.Registry, stored string) (fs.FileInfo, error) {
	location, err := locationOfStoredPath(stored)
	if err != nil {
		return nil, err
	}
	readFS, name, err := readRegistryOrLocal(registry).Read(location)
	if err != nil {
		return nil, err
	}
	return readFS.Stat(name)
}

// isRemoteUnavailable : une coupure du volume distant laisse la proposition en l'état, reprise à la relance.
func isRemoteUnavailable(err error) bool {
	return errors.Is(err, fsys.ErrRemoteUnavailable)
}
