package sources

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"necromanciarr/internal/config"
	"necromanciarr/internal/torrentcheck"
)

const (
	firstAnnounce  = "https://tracker.example.com/" + fakePasskey + "/announce"
	secondAnnounce = "udp://tracker.example.com:6969/announce?pk=" + fakePasskey
)

// toleratedInfoDictionary est non canonique (clés hors d'ordre, clé inconnue) mais lisible par torrentcheck.Parse ;
// infoDictionary, avec son entier « i007e », ne l'est pas.
const toleratedInfoDictionary = "d4:name5:a.mkv6:lengthi5e12:piece lengthi16384e6:pieces20:AAAAAAAAAAAAAAAAAAAA" +
	"7:privatei1e7:x-extral1:zi3eee"

func announceTiers() [][]string { return [][]string{{firstAnnounce, secondAnnounce}} }

// injectableTorrent est indexerTorrent avec un info que torrentcheck.Parse accepte.
func injectableTorrent() []byte {
	return bytes.Replace(indexerTorrent(), []byte(infoDictionary), []byte(toleratedInfoDictionary), 1)
}

func mustInject(t *testing.T, torrent []byte, tiers [][]string) []byte {
	t.Helper()
	injected, err := InjectAnnounce(torrent, tiers)
	if err != nil {
		t.Fatalf("InjectAnnounce: %v", err)
	}
	return injected
}

func TestInjectAnnounce_keepsInfoBytesAndInfohash(t *testing.T) {
	original := injectableTorrent()
	expectedInfohash := infohashOf(t, original)

	injected := mustInject(t, original, announceTiers())

	if gotInfohash := infohashOf(t, injected); gotInfohash != expectedInfohash {
		t.Fatalf("gotValue infohash %s, expectedValue %s", gotInfohash, expectedInfohash)
	}
	if gotInfo := string(topLevelKeys(t, injected)["info"]); gotInfo != toleratedInfoDictionary {
		t.Fatalf("gotValue info %q, expectedValue the original non-canonical bytes", gotInfo)
	}
	parsed, err := torrentcheck.Parse(injected)
	if err != nil || parsed.InfoHash != expectedInfohash {
		t.Fatalf("gotValue %v, %v, expectedValue torrentcheck.Parse to read infohash %s", parsed, err, expectedInfohash)
	}
}

func TestInjectAnnounce_writesTopLevelKeysInBencodeOrder(t *testing.T) {
	injected := mustInject(t, injectableTorrent(), announceTiers())

	expectedPrefix := "d" + bencodedString("announce")
	gotKeys := topLevelKeyOrder(t, injected)
	expectedKeys := []string{"announce", "announce-list", "creation date", "encoding", "info"}
	if !slices.Equal(gotKeys, expectedKeys) || !strings.HasPrefix(string(injected), expectedPrefix) {
		t.Fatalf("gotValue keys %v, expectedValue %v in byte order", gotKeys, expectedKeys)
	}
}

func TestInjectAnnounce_isDeterministic(t *testing.T) {
	first := mustInject(t, injectableTorrent(), announceTiers())
	second := mustInject(t, injectableTorrent(), announceTiers())

	if !bytes.Equal(first, second) {
		t.Fatal("gotValue two different outputs, expectedValue identical bytes for the same input")
	}
}

func TestInjectAnnounce_preservesPrivateFlag(t *testing.T) {
	injected := mustInject(t, injectableTorrent(), announceTiers())

	metaInfo, err := metainfo.Load(bytes.NewReader(injected))
	if err != nil {
		t.Fatalf("metainfo.Load: %v", err)
	}
	info, err := metaInfo.UnmarshalInfo()
	if err != nil {
		t.Fatalf("UnmarshalInfo: %v", err)
	}
	if info.Private == nil || !*info.Private {
		t.Fatalf("gotValue private %v, expectedValue true", info.Private)
	}
}

func TestInjectAnnounce_buildsAnnounceListTiers(t *testing.T) {
	injected := mustInject(t, injectableTorrent(), announceTiers())

	metaInfo, err := metainfo.Load(bytes.NewReader(injected))
	if err != nil {
		t.Fatalf("metainfo.Load: %v", err)
	}
	if metaInfo.Announce != firstAnnounce {
		t.Fatalf("gotValue announce of %d bytes, expectedValue the first URL", len(metaInfo.Announce))
	}
	expectedTiers := [][]string{{firstAnnounce, secondAnnounce}}
	if !slices.EqualFunc([][]string(metaInfo.AnnounceList), expectedTiers, slices.Equal[[]string]) {
		t.Fatalf("gotValue %d tiers, expectedValue one tier with both URLs in order", len(metaInfo.AnnounceList))
	}
}

func TestInjectAnnounce_keepsSeveralTiersInOrder(t *testing.T) {
	tiers := [][]string{{"https://b.example.com/announce"}, {"https://a.example.com/announce", "udp://a.example.com:1/x"}}

	injected := mustInject(t, injectableTorrent(), tiers)

	gotTiers, err := AnnounceTiers(injected)
	if err != nil || !slices.EqualFunc(gotTiers, tiers, slices.Equal[[]string]) {
		t.Fatalf("gotValue %d tiers, %v, expectedValue the tiers unchanged and in order", len(gotTiers), err)
	}
}

func TestInjectAnnounce_dropsCommentAndCreatedBy(t *testing.T) {
	injected := mustInject(t, injectableTorrent(), announceTiers())

	dictionary := topLevelKeys(t, injected)
	for _, droppedKey := range []string{"comment", "created by", "url-list", "httpseeds", "nodes", "publisher-url"} {
		if _, present := dictionary[droppedKey]; present {
			t.Fatalf("gotValue key %q kept, expectedValue removed like Sanitize", droppedKey)
		}
	}
	for _, leaked := range []string{"uploader", "seed.example.com", "/u/42", "from https://example.com"} {
		if bytes.Contains(injected, []byte(leaked)) {
			t.Fatalf("gotValue %q still in the injected torrent", leaked)
		}
	}
}

func TestInjectAnnounce_keepsPieceLayers(t *testing.T) {
	original := []byte("d" + bencodedString("info") + toleratedInfoDictionary + bencodedString("piece layers") + "d" +
		bencodedString(strings.Repeat("B", 32)) + bencodedString(strings.Repeat("C", 20)) + "ee")

	injected := mustInject(t, original, announceTiers())

	if _, present := topLevelKeys(t, injected)["piece layers"]; !present {
		t.Fatal("gotValue piece layers dropped, expectedValue kept as in Sanitize")
	}
}

func TestInjectAnnounce_refusesMalformedTopLevel(t *testing.T) {
	scenarios := map[string][]byte{
		"empty":                        {},
		"not a dictionary":             []byte("l4:infoe"),
		"no info":                      []byte("d8:announce3:urle"),
		"info not a dict":              []byte("d4:info3:abce"),
		"truncated":                    injectableTorrent()[:100],
		"larger than 8 MiB":            append([]byte("d7:comment8388608:"), make([]byte, 8<<20)...),
		"garbage":                      []byte("\x00\xff\xfe"),
		"unterminated dict":            []byte("d4:info" + infoDictionary),
		"info not a torrent":           []byte("d4:info" + "d4:name1:ae" + "e"),
		"info refused by torrentcheck": indexerTorrent(),
	}
	for name, input := range scenarios {
		t.Run(name, func(t *testing.T) {
			injected, err := InjectAnnounce(input, announceTiers())

			if !errors.Is(err, ErrInvalidTorrent) || injected != nil {
				t.Fatalf("gotValue %d bytes, %v, expectedValue ErrInvalidTorrent", len(injected), err)
			}
		})
	}
}

func TestInjectAnnounce_refusesDuplicateKeys(t *testing.T) {
	scenarios := map[string][]byte{
		"duplicate info":    []byte("d4:info" + infoDictionary + "4:info" + infoDictionary + "e"),
		"duplicate comment": []byte("d7:comment1:a7:comment1:b4:info" + infoDictionary + "e"),
	}
	for name, input := range scenarios {
		t.Run(name, func(t *testing.T) {
			if _, err := InjectAnnounce(input, announceTiers()); !errors.Is(err, ErrInvalidTorrent) {
				t.Fatalf("gotValue %v, expectedValue ErrInvalidTorrent", err)
			}
		})
	}
}

func TestInjectAnnounce_refusesTrailingBytes(t *testing.T) {
	if _, err := InjectAnnounce(append(injectableTorrent(), 'x'), announceTiers()); !errors.Is(err, ErrInvalidTorrent) {
		t.Fatalf("gotValue %v, expectedValue ErrInvalidTorrent", err)
	}
}

func TestInjectAnnounce_refusesHostileTiers(t *testing.T) {
	tooManyURLs := [][]string{make([]string, config.MaxAnnounceURLs+1)}
	for urlIndex := range tooManyURLs[0] {
		tooManyURLs[0][urlIndex] = fmt.Sprintf("https://t%d.example.com/announce", urlIndex)
	}
	scenarios := map[string][][]string{
		"no tier":           nil,
		"empty tier":        {{}},
		"empty url":         {{""}},
		"too many urls":     tooManyURLs,
		"url too long":      {{"https://tracker.example.com/" + strings.Repeat("a", config.MaxAnnounceURLBytes)}},
		"newline":           {{"https://tracker.example.com/" + fakePasskey + "\n/announce"}},
		"carriage return":   {{"https://tracker.example.com/" + fakePasskey + "\r"}},
		"space":             {{"https://tracker.example.com/" + fakePasskey + " x"}},
		"nul":               {{"https://tracker.example.com/\x00" + fakePasskey}},
		"tab":               {{"\thttps://tracker.example.com/" + fakePasskey}},
		"invalid utf-8":     {{"https://tracker.example.com/\xff" + fakePasskey}},
		"ftp scheme":        {{"ftp://tracker.example.com/" + fakePasskey}},
		"javascript scheme": {{"javascript:alert(1)//" + fakePasskey}},
		"no scheme":         {{"tracker.example.com/" + fakePasskey}},
		"no host":           {{"https:///" + fakePasskey}},
		"fragment":          {{"https://tracker.example.com/announce#" + fakePasskey}},
		"opaque":            {{"udp:tracker.example.com:" + fakePasskey}},
		"second tier empty": {{firstAnnounce}, {}},
		"no-break space":    {{"https://tracker.example.com/" + fakePasskey + "\u00a0"}},
		"trailing space":    {{"https://tracker.example.com/" + fakePasskey + " "}},
	}
	for name, tiers := range scenarios {
		t.Run(name, func(t *testing.T) {
			injected, err := InjectAnnounce(injectableTorrent(), tiers)

			if !errors.Is(err, ErrInvalidAnnounce) || injected != nil {
				t.Fatalf("gotValue %d bytes, %v, expectedValue ErrInvalidAnnounce", len(injected), err)
			}
			for _, leaked := range []string{fakePasskey, "tracker.example.com", "example.com"} {
				if strings.Contains(err.Error(), leaked) {
					t.Fatalf("gotValue error quoting the announce, expectedValue counts and indexes only")
				}
			}
		})
	}
}

func TestInjectAnnounce_acceptsTheThreeSchemesInAnyCase(t *testing.T) {
	tiers := [][]string{{"HTTPS://tracker.example.com/a", "http://tracker.example.com/a", "UDP://tracker.example.com:6969/a"}}

	if _, err := InjectAnnounce(injectableTorrent(), tiers); err != nil {
		t.Fatalf("gotValue %v, expectedValue http, https and udp accepted", err)
	}
}

func TestInjectAnnounce_refusesInfohashDrift(t *testing.T) {
	reencodingInfo := func(topLevel map[string]bencode.Bytes) ([]byte, error) {
		var info map[string]any
		if err := bencode.Unmarshal(topLevel[infoKey], &info); err != nil {
			return nil, err
		}
		canonicalInfo, err := bencode.Marshal(info)
		if err != nil {
			return nil, err
		}
		topLevel[infoKey] = canonicalInfo
		return bencode.Marshal(topLevel)
	}

	injected, err := injectAnnounce(injectableTorrent(), announceTiers(), reencodingInfo)

	if !errors.Is(err, ErrInfohashChanged) || injected != nil {
		t.Fatalf("gotValue %d bytes, %v, expectedValue ErrInfohashChanged", len(injected), err)
	}
	if ReasonInfohashChanged != "infohash_changed_after_inject" {
		t.Fatalf("gotValue reason %q, expectedValue infohash_changed_after_inject", ReasonInfohashChanged)
	}
}

func TestInjectAnnounce_errorNeverQuotesTheTorrentOrTheTiers(t *testing.T) {
	_, err := InjectAnnounce([]byte("d"+bencodedString("comment")+bencodedString(fakePasskey)+"z"), announceTiers())

	if err == nil || strings.Contains(err.Error(), fakePasskey) || strings.Contains(err.Error(), "example.com") {
		t.Fatalf("gotValue %v, expectedValue an error without content", err)
	}
}

func TestAnnounceTiers_readsAnnounceList(t *testing.T) {
	torrent := []byte("d" + bencodedString("announce") + bencodedString("https://ignored.example.com/a") +
		bencodedString("announce-list") + "ll" + bencodedString(firstAnnounce) + bencodedString(secondAnnounce) + "e" +
		"l" + bencodedString("https://backup.example.com/a") + "ee" + bencodedString("info") + infoDictionary + "e")

	gotTiers, err := AnnounceTiers(torrent)

	expectedTiers := [][]string{{firstAnnounce, secondAnnounce}, {"https://backup.example.com/a"}}
	if err != nil || !slices.EqualFunc(gotTiers, expectedTiers, slices.Equal[[]string]) {
		t.Fatalf("gotValue %d tiers, %v, expectedValue the announce-list tiers", len(gotTiers), err)
	}
}

func TestAnnounceTiers_fallsBackToAnnounce(t *testing.T) {
	torrent := []byte("d" + bencodedString("announce") + bencodedString(firstAnnounce) +
		bencodedString("info") + infoDictionary + "e")

	gotTiers, err := AnnounceTiers(torrent)

	if err != nil || !slices.EqualFunc(gotTiers, [][]string{{firstAnnounce}}, slices.Equal[[]string]) {
		t.Fatalf("gotValue %d tiers, %v, expectedValue one tier with the announce", len(gotTiers), err)
	}
}

func TestAnnounceTiers_dropsInvalidEntries(t *testing.T) {
	torrent := []byte("d" + bencodedString("announce-list") + "l" +
		"l" + "i42e" + bencodedString("") + bencodedString("ftp://bad.example.com/x") + bencodedString(firstAnnounce) + "e" +
		bencodedString("not a tier") + "le" + "l" + "d1:ai1ee" + "e" + "e" +
		bencodedString("info") + infoDictionary + "e")

	gotTiers, err := AnnounceTiers(torrent)

	if err != nil || !slices.EqualFunc(gotTiers, [][]string{{firstAnnounce}}, slices.Equal[[]string]) {
		t.Fatalf("gotValue %d tiers, %v, expectedValue only the valid URL in one tier", len(gotTiers), err)
	}
}

func TestAnnounceTiers_noAnnounceIsErrNoAnnounce(t *testing.T) {
	scenarios := map[string][]byte{
		"none":                 []byte("d4:info" + infoDictionary + "e"),
		"only invalid entries": []byte("d" + bencodedString("announce") + "i1e" + bencodedString("announce-list") + "ll" + bencodedString("x") + "ee4:info" + infoDictionary + "e"),
	}
	for name, input := range scenarios {
		t.Run(name, func(t *testing.T) {
			if gotTiers, err := AnnounceTiers(input); !errors.Is(err, ErrNoAnnounce) || gotTiers != nil {
				t.Fatalf("gotValue %d tiers, %v, expectedValue ErrNoAnnounce", len(gotTiers), err)
			}
		})
	}
}

func TestAnnounceTiers_refusesHostileInput(t *testing.T) {
	var manyTiers strings.Builder
	for urlIndex := 0; urlIndex <= config.MaxAnnounceURLs; urlIndex++ {
		manyTiers.WriteString("l" + bencodedString(fmt.Sprintf("https://t%d.example.com/a", urlIndex)) + "e")
	}
	scenarios := map[string]struct {
		input       []byte
		expectedErr error
	}{
		"too many urls":  {[]byte("d" + bencodedString("announce-list") + "l" + manyTiers.String() + "e4:info" + infoDictionary + "e"), ErrInvalidAnnounce},
		"duplicate key":  {[]byte("d8:announce1:a8:announce1:b4:info" + infoDictionary + "e"), ErrInvalidTorrent},
		"trailing bytes": {append(injectableTorrent(), 'x'), ErrInvalidTorrent},
		"no info":        {[]byte("d8:announce3:urle"), ErrInvalidTorrent},
		"garbage":        {[]byte("\x00\xff"), ErrInvalidTorrent},
	}
	for name, scenario := range scenarios {
		t.Run(name, func(t *testing.T) {
			gotTiers, err := AnnounceTiers(scenario.input)

			if !errors.Is(err, scenario.expectedErr) || gotTiers != nil {
				t.Fatalf("gotValue %d tiers, %v, expectedValue %v", len(gotTiers), err, scenario.expectedErr)
			}
		})
	}
}

func TestDiscoveredSecretValues_extractsPasskeyShapes(t *testing.T) {
	scenarios := map[string]struct {
		announce       string
		expectedValues []string
	}{
		"query passkey":  {"https://t.example/announce?passkey=0000aaaa0000aaaa", []string{"0000aaaa0000aaaa"}},
		"path passkey":   {"https://t.example/0000aaaa0000aaaa/announce", []string{"0000aaaa0000aaaa"}},
		"udp query pk":   {"udp://t.example:6969/announce?pk=0000bbbb0000bbbb", []string{"0000bbbb0000bbbb"}},
		"userinfo":       {"https://alice:fake-pw-1@t.example/announce.php", []string{"alice", "fake-pw-1"}},
		"encoded path":   {"https://t.example/announce/0000%2Bcccc0000", []string{"0000+cccc0000"}},
		"bare query":     {"https://t.example/announce?0000dddd0000dddd", []string{"0000dddd0000dddd"}},
		"short values":   {"https://t.example/ab/announce?uid=42", []string{"ab", "42"}},
		"short path":     {"https://t.example/ab12cd/announce", []string{"ab12cd"}},
		"encoded slash":  {"https://t.example/abc%2Fdefghijk/announce", []string{"abc/defghijk"}},
		"common names":   {"https://t.example/tracker.php/announce.php/scrape.php/index.php", nil},
		"several values": {"https://t.example/0000eeee0000eeee/announce?uid=12345678&pk=0000ffff0000ffff", []string{"0000eeee0000eeee", "12345678", "0000ffff0000ffff"}},
	}
	for name, scenario := range scenarios {
		t.Run(name, func(t *testing.T) {
			gotValues := DiscoveredSecretValues([][]string{{scenario.announce}})

			if !slices.Equal(gotValues, scenario.expectedValues) {
				t.Fatalf("gotValue %q, expectedValue %q", gotValues, scenario.expectedValues)
			}
		})
	}
}

func TestDiscoveredSecretValues_shortValueIsMaskedInsideURLsOnly(t *testing.T) {
	secrets := config.Config{}.WithDiscoveredSecrets(DiscoveredSecretValues([][]string{{"https://t.example/ab12cd/announce"}})...)

	if gotValue := config.RedactText("https://t.example/ab12cd/announce", secrets...); gotValue != "https://t.example/***/announce" {
		t.Fatalf("gotValue %q, expectedValue the short passkey masked in the URL", gotValue)
	}
	prose := "ab12cd is an ordinary word here"
	if gotValue := config.RedactText(prose, secrets...); gotValue != prose {
		t.Fatalf("gotValue %q, expectedValue prose untouched", gotValue)
	}
	encoded, err := config.RedactJSON(map[string]string{"note": "ab12cd"}, secrets...)
	if err != nil || !strings.Contains(string(encoded), `"ab12cd"`) {
		t.Fatalf("gotValue %s, %v, expectedValue JSON untouched", encoded, err)
	}
}

func TestDiscoveredSecretValues_encodedSlashLeavesNoFragment(t *testing.T) {
	announce := "https://t.example/abc%2Fdefghijk/announce"
	secrets := config.Config{}.WithDiscoveredSecrets(DiscoveredSecretValues([][]string{{announce}})...)

	gotValue := config.RedactText("failed: "+announce, secrets...)

	if strings.Contains(gotValue, "abc") || strings.Contains(gotValue, "defghijk") {
		t.Fatalf("gotValue %q, expectedValue no piece of the passkey", gotValue)
	}
}

func TestTiers_rendersNoURL(t *testing.T) {
	tiers := Tiers{{firstAnnounce, secondAnnounce}}
	var logged bytes.Buffer
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("tiers", "tiers", tiers)
	encoded, err := json.Marshal(tiers)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	renderings := []string{
		fmt.Sprintf("%v", tiers), fmt.Sprintf("%+v", tiers), fmt.Sprintf("%#v", tiers), fmt.Sprint(tiers),
		fmt.Sprintf("%v %+v %#v", &tiers, &tiers, &tiers), string(encoded), logged.String(),
	}
	for _, rendering := range renderings {
		if strings.Contains(rendering, fakePasskey) || strings.Contains(rendering, "/announce") ||
			!strings.Contains(rendering, "udp://tracker.example.com:6969/***") {
			t.Fatalf("gotValue %s, expectedValue only redacted hosts", rendering)
		}
	}
}

func TestAnnounceTiers_returnsRedactingTiers(t *testing.T) {
	gotTiers, err := AnnounceTiers(mustInject(t, injectableTorrent(), announceTiers()))
	if err != nil {
		t.Fatalf("AnnounceTiers: %v", err)
	}

	if rendering := fmt.Sprintf("%v", gotTiers); strings.Contains(rendering, fakePasskey) {
		t.Fatalf("gotValue %s, expectedValue redacted", rendering)
	}
	if _, err := InjectAnnounce(injectableTorrent(), gotTiers); err != nil {
		t.Fatalf("gotValue %v, expectedValue Tiers accepted as [][]string", err)
	}
}

func TestDiscoveredSecretValues_deduplicatesAcrossTiers(t *testing.T) {
	tiers := [][]string{{"https://t.example/0000aaaa0000aaaa/announce"}, {"udp://t.example:1/announce?pk=0000aaaa0000aaaa"}}

	gotValues := DiscoveredSecretValues(tiers)

	if !slices.Equal(gotValues, []string{"0000aaaa0000aaaa"}) {
		t.Fatalf("gotValue %q, expectedValue one value", gotValues)
	}
}

// topLevelKeyOrder lit les clés de premier niveau dans l'ordre d'écriture : une map Go perdrait cet ordre.
func topLevelKeyOrder(t *testing.T, torrent []byte) []string {
	t.Helper()
	decoder := bencode.NewDecoder(bytes.NewReader(torrent[1:]))
	var keys []string
	for range len(topLevelKeys(t, torrent)) {
		var key string
		var value bencode.Bytes
		if decoder.Decode(&key) != nil || decoder.Decode(&value) != nil {
			t.Fatal("decode key order")
		}
		keys = append(keys, key)
	}
	return keys
}

func FuzzInjectAnnounce(f *testing.F) {
	f.Add(injectableTorrent(), firstAnnounce)
	f.Add([]byte("d4:info"+toleratedInfoDictionary+"e"), "udp://t.example:1/a")
	f.Add([]byte("d4:infod4:infoli1eeee"), "https://t.example/a\n")
	f.Fuzz(func(t *testing.T, input []byte, announce string) {
		injected, err := InjectAnnounce(input, [][]string{{announce}})
		if err != nil {
			return
		}
		parsed, parseErr := torrentcheck.Parse(injected)
		if parseErr != nil {
			t.Fatalf("gotValue %v, expectedValue the injected torrent to parse", parseErr)
		}
		topLevel, decodeErr := decodeTopLevel(input)
		if decodeErr != nil || parsed.InfoHash != metainfo.HashBytes(topLevel[infoKey]).HexString() {
			t.Fatalf("gotValue %v, infohash drifted", decodeErr)
		}
		if _, present := topLevelKeys(t, injected)["comment"]; present {
			t.Fatal("gotValue comment kept")
		}
	})
}

func FuzzAnnounceTiers(f *testing.F) {
	f.Add(injectableTorrent())
	f.Add([]byte("d8:announce" + bencodedString(firstAnnounce) + "4:info" + toleratedInfoDictionary + "e"))
	f.Add([]byte("d13:announce-listll1:ai1eee4:info" + infoDictionary + "e"))
	f.Fuzz(func(t *testing.T, input []byte) {
		tiers, err := AnnounceTiers(input)
		if err != nil {
			return
		}
		urlCount := 0
		for _, tier := range tiers {
			urlCount += len(tier)
		}
		if len(tiers) == 0 || urlCount > config.MaxAnnounceURLs {
			t.Fatalf("gotValue %d tiers and %d urls, expectedValue bounded and non-empty", len(tiers), urlCount)
		}
		if _, parseErr := torrentcheck.Parse(input); parseErr != nil {
			return
		}
		if _, injectErr := InjectAnnounce(input, tiers); injectErr != nil {
			t.Fatalf("gotValue %v, expectedValue tiers read back to be injectable", injectErr)
		}
	})
}
