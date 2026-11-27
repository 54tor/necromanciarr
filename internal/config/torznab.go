package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// SourceProvider désigne la source « library provider » dans sourceOrder.
	SourceProvider = "provider"
	// SourceTorznabPrefix précède le nom d'un indexeur dans sourceOrder (« torznab:<nom> »).
	SourceTorznabPrefix = "torznab:"

	defaultTorznabMinInterval        = 3 * time.Second
	defaultTorznabTimeout            = 30 * time.Second
	defaultTorznabMaxQueriesPerRun   = 30
	defaultTorznabMaxDownloadsPerRun = 10
	defaultTorznabMaxQueriesPerDay   = 200
	defaultTorznabMaxDownloadsPerDay = 50
	// minTorznabMinInterval protège l'indexeur : en dessous d'une requête par seconde, un tracker ban ou limite.
	minTorznabMinInterval      = time.Second
	maxTorznabMinInterval      = time.Hour
	minTorznabTimeout          = time.Second
	maxTorznabTimeout          = 5 * time.Minute
	maxTorznabCount            = 100000
	maxTorznabNameLength       = 64
	maxTorznabTrackerLength    = 128
	errInvalidIndexerURLFormat = "%s: invalid url (expected http(s)://host[:port]/path?apikey=…, no credentials, no fragment)"
)

var (
	torznabNamePattern    = regexp.MustCompile(`^[a-z0-9-]+$`)
	numericSegmentPattern = regexp.MustCompile(`^[0-9]+$`)
)

// TorznabIndexer décrit un indexeur Torznab (flux Prowlarr, Jackett ou natif). URL porte la clé dans apikey : c'est un
// secret, masqué par String, GoString, LogValue et MarshalJSON.
type TorznabIndexer struct {
	URL  string
	Name string
	// Tracker est gardé tel que saisi (rogné) ; tout rapprochement le compare sous NormalizeTracker (minuscules, espaces
	// retirés, voir Config.TrackerFor), jamais en changeant la valeur stockée.
	Tracker string
	// Categories vide : celles des capacités de l'indexeur.
	Categories                                                                 []int
	MinInterval, Timeout                                                       time.Duration
	MaxQueriesPerRun, MaxDownloadsPerRun, MaxQueriesPerDay, MaxDownloadsPerDay int
}

// ProwlarrID renvoie le dernier segment numérique du chemin de l'URL, l'identifiant de l'indexeur dans Prowlarr.
func (indexer TorznabIndexer) ProwlarrID() (int, bool) {
	parsedURL, err := url.Parse(indexer.URL)
	if err != nil {
		return 0, false
	}
	segment, found := lastNumericSegment(parsedURL.Path)
	if !found {
		return 0, false
	}
	identifier, err := strconv.Atoi(segment)
	return identifier, err == nil
}

type (
	plainTorznabIndexer TorznabIndexer
	jsonTorznabIndexer  struct {
		URL                string `json:"url"`
		Name               string `json:"name"`
		Tracker            string `json:"tracker"`
		Categories         []int  `json:"categories"`
		MinInterval        string `json:"minInterval"`
		Timeout            string `json:"timeout"`
		MaxQueriesPerRun   int    `json:"maxQueriesPerRun"`
		MaxDownloadsPerRun int    `json:"maxDownloadsPerRun"`
		MaxQueriesPerDay   int    `json:"maxQueriesPerDay"`
		MaxDownloadsPerDay int    `json:"maxDownloadsPerDay"`
	}
)

func (indexer TorznabIndexer) redacted() TorznabIndexer {
	indexer.URL = redactedEndpoint(indexer.URL)
	return indexer
}

// String masque la clé pour %v, %+v et %s : l'URL est rendue en schéma, hôte et « /*** ».
func (indexer TorznabIndexer) String() string {
	return fmt.Sprintf("%+v", plainTorznabIndexer(indexer.redacted()))
}

// GoString masque la clé pour %#v.
func (indexer TorznabIndexer) GoString() string { return indexer.String() }

// LogValue masque la clé dans les journaux slog.
func (indexer TorznabIndexer) LogValue() slog.Value { return slog.StringValue(indexer.String()) }

// MarshalJSON masque la clé : toute sérialisation est destinée à l'affichage.
func (indexer TorznabIndexer) MarshalJSON() ([]byte, error) {
	masked := indexer.redacted()
	return json.Marshal(jsonTorznabIndexer{
		URL: masked.URL, Name: masked.Name, Tracker: masked.Tracker, Categories: masked.Categories,
		MinInterval: masked.MinInterval.String(), Timeout: masked.Timeout.String(),
		MaxQueriesPerRun: masked.MaxQueriesPerRun, MaxDownloadsPerRun: masked.MaxDownloadsPerRun,
		MaxQueriesPerDay: masked.MaxQueriesPerDay, MaxDownloadsPerDay: masked.MaxDownloadsPerDay,
	})
}

// redactedEndpoint ne garde de l'URL que le schéma et l'hôte : le chemin peut contenir des segments secrets, la
// requête porte la clé. Une URL illisible est masquée en entier.
func redactedEndpoint(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	return RedactAnnounce(rawURL)
}

// torznabFile est la forme brute d'une entrée objet ; les entrées chaîne ne portent que l'URL.
type torznabFile struct {
	URL                string `json:"url"`
	Name               string `json:"name"`
	Tracker            string `json:"tracker"`
	Categories         []int  `json:"categories"`
	MinInterval        string `json:"minInterval"`
	Timeout            string `json:"timeout"`
	MaxQueriesPerRun   *int   `json:"maxQueriesPerRun"`
	MaxDownloadsPerRun *int   `json:"maxDownloadsPerRun"`
	MaxQueriesPerDay   *int   `json:"maxQueriesPerDay"`
	MaxDownloadsPerDay *int   `json:"maxDownloadsPerDay"`
}

// parseTorznab lit la liste « torznab » : chaque entrée est une URL ou un objet. Les messages ne reprennent jamais
// l'URL, qui porte la clé.
func parseTorznab(rawEntries []json.RawMessage) ([]TorznabIndexer, error) {
	var indexers []TorznabIndexer
	usedNames := map[string]bool{}
	for entryIndex, rawEntry := range rawEntries {
		indexer, err := parseTorznabEntry(rawEntry, entryIndex)
		if err != nil {
			return nil, fmt.Errorf("torznab[%d]: %w", entryIndex, err)
		}
		if usedNames[indexer.Name] {
			return nil, fmt.Errorf("torznab[%d]: name %q is already used (set a distinct \"name\")", entryIndex, indexer.Name)
		}
		usedNames[indexer.Name] = true
		indexers = append(indexers, indexer)
	}
	return indexers, nil
}

func parseTorznabEntry(rawEntry json.RawMessage, entryIndex int) (TorznabIndexer, error) {
	rawFile, err := decodeTorznabFile(rawEntry)
	if err != nil {
		return TorznabIndexer{}, err
	}
	if err := validateIndexerURL(rawFile.URL, true); err != nil {
		return TorznabIndexer{}, err
	}
	indexer := TorznabIndexer{URL: rawFile.URL, Tracker: strings.TrimSpace(rawFile.Tracker), Categories: rawFile.Categories}
	if len(indexer.Tracker) > maxTorznabTrackerLength {
		return TorznabIndexer{}, errors.New("tracker is too long")
	}
	if indexer.Name, err = torznabName(rawFile, entryIndex); err != nil {
		return TorznabIndexer{}, err
	}
	for _, category := range indexer.Categories {
		if category <= 0 {
			return TorznabIndexer{}, errors.New("categories: every identifier must be a positive integer")
		}
	}
	if err := applyTorznabLimits(&indexer, rawFile); err != nil {
		return TorznabIndexer{}, err
	}
	return indexer, nil
}

func decodeTorznabFile(rawEntry json.RawMessage) (torznabFile, error) {
	var rawFile torznabFile
	if trimmed := bytes.TrimSpace(rawEntry); len(trimmed) > 0 && trimmed[0] == '"' {
		if err := json.Unmarshal(trimmed, &rawFile.URL); err != nil {
			return torznabFile{}, errors.New("must be a URL string or an object with a \"url\"")
		}
		return rawFile, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(rawEntry))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&rawFile); err != nil {
		return torznabFile{}, fmt.Errorf("must be a URL string or an object with a \"url\" and known keys: %w", err)
	}
	return rawFile, nil
}

// torznabName donne le nom explicite, sinon le dernier segment numérique du chemin, sinon le rang dans la liste.
func torznabName(rawFile torznabFile, entryIndex int) (string, error) {
	name := rawFile.Name
	if name == "" {
		parsedURL, _ := url.Parse(rawFile.URL)
		if segment, found := lastNumericSegment(parsedURL.Path); found {
			name = segment
		} else {
			name = strconv.Itoa(entryIndex)
		}
	}
	if len(name) > maxTorznabNameLength || !torznabNamePattern.MatchString(name) {
		return "", fmt.Errorf("name must match %s (at most %d characters)", torznabNamePattern, maxTorznabNameLength)
	}
	return name, nil
}

func lastNumericSegment(path string) (string, bool) {
	segments := strings.Split(path, "/")
	for segmentIndex := len(segments) - 1; segmentIndex >= 0; segmentIndex-- {
		if numericSegmentPattern.MatchString(segments[segmentIndex]) {
			return segments[segmentIndex], true
		}
	}
	return "", false
}

func applyTorznabLimits(indexer *TorznabIndexer, rawFile torznabFile) error {
	var err error
	if indexer.MinInterval, err = boundedDuration("minInterval", rawFile.MinInterval, defaultTorznabMinInterval, minTorznabMinInterval, maxTorznabMinInterval); err != nil {
		return err
	}
	if indexer.Timeout, err = boundedDuration("timeout", rawFile.Timeout, defaultTorznabTimeout, minTorznabTimeout, maxTorznabTimeout); err != nil {
		return err
	}
	counters := []struct {
		name     string
		raw      *int
		fallback int
		target   *int
	}{
		{"maxQueriesPerRun", rawFile.MaxQueriesPerRun, defaultTorznabMaxQueriesPerRun, &indexer.MaxQueriesPerRun},
		{"maxDownloadsPerRun", rawFile.MaxDownloadsPerRun, defaultTorznabMaxDownloadsPerRun, &indexer.MaxDownloadsPerRun},
		{"maxQueriesPerDay", rawFile.MaxQueriesPerDay, defaultTorznabMaxQueriesPerDay, &indexer.MaxQueriesPerDay},
		{"maxDownloadsPerDay", rawFile.MaxDownloadsPerDay, defaultTorznabMaxDownloadsPerDay, &indexer.MaxDownloadsPerDay},
	}
	for _, counter := range counters {
		*counter.target = counter.fallback
		if counter.raw == nil {
			continue
		}
		if *counter.raw <= 0 || *counter.raw > maxTorznabCount {
			return fmt.Errorf("%s must be an integer between 1 and %d", counter.name, maxTorznabCount)
		}
		*counter.target = *counter.raw
	}
	return nil
}

func boundedDuration(name, raw string, fallback, floor, ceiling time.Duration) (time.Duration, error) {
	if raw == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed < floor || parsed > ceiling {
		return 0, fmt.Errorf("%s: %q is not a Go duration between %s and %s", name, truncateRunes(raw, maxEchoedLanguageRunes), floor, ceiling)
	}
	return parsed, nil
}

// validateIndexerURL contrôle l'URL d'un indexeur ou de Prowlarr : http(s), hôte, ni fragment ni identifiants, seul
// paramètre admis apikey (une fois, non vide si requis). Le message ne reprend jamais l'URL.
func validateIndexerURL(rawURL string, requireKey bool) error {
	parsedURL, err := parseHTTPURL(rawURL)
	if err != nil || parsedURL.User != nil {
		return fmt.Errorf(errInvalidIndexerURLFormat, "url")
	}
	query, err := url.ParseQuery(parsedURL.RawQuery)
	if err != nil {
		return fmt.Errorf(errInvalidIndexerURLFormat, "url")
	}
	for parameterName, values := range query {
		if parameterName != apiKeyQueryParam || len(values) != 1 {
			return errors.New("url: the only query parameter allowed is a single " + apiKeyQueryParam)
		}
	}
	if requireKey && query.Get(apiKeyQueryParam) == "" {
		return errors.New("url: missing " + apiKeyQueryParam + " query parameter")
	}
	return nil
}

// parseProwlarr valide l'URL de l'API native de Prowlarr ; vide, elle est absente.
func parseProwlarr(rawURL string) error {
	if rawURL == "" {
		return nil
	}
	if err := validateIndexerURL(rawURL, true); err != nil {
		return fmt.Errorf("prowlarr: %w", err)
	}
	return nil
}

// resolveSourceOrder renvoie sourceOrder : par défaut « provider » puis les indexeurs dans l'ordre de la liste. Un
// ordre donné doit commencer par provider (jamais réordonné ni complété en silence) et ne citer que des sources
// connues, chacune une fois.
func resolveSourceOrder(rawOrder []string, indexers []TorznabIndexer) ([]string, error) {
	if len(rawOrder) == 0 {
		order := []string{SourceProvider}
		for _, indexer := range indexers {
			order = append(order, SourceTorznabPrefix+indexer.Name)
		}
		return order, nil
	}
	if rawOrder[0] != SourceProvider {
		return nil, errors.New("sourceOrder: provider must be the first source (it is not reordered for you)")
	}
	known := map[string]bool{SourceProvider: true}
	names := make([]string, 0, len(indexers))
	for _, indexer := range indexers {
		known[SourceTorznabPrefix+indexer.Name] = true
		names = append(names, indexer.Name)
	}
	seen := map[string]bool{}
	for sourceIndex, source := range rawOrder {
		if seen[source] {
			return nil, fmt.Errorf("sourceOrder[%d]: source listed twice", sourceIndex)
		}
		seen[source] = true
		if !known[source] {
			return nil, fmt.Errorf("sourceOrder[%d]: unknown source (expected %s or %s<name>, configured names: %s)",
				sourceIndex, SourceProvider, SourceTorznabPrefix, strings.Join(names, ", "))
		}
	}
	return slices.Clone(rawOrder), nil
}

// torznabSecrets liste les clés d'API (forme décodée et forme encodée) d'URL d'indexeurs et de Prowlarr.
func torznabSecrets(rawURLs ...string) []string {
	var secrets []string
	for _, rawURL := range rawURLs {
		parsedURL, err := url.Parse(rawURL)
		if err != nil {
			continue
		}
		secrets = append(secrets, parsedURL.Query().Get(apiKeyQueryParam), strings.TrimPrefix(parsedURL.RawQuery, apiKeyQueryParam+"="))
	}
	return secrets
}

// NewTorznabCandidate construit l'indexeur d'un élément saisi (nom, url sans clé, clé d'API) comme la configuration
// relit un élément enregistré : mêmes contrôles, clé jointe à l'url. Les refus sont des erreurs d'usage sans la clé.
func NewTorznabCandidate(name, rawURL, apiKey string) (TorznabIndexer, error) {
	if name == "" || apiKey == "" {
		return TorznabIndexer{}, usageError{message: "torznab: a name and an API key are required"}
	}
	element := Element{fieldURL: rawURL, fieldAPIKey: apiKey, "name": name}
	if err := RejectSecretInURL(listTorznab, element); err != nil {
		return TorznabIndexer{}, err
	}
	rawEntry, err := torznabRawFromElement(element)
	if err != nil {
		return TorznabIndexer{}, usageError{message: "torznab: invalid element"}
	}
	indexer, err := parseTorznabEntry(rawEntry.(json.RawMessage), 0)
	if err != nil {
		return TorznabIndexer{}, usageError{message: "torznab: " + err.Error()}
	}
	return indexer, nil
}
