package crossseed

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"slices"
	"strings"
)

const (
	redactedMark         = "***"
	maxPort              = 65535
	schemeSeparator      = "://"
	authorityTerminators = "/?#"
	// hostCharacters : nom, IPv4, IPv6 entre crochets (zone %25 comprise) et port.
	hostCharacters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789.-_:[]%"
)

// linkTypes sont les valeurs de linkType de cross-seed ; elles portent le même nom que les sortes de liens du linker.
var linkTypes = []string{"hardlink", "symlink", "reflink"}

// Imported regroupe les clés de cross-seed reprises par Necromanciarr. Radarr, Sonarr et TorrentClients gardent les
// chaînes de cross-seed telles quelles (identifiants compris) ; leur analyse revient au paquet config.
type Imported struct {
	Radarr         []string
	Sonarr         []string
	TorrentClients []string
	LinkDirs       []string
	LinkType       string
	SkipRecheck    bool
	LinkCategory   string
	Host           string
	Port           int
	APIKey         string
	// IgnoredKeys nomme, triées, les clés présentes mais sans objet ici (règles de recherche par indexeurs…).
	IgnoredKeys []string
	// ImportedKeys nomme, triées, les clés reprises avec une valeur (null et undefined valent absence).
	ImportedKeys []string
}

// keyDecoder range une valeur JSON dans Imported ; l'erreur décrit le type attendu, sans la valeur.
type keyDecoder func(raw json.RawMessage, target *Imported) error

var keyDecoders = map[string]keyDecoder{
	"radarr":         decodeStrings(func(target *Imported) *[]string { return &target.Radarr }),
	"sonarr":         decodeStrings(func(target *Imported) *[]string { return &target.Sonarr }),
	"torrentClients": decodeStrings(func(target *Imported) *[]string { return &target.TorrentClients }),
	"linkDirs":       decodeStrings(func(target *Imported) *[]string { return &target.LinkDirs }),
	"linkType":       decodeLinkType,
	"skipRecheck":    decodeBool(func(target *Imported) *bool { return &target.SkipRecheck }),
	"linkCategory":   decodeString(func(target *Imported) *string { return &target.LinkCategory }),
	"host":           decodeString(func(target *Imported) *string { return &target.Host }),
	"port":           decodePort,
	"apiKey":         decodeString(func(target *Imported) *string { return &target.APIKey }),
}

// parseExports trie les clés de module.exports : connues et typées, ou ignorées et seulement nommées.
func parseExports(exportedJSON string) (Imported, error) {
	var entries map[string]json.RawMessage
	if err := json.Unmarshal([]byte(exportedJSON), &entries); err != nil {
		return Imported{}, errors.New("cross-seed config: module.exports must be a plain object")
	}
	var imported Imported
	for _, key := range slices.Sorted(maps.Keys(entries)) {
		decoder, known := keyDecoders[key]
		if !known {
			imported.IgnoredKeys = append(imported.IgnoredKeys, key)
			continue
		}
		raw := entries[key]
		if bytes.Equal(raw, []byte("null")) {
			continue
		}
		if err := decoder(raw, &imported); err != nil {
			return Imported{}, fmt.Errorf("cross-seed config: key %q: %w", key, err)
		}
		imported.ImportedKeys = append(imported.ImportedKeys, key)
	}
	return imported, nil
}

func decodeStrings(field func(*Imported) *[]string) keyDecoder {
	return func(raw json.RawMessage, target *Imported) error {
		if json.Unmarshal(raw, field(target)) != nil {
			return errors.New("expected an array of strings")
		}
		return nil
	}
}

func decodeString(field func(*Imported) *string) keyDecoder {
	return func(raw json.RawMessage, target *Imported) error {
		if json.Unmarshal(raw, field(target)) != nil {
			return errors.New("expected a string")
		}
		return nil
	}
}

func decodeBool(field func(*Imported) *bool) keyDecoder {
	return func(raw json.RawMessage, target *Imported) error {
		if json.Unmarshal(raw, field(target)) != nil {
			return errors.New("expected a boolean")
		}
		return nil
	}
}

func decodeLinkType(raw json.RawMessage, target *Imported) error {
	if json.Unmarshal(raw, &target.LinkType) != nil {
		return errors.New("expected a string")
	}
	if !slices.Contains(linkTypes, target.LinkType) {
		return fmt.Errorf("unsupported link type (allowed: %s)", strings.Join(linkTypes, ", "))
	}
	return nil
}

func decodePort(raw json.RawMessage, target *Imported) error {
	var port float64
	if json.Unmarshal(raw, &port) != nil {
		return errors.New("expected a number")
	}
	isValidPort := port == math.Trunc(port) && port >= 1 && port <= maxPort
	if !isValidPort {
		return fmt.Errorf("expected an integer port between 1 and %d", maxPort)
	}
	target.Port = int(port)
	return nil
}

// redacted renvoie une copie dont les chaînes de service ne gardent que préfixe, schéma et hôte, et dont la clé
// d'API est masquée : identifiants, requête et chemin (clé d'un proxy qui) disparaissent.
func (imported Imported) redacted() Imported {
	imported.Radarr = redactServices(imported.Radarr)
	imported.Sonarr = redactServices(imported.Sonarr)
	imported.TorrentClients = redactServices(imported.TorrentClients)
	if imported.APIKey != "" {
		imported.APIKey = redactedMark
	}
	return imported
}

func redactServices(rawServices []string) []string {
	redactedServices := make([]string, 0, len(rawServices))
	for _, rawService := range rawServices {
		redactedServices = append(redactedServices, redactService(rawService))
	}
	return redactedServices
}

// redactService garde « type:readonly:schéma:// » et l'hôte, sans jamais passer par url.Parse : un mot de passe non
// encodé contenant « / », « ? » ou « # » y deviendrait un morceau d'hôte. Au moindre doute, l'hôte est masqué.
func redactService(rawService string) string {
	schemeEnd := strings.Index(rawService, schemeSeparator)
	if schemeEnd < 0 {
		return redactedMark
	}
	prefix := rawService[:schemeEnd+len(schemeSeparator)]
	return prefix + hostOf(rawService[len(prefix):])
}

// hostOf rend l'hôte d'une autorité suivie de chemin, requête ou fragment. Avec un « @ », l'hôte suit le dernier ;
// si ce qui le précède contient « / », « ? » ou « # », on ne sait plus où finit l'userinfo : tout est masqué.
func hostOf(afterScheme string) string {
	candidate := afterScheme
	if atIndex := strings.LastIndex(afterScheme, "@"); atIndex >= 0 {
		if strings.ContainsAny(afterScheme[:atIndex], authorityTerminators) {
			return redactedMark
		}
		candidate = afterScheme[atIndex+1:]
	}
	if terminatorIndex := strings.IndexAny(candidate, authorityTerminators); terminatorIndex >= 0 {
		candidate = candidate[:terminatorIndex]
	}
	if candidate == "" || strings.Trim(candidate, hostCharacters) != "" {
		return redactedMark
	}
	return candidate
}

type plainImported Imported

// String masque les identifiants pour %v, %+v et %s.
func (imported Imported) String() string {
	return fmt.Sprintf("%+v", plainImported(imported.redacted()))
}

// GoString masque les identifiants pour %#v.
func (imported Imported) GoString() string { return imported.String() }

// LogValue masque les identifiants dans les journaux slog.
func (imported Imported) LogValue() slog.Value { return slog.StringValue(imported.String()) }

// MarshalJSON masque les identifiants : toute sérialisation est destinée à l'affichage.
func (imported Imported) MarshalJSON() ([]byte, error) {
	return json.Marshal(plainImported(imported.redacted()))
}
