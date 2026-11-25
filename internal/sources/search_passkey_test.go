package sources

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"necromanciarr/internal/torznab"
)

// shortPasskey fait 7 octets : sous config.MinKnownSecretLength, il n'est masqué que dans une URL.
const shortPasskey = "abc1234"

// announcedTorrent est un .torrent v1 d'un fichier de cette taille, nommé root (dans un dossier root si path est
// donné), annoncé sur announce.
func announcedTorrent(t *testing.T, announce, root, path string, size int64) []byte {
	t.Helper()
	info := metainfo.Info{Name: root, PieceLength: searchPieceLength, Pieces: bytes.Repeat([]byte("A"), 20)}
	if path == "" {
		info.Length = size
	} else {
		info.Files = []metainfo.FileInfo{{Path: []string{path}, Length: size}}
	}
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := bencode.Marshal(metainfo.MetaInfo{InfoBytes: infoBytes, Announce: announce})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func passkeyAnnounce(passkey string) string {
	return "https://tracker-a.example.com/" + passkey + "/announce"
}

// percentEncodeAll échappe chaque octet : la forme qu'aucun url.QueryEscape ne produit pour une passkey alphanumérique.
func percentEncodeAll(text string) string {
	var encoded strings.Builder
	for _, character := range []byte(text) {
		fmt.Fprintf(&encoded, "%%%02X", character)
	}
	return encoded.String()
}

func searchOneItem(t *testing.T, title string, raw []byte) ([]Found, error) {
	t.Helper()
	setup := newSearchSetup(t)
	indexer := setup.add(indexerA, movieCaps())
	item := indexer.serve("win", 5, raw)
	item.Title = title
	indexer.results = torznab.Results{Items: []torznab.Item{item}}
	found, _, err := runSearch(t, setup.chain(t), movieQuery(5))
	return found, err
}

func foundNames(found []Found) []string {
	names := make([]string, 0, len(found))
	for _, kept := range found {
		names = append(names, kept.Release.Name)
	}
	return names
}

func TestSearch_titleCarryingTheAnnouncePasskeyIsReplacedByTheTorrentName(t *testing.T) {
	cases := []struct{ name, title string }{
		{"raw", "Release " + fakePasskey},
		{"upper case", "Release " + strings.ToUpper(fakePasskey)},
		{"percent-encoded", "Release " + percentEncodeAll(fakePasskey)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			found, err := searchOneItem(t, testCase.title, announcedTorrent(t, passkeyAnnounce(fakePasskey), "Movie.Title.mkv", "", 5))

			valeurAttendue := "Movie.Title.mkv"
			if valeurObtenue := foundNames(found); err != nil || len(valeurObtenue) != 1 || valeurObtenue[0] != valeurAttendue {
				t.Fatalf("valeurObtenue names %q err %v, valeurAttendue one candidate named %q", valeurObtenue, err, valeurAttendue)
			}
		})
	}
}

func TestSearch_torrentWhoseNameOrPathCarriesThePasskeyIsRefused(t *testing.T) {
	cases := []struct{ name, root, path string }{
		{"root", "Movie " + fakePasskey + ".mkv", ""},
		{"file path", "Movie", "Movie " + fakePasskey + ".mkv"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			found, err := searchOneItem(t, "Release win", announcedTorrent(t, passkeyAnnounce(fakePasskey), testCase.root, testCase.path, 5))

			if len(found) != 0 || !containsError(searchRefusals(t, err).Skipped, ErrInvalidTorrent) {
				t.Fatalf("valeurObtenue found %d err %v, valeurAttendue the item refused as an invalid torrent", len(found), err)
			}
		})
	}
}

func TestSearch_shortAnnounceValueIsNeitherMaskedNorRefused(t *testing.T) {
	title := "Release " + shortPasskey
	found, err := searchOneItem(t, title, announcedTorrent(t, passkeyAnnounce(shortPasskey), "Movie "+shortPasskey+".mkv", "", 5))

	if valeurObtenue := foundNames(found); err != nil || len(valeurObtenue) != 1 || valeurObtenue[0] != title {
		t.Fatalf("valeurObtenue names %q err %v, valeurAttendue the item kept under its title %q", valeurObtenue, err, title)
	}
}

// titleCutAt fait tenir la coupe de maxFallbackQueryRunes juste après le texte voulu : padding de « x », espace, puis
// head (gardé en entier) suivi de tail (coupé).
func titleCutAt(head, tail string) string {
	return strings.Repeat("x", maxFallbackQueryRunes-1-len(head)) + " " + head + tail
}

func TestSearch_titleCarryingThePasskeyAcrossTheCutIsReplacedByTheTorrentName(t *testing.T) {
	cases := []struct{ name, title string }{
		{"eleven bytes kept", titleCutAt(fakePasskey[:11], fakePasskey[11:])},
		{"six bytes kept", titleCutAt(fakePasskey[:6], fakePasskey[6:])},
		{"passkey fully before the cut", titleCutAt(fakePasskey, strings.Repeat("y", 40))},
		{"prefix kept, passkey never whole", titleCutAt(fakePasskey[:11], "yyyyyyyy")},
		{"percent-encoded prefix kept", titleCutAt(percentEncodeAll(fakePasskey[:8]), percentEncodeAll(fakePasskey[8:]))},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			found, err := searchOneItem(t, testCase.title, announcedTorrent(t, passkeyAnnounce(fakePasskey), "Movie.Title.mkv", "", 5))

			valeurAttendue := "Movie.Title.mkv"
			if valeurObtenue := foundNames(found); err != nil || len(valeurObtenue) != 1 || valeurObtenue[0] != valeurAttendue {
				t.Fatalf("valeurObtenue names %q err %v, valeurAttendue one candidate named %q", valeurObtenue, err, valeurAttendue)
			}
		})
	}
}

func TestSearch_longTitleWithoutPasskeyIsTruncatedAndKept(t *testing.T) {
	cases := []struct{ name, announced, title string }{
		{"no announce value in the title", fakePasskey, strings.Repeat("r", 300)},
		{"short announce value across the cut", shortPasskey, titleCutAt(shortPasskey[:5], shortPasskey[5:])},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			found, err := searchOneItem(t, testCase.title, announcedTorrent(t, passkeyAnnounce(testCase.announced), "Movie.Title.mkv", "", 5))

			valeurAttendue := string([]rune(testCase.title)[:maxFallbackQueryRunes])
			if valeurObtenue := foundNames(found); err != nil || len(valeurObtenue) != 1 || valeurObtenue[0] != valeurAttendue {
				t.Fatalf("valeurObtenue names %q err %v, valeurAttendue the title cut at %d runes", valeurObtenue, err, maxFallbackQueryRunes)
			}
		})
	}
}
