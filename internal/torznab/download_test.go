package torznab

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1" //nolint:gosec // l'infohash v1 est par définition un SHA-1.
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"necromanciarr/internal/torrentcheck"
)

// Valeur volontairement pauvre en entropie, comme testAPIKey.
const downloadPasskey = "fake-passkey-0000"

const (
	torrentContentType = "application/x-bittorrent"
	testPieceLength    = 16 << 10
	v2MetaVersion      = 2
)

// torrentOptions décrit le .torrent factice : v1 (défaut), hybride ou v2 seul, avec un commentaire de remplissage.
type torrentOptions struct {
	metaVersion int
	withoutV1   bool
	comment     string
}

// buildTorrent rend un .torrent dont l'annonce porte une passkey, et son infohash v1.
func buildTorrent(t *testing.T, options torrentOptions) ([]byte, string) {
	t.Helper()
	content := []byte("invented content")
	info := metainfo.Info{Name: "invented.mkv", PieceLength: testPieceLength, Length: int64(len(content)), MetaVersion: int64(options.metaVersion)}
	if !options.withoutV1 {
		pieceHash := sha1.Sum(content) //nolint:gosec // hash de pièce v1.
		info.Pieces = pieceHash[:]
	}
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := bencode.Marshal(metainfo.MetaInfo{
		InfoBytes: infoBytes,
		Announce:  "https://tracker.example.com/" + downloadPasskey + "/announce",
		Comment:   options.comment,
	})
	if err != nil {
		t.Fatal(err)
	}
	infoHash := sha1.Sum(infoBytes) //nolint:gosec // infohash v1.
	return raw, hex.EncodeToString(infoHash[:])
}

// torrentOfSize rend un .torrent valide d'exactement size octets, grossi par son commentaire.
func torrentOfSize(t *testing.T, size int) []byte {
	t.Helper()
	commentLength := 1
	for {
		raw, _ := buildTorrent(t, torrentOptions{comment: strings.Repeat("x", commentLength)})
		missing := size - len(raw)
		if missing == 0 {
			return raw
		}
		commentLength += missing
	}
}

func torrentResponse(raw []byte, contentType string) http.HandlerFunc {
	return func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", contentType)
		_, _ = writer.Write(raw)
	}
}

// itemFor crée un résultat dont le lien de téléchargement est link.
func itemFor(link, infoHash string) Item {
	return Item{Title: "Invented", InfoHash: infoHash, downloadURL: link}
}

// recordingTransport répond sans réseau et garde chaque URL demandée.
type recordingTransport struct {
	mutex     sync.Mutex
	requested []string
	hosts     []string
	respond   func(request *http.Request) *http.Response
}

func (transport *recordingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.mutex.Lock()
	transport.requested = append(transport.requested, request.URL.String())
	transport.hosts = append(transport.hosts, request.Host)
	transport.mutex.Unlock()
	return transport.respond(request), nil
}

func (transport *recordingTransport) requests() []string {
	transport.mutex.Lock()
	defer transport.mutex.Unlock()
	return append([]string(nil), transport.requested...)
}

func torrentTransport(raw []byte) *recordingTransport {
	return &recordingTransport{respond: func(request *http.Request) *http.Response {
		header := http.Header{"Content-Type": []string{torrentContentType}}
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(bytes.NewReader(raw)), Request: request}
	}}
}

func clientWithTransport(t *testing.T, baseURL string, transport http.RoundTripper) *Client {
	t.Helper()
	client, err := New(endpointFor(baseURL), Deps{HTTP: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	client.wait = func(context.Context, time.Duration) error { return nil }
	return client
}

func TestDownload_returnsTheTorrentBytesAndSendsTheKey(t *testing.T) {
	raw, infoHash := buildTorrent(t, torrentOptions{})
	indexer := newFakeIndexer(t, torrentResponse(raw, torrentContentType+"; charset=binary"))
	client, _, _ := newTestClient(t, indexer.server.URL)

	gotValue, err := client.Download(context.Background(), itemFor(indexer.server.URL+"/dl/1", infoHash))
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(gotValue, raw) {
		t.Fatal("expectedValue the raw torrent bytes, announce included")
	}
	if gotKey := indexer.query()[apiKeyParameter]; len(gotKey) != 1 || gotKey[0] != testAPIKey {
		t.Fatalf("expectedValue the API key once, gotValue %q", gotKey)
	}
}

func TestDownload_keepsTheKeyOfTheLink(t *testing.T) {
	raw, _ := buildTorrent(t, torrentOptions{})
	indexer := newFakeIndexer(t, torrentResponse(raw, "application/octet-stream"))
	client, _, _ := newTestClient(t, indexer.server.URL)

	_, err := client.Download(context.Background(), itemFor(indexer.server.URL+"/1/download?apikey=link-key&file=x", ""))
	if err != nil {
		t.Fatal(err)
	}

	if gotKey := indexer.query()[apiKeyParameter]; len(gotKey) != 1 || gotKey[0] != "link-key" {
		t.Fatalf("expectedValue the link's own key only, gotValue %q", gotKey)
	}
}

func TestDownload_refusesRedirect(t *testing.T) {
	var targetRequests atomic.Int32
	target := newFakeIndexer(t, func(http.ResponseWriter, *http.Request) { targetRequests.Add(1) })
	indexer := newFakeIndexer(t, func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.server.URL+"/dl/1", http.StatusFound)
	})
	client, _, _ := newTestClient(t, indexer.server.URL)

	_, gotErr := client.Download(context.Background(), itemFor(indexer.server.URL+"/dl/1", ""))

	if !errors.Is(gotErr, ErrBadResponse) {
		t.Fatalf("expectedValue ErrBadResponse, gotErr %v", gotErr)
	}
	if gotValue := targetRequests.Load(); gotValue != 0 {
		t.Fatalf("expectedValue redirect target never requested, gotValue %d", gotValue)
	}
}

func TestDownload_redirectBodyIsNeverRead(t *testing.T) {
	body := &endlessBody{}
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		header := http.Header{"Location": []string{"http://elsewhere.example.com/"}, "Content-Type": []string{torrentContentType}}
		return &http.Response{StatusCode: http.StatusMovedPermanently, Header: header, Body: body, Request: request}, nil
	})
	client := clientWithTransport(t, "http://indexer.example.com", transport)

	_, gotErr := client.Download(context.Background(), itemFor("http://indexer.example.com/dl/1", ""))

	if !errors.Is(gotErr, ErrBadResponse) {
		t.Fatalf("expectedValue ErrBadResponse, gotErr %v", gotErr)
	}
	if gotValue := body.read.Load(); gotValue != 0 {
		t.Fatalf("expectedValue redirect body never read, gotValue %d bytes", gotValue)
	}
}

// originCase est une ligne de la table des contournements de la comparaison d'origine.
type originCase struct {
	name     string
	base     string
	link     string
	accepted bool
}

var originCases = []originCase{
	{"same origin", "http://indexer.example.com", "http://indexer.example.com/dl/1", true},
	{"host case", "http://indexer.example.com", "http://INDEXER.Example.COM/dl/1", true},
	{"scheme case", "http://indexer.example.com", "HTTP://indexer.example.com/dl/1", true},
	{"base host case", "http://Indexer.EXAMPLE.com", "http://indexer.example.com/dl/1", true},
	{"trailing dot in link", "http://indexer.example.com", "http://indexer.example.com./dl/1", true},
	{"trailing dot in base", "http://indexer.example.com.", "http://indexer.example.com/dl/1", true},
	{"explicit default http port", "http://indexer.example.com", "http://indexer.example.com:80/dl/1", true},
	{"explicit default https port in base", "https://indexer.example.com:443", "https://indexer.example.com/dl/1", true},
	{"same custom port", "http://indexer.example.com:9696", "http://indexer.example.com:9696/1/download", true},
	{"absolute relative path", "http://indexer.example.com", "/dl/1", true},
	{"relative path", "http://indexer.example.com", "dl/1", true},
	{"IDN link in punycode", "http://bücher.example", "http://xn--bcher-kva.example/dl/1", true},
	{"IDN base in punycode", "http://xn--bcher-kva.example", "http://BÜCHER.example/dl/1", true},
	{"IPv6 same address other spelling", "http://[::1]:9696", "http://[0:0::1]:9696/dl/1", true},
	{"docker service name", "http://prowlarr_app:9696", "http://prowlarr_app:9696/1/download", true},
	{"docker service name relative", "http://prowlarr_app:9696", "/1/download", true},
	{"docker service name case", "http://prowlarr_app:9696", "http://Prowlarr_App:9696/1/download", true},
	{"label starting with a hyphen", "http://-indexer.example.com", "http://-INDEXER.example.com/dl/1", true},

	{"empty link", "http://indexer.example.com", "", false},
	{"other host", "http://indexer.example.com", "http://other.example.com/dl/1", false},
	{"underscore host on another base", "http://indexer.example.com", "http://indexer_example.com/dl/1", false},
	{"other docker service", "http://prowlarr_app:9696", "http://jackett_app:9696/dl/1", false},
	{"empty label", "http://indexer.example.com", "http://indexer..example.com/dl/1", false},
	{"empty label on both sides", "http://indexer..example.com", "http://indexer..example.com/dl/1", false},
	{"host as a prefix", "http://indexer.example.com", "http://indexer.example.com.other.example.com/dl/1", false},
	{"host as a suffix", "http://indexer.example.com", "http://evilindexer.example.com/dl/1", false},
	{"other port", "http://indexer.example.com", "http://indexer.example.com:8080/dl/1", false},
	{"default port instead of custom", "http://indexer.example.com:9696", "http://indexer.example.com/dl/1", false},
	{"other scheme", "http://indexer.example.com", "https://indexer.example.com/dl/1", false},
	{"http on the https port", "https://indexer.example.com", "http://indexer.example.com:443/dl/1", false},
	{"userinfo", "http://indexer.example.com", "http://user@indexer.example.com/dl/1", false},
	{"userinfo with password", "http://indexer.example.com", "http://user:secret@indexer.example.com/dl/1", false},
	{"host in userinfo", "http://indexer.example.com", "http://indexer.example.com@other.example.com/dl/1", false},
	{"protocol-relative other host", "http://indexer.example.com", "//other.example.com/dl/1", false},
	{"protocol-relative userinfo", "http://indexer.example.com", "//user@indexer.example.com/dl/1", false},
	{"backslash before at", "http://indexer.example.com", `http://other.example.com\@indexer.example.com/dl/1`, false},
	{"backslash authority", "http://indexer.example.com", `\\other.example.com/dl/1`, false},
	{"slash backslash authority", "http://indexer.example.com", `/\other.example.com/dl/1`, false},
	{"backslash in host", "http://indexer.example.com", `http://indexer.example.com\.other.example.com/dl/1`, false},
	{"percent-encoded dots", "http://indexer.example.com", "http://indexer%2eexample%2ecom/dl/1", false},
	{"percent-encoded percent", "http://indexer.example.com", "http://indexer.example.com%25/dl/1", false},
	{"percent-encoded other host", "http://indexer.example.com", "http://other%2eexample.com/dl/1", false},
	{"double trailing dot", "http://indexer.example.com", "http://indexer.example.com../dl/1", false},
	{"opaque URL", "http://indexer.example.com", "http:other.example.com/dl/1", false},
	{"ftp scheme", "http://indexer.example.com", "ftp://indexer.example.com/dl/1", false},
	{"file scheme", "http://indexer.example.com", "file:///etc/passwd", false},
	{"javascript scheme", "http://indexer.example.com", "javascript:alert(1)", false},
	{"magnet link", "http://indexer.example.com", "magnet:?xt=urn:btih:" + strings.Repeat("a", 40), false},
	{"IPv6 zone", "http://[fe80::1]:9696", "http://[fe80::1%25eth0]:9696/dl/1", false},
	{"other IPv6 address", "http://[::1]:9696", "http://[::2]:9696/dl/1", false},
	{"IPv4-mapped IPv6", "http://127.0.0.1:9696", "http://[::ffff:127.0.0.1]:9696/dl/1", false},
	{"tab in host", "http://indexer.example.com", "http://indexer.example.com\t.other.example.com/dl/1", false},
	{"leading space", "http://indexer.example.com", " http://other.example.com/dl/1", false},
}

func TestDownload_refusesForeignHost(t *testing.T) {
	raw, _ := buildTorrent(t, torrentOptions{})
	for _, origin := range originCases {
		t.Run(origin.name, func(t *testing.T) {
			transport := torrentTransport(raw)
			client := clientWithTransport(t, origin.base, transport)

			_, gotErr := client.Download(context.Background(), itemFor(origin.link, ""))

			gotRequests := transport.requests()
			if origin.accepted {
				if gotErr != nil || len(gotRequests) != 1 {
					t.Fatalf("expectedValue one request and success, gotErr %v, gotValue %q", gotErr, gotRequests)
				}
				return
			}
			if !errors.Is(gotErr, ErrForeignDownloadHost) {
				t.Fatalf("expectedValue ErrForeignDownloadHost, gotErr %v", gotErr)
			}
			if len(gotRequests) != 0 {
				t.Fatalf("expectedValue zero requests, gotValue %q", gotRequests)
			}
		})
	}
}

func TestDownload_requestsTheResolvedLinkOnTheIndexerHost(t *testing.T) {
	raw, _ := buildTorrent(t, torrentOptions{})
	transport := torrentTransport(raw)
	client := clientWithTransport(t, "http://indexer.example.com:9696", transport)

	if _, err := client.Download(context.Background(), itemFor("/dl/1?file=x", "")); err != nil {
		t.Fatal(err)
	}

	expectedValue := "http://indexer.example.com:9696/dl/1?file=x&apikey=" + testAPIKey
	if gotValue := transport.requests(); len(gotValue) != 1 || gotValue[0] != expectedValue {
		t.Fatalf("expectedValue %q, gotValue %q", expectedValue, gotValue)
	}
}

func TestDownload_requestsTheCanonicalHost(t *testing.T) {
	cases := []struct{ base, link, expectedValue string }{
		{"http://indexer.example.com", "http://INDEXER.Example.COM./dl/1", "http://indexer.example.com/dl/1"},
		{"http://indexer.example.com", "http://indexer.example.com:80/dl/1", "http://indexer.example.com/dl/1"},
		{"http://xn--bcher-kva.example", "http://BÜCHER.example/dl/1", "http://xn--bcher-kva.example/dl/1"},
		{"http://bücher.example", "http://b%C3%BCcher.example/dl/1", "http://xn--bcher-kva.example/dl/1"},
		{"http://[::1]:9696", "http://[0:0::1]:9696/dl/1", "http://[::1]:9696/dl/1"},
	}
	raw, _ := buildTorrent(t, torrentOptions{})
	for _, canonical := range cases {
		t.Run(canonical.link, func(t *testing.T) {
			transport := torrentTransport(raw)
			client := clientWithTransport(t, canonical.base, transport)
			if _, err := client.Download(context.Background(), itemFor(canonical.link, "")); err != nil {
				t.Fatal(err)
			}
			expectedValue := canonical.expectedValue + "?apikey=" + testAPIKey
			if gotValue := transport.requests(); len(gotValue) != 1 || gotValue[0] != expectedValue {
				t.Fatalf("expectedValue %q, gotValue %q", expectedValue, gotValue)
			}
			// L'en-tête Host envoyé est l'hôte comparé, pas celui du lien brut.
			expectedHost := strings.TrimPrefix(strings.SplitN(canonical.expectedValue, "/dl/", 2)[0], "http://")
			if gotHost := transport.hosts; len(gotHost) != 1 || gotHost[0] != expectedHost {
				t.Fatalf("expectedValue Host %q, gotValue %q", expectedHost, gotHost)
			}
		})
	}
}

// countingBody rend content puis compte les octets lus.
type countingBody struct {
	reader io.Reader
	read   atomic.Int64
}

func (body *countingBody) Read(buffer []byte) (int, error) {
	read, err := body.reader.Read(buffer)
	body.read.Add(int64(read))
	return read, err
}

func (body *countingBody) Close() error { return nil }

func downloadBody(t *testing.T, content []byte, contentType string) ([]byte, *countingBody, error) {
	t.Helper()
	body := &countingBody{reader: bytes.NewReader(content)}
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		header := http.Header{"Content-Type": []string{contentType}}
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: body, Request: request}, nil
	})
	client := clientWithTransport(t, "http://indexer.example.com", transport)
	downloaded, err := client.Download(context.Background(), itemFor("http://indexer.example.com/dl/1", ""))
	return downloaded, body, err
}

func TestDownload_capsTorrentSize(t *testing.T) {
	_, body, gotErr := downloadBody(t, torrentOfSize(t, maxTorrentBytes+1), torrentContentType)

	if !errors.Is(gotErr, ErrBadResponse) || !strings.Contains(gotErr.Error(), "larger than") {
		t.Fatalf("expectedValue the size overflow, gotErr %v", gotErr)
	}
	if gotValue := body.read.Load(); gotValue > maxTorrentBytes+1 {
		t.Fatalf("expectedValue at most %d bytes read, gotValue %d", maxTorrentBytes+1, gotValue)
	}
}

func TestDownload_acceptsTorrentOfExactlyTheLimit(t *testing.T) {
	expectedValue := torrentOfSize(t, maxTorrentBytes)

	gotValue, _, err := downloadBody(t, expectedValue, torrentContentType)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(gotValue, expectedValue) {
		t.Fatal("expectedValue the whole torrent")
	}
}

func TestDownload_capsGzipBomb(t *testing.T) {
	raw, _ := buildTorrent(t, torrentOptions{})
	var compressedWritten atomic.Int64
	served := make(chan struct{})
	indexer := newFakeIndexer(t, func(writer http.ResponseWriter, _ *http.Request) {
		defer close(served)
		writer.Header().Set("Content-Type", torrentContentType)
		writer.Header().Set("Content-Encoding", "gzip")
		compressor := gzip.NewWriter(countingWriter{writer: writer, counter: &compressedWritten})
		if _, err := compressor.Write(raw); err != nil {
			return
		}
		padding := make([]byte, 1<<20)
		for {
			if _, err := compressor.Write(padding); err != nil {
				return
			}
		}
	})
	client, _, _ := newTestClient(t, indexer.server.URL)

	_, gotErr := client.Download(context.Background(), itemFor(indexer.server.URL+"/dl/1", ""))

	if !errors.Is(gotErr, ErrBadResponse) || !strings.Contains(gotErr.Error(), "larger than") {
		t.Fatalf("expectedValue the size overflow on decompressed bytes, gotErr %v", gotErr)
	}
	select {
	case <-served:
	case <-time.After(10 * time.Second):
		t.Fatal("expectedValue the client to stop reading, the server is still writing")
	}
	if gotValue := compressedWritten.Load(); gotValue > 64<<20 {
		t.Fatalf("expectedValue a bounded read, gotValue %d compressed bytes written", gotValue)
	}
}

func TestDownload_refusesWrongContentType(t *testing.T) {
	raw, _ := buildTorrent(t, torrentOptions{})
	for _, contentType := range []string{"", "text/html", "application/xml", "application/x-bittorrent-x", "text/plain; charset=utf-8", "garbage;;"} {
		t.Run(contentType, func(t *testing.T) {
			_, body, gotErr := downloadBody(t, raw, contentType)

			if !errors.Is(gotErr, ErrBadResponse) {
				t.Fatalf("expectedValue ErrBadResponse, gotErr %v", gotErr)
			}
			if gotValue := body.read.Load(); gotValue != 0 {
				t.Fatalf("expectedValue body never read, gotValue %d bytes", gotValue)
			}
		})
	}
	for _, contentType := range []string{"application/x-bittorrent", "Application/X-BitTorrent", "application/octet-stream; charset=binary"} {
		t.Run(contentType, func(t *testing.T) {
			if _, _, err := downloadBody(t, raw, contentType); err != nil {
				t.Fatalf("expectedValue %q accepted, gotErr %v", contentType, err)
			}
		})
	}
}

func TestDownload_checksAttributeInfohash(t *testing.T) {
	raw, infoHash := buildTorrent(t, torrentOptions{})
	for name, attribute := range map[string]string{"other hash": strings.Repeat("ab", 20), "truncated hash": infoHash[:39]} {
		t.Run(name, func(t *testing.T) {
			client := clientWithTransport(t, "http://indexer.example.com", torrentTransport(raw))
			_, gotErr := client.Download(context.Background(), itemFor("http://indexer.example.com/dl/1", attribute))
			if !errors.Is(gotErr, ErrInfohashMismatch) {
				t.Fatalf("expectedValue ErrInfohashMismatch, gotErr %v", gotErr)
			}
		})
	}
	for name, attribute := range map[string]string{"same hash": infoHash, "same hash in upper case": strings.ToUpper(infoHash), "no attribute": ""} {
		t.Run(name, func(t *testing.T) {
			client := clientWithTransport(t, "http://indexer.example.com", torrentTransport(raw))
			if _, err := client.Download(context.Background(), itemFor("http://indexer.example.com/dl/1", attribute)); err != nil {
				t.Fatalf("expectedValue success, gotErr %v", err)
			}
		})
	}
}

func TestDownload_refusesV2OnlyTorrent(t *testing.T) {
	raw, _ := buildTorrent(t, torrentOptions{metaVersion: v2MetaVersion, withoutV1: true})
	client := clientWithTransport(t, "http://indexer.example.com", torrentTransport(raw))

	_, gotErr := client.Download(context.Background(), itemFor("http://indexer.example.com/dl/1", ""))

	if !errors.Is(gotErr, ErrBadResponse) || !errors.Is(gotErr, torrentcheck.ErrV2Only) {
		t.Fatalf("expectedValue ErrBadResponse and ErrV2Only, gotErr %v", gotErr)
	}
}

func TestDownload_acceptsHybridTorrentByItsV1Infohash(t *testing.T) {
	raw, infoHash := buildTorrent(t, torrentOptions{metaVersion: v2MetaVersion})
	client := clientWithTransport(t, "http://indexer.example.com", torrentTransport(raw))

	gotValue, err := client.Download(context.Background(), itemFor("http://indexer.example.com/dl/1", infoHash))
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(gotValue, raw) {
		t.Fatal("expectedValue the hybrid torrent bytes")
	}
}

func TestDownload_refusesInvalidTorrent(t *testing.T) {
	_, _, gotErr := downloadBody(t, []byte("d8:announce4:http4:infoi1ee"), torrentContentType)

	if !errors.Is(gotErr, ErrBadResponse) || !errors.Is(gotErr, torrentcheck.ErrInvalidTorrent) {
		t.Fatalf("expectedValue ErrBadResponse and ErrInvalidTorrent, gotErr %v", gotErr)
	}
}

func TestDownload_usesTheIndexerLimiter(t *testing.T) {
	raw, _ := buildTorrent(t, torrentOptions{})
	indexer := newFakeIndexer(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api" {
			xmlResponse(rssWithItems())(writer, request)
			return
		}
		torrentResponse(raw, torrentContentType)(writer, request)
	})
	client, _, waits := newTestClient(t, indexer.server.URL)

	if _, err := client.Search(context.Background(), Query{Mode: "search", Text: "invented"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Download(context.Background(), itemFor(indexer.server.URL+"/dl/1", "")); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Download(context.Background(), itemFor(indexer.server.URL+"/dl/2", "")); err != nil {
		t.Fatal(err)
	}

	expectedValue := []time.Duration{3 * time.Second, 3 * time.Second}
	if gotValue := *waits; fmt.Sprint(gotValue) != fmt.Sprint(expectedValue) {
		t.Fatalf("expectedValue %v, gotValue %v", expectedValue, gotValue)
	}
}

func TestDownload_foreignHostDoesNotSpendTheLimiter(t *testing.T) {
	raw, _ := buildTorrent(t, torrentOptions{})
	indexer := newFakeIndexer(t, torrentResponse(raw, torrentContentType))
	client, _, waits := newTestClient(t, indexer.server.URL)

	_, _ = client.Download(context.Background(), itemFor("http://other.example.com/dl/1", ""))
	if _, err := client.Download(context.Background(), itemFor(indexer.server.URL+"/dl/1", "")); err != nil {
		t.Fatal(err)
	}

	if gotValue := len(*waits); gotValue != 0 {
		t.Fatalf("expectedValue no wait, gotValue %v", *waits)
	}
}

// secretLink est un lien de téléchargement qui porte la clé et une passkey.
func secretLink(base string) string {
	return base + "/dl/1?apikey=" + testAPIKey + "&passkey=" + downloadPasskey
}

func echoBody(status int, contentType string) http.HandlerFunc {
	echo := "Invalid key " + testAPIKey + " passkey " + downloadPasskey
	return func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", contentType)
		writer.WriteHeader(status)
		_, _ = io.WriteString(writer, `<error code="201" description="`+echo+`"/>`)
	}
}

// errorPath produit une erreur d'un appel du client contre un indexeur factice.
type errorPath func(t *testing.T) error

func downloadPath(handler http.HandlerFunc, infoHash string) errorPath {
	return func(t *testing.T) error {
		t.Helper()
		indexer := newFakeIndexer(t, handler)
		client, _, _ := newTestClient(t, indexer.server.URL)
		_, err := client.Download(context.Background(), itemFor(secretLink(indexer.server.URL), infoHash))
		return err
	}
}

func searchPath(handler http.HandlerFunc) errorPath {
	return func(t *testing.T) error {
		t.Helper()
		_, err := searchAgainst(t, handler)
		return err
	}
}

func capsPath(handler http.HandlerFunc) errorPath {
	return func(t *testing.T) error {
		t.Helper()
		indexer := newFakeIndexer(t, handler)
		client, _, _ := newTestClient(t, indexer.server.URL)
		_, err := client.Caps(context.Background())
		return err
	}
}

func closedServerDownload(t *testing.T) error {
	t.Helper()
	indexer := newFakeIndexer(t, xmlResponse(rssWithItems()))
	client, _, _ := newTestClient(t, indexer.server.URL)
	indexer.server.Close()
	_, err := client.Download(context.Background(), itemFor(secretLink(indexer.server.URL), ""))
	return err
}

func closedServerCaps(t *testing.T) error {
	t.Helper()
	indexer := newFakeIndexer(t, xmlResponse(rssWithItems()))
	client, _, _ := newTestClient(t, indexer.server.URL)
	indexer.server.Close()
	_, err := client.Caps(context.Background())
	return err
}

// oversizedBody répète la clé et la passkey au-delà du plafond XML et du plafond .torrent.
func oversizedBody(contentType string) http.HandlerFunc {
	chunk := []byte(strings.Repeat("Invalid key "+testAPIKey+" passkey "+downloadPasskey+" ", 1024))
	return func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", contentType)
		for written := 0; written <= maxTorrentBytes; written += len(chunk) {
			if _, err := writer.Write(chunk); err != nil {
				return
			}
		}
	}
}

func closedServerSearch(t *testing.T) error {
	t.Helper()
	indexer := newFakeIndexer(t, xmlResponse(rssWithItems()))
	client, _, _ := newTestClient(t, indexer.server.URL)
	indexer.server.Close()
	_, err := client.Search(context.Background(), Query{Mode: "search", Text: "invented"})
	return err
}

// cancelledDownload attend le limiteur avec un contexte annulé : l'erreur du limiteur ne cite pas le lien.
func cancelledDownload(t *testing.T) error {
	t.Helper()
	indexer := newFakeIndexer(t, xmlResponse(rssWithItems()))
	client, _, _ := newTestClient(t, indexer.server.URL)
	client.wait = sleepContext
	if _, err := client.Search(context.Background(), Query{Mode: "search", Text: "invented"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.Download(ctx, itemFor(secretLink(indexer.server.URL), ""))
	return err
}

func foreignDownload(t *testing.T) error {
	t.Helper()
	client := clientWithTransport(t, "http://indexer.example.com", torrentTransport(nil))
	_, err := client.Download(context.Background(), itemFor(secretLink("http://other.example.com"), ""))
	return err
}

func TestErrors_neverContainAPIKey(t *testing.T) {
	v2Only, _ := buildTorrent(t, torrentOptions{metaVersion: v2MetaVersion, withoutV1: true})
	valid, _ := buildTorrent(t, torrentOptions{})
	announce := "https://tracker.example.com/" + downloadPasskey
	invalid := []byte(fmt.Sprintf("d8:announce%d:%s4:infoi1ee", len(announce), announce))
	redirect := func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, secretLink("http://elsewhere.example.com"), http.StatusFound)
	}
	paths := map[string]errorPath{
		"caps transport error":      closedServerCaps,
		"caps access denied":        capsPath(echoBody(http.StatusUnauthorized, "application/xml")),
		"caps error element":        capsPath(echoBody(http.StatusOK, "application/xml")),
		"caps http 500 body":        capsPath(echoBody(http.StatusInternalServerError, "text/plain")),
		"search error element":      searchPath(echoBody(http.StatusOK, "application/xml")),
		"search http 418 body":      searchPath(echoBody(http.StatusTeapot, "application/xml")),
		"search redirect":           searchPath(redirect),
		"download transport error":  closedServerDownload,
		"download foreign host":     foreignDownload,
		"download redirect":         downloadPath(redirect, ""),
		"download access denied":    downloadPath(echoBody(http.StatusForbidden, "application/xml"), ""),
		"download http 500 body":    downloadPath(echoBody(http.StatusInternalServerError, "text/plain"), ""),
		"download http 418 body":    downloadPath(echoBody(http.StatusTeapot, "application/xml"), ""),
		"download wrong type":       downloadPath(echoBody(http.StatusOK, "text/html"), ""),
		"download invalid torrent":  downloadPath(torrentResponse(invalid, torrentContentType), ""),
		"download v2-only torrent":  downloadPath(torrentResponse(v2Only, torrentContentType), ""),
		"download infohash differs": downloadPath(torrentResponse(valid, torrentContentType), strings.Repeat("ab", 20)),
		"download http 429":         downloadPath(echoBody(http.StatusTooManyRequests, "text/plain"), ""),
		"download http 503":         downloadPath(echoBody(http.StatusServiceUnavailable, "text/plain"), ""),
		"download oversized body":   downloadPath(oversizedBody(torrentContentType), ""),
		"download cancelled":        cancelledDownload,
		"caps wrong type":           capsPath(echoBody(http.StatusOK, "text/html")),
		"caps oversized body":       capsPath(oversizedBody("application/xml")),
		"caps redirect":             capsPath(redirect),
		"search wrong type":         searchPath(echoBody(http.StatusOK, "text/html")),
		"search oversized body":     searchPath(oversizedBody("application/xml")),
		"search transport error":    closedServerSearch,
	}
	for name, path := range paths {
		t.Run(name, func(t *testing.T) {
			gotErr := path(t)
			if gotErr == nil {
				t.Fatal("expectedValue an error")
			}
			for _, rendering := range []string{gotErr.Error(), fmt.Sprintf("%+v", gotErr), fmt.Sprintf("%#v", gotErr)} {
				for _, secret := range []string{testAPIKey, downloadPasskey, "/dl/1", "Invalid key"} {
					if strings.Contains(rendering, secret) {
						t.Fatalf("error text leaks %q: %s", secret, rendering)
					}
				}
			}
		})
	}
}
