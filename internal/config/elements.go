package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"

	"necromanciarr/internal/store"
)

// Element est une entrée d'une liste de configuration, décodée depuis du JSON.
type Element map[string]any

// ElementOrigin dit d'où vient un élément résolu.
type ElementOrigin string

const (
	OriginCrossSeed ElementOrigin = "cross-seed"
	OriginOverride  ElementOrigin = "override"
	OriginAdded     ElementOrigin = "added"
)

const collisionSuffixFormat = "%s#%d"

// ResolvedElement est un élément de liste après application des surcharges.
type ResolvedElement struct {
	ItemID  string
	Element Element
	Origin  ElementOrigin
	Drift   bool
}

// OrphanOverride est une surcharge dont l'élément amont a disparu.
type OrphanOverride struct{ List, ItemID string }

// ElementID calcule l'identité d'un élément selon sa liste.
func ElementID(list string, element Element) string {
	switch list {
	case "linkDirs":
		return stringField(element, "path")
	case "trackers":
		return stringField(element, "key")
	case "fsProviders":
		return stringField(element, "name")
	case "radarr", "sonarr", "torznab", "torrentClients":
		return endpointOf(stringField(element, "url"))
	}
	return ""
}

func stringField(element Element, name string) string {
	valeur, _ := element[name].(string)
	return valeur
}

func endpointOf(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return rawURL
	}
	return parsed.Scheme + "://" + parsed.Host
}

// AssignIDs donne un identifiant à chaque élément ; les collisions reçoivent #2, #3 dans l'ordre.
func AssignIDs(list string, elements []Element) []string {
	seenCount := make(map[string]int, len(elements))
	identifiers := make([]string, len(elements))
	for position, elementCourant := range elements {
		baseID := ElementID(list, elementCourant)
		seenCount[baseID]++
		identifiers[position] = baseID
		if seenCount[baseID] > 1 {
			identifiers[position] = fmt.Sprintf(collisionSuffixFormat, baseID, seenCount[baseID])
		}
	}
	return identifiers
}

// Fingerprint est le SHA-256 hexadécimal du JSON canonique de l'élément (clés triées par encoding/json).
func Fingerprint(element Element) string {
	encoded, _ := json.Marshal(element)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// ResolveList applique les surcharges de la liste à l'amont ; les orphelines sont rendues à part.
func ResolveList(list string, upstream []Element, overrides []store.ConfigOverride) ([]ResolvedElement, []OrphanOverride) {
	overridesByID := make(map[string]store.ConfigOverride, len(overrides))
	for _, overrideCourante := range overrides {
		if overrideCourante.List == list {
			overridesByID[overrideCourante.ItemID] = overrideCourante
		}
	}

	resolved := make([]ResolvedElement, 0, len(upstream))
	for position, identifier := range AssignIDs(list, upstream) {
		upstreamElement := upstream[position]
		overrideCourante, overridden := overridesByID[identifier]
		if !overridden {
			resolved = append(resolved, ResolvedElement{ItemID: identifier, Element: upstreamElement, Origin: OriginCrossSeed})
			continue
		}
		delete(overridesByID, identifier)
		if overrideCourante.Deleted {
			continue
		}
		replacement, err := decodeElement(overrideCourante.Value)
		if err != nil {
			continue
		}
		drift := overrideCourante.UpstreamFingerprint != "" &&
			overrideCourante.UpstreamFingerprint != Fingerprint(upstreamElement)
		resolved = append(resolved, ResolvedElement{ItemID: identifier, Element: replacement, Origin: OriginOverride, Drift: drift})
	}

	added, orphans := splitUnmatched(list, overridesByID)
	return append(resolved, added...), orphans
}

func splitUnmatched(list string, unmatched map[string]store.ConfigOverride) ([]ResolvedElement, []OrphanOverride) {
	identifiers := make([]string, 0, len(unmatched))
	for identifier := range unmatched {
		identifiers = append(identifiers, identifier)
	}
	sort.Strings(identifiers)

	var added []ResolvedElement
	var orphans []OrphanOverride
	for _, identifier := range identifiers {
		overrideCourante := unmatched[identifier]
		addedElement, err := decodeElement(overrideCourante.Value)
		isAddition := !overrideCourante.Deleted && overrideCourante.UpstreamFingerprint == "" && err == nil
		if !isAddition {
			orphans = append(orphans, OrphanOverride{List: list, ItemID: identifier})
			continue
		}
		added = append(added, ResolvedElement{ItemID: identifier, Element: addedElement, Origin: OriginAdded})
	}
	return added, orphans
}

func decodeElement(raw []byte) (Element, error) {
	var element Element
	if err := json.Unmarshal(raw, &element); err != nil {
		return nil, fmt.Errorf("decode config element: %w", err)
	}
	return element, nil
}
