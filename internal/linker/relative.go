package linker

import (
	"errors"
	"fmt"
	"path/filepath"
)

// ErrNoRelativePath signale qu'aucun chemin relatif ne relie le lien à sa cible, comme entre deux volumes
// Windows.
var ErrNoRelativePath = errors.New("linker: no relative path between link and target")

// RelativeSymlinkTarget renvoie la cible relative à écrire dans un lien symbolique situé à linkPath et visant
// targetPath. Les dossiers des deux chemins sont résolus (EvalSymlinks) : le lien est interprété depuis son dossier
// réel, pas depuis le chemin par lequel on l'a atteint.
func RelativeSymlinkTarget(linkPath, targetPath string) (string, error) {
	linkDir, err := resolvedDirectory(linkPath)
	if err != nil {
		return "", err
	}
	targetDir, err := resolvedDirectory(targetPath)
	if err != nil {
		return "", err
	}
	return relativeBetween(linkDir, targetDir, filepath.Base(targetPath))
}

func resolvedDirectory(path string) (string, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(filepath.Dir(absolutePath))
}

func relativeBetween(linkDir, targetDir, targetName string) (string, error) {
	relativeDir, err := filepath.Rel(linkDir, targetDir)
	if err != nil {
		return "", fmt.Errorf("%w: %s -> %s: %w", ErrNoRelativePath, linkDir, targetDir, err)
	}
	return filepath.Join(relativeDir, targetName), nil
}
