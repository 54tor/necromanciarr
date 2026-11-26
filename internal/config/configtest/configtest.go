// Package configtest range, pour les tests, un document à la forme de l'ancien config.json dans la configuration en
// base : chaque entrée de liste devient un ajout, chaque autre clé un réglage.
package configtest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"testing"

	"necromanciarr/internal/config"
	"necromanciarr/internal/store"
)

const (
	listTrackers = "trackers"
	listTorznab  = "torznab"
	fieldURL     = "url"
	fieldKey     = "key"
	fieldName    = "name"
	fieldAnnonce = "announce"
)

// listNames sont les clés lues comme des listes à éléments ; les autres sont des réglages.
var listNames = []string{"radarr", "sonarr", "torrentClients", "linkDirs", listTrackers, listTorznab, "fsProviders"}

// records est le contenu de base tiré d'un document.
type records struct {
	settings  map[string][]byte
	overrides []store.ConfigOverride
}

func (stored records) Settings(context.Context) (map[string][]byte, error) {
	return stored.settings, nil
}

func (stored records) ConfigOverrides(context.Context) ([]store.ConfigOverride, error) {
	return stored.overrides, nil
}

// Opener rend un ouvreur de base en mémoire pour config.LoadWith, chargé du document.
func Opener(t testing.TB, content string) func(string) (config.StoredReader, io.Closer, error) {
	t.Helper()
	stored := recordsFromJSON(t, content)
	return func(string) (config.StoredReader, io.Closer, error) { return stored, nil, nil }
}

// WriteDatabase crée la base dbPath et y écrit le document.
func WriteDatabase(t testing.TB, dbPath, content string) {
	t.Helper()
	openedStore, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = openedStore.Close() }()
	stored := recordsFromJSON(t, content)
	ctx := context.Background()
	for key, value := range stored.settings {
		if err := openedStore.SetSetting(ctx, key, value); err != nil {
			t.Fatal(err)
		}
	}
	for _, override := range stored.overrides {
		if err := openedStore.PutConfigOverride(ctx, override); err != nil {
			t.Fatal(err)
		}
	}
}

func recordsFromJSON(t testing.TB, content string) records {
	t.Helper()
	var document map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &document); err != nil {
		t.Fatal(err)
	}
	stored := records{settings: map[string][]byte{}}
	for _, key := range sortedKeys(document) {
		if !slices.Contains(listNames, key) {
			stored.settings[key] = document[key]
			continue
		}
		stored.overrides = append(stored.overrides, addedOverrides(t, key, listElements(t, key, document[key]))...)
	}
	return stored
}

func sortedKeys[Value any](entries map[string]Value) []string {
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func listElements(t testing.TB, list string, rawValue json.RawMessage) []config.Element {
	t.Helper()
	if list == listTrackers {
		return trackerElements(t, rawValue)
	}
	var rawEntries []json.RawMessage
	if err := json.Unmarshal(rawValue, &rawEntries); err != nil {
		t.Fatal(err)
	}
	elements := make([]config.Element, 0, len(rawEntries))
	for position, rawEntry := range rawEntries {
		element := listElement(t, list, rawEntry)
		nameUnnamedIndexer(list, element, position)
		elements = append(elements, element)
	}
	return elements
}

// listElement lit une entrée : objet tel quel, URL Torznab seule, ou chaîne brute décomposée par ElementFromRaw.
func listElement(t testing.TB, list string, rawEntry json.RawMessage) config.Element {
	t.Helper()
	var text string
	if json.Unmarshal(rawEntry, &text) != nil {
		var element config.Element
		if err := json.Unmarshal(rawEntry, &element); err != nil {
			t.Fatal(err)
		}
		return element
	}
	if list == listTorznab {
		return config.Element{fieldURL: text}
	}
	element, err := config.ElementFromRaw(list, text)
	if err != nil {
		t.Fatal(err)
	}
	return element
}

// nameUnnamedIndexer nomme indexer-<rang> un indexeur sans name : la base l'exige.
func nameUnnamedIndexer(list string, element config.Element, position int) {
	if _, hasName := element[fieldName]; list == listTorznab && !hasName {
		element[fieldName] = fmt.Sprintf("indexer-%d", position)
	}
}

// trackerElements éclate la section trackers par clé ; une annonce seule devient un tableau.
func trackerElements(t testing.TB, rawSection json.RawMessage) []config.Element {
	t.Helper()
	var section map[string]any
	if err := json.Unmarshal(rawSection, &section); err != nil {
		t.Fatal(err)
	}
	elements := make([]config.Element, 0, len(section))
	for _, key := range sortedKeys(section) {
		element := config.Element{fieldKey: key}
		switch entry := section[key].(type) {
		case map[string]any:
			for field, value := range entry {
				element[field] = value
			}
		case string:
			element[fieldAnnonce] = []any{entry}
		default:
			element[fieldAnnonce] = entry
		}
		elements = append(elements, element)
	}
	return elements
}

func addedOverrides(t testing.TB, list string, elements []config.Element) []store.ConfigOverride {
	t.Helper()
	overrides := make([]store.ConfigOverride, 0, len(elements))
	for position, itemID := range config.AssignIDs(list, elements) {
		encoded, err := json.Marshal(elements[position])
		if err != nil {
			t.Fatal(err)
		}
		overrides = append(overrides, store.ConfigOverride{List: list, ItemID: itemID, Value: encoded})
	}
	return overrides
}
