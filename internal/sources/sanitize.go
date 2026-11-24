package sources

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/anacrolix/torrent/bencode"
)

const (
	// MaxTorrentBytes borne un .torrent d'indexeur, comme le téléchargement Torznab (8 Mio).
	MaxTorrentBytes = 8 << 20
	// maxBencodeDepth borne l'imbrication des listes et dictionnaires (défaut du décodeur, rendu explicite).
	maxBencodeDepth = bencode.DefaultMaxDepth
	infoKey         = "info"
	dictionaryEnd   = 'e'
)

// ErrInvalidTorrent refuse un .torrent illisible, trop gros ou trop imbriqué ; l'erreur ne cite jamais son contenu.
var ErrInvalidTorrent = errors.New("sources: invalid torrent")

// keptTopLevelKeys sont les seules clés gardées hors de info : aucune ne peut porter de tracker ni de passkey.
// « piece layers » ne contient que des hashes (torrents mixtes v1 et v2). Tout le reste (announce, announce-list,
// comment, created by, url-list, httpseeds, nodes, publisher-url, azureus_properties, clés inconnues) est retiré.
var keptTopLevelKeys = map[string]bool{infoKey: true, "creation date": true, "encoding": true, "piece layers": true}

// Sanitize réencode le dictionnaire de premier niveau sans rien qui puisse porter une annonce ou une passkey. Le
// dictionnaire info est recopié octet pour octet, jamais décodé puis réencodé : l'infohash ne peut pas dériver.
func Sanitize(torrent []byte) (sanitized []byte, err error) {
	// Le décodeur panique sur certaines erreurs d'exécution : une entrée hostile devient une erreur, jamais une panique.
	defer func() {
		if recover() != nil {
			sanitized, err = nil, ErrInvalidTorrent
		}
	}()
	topLevel, err := decodeTopLevel(torrent)
	if err != nil {
		return nil, err
	}
	kept := make(map[string]bencode.Bytes, len(keptTopLevelKeys))
	for key, value := range topLevel {
		if keptTopLevelKeys[key] {
			kept[key] = value
		}
	}
	encoded, err := bencode.Marshal(kept)
	if err != nil {
		return nil, ErrInvalidTorrent
	}
	return encoded, nil
}

// decodeTopLevel garde chaque valeur de premier niveau en octets bruts et exige un dictionnaire info. Les paires sont
// lues une à une pour refuser une clé en double : sinon la dernière l'emporterait sans que rien ne le signale.
func decodeTopLevel(torrent []byte) (map[string]bencode.Bytes, error) {
	if len(torrent) == 0 || len(torrent) > MaxTorrentBytes || torrent[0] != 'd' {
		return nil, ErrInvalidTorrent
	}
	reader := bytes.NewReader(torrent[1:])
	decoder := bencode.NewDecoder(reader)
	decoder.MaxStrLen = MaxTorrentBytes
	decoder.MaxDepth = maxBencodeDepth
	topLevel := map[string]bencode.Bytes{}
	for {
		next, err := reader.ReadByte()
		if err != nil {
			return nil, fmt.Errorf("%w: unterminated dictionary", ErrInvalidTorrent)
		}
		if next == dictionaryEnd {
			break
		}
		_ = reader.UnreadByte()
		key, value, err := decodePair(decoder)
		if err != nil {
			return nil, err
		}
		if _, duplicate := topLevel[key]; duplicate {
			return nil, fmt.Errorf("%w: duplicate top-level key", ErrInvalidTorrent)
		}
		topLevel[key] = value
	}
	if reader.Len() != 0 {
		return nil, fmt.Errorf("%w: trailing bytes", ErrInvalidTorrent)
	}
	info := topLevel[infoKey]
	if len(info) == 0 || info[0] != 'd' {
		return nil, fmt.Errorf("%w: missing info dictionary", ErrInvalidTorrent)
	}
	return topLevel, nil
}

// decodePair lit une clé (chaîne) puis sa valeur en octets bruts ; le décodeur ne lit pas au-delà de la valeur.
func decodePair(decoder *bencode.Decoder) (string, bencode.Bytes, error) {
	var key string
	if err := decoder.Decode(&key); err != nil {
		return "", nil, fmt.Errorf("%w: undecodable key", ErrInvalidTorrent)
	}
	var value bencode.Bytes
	if err := decoder.Decode(&value); err != nil {
		return "", nil, fmt.Errorf("%w: undecodable value", ErrInvalidTorrent)
	}
	return key, value, nil
}
