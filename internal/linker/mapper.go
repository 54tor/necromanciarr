// Package linker traduit les chemins entre les vues du stockage et reconstitue l'arborescence d'un torrent
// par des liens vers les fichiers de la bibliothèque, sans jamais modifier ces fichiers.
package linker

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"necromanciarr/internal/config"
)

// ErrNoRule signale qu'aucune règle de mapping ne couvre le chemin demandé.
var ErrNoRule = errors.New("linker: no path rule matches")

const (
	slashSeparator   = "/"
	windowsSeparator = `\`
	uncPrefix        = `\\`
)

// Mapper applique les règles de mapping de chemins.
//
// Règle de nettoyage : chemins et préfixes sont nettoyés selon la sémantique de path (« / », « .. » résolu avant la
// comparaison). Un côté de règle qui commence par une lettre de lecteur (« D: ») ou par « \\ » est traité comme un
// chemin Windows : « \ » y vaut « / », la comparaison ignore la casse et le résultat reprend « \ ». Un préfixe ne
// correspond que sur une frontière de segment ; à égalité de longueur, la première règle l'emporte.
type Mapper struct {
	rules []config.PathRule
}

// NewMapper construit un Mapper sur une copie des règles.
func NewMapper(rules []config.PathRule) *Mapper {
	return &Mapper{rules: slices.Clone(rules)}
}

// Translate traduit un chemin du côté From vers le côté To ; la règle au préfixe le plus long l'emporte.
func (mapper *Mapper) Translate(original string) (string, error) {
	return mapper.remap(original, func(rule config.PathRule) (string, string) { return rule.From, rule.To })
}

// Reverse traduit un chemin du côté To vers le côté From, avec la même règle du préfixe le plus long.
func (mapper *Mapper) Reverse(original string) (string, error) {
	return mapper.remap(original, func(rule config.PathRule) (string, string) { return rule.To, rule.From })
}

func (mapper *Mapper) remap(original string, sides func(config.PathRule) (string, string)) (string, error) {
	cleanedPath := normalize(original)
	bestLength := -1
	var bestRest, bestTarget string
	for _, rule := range mapper.rules {
		prefix, target := sides(rule)
		if prefix == "" {
			continue
		}
		boundary := strings.TrimSuffix(normalize(prefix), slashSeparator)
		rest, matched := cutSegmentPrefix(cleanedPath, boundary, isWindowsStyle(prefix))
		if matched && len(boundary) > bestLength {
			bestLength, bestRest, bestTarget = len(boundary), rest, target
		}
	}
	if bestLength < 0 {
		return "", fmt.Errorf("%w: %s", ErrNoRule, original)
	}
	return joinTarget(bestTarget, bestRest), nil
}

func cutSegmentPrefix(cleanedPath, boundary string, ignoreCase bool) (string, bool) {
	if len(cleanedPath) < len(boundary) {
		return "", false
	}
	head, rest := cleanedPath[:len(boundary)], cleanedPath[len(boundary):]
	headMatches := head == boundary || (ignoreCase && strings.EqualFold(head, boundary))
	onSegmentBoundary := rest == "" || strings.HasPrefix(rest, slashSeparator)
	return rest, headMatches && onSegmentBoundary
}

func joinTarget(target, rest string) string {
	joined := strings.TrimSuffix(normalize(target), slashSeparator) + rest
	if joined == "" || isBareDrive(joined) {
		joined += slashSeparator
	}
	if isWindowsStyle(target) {
		return strings.ReplaceAll(joined, slashSeparator, windowsSeparator)
	}
	return joined
}

func normalize(original string) string {
	if isWindowsStyle(original) {
		original = strings.ReplaceAll(original, windowsSeparator, slashSeparator)
	}
	// path.Clean réduit « // » en « / » : on garde le double slash d'un chemin UNC.
	if strings.HasPrefix(original, "//") {
		return slashSeparator + path.Clean(original[1:])
	}
	return path.Clean(original)
}

func isWindowsStyle(candidate string) bool {
	return hasDriveLetter(candidate) || strings.HasPrefix(candidate, uncPrefix)
}

func hasDriveLetter(candidate string) bool {
	if len(candidate) < 2 || candidate[1] != ':' {
		return false
	}
	letter := candidate[0]
	return ('a' <= letter && letter <= 'z') || ('A' <= letter && letter <= 'Z')
}

func isBareDrive(candidate string) bool {
	return len(candidate) == 2 && hasDriveLetter(candidate)
}
