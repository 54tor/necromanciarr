package torznab

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func rssWithItems(items ...string) string {
	return `<?xml version="1.0" encoding="UTF-8"?><rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>` +
		strings.Join(items, "") + `</channel></rss>`
}

func plainItem(title string, size string) string {
	return `<item><title>` + title + `</title><guid>` + title + `</guid>` +
		`<link>http://indexer.example.com/dl/x</link><size>` + size + `</size></item>`
}

func searchAgainst(t *testing.T, handler http.HandlerFunc) (Results, error) {
	t.Helper()
	indexer := newFakeIndexer(t, handler)
	client, _, _ := newTestClient(t, indexer.server.URL)
	return client.Search(context.Background(), Query{Mode: "search", Text: "invented"})
}

func TestSearch_parsesItemsAndSendsTheQuery(t *testing.T) {
	indexer := newFakeIndexer(t, xmlResponse(fixture(t, "search-movie.xml")))
	client, _, _ := newTestClient(t, indexer.server.URL)

	results, err := client.Search(context.Background(), Query{
		Mode: "movie", TMDBID: 603, IMDBID: "tt0000001", Categories: []int{2000, 2040},
	})
	if err != nil {
		t.Fatal(err)
	}

	sent := indexer.query()
	expectedQuery := map[string]string{
		"t": "movie", "tmdbid": "603", "imdbid": "tt0000001", "cat": "2000,2040", "apikey": testAPIKey, "limit": "500",
	}
	for name, expectedValue := range expectedQuery {
		if gotValue := sent.Get(name); gotValue != expectedValue {
			t.Errorf("query %s: expectedValue %q, gotValue %q", name, expectedValue, gotValue)
		}
	}
	for _, absent := range []string{"q", "tvdbid", "season", "ep"} {
		if sent.Has(absent) {
			t.Errorf("unexpected query parameter %s", absent)
		}
	}
	if len(results.Items) != 2 {
		t.Fatalf("expectedValue 2 items, gotValue %+v", results.Items)
	}
	first := results.Items[0]
	if first.Title != "Invented Movie 2001 1080p" || first.GUID != hashedGUID("demo-guid-1") || first.Size != 4000000000 ||
		first.InfoHash != strings.Repeat("a", 40) || !first.Magnet ||
		first.downloadURL != "http://indexer.example.com/dl/1?file=invented" {
		t.Errorf("gotValue first item %#v (link %q)", first, first.downloadURL)
	}
	second := results.Items[1]
	if second.Size != 2000000000 || second.InfoHash != "" || second.Magnet || !strings.HasPrefix(second.GUID, "sha256:") {
		t.Errorf("gotValue second item %#v", second)
	}
}

func TestSearch_sendsTVSearchParameters(t *testing.T) {
	indexer := newFakeIndexer(t, xmlResponse(rssWithItems()))
	client, _, _ := newTestClient(t, indexer.server.URL)

	if _, err := client.Search(context.Background(), Query{Mode: "tvsearch", TVDBID: 81189, Season: 2, Episode: 5}); err != nil {
		t.Fatal(err)
	}

	sent := indexer.query()
	expectedQuery := map[string]string{"t": "tvsearch", "tvdbid": "81189", "season": "2", "ep": "5"}
	for name, expectedValue := range expectedQuery {
		if gotValue := sent.Get(name); gotValue != expectedValue {
			t.Errorf("query %s: expectedValue %q, gotValue %q", name, expectedValue, gotValue)
		}
	}
}

func TestSearch_refusesUnknownMode(t *testing.T) {
	indexer := newFakeIndexer(t, xmlResponse(rssWithItems()))
	client, _, _ := newTestClient(t, indexer.server.URL)

	if _, gotErr := client.Search(context.Background(), Query{Mode: "caps"}); gotErr == nil {
		t.Fatal("expectedValue an error for mode caps")
	}
	if gotValue := indexer.requestCount(); gotValue != 0 {
		t.Fatalf("expectedValue no request, gotValue %d", gotValue)
	}
}

func TestSearch_refusesDoctypeAndEntities(t *testing.T) {
	hostileBodies := map[string]string{
		"doctype":         `<?xml version="1.0"?><!DOCTYPE rss><rss><channel></channel></rss>`,
		"internal entity": `<?xml version="1.0"?><!DOCTYPE rss [<!ENTITY name "value">]><rss><channel><item><title>&name;</title></item></channel></rss>`,
		"external entity": `<?xml version="1.0"?><!DOCTYPE rss [<!ENTITY xxe SYSTEM "file:///etc/hostname">]><rss><channel><title>&xxe;</title></channel></rss>`,
		"billion laughs":  `<?xml version="1.0"?><!DOCTYPE lolz [<!ENTITY lol "lol"><!ENTITY lol2 "&lol;&lol;&lol;&lol;">]><rss>&lol2;</rss>`,
		"entity alone":    `<rss><channel></channel><!ENTITY name "value"></rss>`,
		"caps doctype":    `<!DOCTYPE caps><caps></caps>`,
	}
	for name, body := range hostileBodies {
		t.Run(name, func(t *testing.T) {
			_, gotErr := searchAgainst(t, xmlResponse(body))
			if !errors.Is(gotErr, ErrHostileResponse) {
				t.Fatalf("expectedValue ErrHostileResponse, gotErr %v", gotErr)
			}
		})
	}

	t.Run("caps", func(t *testing.T) {
		indexer := newFakeIndexer(t, xmlResponse(hostileBodies["caps doctype"]))
		client, _, _ := newTestClient(t, indexer.server.URL)
		if _, gotErr := client.Caps(context.Background()); !errors.Is(gotErr, ErrHostileResponse) {
			t.Fatalf("expectedValue ErrHostileResponse, gotErr %v", gotErr)
		}
	})

	// Sans déclaration, une entité inconnue n'est jamais résolue : le décodeur strict refuse le document.
	t.Run("undeclared entity", func(t *testing.T) {
		_, gotErr := searchAgainst(t, xmlResponse(`<rss><channel><item><title>&xxe;</title></item></channel></rss>`))
		if !errors.Is(gotErr, ErrBadResponse) {
			t.Fatalf("expectedValue ErrBadResponse, gotErr %v", gotErr)
		}
	})
}

func TestSearch_refusesDeepNesting(t *testing.T) {
	body := `<rss><channel>` + strings.Repeat("<deep>", maxDepth) + strings.Repeat("</deep>", maxDepth) + `</channel></rss>`

	_, gotErr := searchAgainst(t, xmlResponse(body))

	if !errors.Is(gotErr, ErrHostileResponse) {
		t.Fatalf("expectedValue ErrHostileResponse, gotErr %v", gotErr)
	}
}

func TestSearch_refusesTruncatedDocument(t *testing.T) {
	truncated := rssWithItems(plainItem("Invented", "100"))
	truncated = truncated[:len(truncated)-len("</channel></rss>")]

	_, gotErr := searchAgainst(t, xmlResponse(truncated))

	if !errors.Is(gotErr, ErrBadResponse) {
		t.Fatalf("expectedValue ErrBadResponse, gotErr %v", gotErr)
	}
}

func TestSearch_refusesUnexpectedRootAndNonXMLContentType(t *testing.T) {
	_, gotErr := searchAgainst(t, xmlResponse(`<html><body></body></html>`))
	if !errors.Is(gotErr, ErrBadResponse) {
		t.Fatalf("root html: gotErr %v", gotErr)
	}

	for _, contentType := range []string{"application/json", "text/html; charset=utf-8", ""} {
		_, gotErr := searchAgainst(t, func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", contentType)
			_, _ = writer.Write([]byte(rssWithItems()))
		})
		if !errors.Is(gotErr, ErrBadResponse) {
			t.Fatalf("content type %q: gotErr %v", contentType, gotErr)
		}
	}
}

func TestSearch_refusesNonUTF8Charset(t *testing.T) {
	body := `<?xml version="1.0" encoding="ISO-8859-1"?><rss><channel></channel></rss>`

	_, gotErr := searchAgainst(t, xmlResponse(body))

	if !errors.Is(gotErr, ErrBadResponse) {
		t.Fatalf("expectedValue ErrBadResponse, gotErr %v", gotErr)
	}
}

// endlessBody fournit un document complet suivi d'espaces sans fin et compte les octets lus : seul le plafond, pas
// l'analyse, peut refuser ce corps.
type endlessBody struct {
	prefix []byte
	read   atomic.Int64
}

func (body *endlessBody) Read(buffer []byte) (int, error) {
	written := copy(buffer, body.prefix)
	body.prefix = body.prefix[written:]
	for index := written; index < len(buffer); index++ {
		buffer[index] = ' '
	}
	body.read.Add(int64(len(buffer)))
	return len(buffer), nil
}

func (body *endlessBody) Close() error { return nil }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestSearch_capsBodySize(t *testing.T) {
	body := &endlessBody{prefix: []byte(rssWithItems(plainItem("Invented", "100")))}
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		header := http.Header{"Content-Type": []string{"application/xml"}}
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: body, Request: request}, nil
	})
	endpoint := endpointFor("http://indexer.example.com")
	client, err := New(endpoint, Deps{HTTP: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}

	_, gotErr := client.Search(context.Background(), Query{Mode: "search", Text: "invented"})

	if !errors.Is(gotErr, ErrBadResponse) {
		t.Fatalf("expectedValue ErrBadResponse, gotErr %v", gotErr)
	}
	if gotValue := body.read.Load(); gotValue > maxXMLBytes+1 {
		t.Fatalf("expectedValue at most %d bytes read, gotValue %d", maxXMLBytes+1, gotValue)
	}
}

func TestSearch_acceptsBodyOfExactlyTheLimit(t *testing.T) {
	document := rssWithItems(plainItem("Invented", "100"))
	padded := document + strings.Repeat(" ", maxXMLBytes-len(document))

	results, err := searchAgainst(t, xmlResponse(padded))
	if err != nil {
		t.Fatal(err)
	}

	if len(results.Items) != 1 {
		t.Fatalf("expectedValue 1 item, gotValue %d", len(results.Items))
	}
}

func TestSearch_capsItemCount(t *testing.T) {
	itemsAtLimit := make([]string, 0, maxItems+1)
	for index := range maxItems {
		itemsAtLimit = append(itemsAtLimit, plainItem(fmt.Sprintf("Invented %d", index), "100"))
	}
	results, err := searchAgainst(t, xmlResponse(rssWithItems(itemsAtLimit...)))
	if err != nil || len(results.Items) != maxItems {
		t.Fatalf("at the limit: gotErr %v, gotValue %d items", err, len(results.Items))
	}

	itemsAtLimit = append(itemsAtLimit, plainItem("Invented extra", "100"))
	_, gotErr := searchAgainst(t, xmlResponse(rssWithItems(itemsAtLimit...)))
	if !errors.Is(gotErr, ErrHostileResponse) {
		t.Fatalf("expectedValue ErrHostileResponse, gotErr %v", gotErr)
	}
}

func TestSearch_countsMagnetOnly(t *testing.T) {
	results, err := searchAgainst(t, xmlResponse(fixture(t, "search-movie.xml")))
	if err != nil {
		t.Fatal(err)
	}

	if results.MagnetOnly != 2 {
		t.Fatalf("expectedValue 2 magnet-only results, gotValue %d", results.MagnetOnly)
	}
	for _, item := range results.Items {
		if strings.Contains(item.Title, "magnet only") || strings.HasPrefix(item.downloadURL, "magnet:") {
			t.Errorf("magnet-only result kept: %#v", item)
		}
	}
}

func TestSearch_dropsNegativeOrNonIntegerSize(t *testing.T) {
	body := rssWithItems(
		plainItem("negative", "-1"),
		plainItem("decimal", "12.5"),
		plainItem("exponent", "1e9"),
		plainItem("text", "large"),
		plainItem("overflow", "99999999999999999999"),
		`<item><title>missing</title><link>http://indexer.example.com/dl/x</link></item>`,
		`<item><title>bad attr</title><link>http://indexer.example.com/dl/x</link><torznab:attr name="size" value="-5"/></item>`,
		plainItem("valid", "100"),
		plainItem("zero", "0"),
	)

	results, err := searchAgainst(t, xmlResponse(body))
	if err != nil {
		t.Fatal(err)
	}

	var gotTitles []string
	for _, item := range results.Items {
		gotTitles = append(gotTitles, item.Title)
	}
	if gotValue := strings.Join(gotTitles, ","); gotValue != "valid,zero" {
		t.Fatalf("expectedValue valid,zero, gotValue %s", gotValue)
	}
}

func TestSearch_dropsInvalidInfohashAndLinks(t *testing.T) {
	body := rssWithItems(
		`<item><title>short hash</title><link>http://indexer.example.com/dl/x</link><size>1</size><torznab:attr name="infohash" value="abc"/></item>`,
		`<item><title>non hex</title><link>http://indexer.example.com/dl/x</link><size>1</size><torznab:attr name="infohash" value="`+strings.Repeat("z", 40)+`"/></item>`,
		`<item><title>relative link</title><link>/dl/x</link><size>1</size></item>`,
		`<item><title>file link</title><link>file:///etc/hostname</link><size>1</size></item>`,
		`<item><title>no link</title><size>1</size></item>`,
		`<item><title>valid</title><link>https://indexer.example.com/dl/x</link><size>1</size><torznab:attr name="infohash" value="`+strings.Repeat("0", 40)+`"/></item>`,
	)

	results, err := searchAgainst(t, xmlResponse(body))
	if err != nil {
		t.Fatal(err)
	}

	if len(results.Items) != 1 || results.Items[0].Title != "valid" || results.MagnetOnly != 0 {
		t.Fatalf("expectedValue only valid, gotValue %+v (magnet only %d)", results.Items, results.MagnetOnly)
	}
}

func TestSearch_mapsInvalidKeyToAccessDenied(t *testing.T) {
	bodies := map[string]string{
		"100":         fixture(t, "error-100.xml"),
		"101":         `<?xml version="1.0"?><error code="101" description="Account suspended"/>`,
		"http status": `<error code="100" description="Incorrect user credentials"/>`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			status := http.StatusOK
			if name == "http status" {
				status = http.StatusBadRequest
			}
			_, gotErr := searchAgainst(t, func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/xml")
				writer.WriteHeader(status)
				_, _ = writer.Write([]byte(body))
			})
			if !errors.Is(gotErr, ErrAccessDenied) {
				t.Fatalf("expectedValue ErrAccessDenied, gotErr %v", gotErr)
			}
		})
	}

	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		_, gotErr := searchAgainst(t, func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(status) })
		if !errors.Is(gotErr, ErrAccessDenied) {
			t.Fatalf("HTTP %d: gotErr %v", status, gotErr)
		}
	}
}

func TestSearch_mapsRetryAfterToUnavailable(t *testing.T) {
	_, gotErr := searchAgainst(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Retry-After", "120")
		writer.WriteHeader(http.StatusTooManyRequests)
	})

	var unavailable *UnavailableError
	if !errors.Is(gotErr, ErrUnavailable) || !errors.As(gotErr, &unavailable) || unavailable.RetryAfter != 120*time.Second {
		t.Fatalf("gotErr = %v", gotErr)
	}

	// Un quota signalé par un élément <error> garde le délai de l'en-tête.
	_, gotErr = searchAgainst(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/xml")
		writer.Header().Set("Retry-After", "60")
		_, _ = writer.Write([]byte(`<error code="500" description="Request limit reached"/>`))
	})
	if !errors.As(gotErr, &unavailable) || unavailable.RetryAfter != time.Minute {
		t.Fatalf("quota element: gotErr = %v", gotErr)
	}

	for _, status := range []int{http.StatusServiceUnavailable, http.StatusInternalServerError, http.StatusBadGateway} {
		_, gotErr := searchAgainst(t, func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(status) })
		if !errors.Is(gotErr, ErrUnavailable) {
			t.Fatalf("HTTP %d: gotErr %v", status, gotErr)
		}
	}
}

func TestSearch_refusesRedirect(t *testing.T) {
	var targetRequests atomic.Int32
	target := newFakeIndexer(t, func(http.ResponseWriter, *http.Request) { targetRequests.Add(1) })

	_, gotErr := searchAgainst(t, func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.server.URL+"/api?"+request.URL.RawQuery, http.StatusFound)
	})

	if !errors.Is(gotErr, ErrBadResponse) {
		t.Fatalf("expectedValue ErrBadResponse, gotErr %v", gotErr)
	}
	if gotValue := targetRequests.Load(); gotValue != 0 {
		t.Fatalf("expectedValue redirect target never requested, gotValue %d", gotValue)
	}
}

func TestSearch_neverCopiesErrorText(t *testing.T) {
	echo := "Invalid key " + testAPIKey + " for secret description"
	handlers := map[string]http.HandlerFunc{
		"error element": xmlResponse(`<error code="100" description="` + echo + `"/>`),
		"unknown code":  xmlResponse(`<error code="201" description="` + echo + `"/>`),
		"http 500 body": func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(writer, echo)
		},
		"http 418 body": func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusTeapot)
			_, _ = io.WriteString(writer, echo)
		},
		"syntax error": xmlResponse(`<rss><channel><title>` + echo + `</title><`),
	}
	for name, handler := range handlers {
		t.Run(name, func(t *testing.T) {
			_, gotErr := searchAgainst(t, handler)
			if gotErr == nil {
				t.Fatal("expectedValue an error")
			}
			for _, rendering := range []string{gotErr.Error(), fmt.Sprintf("%+v %#v", gotErr, gotErr)} {
				if strings.Contains(rendering, testAPIKey) || strings.Contains(rendering, "secret description") {
					t.Fatalf("error text leaks: %s", rendering)
				}
			}
		})
	}

	// Une erreur de transport (url.Error) porte l'URL complète, donc la clé : elle n'est jamais recopiée.
	t.Run("transport error", func(t *testing.T) {
		indexer := newFakeIndexer(t, xmlResponse(rssWithItems()))
		client, _, _ := newTestClient(t, indexer.server.URL)
		indexer.server.Close()
		_, gotErr := client.Search(context.Background(), Query{Mode: "search", Text: "invented"})
		if !errors.Is(gotErr, ErrUnavailable) || strings.Contains(fmt.Sprintf("%v %+v", gotErr, gotErr), testAPIKey) {
			t.Fatalf("gotErr = %v", gotErr)
		}
	})
}

func hashedGUID(raw string) string {
	digest := sha256.Sum256([]byte(raw))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func itemWith(title, inner string) string {
	return `<item><title>` + title + `</title>` + inner + `</item>`
}

func TestSearch_alwaysHashesTheGUID(t *testing.T) {
	rawGUIDs := []string{
		"magnet:?xt=urn:btih:" + strings.Repeat("d", 40) + "&amp;tr=https%3A%2F%2Ftracker.example.com%2Fannounce%2Ffake-passkey-0000",
		"//indexer.example.com/dl?passkey=fake-passkey-0000",
		"indexer.example.com/dl?passkey=fake-passkey-0000",
		"demo-guid-plain",
	}
	expectedGUIDs := []string{
		hashedGUID("magnet:?xt=urn:btih:" + strings.Repeat("d", 40) + "&tr=https%3A%2F%2Ftracker.example.com%2Fannounce%2Ffake-passkey-0000"),
		hashedGUID(rawGUIDs[1]), hashedGUID(rawGUIDs[2]), hashedGUID(rawGUIDs[3]),
	}
	items := make([]string, 0, len(rawGUIDs))
	for index, rawGUID := range rawGUIDs {
		items = append(items, itemWith(fmt.Sprintf("Invented %d", index),
			`<guid>`+rawGUID+`</guid><link>http://indexer.example.com/dl/x</link><size>1</size>`))
	}

	results, err := searchAgainst(t, xmlResponse(rssWithItems(items...)))
	if err != nil {
		t.Fatal(err)
	}

	if len(results.Items) != len(expectedGUIDs) {
		t.Fatalf("expectedValue %d items, gotValue %d", len(expectedGUIDs), len(results.Items))
	}
	for index, item := range results.Items {
		if gotValue := item.GUID; gotValue != expectedGUIDs[index] {
			t.Errorf("item %d: expectedValue %s, gotValue %s", index, expectedGUIDs[index], gotValue)
		}
		if rendering := fmt.Sprintf("%v %#v", item, item); strings.Contains(rendering, "fake-passkey-0000") {
			t.Errorf("item %d leaks its passkey: %s", index, rendering)
		}
	}
}

func TestSearch_magnetURLAttributeIsNeverADownloadLink(t *testing.T) {
	body := rssWithItems(itemWith("attr only", `<size>1</size><torznab:attr name="magneturl" value="http://indexer.example.com/dl/x"/>`))

	results, err := searchAgainst(t, xmlResponse(body))
	if err != nil {
		t.Fatal(err)
	}

	if len(results.Items) != 0 || results.MagnetOnly != 0 {
		t.Fatalf("expectedValue the item dropped and not counted as magnet, gotValue %+v (magnet only %d)", results.Items, results.MagnetOnly)
	}
}

func TestSearch_redirectBodyIsNeverRead(t *testing.T) {
	body := &endlessBody{}
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		header := http.Header{"Location": []string{"http://elsewhere.example.com/"}}
		return &http.Response{StatusCode: http.StatusFound, Header: header, Body: body, Request: request}, nil
	})
	client, err := New(endpointFor("http://indexer.example.com"), Deps{HTTP: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}

	_, gotErr := client.Search(context.Background(), Query{Mode: "search", Text: "invented"})

	if !errors.Is(gotErr, ErrBadResponse) {
		t.Fatalf("expectedValue ErrBadResponse, gotErr %v", gotErr)
	}
	if gotValue := body.read.Load(); gotValue != 0 {
		t.Fatalf("expectedValue redirect body never read, gotValue %d bytes", gotValue)
	}
}

func TestSearch_keepsRetryAfterOnEveryUnavailableStatus(t *testing.T) {
	statuses := []int{
		http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout,
	}
	for _, status := range statuses {
		_, gotErr := searchAgainst(t, func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Retry-After", "30")
			writer.WriteHeader(status)
		})
		var unavailable *UnavailableError
		if !errors.As(gotErr, &unavailable) || unavailable.RetryAfter != 30*time.Second {
			t.Errorf("HTTP %d: expectedValue UnavailableError with 30s, gotErr %v", status, gotErr)
		}
	}
}

func TestSearch_countsInvalidItems(t *testing.T) {
	body := rssWithItems(
		plainItem("negative", "-1"),
		plainItem("decimal", "12.5"),
		itemWith("no size", `<link>http://indexer.example.com/dl/x</link>`),
		itemWith("bad hash", `<link>http://indexer.example.com/dl/x</link><size>1</size><torznab:attr name="infohash" value="abc"/>`),
		itemWith("no link", `<size>1</size>`),
		itemWith("magnet", `<link>magnet:?xt=urn:btih:`+strings.Repeat("e", 40)+`</link><size>1</size>`),
		plainItem("valid", "100"),
	)

	results, err := searchAgainst(t, xmlResponse(body))
	if err != nil {
		t.Fatal(err)
	}

	if len(results.Items) != 1 || results.MagnetOnly != 1 || results.Invalid != 5 {
		t.Fatalf("expectedValue 1 item, 1 magnet only, 5 invalid, gotValue %d, %d, %d",
			len(results.Items), results.MagnetOnly, results.Invalid)
	}
}

func TestSearch_boundsAndCleansTitles(t *testing.T) {
	hostileTitle := "Invented\u009b31m Movie\u009d0;title\t\n" + strings.Repeat("a", 600)

	results, err := searchAgainst(t, xmlResponse(rssWithItems(plainItem(hostileTitle, "1"))))
	if err != nil {
		t.Fatal(err)
	}

	gotValue := results.Items[0].Title
	if strings.ContainsAny(gotValue, "\u009b\u009d\t\n") || len([]rune(gotValue)) != maxTitleRunes {
		t.Fatalf("expectedValue %d runes without control characters, gotValue %d runes %q", maxTitleRunes, len([]rune(gotValue)), gotValue[:40])
	}
	if !strings.HasPrefix(gotValue, "Invented31m Movie0;title") {
		t.Fatalf("gotValue prefix %q", gotValue[:40])
	}
}

// gzipBomb envoie sans fin un document compressé : quelques Kio compressés valent des Mio une fois décompressés.
type gzipBomb struct{ compressedWritten atomic.Int64 }

type countingWriter struct {
	writer  io.Writer
	counter *atomic.Int64
}

func (counting countingWriter) Write(data []byte) (int, error) {
	written, err := counting.writer.Write(data)
	counting.counter.Add(int64(written))
	return written, err
}

func (bomb *gzipBomb) serve(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "application/xml")
	writer.Header().Set("Content-Encoding", "gzip")
	compressor := gzip.NewWriter(countingWriter{writer: writer, counter: &bomb.compressedWritten})
	if _, err := io.WriteString(compressor, rssWithItems(plainItem("Invented", "100"))); err != nil {
		return
	}
	padding := []byte(strings.Repeat(" ", 1<<20))
	for {
		if _, err := compressor.Write(padding); err != nil {
			return
		}
	}
}

func TestSearch_capsGzipBomb(t *testing.T) {
	bomb := &gzipBomb{}
	served := make(chan struct{})
	indexer := newFakeIndexer(t, func(writer http.ResponseWriter, request *http.Request) {
		defer close(served)
		bomb.serve(writer, request)
	})
	client, _, _ := newTestClient(t, indexer.server.URL)

	_, gotErr := client.Search(context.Background(), Query{Mode: "search", Text: "invented"})

	if !errors.Is(gotErr, ErrBadResponse) || !strings.Contains(gotErr.Error(), "larger than") {
		t.Fatalf("expectedValue the size overflow, gotErr %v", gotErr)
	}
	select {
	case <-served:
	case <-time.After(10 * time.Second):
		t.Fatal("expectedValue the client to stop reading, the server is still writing")
	}
	// 64 Mio compressés vaudraient environ 64 Gio : le client a lâché bien avant.
	if gotValue := bomb.compressedWritten.Load(); gotValue > 64<<20 {
		t.Fatalf("expectedValue a bounded read, gotValue %d compressed bytes written", gotValue)
	}
}
