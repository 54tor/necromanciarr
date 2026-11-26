package app

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"necromanciarr/internal/config"
	"necromanciarr/internal/store"
)

// destinationFields désignent la destination des secrets d'un élément (url en clair : schéma, hôte, port et chemin) : un
// secret omis n'y est jamais reconduit vers une autre destination.
var destinationFields = map[string][]string{
	"radarr": {"url"}, "sonarr": {"url"}, "torrentClients": {"url"}, "torznab": {"url"}, "fsProviders": {"address"},
}

// itemOverride prépare la surcharge d'un élément : un secret absent ou null garde la valeur précédente (enregistrée,
// sinon cross-seed) si la destination ne change pas ; les secrets sont scellés ; l'empreinte amont date la surcharge.
func (svc *service) itemOverride(snapshot storedSnapshot, list, itemID string, element map[string]any) (store.ConfigOverride, error) {
	if err := config.RejectSecretInURL(list, element); err != nil {
		return store.ConfigOverride{}, err
	}
	candidate := maps.Clone(element)
	omitted := dropOmittedSecrets(list, candidate)
	if err := svc.requireDerivableItemID(snapshot, list, itemID); err != nil {
		return store.ConfigOverride{}, err
	}
	if itemID == "" {
		itemID = config.ElementID(list, candidate)
	}
	if itemID == "" {
		return store.ConfigOverride{}, fmt.Errorf("%w: %s: an item id is required", config.ErrUsage, list)
	}
	sealer, err := svc.sealer()
	if err != nil {
		return store.ConfigOverride{}, err
	}
	upstream, isUpstream := svc.upstreamElement(list, itemID)
	previous, err := previousElement(snapshot, sealer, list, itemID, upstream)
	if err != nil {
		return store.ConfigOverride{}, err
	}
	if err := carryOmittedSecrets(list, itemID, candidate, previous, omitted); err != nil {
		return store.ConfigOverride{}, err
	}
	if err := sealer.SealElement(list, itemID, candidate); err != nil {
		return store.ConfigOverride{}, err
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

// requireDerivableItemID refuse un nouvel identifiant qu'ElementID ne produirait pas ; l'identifiant n'est pas cité.
func (svc *service) requireDerivableItemID(snapshot storedSnapshot, list, itemID string) error {
	_, isStored := snapshot.override(list, itemID)
	_, isUpstream := svc.upstreamElement(list, itemID)
	isAccepted := itemID == "" || isStored || isUpstream || config.IsDerivableItemID(list, itemID)
	if isAccepted {
		return nil
	}
	return fmt.Errorf("%w: %s: the item id must be scheme://host[:port] without credentials, path or query, "+
		"with an optional #n suffix", config.ErrUsage, list)
}

// previousElement rend l'élément en clair que l'écriture remplace : la surcharge enregistrée descellée, sinon l'amont.
func previousElement(snapshot storedSnapshot, sealer *config.FieldSealer, list, itemID string, upstream config.Element) (map[string]any, error) {
	stored, hasStored := snapshot.override(list, itemID)
	if !hasStored || stored.Deleted {
		return upstream, nil
	}
	var storedElement map[string]any
	if err := json.Unmarshal(stored.Value, &storedElement); err != nil {
		return nil, fmt.Errorf("%w: stored %s item %q: unreadable element", config.ErrUsage, list, itemID)
	}
	if err := sealer.OpenElement(list, itemID, storedElement); err != nil {
		return nil, err
	}
	return storedElement, nil
}

// carryOmittedSecrets reprend les secrets omis de l'élément précédent, sauf si la destination change.
func carryOmittedSecrets(list, itemID string, candidate, previous map[string]any, omitted []string) error {
	carried := slices.DeleteFunc(slices.Clone(omitted), func(field string) bool {
		_, present := previous[field]
		return !present
	})
	if len(carried) == 0 {
		return nil
	}
	for _, field := range destinationFields[list] {
		value, isProvided := candidate[field]
		if isProvided && value != previous[field] {
			return fmt.Errorf("%w: %s item %q: re-enter the secrets when %s changes", config.ErrUsage, list, itemID, field)
		}
	}
	copyFields(candidate, previous, carried)
	return nil
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

// upstreamElement rend l'élément importé de cross-seed sous cet identifiant (secrets en clair).
func (svc *service) upstreamElement(list, itemID string) (config.Element, bool) {
	elements := svc.deps.Config.UpstreamElements[list]
	position := slices.Index(config.AssignIDs(list, elements), itemID)
	if position < 0 {
		return nil, false
	}
	return elements[position], true
}
