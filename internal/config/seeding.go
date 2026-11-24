package config

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	defaultSeedTag = "necromanciarr"
	// maxSeedTags et maxSeedTagLength bornent ce que l'on envoie au client torrent à chaque ajout.
	maxSeedTags      = 20
	maxSeedTagLength = 64
)

// normalizeSeedTags retire les espaces et les doublons ; une liste vide garde le tag par défaut. La virgule est
// refusée : qBittorrent sépare les tags par des virgules.
func normalizeSeedTags(rawTags []string) ([]string, error) {
	var tags []string
	for tagIndex, rawTag := range rawTags {
		tag := strings.TrimSpace(rawTag)
		switch {
		case tag == "":
			return nil, fmt.Errorf("seedTags[%d]: empty tag", tagIndex)
		case strings.Contains(tag, ","):
			return nil, fmt.Errorf("seedTags[%d]: a tag cannot contain a comma", tagIndex)
		case utf8.RuneCountInString(tag) > maxSeedTagLength:
			return nil, fmt.Errorf("seedTags[%d]: longer than %d characters", tagIndex, maxSeedTagLength)
		}
		if !slices.Contains(tags, tag) {
			tags = append(tags, tag)
		}
	}
	if len(tags) > maxSeedTags {
		return nil, fmt.Errorf("seedTags: at most %d tags", maxSeedTags)
	}
	if len(tags) == 0 {
		return []string{defaultSeedTag}, nil
	}
	return tags, nil
}

// defaultPartialMaxMissingPercent ne plafonne rien : un pourcentage de pièces manquantes ne dépasse jamais 100.
const defaultPartialMaxMissingPercent = 100.0

// applyPartialCapOverride lit le plafond de l'environnement, qui l'emporte sur config.json.
func applyPartialCapOverride(target *Config, lookup func(string) (string, bool)) error {
	rawValue, found := lookup(envPartialMaxMissingPercent)
	if !found || rawValue == "" {
		return nil
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(rawValue), 64)
	if err != nil {
		return fmt.Errorf("partialMaxMissingPercent (%s): not a number", envPartialMaxMissingPercent)
	}
	target.PartialMaxMissingPercent = parsed
	return nil
}

func validatePartialCap(percent float64) error {
	isInRange := percent >= 0 && percent <= 100
	if !isInRange {
		return errors.New("partialMaxMissingPercent must be between 0 and 100")
	}
	return nil
}
