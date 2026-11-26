package app

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"necromanciarr/internal/config"
	"necromanciarr/internal/store"
)

// itemOverride prépare la surcharge d'un élément : un secret absent ou null garde la valeur enregistrée (scellée) ou,
// à défaut, celle de cross-seed ; les secrets fournis sont scellés ; l'empreinte amont date la surcharge.
func (svc *service) itemOverride(snapshot storedSnapshot, list, itemID string, element map[string]any) (store.ConfigOverride, error) {
	candidate := maps.Clone(element)
	omitted := dropOmittedSecrets(list, candidate)
	if itemID == "" {
		itemID = config.ElementID(list, candidate)
	}
	if itemID == "" {
		return store.ConfigOverride{}, fmt.Errorf("%w: %s: an item id is required", config.ErrUsage, list)
	}
	upstream, isUpstream := svc.upstreamElement(list, itemID)
	stored, hasStored := snapshot.override(list, itemID)
	keepsStoredSecrets := hasStored && !stored.Deleted
	if !keepsStoredSecrets && isUpstream {
		copyFields(candidate, upstream, omitted)
	}
	if err := svc.sealElement(list, itemID, candidate); err != nil {
		return store.ConfigOverride{}, err
	}
	if keepsStoredSecrets {
		if err := copyStoredSecrets(candidate, stored, omitted); err != nil {
			return store.ConfigOverride{}, err
		}
	}
	value, err := json.Marshal(candidate)
	if err != nil {
		return store.ConfigOverride{}, fmt.Errorf("%w: %s item %q: unreadable element", config.ErrUsage, list, itemID)
	}
	override := store.ConfigOverride{List: list, ItemID: itemID, Value: value}
	if isUpstream {
		override.UpstreamFingerprint = config.Fingerprint(upstream)
	}
	return override, nil
}

func (svc *service) sealElement(list, itemID string, element map[string]any) error {
	sealer, err := svc.sealer()
	if err != nil {
		return err
	}
	return sealer.SealElement(list, itemID, element)
}

// dropOmittedSecrets retire les secrets null et rend les champs secrets que l'élément ne fournit pas.
func dropOmittedSecrets(list string, element map[string]any) []string {
	var omitted []string
	for _, field := range config.SecretFields(list) {
		value, present := element[field]
		if present && value != nil {
			continue
		}
		delete(element, field)
		omitted = append(omitted, field)
	}
	return omitted
}

func copyFields(target, source map[string]any, fields []string) {
	for _, field := range fields {
		if value, present := source[field]; present {
			target[field] = value
		}
	}
}

func copyStoredSecrets(target map[string]any, stored store.ConfigOverride, fields []string) error {
	var storedElement map[string]any
	if err := json.Unmarshal(stored.Value, &storedElement); err != nil {
		return fmt.Errorf("%w: stored %s item %q: unreadable element", config.ErrUsage, stored.List, stored.ItemID)
	}
	copyFields(target, storedElement, fields)
	return nil
}

// upstreamElement rend l'élément importé de cross-seed sous cet identifiant (secrets en clair).
func (svc *service) upstreamElement(list, itemID string) (config.Element, bool) {
	elements := svc.deps.Config.UpstreamElements[list]
	position := slices.Index(config.AssignIDs(list, elements), itemID)
	if position < 0 {
		return nil, false
	}
	return elements[position], true
}
