package sources

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"necromanciarr/internal/config"
	"necromanciarr/internal/torrentcheck"
)

const (
	// MaxAnnounceURLs borne le nombre total d'URL d'annonce, tous paliers confondus : un palier est non vide, donc le
	// nombre de paliers l'est aussi.
	MaxAnnounceURLs = 512
	// MaxAnnounceURLBytes borne la longueur d'une URL d'annonce.
	MaxAnnounceURLBytes = 2048
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

	allowedAnnounceSchemes = []string{"http", "https", "udp"}
	// marshalTopLevel encode le premier niveau (clés triées octet par octet) ; remplacé seulement par les tests pour
	// prouver le contrôle de l'infohash.
	marshalTopLevel = func(topLevel map[string]bencode.Bytes) ([]byte, error) { return bencode.Marshal(topLevel) }
	// nonSecretSegments sont les noms de fichier usuels d'une annonce : jamais une passkey.
	nonSecretSegments = []string{"announce", "announce.php", "scrape", "scrape.php", "tracker.php", "index.php"}
)

// InjectAnnounce rend un .torrent identique au premier pour le dictionnaire info, avec l'annonce donnée : announce =
// première URL du premier palier, announce-list = tiers. Le premier niveau passe par la liste blanche de Sanitize
// (comment, created by et toute clé pouvant porter une passkey retirés) ; info est recopié octet pour octet. Le
// résultat porte la passkey : il ne doit vivre qu'en mémoire, jamais être rendu ni écrit dans un journal.
func InjectAnnounce(torrent []byte, tiers [][]string) (injected []byte, err error) {
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
	encoded, err := marshalTopLevel(topLevel)
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
		if urlCount > MaxAnnounceURLs {
			return fmt.Errorf("%w: more than %d URLs", ErrInvalidAnnounce, MaxAnnounceURLs)
		}
		for urlIndex, announce := range tier {
			if !isValidAnnounce(announce) {
				return fmt.Errorf("%w: tier %d, URL %d is not an http, https or udp URL with a host", ErrInvalidAnnounce, tierIndex, urlIndex)
			}
		}
	}
	return nil
}

// isValidAnnounce accepte http, https ou udp avec un hôte, sans fragment, espace ni caractère de contrôle : une URL
// qui couperait l'en-tête ou la requête du client est refusée.
func isValidAnnounce(announce string) bool {
	hasForbiddenCharacter := strings.ContainsFunc(announce, func(character rune) bool {
		return unicode.IsSpace(character) || unicode.IsControl(character) || character == '#'
	})
	if announce == "" || len(announce) > MaxAnnounceURLBytes || !utf8.ValidString(announce) || hasForbiddenCharacter {
		return false
	}
	parsedURL, err := url.Parse(announce)
	if err != nil || parsedURL.Opaque != "" || parsedURL.Hostname() == "" {
		return false
	}
	return slices.Contains(allowedAnnounceSchemes, parsedURL.Scheme)
}

// AnnounceTiers lit announce-list (à défaut announce) d'un .torrent d'indexeur non scellé. Une entrée qui n'est pas
// une URL valide (pas une chaîne, vide, autre schéma) est ignorée, comme le font les clients (BEP 12), et un palier
// vidé disparaît ; plus de MaxAnnounceURLs URL valides est une erreur, jamais une troncature. Les URL rendues sont
// des secrets : l'appelant ajoute DiscoveredSecretValues aux secrets connus de l'opération.
func AnnounceTiers(torrent []byte) (tiers [][]string, err error) {
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

func announceListTiers(rawList bencode.Bytes) [][]string {
	var rawTiers []bencode.Bytes
	if len(rawList) == 0 || bencode.Unmarshal(rawList, &rawTiers) != nil {
		return nil
	}
	var tiers [][]string
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

func announceTier(rawAnnounce bencode.Bytes) [][]string {
	if announces := validAnnounces(rawAnnounce); len(announces) > 0 {
		return [][]string{announces}
	}
	return nil
}

// validAnnounces rend l'URL encodée dans raw si c'est une chaîne d'annonce valide, sinon rien.
func validAnnounces(raw bencode.Bytes) []string {
	var announce string
	if len(raw) == 0 || raw[0] < '0' || raw[0] > '9' || bencode.Unmarshal(raw, &announce) != nil || !isValidAnnounce(announce) {
		return nil
	}
	return []string{announce}
}

// DiscoveredSecretValues extrait les valeurs de passkey supposées d'annonces découvertes, pour
// Config.WithDiscoveredSecrets : l'utilisateur et le mot de passe de l'URL (toute longueur), chaque segment de
// chemin décodé et chaque valeur de requête décodée d'au moins config.MinKnownSecretLength octets, hors noms de
// fichier usuels (announce, scrape, index.php…) ; un paramètre sans valeur (« ?PASSKEY ») compte comme une valeur.
// Plus courts, segments et valeurs sont laissés aux règles d'URL de config.RedactText. Ordre d'apparition, sans
// doublon.
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
	for _, segment := range strings.Split(parsedURL.Path, "/") {
		values = appendIfSecretLike(values, segment)
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
	if len(candidate) < config.MinKnownSecretLength || isFileName {
		return values
	}
	return append(values, candidate)
}
