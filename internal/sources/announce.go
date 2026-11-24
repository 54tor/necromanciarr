package sources

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"necromanciarr/internal/config"
	"necromanciarr/internal/torrentcheck"
)

const (
	// ReasonInfohashChanged est la raison stable d'ErrInfohashChanged.
	ReasonInfohashChanged = "infohash_changed_after_inject"
	announceKey           = "announce"
	announceListKey       = "announce-list"
)

var (
	// ErrInvalidAnnounce refuse des paliers d'annonce vides, trop nombreux ou mal formés ; l'erreur ne donne que des
	// indices, jamais une URL (elle porte la passkey).
	ErrInvalidAnnounce = errors.New("sources: invalid announce")
	// ErrInfohashChanged signale que l'infohash recalculé après injection diffère de celui d'avant : rien ne doit
	// être remis au client.
	ErrInfohashChanged = errors.New("sources: infohash changed after inject")
	// ErrNoAnnounce signale un .torrent sans aucune URL d'annonce utilisable.
	ErrNoAnnounce = errors.New("sources: torrent has no usable announce")

	// nonSecretSegments sont les noms de fichier usuels d'une annonce : jamais une passkey.
	nonSecretSegments = []string{"announce", "announce.php", "scrape", "scrape.php", "tracker.php", "index.php"}
)

// InjectAnnounce rend un .torrent identique au premier pour le dictionnaire info, avec l'annonce donnée : announce =
// première URL du premier palier, announce-list = tiers. Le premier niveau passe par la liste blanche de Sanitize
// (comment, created by et toute clé pouvant porter une passkey retirés) ; info est recopié octet pour octet. Le
// résultat porte la passkey : il ne doit vivre qu'en mémoire, jamais être rendu ni écrit dans un journal.
func InjectAnnounce(torrent []byte, tiers [][]string) ([]byte, error) {
	return injectAnnounce(torrent, tiers, marshalTopLevel)
}

// marshalTopLevel encode le premier niveau : anacrolix trie les clés octet par octet et recopie bencode.Bytes tel quel.
func marshalTopLevel(topLevel map[string]bencode.Bytes) ([]byte, error) {
	return bencode.Marshal(topLevel)
}

// injectAnnounce reçoit l'encodeur pour que les tests prouvent le contrôle de l'infohash avec un encodeur fautif.
func injectAnnounce(torrent []byte, tiers [][]string, marshal func(map[string]bencode.Bytes) ([]byte, error)) (injected []byte, err error) {
	defer func() {
		if recover() != nil {
			injected, err = nil, ErrInvalidTorrent
		}
	}()
	if err := checkTiers(tiers); err != nil {
		return nil, err
	}
	topLevel, err := sanitizedTopLevel(torrent)
	if err != nil {
		return nil, err
	}
	infohashBefore := metainfo.HashBytes(topLevel[infoKey]).HexString()
	if err := addAnnounce(topLevel, tiers); err != nil {
		return nil, err
	}
	encoded, err := marshal(topLevel)
	if err != nil {
		return nil, ErrInvalidTorrent
	}
	return checkInfohashUnchanged(encoded, infohashBefore)
}

// checkInfohashUnchanged relit le résultat avec torrentcheck.Parse, indépendant de l'encodage ci-dessus.
func checkInfohashUnchanged(encoded []byte, infohashBefore string) ([]byte, error) {
	parsed, err := torrentcheck.Parse(encoded)
	if err != nil {
		return nil, fmt.Errorf("%w: injected torrent does not parse", ErrInvalidTorrent)
	}
	if parsed.InfoHash != infohashBefore {
		return nil, ErrInfohashChanged
	}
	return encoded, nil
}

func addAnnounce(topLevel map[string]bencode.Bytes, tiers [][]string) error {
	announce, err := bencode.Marshal(tiers[0][0])
	if err != nil {
		return fmt.Errorf("%w: unencodable announce", ErrInvalidAnnounce)
	}
	announceList, err := bencode.Marshal(tiers)
	if err != nil {
		return fmt.Errorf("%w: unencodable announce-list", ErrInvalidAnnounce)
	}
	topLevel[announceKey] = announce
	topLevel[announceListKey] = announceList
	return nil
}

func checkTiers(tiers [][]string) error {
	if len(tiers) == 0 {
		return fmt.Errorf("%w: no tier", ErrInvalidAnnounce)
	}
	urlCount := 0
	for tierIndex, tier := range tiers {
		if len(tier) == 0 {
			return fmt.Errorf("%w: tier %d is empty", ErrInvalidAnnounce, tierIndex)
		}
		urlCount += len(tier)
		if urlCount > config.MaxAnnounceURLs {
			return fmt.Errorf("%w: more than %d URLs", ErrInvalidAnnounce, config.MaxAnnounceURLs)
		}
		for urlIndex, announce := range tier {
			if config.ValidAnnounceURL(announce) != nil {
				return fmt.Errorf("%w: tier %d, URL %d is not an http, https or udp URL with a host", ErrInvalidAnnounce, tierIndex, urlIndex)
			}
		}
	}
	return nil
}

// AnnounceTiers lit announce-list (à défaut announce) d'un .torrent d'indexeur non scellé. Une entrée qui n'est pas
// une URL valide (pas une chaîne, vide, autre schéma) est ignorée, comme le font les clients (BEP 12), et un palier
// vidé disparaît ; plus de config.MaxAnnounceURLs URL valides est une erreur, jamais une troncature. Les URL rendues sont
// des secrets : l'appelant ajoute DiscoveredSecretValues aux secrets connus de l'opération.
func AnnounceTiers(torrent []byte) (tiers Tiers, err error) {
	defer func() {
		if recover() != nil {
			tiers, err = nil, ErrInvalidTorrent
		}
	}()
	topLevel, err := decodeTopLevel(torrent)
	if err != nil {
		return nil, err
	}
	tiers = announceListTiers(topLevel[announceListKey])
	if len(tiers) == 0 {
		tiers = announceTier(topLevel[announceKey])
	}
	if len(tiers) == 0 {
		return nil, ErrNoAnnounce
	}
	if err := checkTiers(tiers); err != nil {
		return nil, err
	}
	return tiers, nil
}

func announceListTiers(rawList bencode.Bytes) Tiers {
	var rawTiers []bencode.Bytes
	if len(rawList) == 0 || bencode.Unmarshal(rawList, &rawTiers) != nil {
		return nil
	}
	var tiers Tiers
	for _, rawTier := range rawTiers {
		var rawURLs []bencode.Bytes
		if bencode.Unmarshal(rawTier, &rawURLs) != nil {
			continue
		}
		var tier []string
		for _, rawURL := range rawURLs {
			tier = append(tier, validAnnounces(rawURL)...)
		}
		if len(tier) > 0 {
			tiers = append(tiers, tier)
		}
	}
	return tiers
}

func announceTier(rawAnnounce bencode.Bytes) Tiers {
	if announces := validAnnounces(rawAnnounce); len(announces) > 0 {
		return Tiers{announces}
	}
	return nil
}

// validAnnounces rend l'URL encodée dans raw si c'est une chaîne d'annonce valide, sinon rien.
func validAnnounces(raw bencode.Bytes) []string {
	var announce string
	if len(raw) == 0 || raw[0] < '0' || raw[0] > '9' || bencode.Unmarshal(raw, &announce) != nil || config.ValidAnnounceURL(announce) != nil {
		return nil
	}
	return []string{announce}
}

// DiscoveredSecretValues extrait les valeurs de passkey supposées d'annonces découvertes, pour
// Config.WithDiscoveredSecrets : l'utilisateur et le mot de passe de l'URL, chaque segment du chemin encodé puis décodé
// un à un, chaque valeur de requête décodée, hors noms de fichier usuels (announce, scrape, index.php…) ; un paramètre
// sans valeur (« ?PASSKEY ») compte comme une valeur. Toute longueur est rendue : WithDiscoveredSecrets réserve aux
// URL une valeur de moins de config.MinKnownSecretLength octets, si bien qu'un mot court n'est jamais masqué dans la
// prose ni le JSON. Ordre d'apparition, sans doublon.
func DiscoveredSecretValues(tiers [][]string) []string {
	var values []string
	for _, tier := range tiers {
		for _, announce := range tier {
			for _, value := range announceSecretValues(announce) {
				if !slices.Contains(values, value) {
					values = append(values, value)
				}
			}
		}
	}
	return values
}

func announceSecretValues(announce string) []string {
	parsedURL, err := url.Parse(announce)
	if err != nil {
		return nil
	}
	values := userinfoValues(parsedURL.User)
	// Découper le chemin encodé garde « %2F » dans son segment : « abc%2Fdefghijk » reste une seule valeur.
	for _, segment := range strings.Split(parsedURL.EscapedPath(), "/") {
		values = appendIfSecretLike(values, unescapePathSegment(segment))
	}
	for _, pair := range strings.FieldsFunc(parsedURL.RawQuery, isQuerySeparator) {
		name, value, hasValue := strings.Cut(pair, "=")
		if !hasValue {
			value = name
		}
		values = appendIfSecretLike(values, unescapeQueryPart(value))
	}
	return values
}

// unescapePathSegment décode un segment de chemin ; mal encodé, il est gardé tel quel.
func unescapePathSegment(segment string) string {
	if decoded, err := url.PathUnescape(segment); err == nil {
		return decoded
	}
	return segment
}

// isQuerySeparator coupe la requête sur « & » et « ; », comme le masquage de config : les paires restent dans l'ordre.
func isQuerySeparator(character rune) bool { return character == '&' || character == ';' }

// unescapeQueryPart décode une valeur de requête ; mal encodée, elle est gardée telle quelle.
func unescapeQueryPart(part string) string {
	if decoded, err := url.QueryUnescape(part); err == nil {
		return decoded
	}
	return part
}

func userinfoValues(userinfo *url.Userinfo) []string {
	if userinfo == nil {
		return nil
	}
	var values []string
	if username := userinfo.Username(); username != "" {
		values = append(values, username)
	}
	if password, hasPassword := userinfo.Password(); hasPassword && password != "" {
		values = append(values, password)
	}
	return values
}

func appendIfSecretLike(values []string, candidate string) []string {
	isFileName := slices.Contains(nonSecretSegments, strings.ToLower(candidate))
	if candidate == "" || isFileName {
		return values
	}
	return append(values, candidate)
}

// Tiers sont des paliers d'announce-list ; chaque URL porte la passkey. String, GoString, LogValue et MarshalJSON ne
// rendent que config.RedactAnnounce de chaque URL. Limite de Go : un champ non exporté d'une structure, s'il est de
// type Tiers, est imprimé brut par %v (fmt n'appelle pas les méthodes d'un champ non exporté).
type Tiers [][]string

func (tiers Tiers) redacted() [][]string {
	redactedTiers := make([][]string, 0, len(tiers))
	for _, tier := range tiers {
		redactedTier := make([]string, 0, len(tier))
		for _, announce := range tier {
			redactedTier = append(redactedTier, config.RedactAnnounce(announce))
		}
		redactedTiers = append(redactedTiers, redactedTier)
	}
	return redactedTiers
}

// String masque les URL pour %v, %+v et %s.
func (tiers Tiers) String() string { return fmt.Sprint(tiers.redacted()) }

// GoString masque les URL pour %#v.
func (tiers Tiers) GoString() string { return "sources.Tiers" + tiers.String() }

// LogValue masque les URL dans les journaux slog.
func (tiers Tiers) LogValue() slog.Value { return slog.StringValue(tiers.String()) }

// MarshalJSON rend l'hôte de chaque URL, jamais la passkey.
func (tiers Tiers) MarshalJSON() ([]byte, error) { return json.Marshal(tiers.redacted()) }
