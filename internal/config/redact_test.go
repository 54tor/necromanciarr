package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestRedactText_masksUrlsProxySegmentsAndKnownSecrets(t *testing.T) {
	text := "get http://user:pw@example.com/proxy/QUIKEY/x?apikey=abc failed, token=zzz, bare KNOWNSECRET"

	gotValue := RedactText(text, "KNOWNSECRET")

	for _, leaked := range []string{"pw@", "QUIKEY", "abc", "zzz", "KNOWNSECRET"} {
		if strings.Contains(gotValue, leaked) {
			t.Fatalf("%q leaked in %q", leaked, gotValue)
		}
	}
}

func TestKnownSecrets_listsProxyKeyAndPasswords(t *testing.T) {
	settings := Config{
		AuthToken:      "tok",
		TorrentClients: []TorrentClient{{BaseURL: "http://example.com/proxy/KEY/qbittorrent", Password: "pw"}},
		FSProviders:    []FSProvider{{RC: "http://u:rcpw@example.com"}},
	}

	gotValue := strings.Join(settings.KnownSecrets(), "|")

	for _, expectedFragment := range []string{"tok", "KEY", "pw", "rcpw"} {
		if !strings.Contains(gotValue, expectedFragment) {
			t.Fatalf("expectedValue %q in %q", expectedFragment, gotValue)
		}
	}
}

func TestRedactText_masksLongestSecretFirst(t *testing.T) {
	gotValue := RedactText("value ABCDEF end", "ABC", "ABCDEF")

	if strings.Contains(gotValue, "DEF") {
		t.Fatalf("gotValue %q", gotValue)
	}
}

func TestKnownSecrets_includesLibraryApiCredentials(t *testing.T) {
	settings := Config{LibraryAPI: "http://u:apipw@example.com/proxy/APIKEY/v1?token=QUERYTOKEN"}

	gotValue := strings.Join(settings.KnownSecrets(), "|")

	for _, expectedFragment := range []string{"apipw", "APIKEY", "QUERYTOKEN"} {
		if !strings.Contains(gotValue, expectedFragment) {
			t.Fatalf("expectedValue %q in %q", expectedFragment, gotValue)
		}
	}
}

// passkeyPlacements place une passkey courte, sans motif de longueur, derrière chaque nom de paramètre sensible.
func TestRedact_announcePasskeyInQuery(t *testing.T) {
	const shortPasskey = "zq7x"
	for _, parameterName := range []string{"passkey", "pk", "authkey", "torrent_pass", "key", "uid", "apikey", "secret", "token", "PASSKEY", "Pk", "pass%6Bey", "pass%256Bey"} {
		for _, template := range []string{
			"https://tracker-a.example.com/announce.php?%s=%s",
			"https://tracker-a.example.com/announce.php?info=1&%s=%s&x=2",
			"https://tracker-a.example.com/announce.php?info=1;%s=%s;x=2",
			"https://tracker-a.example.com/announce.php#%s=%s",
			"http://tracker-a.example.com/announce/%s/%s/x",
			"http://tracker-a.example.com/announce;%s=%s",
			"UDP://TRACKER-A.example.com:6969/?%s=%s",
		} {
			rawURL := fmt.Sprintf(template, parameterName, shortPasskey)

			gotValue := RedactText("announce failed: " + rawURL + " (timeout)")

			if strings.Contains(gotValue, shortPasskey) || !strings.Contains(strings.ToLower(gotValue), "tracker-a.example.com") || !strings.HasSuffix(gotValue, " (timeout)") {
				t.Fatalf("%s: gotValue %q, expectedValue the value masked and the host kept", rawURL, gotValue)
			}
		}
	}
}

func TestRedact_announcePasskeyInPathSegment(t *testing.T) {
	const passkey = "0000aaaa0000aaaa0000aaaa0000aaaa"
	percentEncoded := ""
	for _, character := range passkey {
		percentEncoded += fmt.Sprintf("%%%02X", character)
	}
	for name, rawURL := range map[string]string{
		"plain segment":         "https://tracker-a.example.com/" + passkey + "/announce",
		"upper case":            "https://tracker-a.example.com/" + strings.ToUpper(passkey) + "/announce",
		"percent-encoded":       "https://tracker-a.example.com/" + percentEncoded + "/announce",
		"lower percent-encoded": "https://tracker-a.example.com/" + strings.ToLower(percentEncoded) + "/announce",
		"encoded separator":     "https://tracker-a.example.com/announce%2F" + passkey,
		"double-encoded":        "https://tracker-a.example.com/announce%252F" + strings.ReplaceAll(percentEncoded, "%", "%25"),
		"with suffix":           "https://tracker-a.example.com/announce/" + passkey + ".torrent",
		"base64url":             "https://tracker-a.example.com/ann/Zm9vYmFyLWZha2Vfa2V5LTAw/x",
		"user info":             "https://alice:" + passkey + "@tracker-a.example.com/announce",
		"user only":             "https://" + passkey + "@tracker-a.example.com/announce",
		"query name":            "https://tracker-a.example.com/announce?" + passkey,
		"fragment":              "https://tracker-a.example.com/announce#" + passkey,
		"nested encoded url":    "http://127.0.0.1:8080/add?url=https%3A%2F%2Ftracker-a.example.com%2F" + passkey + "%2Fannounce",
	} {
		gotValue := RedactText("error: " + rawURL + " refused")

		for _, form := range []string{passkey, strings.ToUpper(passkey), percentEncoded, strings.ToLower(percentEncoded), "Zm9vYmFyLWZha2Vfa2V5LTAw", "%30%30%30%30%61"} {
			if strings.Contains(gotValue, form) {
				t.Fatalf("%s: gotValue %q, expectedValue no %q", name, gotValue, form)
			}
		}
		if !strings.Contains(gotValue, "example.com") && name != "nested encoded url" {
			t.Fatalf("%s: gotValue %q, expectedValue the host kept", name, gotValue)
		}
	}
}

func TestRedact_renderedAnnounceKeepsOnlySchemeAndHost(t *testing.T) {
	for rawURL, expectedValue := range map[string]string{
		"https://u:p@tracker-a.example.com:8443/abc/announce?pk=x":   "https://tracker-a.example.com:8443/***",
		"udp://[2001:db8::1]:6969/announce?pk=x":                     "udp://[2001:db8::1]:6969/***",
		"http://[fe80::1%25eth0]/announce/x":                         "http://[fe80::1%25eth0]/***",
		"HTTPS://Tracker-A.example.com/abc":                          "https://tracker-a.example.com/***",
		"udp://tracker-b.example.com:6969":                           "udp://tracker-b.example.com:6969/***",
		"http://203.0.113.7/announce?passkey=x#frag":                 "http://203.0.113.7/***",
		"tracker-a.example.com/announce":                             redactedMark,
		"udp:tracker-a.example.com":                                  redactedMark,
		"https:///announce":                                          redactedMark,
		"https://tracker-a.example.com:port/x":                       redactedMark,
		"":                                                           redactedMark,
		"https://tracker-a.example.com/\x01":                         redactedMark,
		"//tracker-a.example.com/announce":                           redactedMark,
		"https://user@tracker-a.example.com/announce/0000aaaa0000aa": "https://tracker-a.example.com/***",
	} {
		gotValue := RedactAnnounce(rawURL)

		if gotValue != expectedValue {
			t.Fatalf("RedactAnnounce(%q): gotValue %q, expectedValue %q", rawURL, gotValue, expectedValue)
		}
		if gotValue == redactedMark {
			continue
		}
		reparsed, err := url.Parse(gotValue)
		if err != nil || reparsed.Host == "" {
			t.Fatalf("RedactAnnounce(%q): gotValue %q that does not parse back with a host (%v)", rawURL, gotValue, err)
		}
	}
}

func TestRedact_longSegmentInFreeText(t *testing.T) {
	const infohash = "0123456789abcdef0123456789abcdef01234567"
	text := "seed failed for " + infohash + " at http://qbittorrent:8080/api/v2/torrents/" + infohash + "/files?hash=" + infohash + "&tag=necromanciarr"

	gotValue := RedactText(text)

	expectedValue := "seed failed for " + infohash + " at http://qbittorrent:8080/api/v2/torrents/***/files?hash=***&tag=necromanciarr"
	if gotValue != expectedValue {
		t.Fatalf("gotValue %q, expectedValue %q", gotValue, expectedValue)
	}
}

func TestRedact_shortSegmentsAndPlainTextAreKept(t *testing.T) {
	for _, text := range []string{
		"http://qbittorrent:8080/api/v2/torrents/add?category=necromanciarr",
		"https://tracker-a.example.com:8443/announce",
		"film 0123456789abcdef0123456789abcdef01234567 verified, base64url Zm9vYmFyLWZha2Vfa2V5LTAw outside any URL",
		"uid=1000 gid=1000 on /srv/media",
	} {
		if gotValue := RedactText(text); gotValue != text {
			t.Fatalf("gotValue %q, expectedValue %q unchanged", gotValue, text)
		}
	}
}

func TestRedact_passkeyAssignmentInFreeText(t *testing.T) {
	gotValue := RedactText("tracker said passkey=zq7x; authkey=zq8x torrent_pass=zq9x")

	for _, leaked := range []string{"zq7x", "zq8x", "zq9x"} {
		if strings.Contains(gotValue, leaked) {
			t.Fatalf("gotValue %q, expectedValue no %q", gotValue, leaked)
		}
	}
}

func TestRedactText_isIdempotent(t *testing.T) {
	for _, input := range []string{
		"https://u:p@tracker-a.example.com/0123456789abcdef0123456789abcdef/announce?pk=x&passkey=y;uid=1#k=z",
		"http://[2001:db8::1]:6969/announce?pk=%41%42",
		"see https://tracker-a.example.com/x?passkey=<b>#%0X0 and token=abc",
		longHostileText(1),
	} {
		once := RedactText(input)
		if twice := RedactText(once); once != twice {
			t.Fatalf("gotValue %q then %q, expectedValue a second pass to change nothing", truncateForTest(once), truncateForTest(twice))
		}
	}
}

// longHostileText assemble, répétés scale fois, les cas qui coûteraient cher à une expression à retour arrière :
// schémas en chaîne, segments vides, encodage en cascade, paramètres sans fin.
func longHostileText(scale int) string {
	return strings.Repeat("a://", 8000*scale) + " " + strings.Repeat("https://h/", 4000*scale) + " https://h/" +
		strings.Repeat("%25", 16000*scale) + "?" + strings.Repeat("a=b;", 8000*scale) + " " + strings.Repeat("x", 32000*scale)
}

func TestRedactText_staysLinearOnLongInputs(t *testing.T) {
	small, large := longHostileText(1), longHostileText(linearityScale)

	smallElapsed, largeElapsed := timeRedaction(small), timeRedaction(large)

	// Linéaire : le temps suit la taille (×8) ; quadratique, il serait multiplié par 64. La marge absorbe le bruit.
	if largeElapsed > smallElapsed*maxLinearityRatio && largeElapsed > minMeasurableElapsed {
		t.Fatalf("gotValue %s for %d bytes against %s for %d bytes, expectedValue a growth close to the size ratio",
			largeElapsed, len(large), smallElapsed, len(small))
	}
}

const (
	linearityScale       = 8
	maxLinearityRatio    = 24
	minMeasurableElapsed = 50 * time.Millisecond
)

// timeRedaction garde la meilleure de trois mesures : une seule serait faussée par une pause du ramasse-miettes.
func timeRedaction(text string) time.Duration {
	best := time.Duration(0)
	for attempt := range 3 {
		started := time.Now()
		RedactText(text)
		if elapsed := time.Since(started); attempt == 0 || elapsed < best {
			best = elapsed
		}
	}
	return best
}

func truncateForTest(text string) string {
	if len(text) > 200 {
		return text[:200] + "…"
	}
	return text
}

func FuzzRedactText(f *testing.F) {
	for _, seed := range []string{
		"https://u:p@tracker-a.example.com/0123456789abcdef0123456789abcdef/announce?pk=x",
		"udp://[2001:db8::1]:6969/announce?passkey=%41%42;uid=7#pk=1",
		"text a://b/%25252F0123456789abcdef0123 and https://h/x?key",
		"http://h/a;b=c/d?e&f==g&&h=;",
		`{"u":"https:\/\/h\/42?x=42"} https://0000aaaa0000aaaa.h.example:1/42?id=%34%32#k=42`,
		"",
	} {
		f.Add(seed)
	}
	fuzzSecrets := []string{urlOnlySecret("42"), "fuzz-known-secret"}
	f.Fuzz(func(t *testing.T, input string) {
		once := RedactText(input, fuzzSecrets...)
		if twice := RedactText(once, fuzzSecrets...); twice != once {
			t.Fatalf("gotValue %q then %q, expectedValue idempotence", once, twice)
		}
		planted := "https://tracker-a.example.com/" + url.PathEscape(input) + "/0123456789abcdef0123456789abcdef?pk=zq7x"
		if gotValue := RedactText(planted); strings.Contains(gotValue, "0123456789abcdef0123456789abcdef") || strings.Contains(gotValue, "zq7x") {
			t.Fatalf("gotValue %q, expectedValue the planted passkeys masked", gotValue)
		}
	})
}

func TestRedactJSON_shortSecretsKeepTheJSONValidAndNumbersUntouched(t *testing.T) {
	// Aucune clé ne contient « a » : redactTree masque aussi les clés, ce que le test ne vise pas.
	value := map[string]any{"sum": 1420, "id": 42, "level": 0.42, "text": "count 42"}

	encoded, err := RedactJSON(value, "42", "a")
	if err != nil {
		t.Fatal(err)
	}

	var gotValue struct {
		Sum   int     `json:"sum"`
		ID    int     `json:"id"`
		Level float64 `json:"level"`
		Text  string  `json:"text"`
	}
	if err := json.Unmarshal(encoded, &gotValue); err != nil {
		t.Fatalf("gotValue invalid JSON %s (%v), expectedValue valid JSON", encoded, err)
	}
	if gotValue.Sum != 1420 || gotValue.ID != 42 || gotValue.Level != 0.42 || strings.Contains(gotValue.Text, "42") {
		t.Fatalf("gotValue %+v, expectedValue numbers untouched and the string masked", gotValue)
	}
}

func TestRedact_jsonEscapedURLIsMaskedWhole(t *testing.T) {
	body := `{"status":"error","announce":"https:\/\/tracker-a.example.com\/announce\/zq7x?pk=zq7x","next":"ok"}`

	gotValue := RedactText("tracker answered " + body)

	if strings.Contains(gotValue, "zq7x") || !strings.Contains(gotValue, `"next":"ok"`) || !strings.Contains(gotValue, `"announce":"***"`) {
		t.Fatalf("gotValue %q, expectedValue the escaped URL masked whole and the rest kept", gotValue)
	}
}

func TestRedact_tokenLikeHostLabelIsMasked(t *testing.T) {
	const tokenHost = "0000aaaa0000aaaa0000aaaa0000aaaa.t.example"

	gotText := RedactText("dial https://" + tokenHost + ":8443/announce failed")
	gotAnnounce := RedactAnnounce("https://" + tokenHost + ":8443/announce")

	if strings.Contains(gotText, "0000aaaa") || !strings.Contains(gotText, ".t.example:8443/announce") {
		t.Fatalf("gotValue %q, expectedValue the token label masked and the rest of the host kept", gotText)
	}
	if gotAnnounce != "https://***.t.example:8443/***" {
		t.Fatalf("gotValue %q, expectedValue https://***.t.example:8443/***", gotAnnounce)
	}
	if reparsed, err := url.Parse(gotAnnounce); err != nil || reparsed.Hostname() != "***.t.example" {
		t.Fatalf("gotValue %q that does not parse back (%v)", gotAnnounce, err)
	}
	for _, kept := range []string{"https://tracker-a.example.com/x", "udp://[2001:db8::1]:6969/x"} {
		if gotValue := RedactText(kept); gotValue != kept {
			t.Fatalf("gotValue %q, expectedValue %q unchanged", gotValue, kept)
		}
	}
}
