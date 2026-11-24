package torznab

import (
	"context"
	"encoding/xml"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
)

// capsTTL est la durée de vie des capacités en mémoire : elles changent rarement et chaque requête coûte du quota.
const capsTTL = 24 * time.Hour

// Caps sont les capacités annoncées par t=caps.
type Caps struct {
	// Modes associe chaque mode disponible (valeur de t : "search", "movie", "tvsearch") à ses paramètres acceptés,
	// ainsi "movie" → ["q", "imdbid", "tmdbid"].
	Modes map[string][]string
	// Categories liste les identifiants de catégories et sous-catégories, dans l'ordre du document.
	Categories []int
}

// searchingModes traduit les éléments de <searching> en valeurs de t.
var searchingModes = map[string]string{"search": "search", "movie-search": "movie", "tv-search": "tvsearch"}

// defaultParams sont les paramètres à supposer quand supportedParams est absent (spécification Torznab 1.1).
var defaultParams = map[string]string{"search": "q", "movie": "q,imdbid", "tvsearch": "q,rid,season,ep"}

// Caps renvoie les capacités de l'indexeur, gardées 24 h en mémoire.
func (client *Client) Caps(ctx context.Context) (Caps, error) {
	if cached, isFresh := client.freshCaps(); isFresh {
		return cached, nil
	}
	content, retryAfter, err := client.get(ctx, map[string][]string{"t": {"caps"}})
	if err != nil {
		return Caps{}, err
	}
	reader := &capsReader{caps: Caps{Modes: map[string][]string{}}}
	if err := decodeDocument(content, "caps", retryAfter, reader); err != nil {
		return Caps{}, err
	}
	client.storeCaps(reader.caps)
	return cloneCaps(reader.caps), nil
}

func (client *Client) freshCaps() (Caps, bool) {
	client.capsMutex.Lock()
	defer client.capsMutex.Unlock()
	if client.cachedCaps == nil || client.now().Sub(client.capsFetchedAt) >= capsTTL {
		return Caps{}, false
	}
	return cloneCaps(*client.cachedCaps), true
}

func (client *Client) storeCaps(caps Caps) {
	client.capsMutex.Lock()
	defer client.capsMutex.Unlock()
	client.cachedCaps = &caps
	client.capsFetchedAt = client.now()
}

// cloneCaps protège le cache d'une modification par l'appelant.
func cloneCaps(caps Caps) Caps {
	modes := maps.Clone(caps.Modes)
	for mode, params := range modes {
		modes[mode] = slices.Clone(params)
	}
	return Caps{Modes: modes, Categories: slices.Clone(caps.Categories)}
}

type capsReader struct{ caps Caps }

func (reader *capsReader) start(element xml.StartElement, path []string) error {
	switch {
	case len(path) == 3 && path[1] == "searching":
		reader.addMode(element)
	case len(path) == 3 && path[1] == "categories" && path[2] == "category",
		len(path) == 4 && path[1] == "categories" && path[3] == "subcat":
		reader.addCategory(element)
	}
	return nil
}

func (reader *capsReader) addMode(element xml.StartElement) {
	mode, isKnown := searchingModes[element.Name.Local]
	if !isKnown || attribute(element, "available") != "yes" {
		return
	}
	params := defaultParams[mode]
	if declared, isDeclared := attributeValue(element, "supportedParams"); isDeclared {
		params = declared
	}
	reader.caps.Modes[mode] = splitParams(params)
}

func (reader *capsReader) addCategory(element xml.StartElement) {
	identifier, err := strconv.Atoi(attribute(element, "id"))
	if err == nil && identifier > 0 {
		reader.caps.Categories = append(reader.caps.Categories, identifier)
	}
}

func (*capsReader) text([]byte, []string) {}

func (*capsReader) end([]string) error { return nil }

func splitParams(raw string) []string {
	params := []string{}
	for param := range strings.SplitSeq(raw, ",") {
		if trimmed := strings.ToLower(strings.TrimSpace(param)); trimmed != "" {
			params = append(params, trimmed)
		}
	}
	return params
}
