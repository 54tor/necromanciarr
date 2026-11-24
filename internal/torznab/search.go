package torznab

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
)

const (
	infoHashLength = 40
	magnetPrefix   = "magnet:"
	guidHashPrefix = "sha256:"
)

// searchModes sont les valeurs de t acceptées par Search.
var searchModes = map[string]bool{"search": true, "movie": true, "tvsearch": true}

// Query décrit une recherche ; les champs à zéro ou vides ne sont pas envoyés.
type Query struct {
	// Mode est la valeur de t : "search", "movie" ou "tvsearch", selon les Caps de l'indexeur.
	Mode string
	// TMDBID et TVDBID sont les identifiants numériques du média.
	TMDBID, TVDBID int
	// IMDBID est l'identifiant IMDb (« tt… ») ; Text est la recherche libre (q).
	IMDBID, Text string
	// Season et Episode précisent une recherche de série (season, ep).
	Season, Episode int
	// Categories restreint la recherche (cat).
	Categories []int
}

// Results est le résultat d'une recherche.
type Results struct {
	// Items sont les résultats retenus : taille entière ≥ 0 et lien de téléchargement http(s).
	Items []Item
	// MagnetOnly compte les résultats écartés parce qu'ils n'offraient qu'un lien magnet.
	MagnetOnly int
}

// Item est un résultat retenu.
type Item struct {
	// Title est le titre brut de l'indexeur, à passer au filtre des caractères de contrôle avant affichage.
	Title string
	// GUID identifie le résultat ; un GUID en forme d'URL (qui peut porter une passkey) est remplacé par son
	// empreinte « sha256:… ».
	GUID string
	// InfoHash est l'infohash v1 annoncé par l'indexeur (40 hexadécimaux minuscules) ; vide s'il n'est pas annoncé.
	InfoHash string
	// Size est la taille totale annoncée, en octets.
	Size int64
	// Magnet dit si l'indexeur offre aussi un lien magnet ; il n'est jamais utilisé.
	Magnet bool
	// downloadURL porte souvent une passkey : jamais exporté, jamais écrit dans un journal ni stocké.
	downloadURL string
}

// String décrit le résultat sans son lien de téléchargement.
func (item Item) String() string {
	return fmt.Sprintf("torznab.Item{Title: %q, GUID: %q, InfoHash: %q, Size: %d, Magnet: %t}",
		item.Title, item.GUID, item.InfoHash, item.Size, item.Magnet)
}

// GoString décrit le résultat sans son lien de téléchargement pour %#v.
func (item Item) GoString() string { return item.String() }

// LogValue décrit le résultat sans son lien de téléchargement dans les journaux slog.
func (item Item) LogValue() slog.Value { return slog.StringValue(item.String()) }

// Search interroge l'indexeur ; au plus maxItems éléments sont acceptés, au-delà la réponse est hostile.
func (client *Client) Search(ctx context.Context, query Query) (Results, error) {
	if !searchModes[query.Mode] {
		return Results{}, errUnknownMode
	}
	content, retryAfter, err := client.get(ctx, searchParameters(query))
	if err != nil {
		return Results{}, err
	}
	reader := &searchReader{}
	if err := decodeDocument(content, "rss", retryAfter, reader); err != nil {
		return Results{}, err
	}
	return reader.results, nil
}

func searchParameters(query Query) url.Values {
	parameters := url.Values{"t": {query.Mode}, "limit": {strconv.Itoa(maxItems)}}
	setIfNotEmpty(parameters, "q", query.Text)
	setIfNotEmpty(parameters, "imdbid", query.IMDBID)
	setIfPositive(parameters, "tmdbid", query.TMDBID)
	setIfPositive(parameters, "tvdbid", query.TVDBID)
	setIfPositive(parameters, "season", query.Season)
	setIfPositive(parameters, "ep", query.Episode)
	if len(query.Categories) > 0 {
		categories := make([]string, 0, len(query.Categories))
		for _, category := range query.Categories {
			categories = append(categories, strconv.Itoa(category))
		}
		parameters.Set("cat", strings.Join(categories, ","))
	}
	return parameters
}

func setIfNotEmpty(parameters url.Values, name, value string) {
	if value != "" {
		parameters.Set(name, value)
	}
}

func setIfPositive(parameters url.Values, name string, value int) {
	if value > 0 {
		parameters.Set(name, strconv.Itoa(value))
	}
}

// rawItem rassemble les champs bruts d'un <item> avant validation.
type rawItem struct {
	fields        map[string]string
	attributes    map[string]string
	enclosureURL  string
	enclosureSize string
}

// itemFields sont les enfants texte de <item> lus ; les autres sont ignorés.
var itemFields = map[string]bool{"title": true, "guid": true, "link": true, "size": true}

type searchReader struct {
	results  Results
	count    int
	current  *rawItem
	field    string
	fieldBuf strings.Builder
}

func isItemPath(path []string) bool {
	return len(path) == 3 && path[1] == "channel" && path[2] == "item"
}

func (reader *searchReader) start(element xml.StartElement, path []string) error {
	if isItemPath(path) {
		reader.count++
		if reader.count > maxItems {
			return fmt.Errorf("%w: more than %d items", ErrHostileResponse, maxItems)
		}
		reader.current = &rawItem{fields: map[string]string{}, attributes: map[string]string{}}
		return nil
	}
	if reader.current == nil || len(path) != 4 {
		return nil
	}
	switch name := element.Name.Local; {
	case itemFields[name]:
		reader.field = name
		reader.fieldBuf.Reset()
	case name == "enclosure":
		reader.current.enclosureURL = attribute(element, "url")
		reader.current.enclosureSize = attribute(element, "length")
	case name == "attr":
		reader.current.attributes[attribute(element, "name")] = attribute(element, "value")
	}
	return nil
}

func (reader *searchReader) text(data []byte, path []string) {
	if reader.field != "" && len(path) == 4 {
		reader.fieldBuf.Write(data)
	}
}

func (reader *searchReader) end(path []string) error {
	switch {
	case reader.field != "" && len(path) == 4:
		if _, isSet := reader.current.fields[reader.field]; !isSet {
			reader.current.fields[reader.field] = strings.TrimSpace(reader.fieldBuf.String())
		}
		reader.field = ""
	case isItemPath(path) && reader.current != nil:
		reader.keep(*reader.current)
		reader.current = nil
	}
	return nil
}

// keep valide un élément : sans lien http(s) il est écarté (compté s'il n'offrait qu'un magnet), une taille ou un
// infohash invalide l'écarte aussi.
func (reader *searchReader) keep(raw rawItem) {
	links := []string{raw.enclosureURL, raw.fields["link"], raw.attributes["magneturl"]}
	downloadURL, hasMagnet := classifyLinks(links)
	if downloadURL == "" {
		if hasMagnet {
			reader.results.MagnetOnly++
		}
		return
	}
	size, isValidSize := parseSize(firstNonEmpty(raw.attributes["size"], raw.fields["size"], raw.enclosureSize))
	infoHash, isValidHash := parseInfoHash(raw.attributes["infohash"])
	if !isValidSize || !isValidHash {
		return
	}
	reader.results.Items = append(reader.results.Items, Item{
		Title:       raw.fields["title"],
		GUID:        safeGUID(raw.fields["guid"]),
		InfoHash:    infoHash,
		Size:        size,
		Magnet:      hasMagnet,
		downloadURL: downloadURL,
	})
}

// classifyLinks rend le premier lien http(s) absolu et dit si un lien magnet est présent.
func classifyLinks(links []string) (string, bool) {
	downloadURL := ""
	hasMagnet := false
	for _, link := range links {
		link = strings.TrimSpace(link)
		if strings.HasPrefix(strings.ToLower(link), magnetPrefix) {
			hasMagnet = true
			continue
		}
		if downloadURL == "" && isAbsoluteHTTP(link) {
			downloadURL = link
		}
	}
	return downloadURL, hasMagnet
}

func isAbsoluteHTTP(link string) bool {
	parsed, err := url.Parse(link)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// parseSize n'accepte qu'un entier décimal ≥ 0 : pas de décimale, d'exposant ni de dépassement.
func parseSize(raw string) (int64, bool) {
	size, err := strconv.ParseInt(raw, 10, 64)
	return size, err == nil && size >= 0
}

// parseInfoHash rend l'infohash en minuscules ; absent, il est valide et vide.
func parseInfoHash(raw string) (string, bool) {
	infoHash := strings.ToLower(strings.TrimSpace(raw))
	if infoHash == "" {
		return "", true
	}
	if len(infoHash) != infoHashLength {
		return "", false
	}
	_, err := hex.DecodeString(infoHash)
	return infoHash, err == nil
}

// safeGUID remplace un GUID en forme d'URL par son empreinte : c'est souvent le lien de téléchargement, passkey comprise.
func safeGUID(guid string) string {
	if !strings.Contains(guid, "://") {
		return guid
	}
	digest := sha256.Sum256([]byte(guid))
	return guidHashPrefix + hex.EncodeToString(digest[:])
}
