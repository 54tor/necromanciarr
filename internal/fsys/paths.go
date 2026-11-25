package fsys

import (
	"errors"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// PathSyntax isole filepath (local) de path (distant, toujours POSIX).
type PathSyntax interface {
	Join(elements ...string) string
	Rel(base, target string) (string, error)
	Dir(name string) string
	Base(name string) string
	IsAbs(name string) bool
	Clean(name string) string
}

// errNotRelative : comme filepath.Rel, refus quand base et cible ne se rattachent pas sans connaître le disque.
var errNotRelative = errors.New("fsys: target cannot be made relative to base")

type localPaths struct{}

func (localPaths) Join(elements ...string) string          { return filepath.Join(elements...) }
func (localPaths) Rel(base, target string) (string, error) { return filepath.Rel(base, target) }
func (localPaths) Dir(name string) string                  { return filepath.Dir(name) }
func (localPaths) Base(name string) string                 { return filepath.Base(name) }
func (localPaths) IsAbs(name string) bool                  { return filepath.IsAbs(name) }
func (localPaths) Clean(name string) string                { return filepath.Clean(name) }

type remotePaths struct{}

func (remotePaths) Join(elements ...string) string { return path.Join(elements...) }
func (remotePaths) Dir(name string) string         { return path.Dir(name) }
func (remotePaths) Base(name string) string        { return path.Base(name) }
func (remotePaths) IsAbs(name string) bool         { return path.IsAbs(name) }
func (remotePaths) Clean(name string) string       { return path.Clean(name) }

// Rel suit filepath.Rel avec la syntaxe POSIX : le paquet path n'en a pas.
func (remotePaths) Rel(base, target string) (string, error) {
	cleanBase, cleanTarget := path.Clean(base), path.Clean(target)
	if path.IsAbs(cleanBase) != path.IsAbs(cleanTarget) {
		return "", errNotRelative
	}
	if cleanBase == cleanTarget {
		return ".", nil
	}
	baseSegments, targetSegments := posixSegments(cleanBase), posixSegments(cleanTarget)
	commonCount := 0
	for commonCount < len(baseSegments) && commonCount < len(targetSegments) &&
		baseSegments[commonCount] == targetSegments[commonCount] {
		commonCount++
	}
	if slices.Contains(baseSegments[commonCount:], "..") {
		return "", errNotRelative
	}
	relativeSegments := slices.Repeat([]string{".."}, len(baseSegments)-commonCount)
	relativeSegments = append(relativeSegments, targetSegments[commonCount:]...)
	return strings.Join(relativeSegments, "/"), nil
}

// posixSegments découpe un chemin déjà nettoyé ; "/" et "." n'ont aucun segment.
func posixSegments(cleaned string) []string {
	trimmed := strings.Trim(cleaned, "/")
	if trimmed == "" || trimmed == "." {
		return nil
	}
	return strings.Split(trimmed, "/")
}

// LocalPaths rend la syntaxe du disque local (filepath).
func LocalPaths() PathSyntax { return localPaths{} }

// RemotePaths rend la syntaxe des volumes distants : toujours POSIX (path), même sous Windows.
func RemotePaths() PathSyntax { return remotePaths{} }
