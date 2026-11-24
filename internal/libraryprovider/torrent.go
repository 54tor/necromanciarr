package libraryprovider

import (
	"bytes"
	"context"
	"crypto/sha1" // #nosec G505 -- SHA-1 imposé par le format BitTorrent v1
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

var infohashPattern = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

// Nfo est un candidat .nfo ; Data contient les octets exacts décodés.
type Nfo struct {
	Label   string
	Partial bool
	Data    []byte
}

func validateInfohash(infohash string) error {
	if !infohashPattern.MatchString(infohash) {
		return errors.New("library-provider: infohash must be 40 hexadecimal characters")
	}
	return nil
}

// Torrent télécharge le .torrent (sans annonce) ; tracker vide = paramètre non envoyé. Aucun cache.
func (client *Client) Torrent(ctx context.Context, infohash, tracker string) ([]byte, error) {
	if err := validateInfohash(infohash); err != nil {
		return nil, err
	}
	target := client.endpoint(trackerQuery(tracker), segmentReleases, infohash, segmentTorrent)
	body, err := client.get(ctx, target, acceptTorrent, maxTorrentBytes)
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(body, []byte("d")) {
		return nil, fmt.Errorf("%w: not a bencode dictionary", ErrBadResponse)
	}
	return body, nil
}

type nfoPayload struct {
	Items []struct {
		Label   string `json:"label"`
		Partial bool   `json:"partial"`
		Data    string `json:"data"`
	} `json:"items"`
}

// Nfos renvoie les .nfo dédoublonnés par contenu, le canonique d'abord ; résultat mis en cache (1 h, 10 min pour un 404).
func (client *Client) Nfos(ctx context.Context, infohash, tracker string) ([]Nfo, error) {
	if err := validateInfohash(infohash); err != nil {
		return nil, err
	}
	cacheKey := "nfos|" + strings.ToLower(infohash) + "|" + tracker
	if cached, found := client.cache.get(cacheKey); found {
		if cached.err != nil {
			return nil, cached.err
		}
		return cached.value.([]Nfo), nil
	}
	body, err := client.getJSON(ctx, client.endpoint(trackerQuery(tracker), segmentReleases, infohash, segmentNfos))
	if errors.Is(err, ErrNotFound) {
		client.cache.put(cacheKey, nil, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	var payload nfoPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("%w: invalid JSON", ErrBadResponse)
	}
	nfos, err := decodeNfos(payload)
	if err != nil {
		return nil, err
	}
	client.cache.put(cacheKey, nfos, nil)
	return nfos, nil
}

func decodeNfos(payload nfoPayload) ([]Nfo, error) {
	seen := map[[sha1.Size]byte]bool{}
	nfos := []Nfo{}
	for _, item := range payload.Items {
		data, err := decodeBase64(item.Data)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid base64 in nfo %q", ErrBadResponse, truncateRunes(stripControl(item.Label), maxDetailRunes))
		}
		digest := sha1.Sum(data) // #nosec G401 -- empreinte de déduplication, pas de sécurité
		if len(data) == 0 || seen[digest] {
			continue
		}
		seen[digest] = true
		nfos = append(nfos, Nfo{Label: item.Label, Partial: item.Partial, Data: data})
	}
	return nfos, nil
}

// decodeBase64 tolère les retours à la ligne et le remplissage absent ou présent.
func decodeBase64(encoded string) ([]byte, error) {
	compact := strings.Map(func(character rune) rune {
		if unicode.IsSpace(character) {
			return -1
		}
		return character
	}, encoded)
	return base64.RawStdEncoding.DecodeString(strings.TrimRight(compact, "="))
}
