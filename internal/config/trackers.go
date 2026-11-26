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
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// MinKnownSecretLength : une valeur de trackers (ou découverte) plus courte n'entre pas dans les secrets connus,
	// car la masquer littéralement couperait des mots ordinaires ; elle reste masquée dans les URL d'annonce.
	MinKnownSecretLength = 8
	// MaxAnnounceURLBytes borne la longueur d'une URL d'annonce.
	MaxAnnounceURLBytes = 2048
	// MaxAnnounceURLs borne le nombre d'URL d'annonce d'une entrée trackers ou d'un torrent, tous paliers confondus.
	MaxAnnounceURLs      = 512
	maxTrackerNameLength = 128
	errTrackersShape     = `trackers: must be an object mapping each tracker name to an announce URL or to {"announce": [URL, …], "aliases": [name, …]}`
	errTrackerEntryShape = `must be an announce URL or an object with "announce" (a list of URLs) and optional "aliases"`
	errAnnounceURLFormat = "announce[%d]: invalid URL (expected http, https or udp://host[:port]/path, at most 2048 bytes, no fragment, space or control character)"
)

var announceSchemes = []string{"http", "https", "udp"}

// ErrInvalidAnnounceURL refuse une URL d'annonce hors de la règle de ValidAnnounceURL ; il ne cite jamais l'URL.
var ErrInvalidAnnounceURL = errors.New("invalid announce URL")

// Tracker décrit l'annonce d'un tracker, injectée au seed. Announce porte la passkey de l'opérateur : c'est un secret,
// rendu seulement en schéma://hôte[:port]/*** par String, GoString, LogValue et MarshalJSON. Plusieurs URL forment un
// seul palier d'announce-list.
type Tracker struct {
	Key      string
	Announce []string
	Aliases  []string
	// secrets retient les segments et valeurs des annonces (hors mots ordinaires) pour KnownSecrets ; jamais rendu.
	secrets []string
}

type (
	jsonTracker struct {
		Key      string   `json:"key"`
		Aliases  []string `json:"aliases"`
		Announce []string `json:"announce"`
	}
	trackerFile struct {
		Announce []string `json:"announce"`
		Aliases  []string `json:"aliases"`
	}
)

// NormalizeTracker met un nom de tracker en minuscules sans espaces : forme de comparaison des clés, des alias et du
// tracker d'un candidat. La valeur stockée ne change jamais.
func NormalizeTracker(name string) string {
	return strings.ToLower(strings.Map(func(character rune) rune {
		if unicode.IsSpace(character) {
			return -1
		}
		return character
	}, name))
}

// RedactAnnounce rend une URL d'annonce sous la seule forme schéma://hôte[:port]/*** : chemin, requête, fragment et
// identifiants disparaissent, où que soit la passkey ; un mot d'hôte qui a l'allure d'un jeton devient « *** ». Une URL illisible ou sans hôte devient « *** ».
func RedactAnnounce(rawURL string) string {
	parsedURL, err := url.Parse(rawURL)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Opaque != "" || parsedURL.Hostname() == "" {
		return redactedMark
	}
	// url.URL.String réencode l'hôte : une zone IPv6 (« %25eth0 ») reste une URL valide.
	hostOnly := url.URL{Scheme: strings.ToLower(parsedURL.Scheme), Host: maskHostWords(strings.ToLower(parsedURL.Host))}
	return hostOnly.String() + "/" + redactedMark
}

// WithDiscoveredSecrets renvoie les secrets connus plus des valeurs passagères (passkey lue dans l'annonce d'un
// .torrent d'indexeur), sous les mêmes formes que les valeurs de trackers ; une valeur de moins de
// MinKnownSecretLength octets est ignorée. La configuration n'est pas modifiée.
func (loadedConfig Config) WithDiscoveredSecrets(values ...string) []string {
	return append(loadedConfig.KnownSecrets(), valueSecrets(values)...)
}

// TrackerFor trouve l'entrée de trackers dont la clé ou un alias égale name sous NormalizeTracker : la seule règle de
// comparaison des trackers (fournisseur, torznab[].tracker, trackers). Les valeurs stockées ne changent pas.
func (loadedConfig Config) TrackerFor(name string) (Tracker, bool) {
	wanted := NormalizeTracker(name)
	for _, tracker := range loadedConfig.Trackers {
		for _, candidate := range append([]string{tracker.Key}, tracker.Aliases...) {
			if NormalizeTracker(candidate) == wanted {
				return tracker, true
			}
		}
	}
	return Tracker{}, false
}

func (tracker Tracker) redacted() jsonTracker {
	announces := make([]string, 0, len(tracker.Announce))
	for _, announce := range tracker.Announce {
		announces = append(announces, RedactAnnounce(announce))
	}
	return jsonTracker{Key: tracker.Key, Aliases: tracker.Aliases, Announce: announces}
}

// String masque les annonces pour %v, %+v et %s.
func (tracker Tracker) String() string { return fmt.Sprintf("%+v", tracker.redacted()) }

// GoString masque les annonces pour %#v.
func (tracker Tracker) GoString() string { return tracker.String() }

// LogValue masque les annonces dans les journaux slog.
func (tracker Tracker) LogValue() slog.Value { return slog.StringValue(tracker.String()) }

// MarshalJSON rend la clé, les alias, l'hôte de chaque annonce et le nom des variables : toute sérialisation est
// destinée à l'affichage.
func (tracker Tracker) MarshalJSON() ([]byte, error) { return json.Marshal(tracker.redacted()) }

// parseTrackers lit la section « trackers », triée par clé. Les messages ne reprennent jamais une URL.
func parseTrackers(rawSection json.RawMessage) ([]Tracker, error) {
	trimmed := bytes.TrimSpace(rawSection)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var rawEntries map[string]json.RawMessage
	if trimmed[0] != '{' || json.Unmarshal(trimmed, &rawEntries) != nil {
		return nil, errors.New(errTrackersShape)
	}
	keys := make([]string, 0, len(rawEntries))
	for key := range rawEntries {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	owners := map[string]string{}
	trackers := make([]Tracker, 0, len(keys))
	for _, key := range keys {
		tracker, err := parseTrackerEntry(key, rawEntries[key])
		if err != nil {
			return nil, fmt.Errorf("trackers[%q]: %w", key, err)
		}
		if err := claimTrackerNames(owners, tracker); err != nil {
			return nil, err
		}
		trackers = append(trackers, tracker)
	}
	return trackers, nil
}

func parseTrackerEntry(key string, rawEntry json.RawMessage) (Tracker, error) {
	rawFile, err := decodeTrackerFile(rawEntry)
	if err != nil {
		return Tracker{}, err
	}
	tracker := Tracker{Key: strings.TrimSpace(key)}
	if err := checkTrackerName("key", tracker.Key); err != nil {
		return Tracker{}, err
	}
	for _, alias := range rawFile.Aliases {
		alias = strings.TrimSpace(alias)
		if err := checkTrackerName("aliases", alias); err != nil {
			return Tracker{}, err
		}
		tracker.Aliases = append(tracker.Aliases, alias)
	}
	if len(rawFile.Announce) == 0 {
		return Tracker{}, errors.New("announce: at least one URL is required")
	}
	if len(rawFile.Announce) > MaxAnnounceURLs {
		return Tracker{}, fmt.Errorf("announce: at most %d URLs", MaxAnnounceURLs)
	}
	for announceIndex, announce := range rawFile.Announce {
		if ValidAnnounceURL(announce) != nil {
			return Tracker{}, fmt.Errorf(errAnnounceURLFormat, announceIndex)
		}
		tracker.Announce = append(tracker.Announce, announce)
		tracker.secrets = append(tracker.secrets, literalPasskeys(announce)...)
		tracker.secrets = append(tracker.secrets, announceValueSecrets(announce)...)
	}
	return tracker, nil
}

func decodeTrackerFile(rawEntry json.RawMessage) (trackerFile, error) {
	var rawFile trackerFile
	trimmed := bytes.TrimSpace(rawEntry)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var announce string
		if err := json.Unmarshal(trimmed, &announce); err != nil {
			return trackerFile{}, errors.New(errTrackerEntryShape)
		}
		rawFile.Announce = []string{announce}
		return rawFile, nil
	}
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return trackerFile{}, errors.New(errTrackerEntryShape)
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&rawFile); err != nil {
		return trackerFile{}, fmt.Errorf("%s: %w", errTrackerEntryShape, err)
	}
	return rawFile, nil
}

func checkTrackerName(field, name string) error {
	if NormalizeTracker(name) == "" {
		return fmt.Errorf("%s: a tracker name must not be empty", field)
	}
	if len(name) > maxTrackerNameLength {
		return fmt.Errorf("%s: a tracker name has at most %d bytes", field, maxTrackerNameLength)
	}
	return nil
}

// ValidAnnounceURL est la seule règle des URL d'annonce, au chargement de trackers comme à l'injection au seed :
// http, https ou udp avec un hôte, au plus MaxAnnounceURLBytes octets d'UTF-8 valide, sans fragment (même vide : un
// « # » couperait la passkey), sans espace (U+00A0 compris) ni caractère de contrôle. L'erreur ne cite jamais l'URL.
func ValidAnnounceURL(announce string) error {
	hasForbiddenCharacter := strings.ContainsFunc(announce, func(character rune) bool {
		return unicode.IsSpace(character) || unicode.IsControl(character) || character == '#'
	})
	if announce == "" || len(announce) > MaxAnnounceURLBytes || !utf8.ValidString(announce) || hasForbiddenCharacter {
		return ErrInvalidAnnounceURL
	}
	parsedURL, err := url.Parse(announce)
	if err != nil || parsedURL.Opaque != "" || parsedURL.Hostname() == "" || !slices.Contains(announceSchemes, parsedURL.Scheme) {
		return ErrInvalidAnnounceURL
	}
	return nil
}

// claimTrackerNames retient la clé et les alias sous leur forme NormalizeTracker ; un nom déjà pris par une autre entrée est une
// erreur qui nomme les deux clés, jamais une URL.
func claimTrackerNames(owners map[string]string, tracker Tracker) error {
	for _, name := range append([]string{tracker.Key}, tracker.Aliases...) {
		normalized := NormalizeTracker(name)
		if owner, taken := owners[normalized]; taken && owner != tracker.Key {
			return fmt.Errorf("trackers: %q and %q both answer to %q (keys and aliases must stay distinct once lowercased and without spaces)",
				owner, tracker.Key, normalized)
		}
		owners[normalized] = tracker.Key
	}
	return nil
}

// valueSecrets donne les formes d'une valeur assez longue pour le remplacement littéral, et marque une valeur courte
// non vide comme secret réservé aux URL (comparée à l'identique aux segments et valeurs, quel que soit le nom).
func valueSecrets(values []string) []string {
	secrets := passkeyForms(values)
	for _, value := range values {
		if value != "" && len(value) < MinKnownSecretLength {
			secrets = append(secrets, urlOnlySecret(value))
		}
	}
	return secrets
}

// passkeyForms donne chaque valeur d'au moins MinKnownSecretLength octets brute, en capitales, en minuscules, et chacune
// encodée pour une requête ou un chemin ; les formes échappées JSON sont ajoutées par RedactJSON.
func passkeyForms(values []string) []string {
	var forms []string
	for _, value := range values {
		if len(value) < MinKnownSecretLength {
			continue
		}
		for _, variant := range []string{value, strings.ToUpper(value), strings.ToLower(value)} {
			forms = append(forms, variant, url.QueryEscape(variant), url.PathEscape(variant))
		}
	}
	slices.Sort(forms)
	return slices.Compact(forms)
}

// plainWord reconnaît un mot ordinaire d'URL (« announce », « Announce », « ANNOUNCE.php »), casse ignorée :
// jamais pris pour un secret.
var plainWord = regexp.MustCompile(`^(?i)[a-z]+([._-][a-z]+)*$`)

// announceValueSecrets retient chaque segment de chemin et chaque valeur de requête décodés d'une annonce stockée, hors
// mots ordinaires : sans interpolation, la passkey n'a plus d'autre frontière connue.
func announceValueSecrets(announce string) []string {
	parsedURL, err := url.Parse(announce)
	if err != nil {
		return nil
	}
	values := strings.Split(parsedURL.Path, "/")
	query, _ := url.ParseQuery(parsedURL.RawQuery)
	for _, queryValues := range query {
		values = append(values, queryValues...)
	}
	return valueSecrets(slices.DeleteFunc(values, plainWord.MatchString))
}

// literalPasskeys retient les mots longs du chemin et de la requête qui mêlent chiffres et lettres : une passkey écrite
// en clair dans la configuration, sans ${NOM}. L'exigence d'un chiffre épargne des mots ordinaires comme « announce-list ».
func literalPasskeys(announce string) []string {
	parsedURL, err := url.Parse(announce)
	if err != nil {
		return nil
	}
	var passkeys []string
	for _, word := range base64URLWords(parsedURL.Path + " " + decodeRepeatedly(parsedURL.RawQuery)) {
		if len(word) >= minSecretSegmentLength && strings.ContainsAny(word, "0123456789") && strings.IndexFunc(word, unicode.IsLetter) >= 0 {
			passkeys = append(passkeys, word)
		}
	}
	return passkeyForms(passkeys)
}
