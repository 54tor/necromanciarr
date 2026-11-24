package sources

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

// infoDictionary est volontairement non canonique : clés hors d'ordre, entier à zéros de tête et clé inconnue. Un
// décodage puis un réencodage le réordonnerait et changerait l'infohash.
const infoDictionary = "d4:name5:a.mkv6:lengthi5e12:piece lengthi16384e6:pieces20:AAAAAAAAAAAAAAAAAAAA" +
	"7:privatei1e7:x-extrai007ee"

const fakePasskey = "fake-passkey-0000"

// bencodedString encode une chaîne bencode : les longueurs écrites à la main se trompent.
func bencodedString(text string) string { return fmt.Sprintf("%d:%s", len(text), text) }

func indexerTorrent() []byte {
	announce := "https://tracker.example.com/" + fakePasskey + "/announce"
	return []byte("d" + bencodedString("announce") + bencodedString(announce) +
		bencodedString("announce-list") + "ll" + bencodedString(announce) + "ee" +
		bencodedString("comment") + bencodedString("from https://example.com") +
		bencodedString("created by") + bencodedString("uploader") + bencodedString("creation date") + "i1700000000e" +
		bencodedString("encoding") + bencodedString("UTF-8") + bencodedString("info") + infoDictionary +
		bencodedString("url-list") + "l" + bencodedString("https://seed.example.com/"+fakePasskey) + "e" +
		bencodedString("publisher-url") + bencodedString("https://example.com/u/42") +
		bencodedString("nodes") + "ll" + bencodedString("127.0.0.1") + "i6881eee" +
		bencodedString("httpseeds") + "l" + bencodedString("https://seed.example.com") + "e" + "e")
}

func infohashOf(t *testing.T, torrent []byte) string {
	t.Helper()
	metaInfo, err := metainfo.Load(bytes.NewReader(torrent))
	if err != nil {
		t.Fatalf("metainfo.Load: %v", err)
	}
	return metaInfo.HashInfoBytes().HexString()
}

func topLevelKeys(t *testing.T, torrent []byte) map[string]bencode.Bytes {
	t.Helper()
	var dictionary map[string]bencode.Bytes
	if err := bencode.Unmarshal(torrent, &dictionary); err != nil {
		t.Fatalf("decode sanitized torrent: %v", err)
	}
	return dictionary
}

func TestSanitize_keepsInfohashAndDropsAnnounce(t *testing.T) {
	original := indexerTorrent()
	expectedInfohash := infohashOf(t, original)

	sanitized, err := Sanitize(original)
	if err != nil {
		t.Fatalf("Sanitize: %v", err)
	}

	if gotInfohash := infohashOf(t, sanitized); gotInfohash != expectedInfohash {
		t.Fatalf("gotValue infohash %s, expectedValue %s", gotInfohash, expectedInfohash)
	}
	dictionary := topLevelKeys(t, sanitized)
	if gotInfo := string(dictionary["info"]); gotInfo != infoDictionary {
		t.Fatalf("gotValue info %q, expectedValue the original bytes %q", gotInfo, infoDictionary)
	}
	expectedKeys := []string{"creation date", "encoding", "info"}
	if len(dictionary) != len(expectedKeys) {
		t.Fatalf("gotValue keys %v, expectedValue only %v", dictionary, expectedKeys)
	}
	for _, expectedKey := range expectedKeys {
		if _, present := dictionary[expectedKey]; !present {
			t.Fatalf("gotValue keys %v, expectedValue %q kept", dictionary, expectedKey)
		}
	}
	for _, leaked := range []string{fakePasskey, "tracker.example.com", "uploader", "seed.example.com", "/u/42"} {
		if bytes.Contains(sanitized, []byte(leaked)) {
			t.Fatalf("gotValue %q still in %q", leaked, sanitized)
		}
	}
}

func TestSanitize_keepsPieceLayersOfHybridTorrents(t *testing.T) {
	original := []byte("d" + bencodedString("announce") + bencodedString("https://example.com/a") +
		bencodedString("info") + infoDictionary + bencodedString("piece layers") + "d" +
		bencodedString(strings.Repeat("B", 32)) + bencodedString(strings.Repeat("C", 20)) + "ee")

	sanitized, err := Sanitize(original)
	if err != nil {
		t.Fatalf("Sanitize: %v", err)
	}

	if _, present := topLevelKeys(t, sanitized)["piece layers"]; !present {
		t.Fatalf("gotValue %q, expectedValue piece layers kept (hashes only, needed by v2 clients)", sanitized)
	}
}

func TestSanitize_refusesHostileOrInvalidInput(t *testing.T) {
	deepList := strings.Repeat("l", 200) + strings.Repeat("e", 200)
	oversized := []byte("d7:comment" + "8388608:" + strings.Repeat("x", 8<<20) + "4:info" + infoDictionary + "e")
	scenarios := map[string][]byte{
		"empty":                {},
		"not a dictionary":     []byte("l4:infoe"),
		"no info":              []byte("d8:announce3:urle"),
		"info not a dict":      []byte("d4:info3:abce"),
		"truncated":            indexerTorrent()[:100],
		"trailing bytes":       append(indexerTorrent(), 'x'),
		"too deep":             []byte("d7:comment" + deepList + "4:info" + infoDictionary + "e"),
		"larger than 8 MiB":    oversized,
		"string past the end":  []byte("d4:info999999999:e"),
		"negative length":      []byte("d4:info-1:e"),
		"unknown value type":   []byte("d4:infoz"),
		"integer key":          []byte("di1e4:infoe"),
		"garbage":              []byte("\x00\xff\xfe"),
		"unterminated integer": []byte("d4:infoi12"),
		"duplicate info":       []byte("d4:info" + infoDictionary + "4:info" + infoDictionary + "e"),
		"duplicate announce":   []byte("d8:announce1:a8:announce1:b4:info" + infoDictionary + "e"),
		"unterminated dict":    []byte("d4:info" + infoDictionary),
	}
	for name, input := range scenarios {
		t.Run(name, func(t *testing.T) {
			sanitized, err := Sanitize(input)

			if !errors.Is(err, ErrInvalidTorrent) || sanitized != nil {
				t.Fatalf("gotValue %q, %v, expectedValue ErrInvalidTorrent", sanitized, err)
			}
		})
	}
}

func TestSanitize_errorNeverQuotesTheTorrent(t *testing.T) {
	_, err := Sanitize([]byte("d" + bencodedString("announce") + bencodedString("https://tracker.example.com/"+fakePasskey) + "z"))

	if err == nil || strings.Contains(err.Error(), fakePasskey) || strings.Contains(err.Error(), "tracker.example.com") {
		t.Fatalf("gotValue %v, expectedValue an error without the torrent content", err)
	}
}

func FuzzSanitize(f *testing.F) {
	f.Add(indexerTorrent())
	f.Add([]byte("d4:info" + infoDictionary + "e"))
	f.Add([]byte("d4:infod4:infoli1eeee"))
	f.Fuzz(func(t *testing.T, input []byte) {
		sanitized, err := Sanitize(input)
		if err != nil {
			return
		}
		for key := range topLevelKeys(t, sanitized) {
			if !keptTopLevelKeys[key] {
				t.Fatalf("gotValue key %q kept for %q, expectedValue only the allow-list", key, input)
			}
		}
		original, inputErr := metainfo.Load(bytes.NewReader(input))
		if inputErr != nil {
			return
		}
		cleaned, outputErr := metainfo.Load(bytes.NewReader(sanitized))
		if outputErr != nil || cleaned.HashInfoBytes() != original.HashInfoBytes() {
			t.Fatalf("gotValue %v, infohash drifted for %q", outputErr, input)
		}
	})
}
