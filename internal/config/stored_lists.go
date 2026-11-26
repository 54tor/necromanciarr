package config

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"necromanciarr/internal/store"
)

// upstreamLists ont un amont cross-seed ; storedOnlyLists ne viennent que de la base (tout y est ajout).
var (
	upstreamLists   = []string{listRadarr, listSonarr, listTorrentClients, listLinkDirs}
	storedOnlyLists = []string{listTrackers, listTorznab, listFSProviders}
)

// ListNames rend les listes admises en base : celles à amont cross-seed, puis celles de la base seule.
func ListNames() []string { return slices.Concat(upstreamLists, storedOnlyLists) }

var valueOriginOfElement = map[ElementOrigin]string{
	OriginCrossSeed: originCrossSeed,
	OriginOverride:  originOverride,
	OriginAdded:     originAdded,
}

// resolvedLists est le bilan de la couche liste : origine de chaque élément et surcharges orphelines.
type resolvedLists struct {
	origins  []ValueOrigin
	orphans  []OrphanOverride
	upstream map[string][]Element
}

// applyStoredLists résout chaque liste (amont cross-seed + surcharges descellées) et range ses formes brutes dans
// rawFile, pour les analyseurs. Une surcharge d'une liste inconnue est rendue orpheline.
func applyStoredLists(rawFile *fileConfig, upstream map[string][]string, overrides []store.ConfigOverride, sealer *fieldSealer) (resolvedLists, error) {
	opened, err := openOverrides(overrides, sealer)
	if err != nil {
		return resolvedLists{}, err
	}
	result := resolvedLists{upstream: map[string][]Element{}}
	for _, list := range ListNames() {
		elements, err := upstreamElements(list, upstream[list])
		if err != nil {
			return resolvedLists{}, err
		}
		result.upstream[list] = elements
		resolved, orphans := ResolveList(list, elements, opened)
		if err := requireStoredNames(list, resolved); err != nil {
			return resolvedLists{}, err
		}
		if err := assignList(rawFile, list, resolved); err != nil {
			return resolvedLists{}, err
		}
		result.orphans = append(result.orphans, orphans...)
		result.origins = append(result.origins, listOrigins(list, resolved)...)
	}
	result.orphans = append(result.orphans, unknownListOrphans(opened)...)
	return result, nil
}

// requireStoredNames exige un name explicite pour chaque indexeur Torznab : un nom tiré du rang changerait à chaque ajout.
func requireStoredNames(list string, resolved []ResolvedElement) error {
	if list != listTorznab {
		return nil
	}
	for _, element := range resolved {
		if name, _ := element.Element["name"].(string); name == "" {
			return usageError{message: fmt.Sprintf("stored %s item %q: a \"name\" is required", list, element.ItemID)}
		}
	}
	return nil
}

func upstreamElements(list string, rawEntries []string) ([]Element, error) {
	elements := make([]Element, 0, len(rawEntries))
	for position, rawEntry := range rawEntries {
		element, err := ElementFromRaw(list, rawEntry)
		if err != nil {
			return nil, fmt.Errorf("cross-seed config.js: %s[%d]: %w", list, position, err)
		}
		elements = append(elements, element)
	}
	return elements, nil
}

// openOverrides descelle les valeurs avant la résolution : ElementID et Fingerprint ont besoin de l'URL en clair. Une
// valeur illisible reste telle quelle, ResolveList la rend orpheline.
func openOverrides(overrides []store.ConfigOverride, sealer *fieldSealer) ([]store.ConfigOverride, error) {
	opened := make([]store.ConfigOverride, 0, len(overrides))
	for _, override := range overrides {
		openedOverride, err := openOverride(override, sealer)
		if err != nil {
			return nil, err
		}
		opened = append(opened, openedOverride)
	}
	return opened, nil
}

// readableElement décode une surcharge ; illisible, elle passe telle quelle et ResolveList la signale orpheline.
func readableElement(value []byte) (Element, bool) {
	element, err := decodeElement(value)
	return element, err == nil
}

func openOverride(override store.ConfigOverride, sealer *fieldSealer) (store.ConfigOverride, error) {
	if override.Deleted {
		return override, nil
	}
	element, isReadable := readableElement(override.Value)
	if !isReadable {
		return override, nil
	}
	if err := sealer.OpenElement(override.List, override.ItemID, element); err != nil {
		return store.ConfigOverride{}, fmt.Errorf("stored %s item: %w", override.List, err)
	}
	if sealedInClearField(override.List, element) {
		return store.ConfigOverride{}, usageError{message: fmt.Sprintf("stored %s item %q: %s", override.List, override.ItemID, reasonOldFormat)}
	}
	if containsSealed(element) {
		return store.ConfigOverride{}, fmt.Errorf("stored %s item: %w", override.List, ErrSealedSecretUnreadable)
	}
	if err := RejectSecretInURL(override.List, element); err != nil {
		return store.ConfigOverride{}, fmt.Errorf("stored %s item: %w", override.List, err)
	}
	for field, value := range element {
		if containsReference(value) {
			return store.ConfigOverride{}, usageError{message: fmt.Sprintf(
				"stored %s item %q: field %s: ${...} references are not supported, store the value itself",
				override.List, override.ItemID, field)}
		}
	}
	encoded, err := json.Marshal(element)
	if err != nil {
		return store.ConfigOverride{}, fmt.Errorf("stored %s item: %w", override.List, ErrInvalidElement)
	}
	override.Value = encoded
	return override, nil
}

// containsReference repère une référence ${...} : une valeur stockée n'est jamais interpolée.
func containsReference(value any) bool {
	switch typed := value.(type) {
	case string:
		return strings.Contains(typed, "${")
	case []any:
		return slices.ContainsFunc(typed, containsReference)
	case map[string]any:
		for _, entry := range typed {
			if containsReference(entry) {
				return true
			}
		}
	}
	return false
}

func unknownListOrphans(overrides []store.ConfigOverride) []OrphanOverride {
	var orphans []OrphanOverride
	for _, override := range overrides {
		isKnownList := slices.Contains(upstreamLists, override.List) || slices.Contains(storedOnlyLists, override.List)
		if !isKnownList {
			orphans = append(orphans, OrphanOverride{List: override.List, ItemID: override.ItemID})
		}
	}
	return orphans
}

func listOrigins(list string, resolved []ResolvedElement) []ValueOrigin {
	origins := make([]ValueOrigin, 0, len(resolved))
	for _, element := range resolved {
		origins = append(origins, ValueOrigin{
			Key: list, ItemID: element.ItemID, Origin: valueOriginOfElement[element.Origin], Drift: element.Drift,
		})
	}
	return origins
}

func assignList(rawFile *fileConfig, list string, resolved []ResolvedElement) error {
	var err error
	switch list {
	case listRadarr:
		rawFile.Radarr, err = rawStrings(list, resolved)
	case listSonarr:
		rawFile.Sonarr, err = rawStrings(list, resolved)
	case listTorrentClients:
		rawFile.TorrentClients, err = rawStrings(list, resolved)
	case listLinkDirs:
		rawFile.LinkDirs, err = rawStrings(list, resolved)
	case listTorznab:
		rawFile.Torznab, err = rawMessages(list, resolved)
	case listFSProviders:
		rawFile.FSProviders, err = rawMessages(list, resolved)
	case listTrackers:
		rawFile.Trackers, err = trackersSection(resolved)
	}
	return err
}

func rawStrings(list string, resolved []ResolvedElement) ([]string, error) {
	var rawEntries []string
	for position, element := range resolved {
		rawEntry, err := RawFromElement(list, element.Element)
		if err != nil {
			return nil, fmt.Errorf("%s[%d]: %w", list, position, err)
		}
		text, isString := rawEntry.(string)
		if !isString {
			return nil, fmt.Errorf("%s[%d]: %w", list, position, ErrInvalidElement)
		}
		rawEntries = append(rawEntries, text)
	}
	return rawEntries, nil
}

func rawMessages(list string, resolved []ResolvedElement) ([]json.RawMessage, error) {
	var rawEntries []json.RawMessage
	for position, element := range resolved {
		rawEntry, err := RawFromElement(list, element.Element)
		if err != nil {
			return nil, fmt.Errorf("%s[%d]: %w", list, position, err)
		}
		message, isMessage := rawEntry.(json.RawMessage)
		if !isMessage {
			return nil, fmt.Errorf("%s[%d]: %w", list, position, ErrInvalidElement)
		}
		rawEntries = append(rawEntries, message)
	}
	return rawEntries, nil
}

func trackersSection(resolved []ResolvedElement) (json.RawMessage, error) {
	if len(resolved) == 0 {
		return nil, nil
	}
	rawTrackers, err := rawMessages(listTrackers, resolved)
	if err != nil {
		return nil, err
	}
	return TrackersSection(rawTrackers)
}

const reasonOldFormat = "sealed value in a clear field: item from an earlier format, rewrite or remove it"

// sealedInClearField repère une valeur scellée hors des champs secrets de la liste : une ligne d'un format antérieur.
func sealedInClearField(list string, element Element) bool {
	for field, value := range element {
		if slices.Contains(secretFieldsByList[list], field) {
			continue
		}
		if containsSealed(map[string]any{field: value}) {
			return true
		}
	}
	return false
}
