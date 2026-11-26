package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

const (
	settingListen       = "listen"
	settingLibraryAPI   = "libraryApi"
	settingLinkType     = "linkType"
	settingSkipRecheck  = "skipRecheck"
	settingLinkCategory = "linkCategory"
	settingCrossSeed    = "crossSeed"
	settingLanguage     = "language"
)

// settingKeys sont, dans l'ordre d'affichage des origines, les seules clés admises dans la table settings.
var settingKeys = []string{
	settingListen, "readBytesPerSecond", "tieBreakReadBytes", settingLibraryAPI, "libraryRoots", "pathMappings",
	"prowlarr", "sourceOrder", "dedupe", settingLinkType, settingSkipRecheck, settingLinkCategory, settingCrossSeed,
	"crossSeedMode", "seedTags", "partialMaxMissingPercent", settingLanguage,
}

// settingEnvironment nomme la variable d'environnement qui l'emporte sur chaque réglage, quand elle existe.
var settingEnvironment = map[string]string{
	settingListen: envListen, settingLibraryAPI: envLibraryAPI, "crossSeedMode": envCrossSeedMode,
	"seedTags": envSeedTags, settingLinkCategory: envSeedCategory,
	"partialMaxMissingPercent": envPartialMaxMissingPercent, settingLanguage: envLanguage,
}

// settingFlags nomme l'option de ligne de commande qui l'emporte sur chaque réglage, quand elle existe.
var settingFlags = map[string]string{
	settingListen: "listen", settingLibraryAPI: "library-api", "readBytesPerSecond": "read-bytes-per-second",
	"crossSeedMode": "cross-seed-mode", settingLanguage: "language",
}

func settingTargets(rawFile *fileConfig) map[string]any {
	return map[string]any{
		settingListen: &rawFile.Listen, "readBytesPerSecond": &rawFile.ReadBytesPerSecond,
		"tieBreakReadBytes": &rawFile.TieBreakReadBytes, settingLibraryAPI: &rawFile.LibraryAPI,
		"libraryRoots": &rawFile.LibraryRoots, "pathMappings": &rawFile.PathMappings, "prowlarr": &rawFile.Prowlarr,
		"sourceOrder": &rawFile.SourceOrder, "dedupe": &rawFile.Dedupe, settingLinkType: &rawFile.LinkType,
		settingSkipRecheck: &rawFile.SkipRecheck, settingLinkCategory: &rawFile.LinkCategory,
		settingCrossSeed: &rawFile.CrossSeed, "crossSeedMode": &rawFile.CrossSeedMode, "seedTags": &rawFile.SeedTags,
		"partialMaxMissingPercent": &rawFile.PartialMaxMissingPercent, settingLanguage: &rawFile.Language,
	}
}

// applyStoredSettings range les réglages de la base dans rawFile ; une clé inconnue ou une valeur illisible est une
// erreur d'usage qui nomme la clé, jamais la valeur.
func applyStoredSettings(rawFile *fileConfig, settings map[string][]byte, sealer *fieldSealer) error {
	targets := settingTargets(rawFile)
	storedKeys := make([]string, 0, len(settings))
	for key := range settings {
		storedKeys = append(storedKeys, key)
	}
	slices.Sort(storedKeys)
	for _, key := range storedKeys {
		target, isKnown := targets[key]
		if !isKnown {
			return usageError{message: fmt.Sprintf("unknown stored setting %q", key)}
		}
		value, err := openedSettingValue(key, settings[key], sealer)
		if err != nil {
			return err
		}
		if err := decodeStrict(value, target); err != nil {
			return usageError{message: fmt.Sprintf("stored setting %q: unreadable value", key)}
		}
	}
	return nil
}

// openedSettingValue descelle un réglage secret scellé ; les autres valeurs passent telles quelles.
func openedSettingValue(key string, value []byte, sealer *fieldSealer) ([]byte, error) {
	var text string
	isSealedSecret := slices.Contains(secretSettings, key) && json.Unmarshal(value, &text) == nil &&
		strings.HasPrefix(text, sealedPrefix)
	if !isSealedSecret {
		return value, nil
	}
	plain, err := sealer.openSetting(key, text)
	if err != nil {
		return nil, err
	}
	return json.Marshal(plain)
}

func decodeStrict(value []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("unexpected data after the JSON value")
	}
	return nil
}

// originSources rassemble ce qui décide de l'origine d'un réglage.
type originSources struct {
	stored        map[string][]byte
	crossSeedKeys []string
	options       commandLine
	lookup        func(string) (string, bool)
}

// settingOrigins suit la priorité de Load : option > environnement > base > cross-seed > défaut.
func settingOrigins(sources originSources) []ValueOrigin {
	origins := make([]ValueOrigin, 0, len(settingKeys))
	for _, key := range settingKeys {
		origins = append(origins, ValueOrigin{Key: key, Origin: settingOrigin(key, sources)})
	}
	return origins
}

func settingOrigin(key string, sources originSources) string {
	if flagName, hasFlag := settingFlags[key]; hasFlag && sources.options.set[flagName] {
		return originFlag
	}
	if isEnvironmentSet(key, sources.lookup) {
		return originEnv
	}
	if _, isStored := sources.stored[key]; isStored {
		return originOverride
	}
	if slices.Contains(sources.crossSeedKeys, key) {
		return originCrossSeed
	}
	return originDefault
}

// isEnvironmentSet reprend le test d'applyOverrides : une variable vide ne compte pas (blanche, pour la langue).
func isEnvironmentSet(key string, lookup func(string) (string, bool)) bool {
	variable, hasVariable := settingEnvironment[key]
	if !hasVariable {
		return false
	}
	value, found := lookup(variable)
	if key == settingLanguage {
		value = strings.TrimSpace(value)
	}
	return found && value != ""
}
