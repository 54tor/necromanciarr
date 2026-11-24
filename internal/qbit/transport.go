package qbit

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
)

const (
	// maxTorrentListBytes borne torrents/info : sans filtre, la liste entière d'un gros client (environ 30 000
	// torrents) doit tenir. maxListBytes borne les fichiers d'un torrent et l'export d'un .torrent ; maxSmallBytes
	// borne les autres réponses.
	maxTorrentListBytes = 64 << 20
	maxListBytes        = 8 << 20
	maxSmallBytes       = 64 << 10
	loginRejectedBody   = "Fails."
	pathSuffixLogin     = "/api/v2/auth/login"
	pathSuffixAdd       = "/api/v2/torrents/add"
	textPlainMediaType  = "text/plain"
	headerContentType   = "Content-Type"
	headerReferer       = "Referer"
	headerOrigin        = "Origin"
	pathSuffixInfo      = "/api/v2/torrents/info"
	pathSuffixFiles     = "/api/v2/torrents/files"
	pathSuffixExport    = "/api/v2/torrents/export"
	probeContextKeyName = "qbit-call-probe"
)

var errResponseTooLarge = errors.New("response too large")

type probeContextKey string

// callProbe note ce que le transport a vu pendant un appel de la bibliothèque : ses erreurs ne se déballent pas
// toujours (liste de retry-go) et portent l'URL, elles ne sont donc jamais lues.
type callProbe struct {
	lastStatus      int
	forbidden       bool
	loginRejected   bool
	addRejected     bool
	tooLarge        bool
	redirectRefused bool
}

func withProbe(ctx context.Context, probe *callProbe) context.Context {
	return context.WithValue(ctx, probeContextKey(probeContextKeyName), probe)
}

// probeFrom renvoie une sonde jetable si l'appel n'en porte pas (la bibliothèque sonde la version sans contexte).
func probeFrom(ctx context.Context) *callProbe {
	if probe, found := ctx.Value(probeContextKey(probeContextKeyName)).(*callProbe); found {
		return probe
	}
	return &callProbe{}
}

// guardTransport ajoute Referer et Origin (protection CSRF de qBittorrent), plafonne les corps de réponse et
// relève les codes HTTP dans la sonde de l'appel.
type guardTransport struct {
	base    http.RoundTripper
	referer string
	origin  string
}

func (transport *guardTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	outgoing := request.Clone(request.Context())
	outgoing.Header.Set(headerReferer, transport.referer)
	outgoing.Header.Set(headerOrigin, transport.origin)
	response, err := transport.base.RoundTrip(outgoing)
	if err != nil {
		return nil, err
	}
	probe := probeFrom(request.Context())
	probe.lastStatus = response.StatusCode
	if response.StatusCode == http.StatusForbidden {
		probe.forbidden = true
	}
	path := request.URL.Path
	if strings.HasSuffix(path, pathSuffixLogin) && response.StatusCode == http.StatusForbidden {
		probe.loginRejected = true
	}
	response.Body = &cappedBody{source: response.Body, remaining: responseLimit(path), probe: probe}
	if needsPlainBodyPeek(path, response) {
		return peekPlainBody(response, path, probe)
	}
	return response, nil
}

func responseLimit(path string) int64 {
	switch {
	case strings.HasSuffix(path, pathSuffixInfo):
		return maxTorrentListBytes
	case strings.HasSuffix(path, pathSuffixFiles), strings.HasSuffix(path, pathSuffixExport):
		return maxListBytes
	default:
		return maxSmallBytes
	}
}

// needsPlainBodyPeek : la bibliothèque ne lit pas le corps texte de login et d'ajout, où « Fails. » signale un refus.
func needsPlainBodyPeek(path string, response *http.Response) bool {
	isLoginOrAdd := strings.HasSuffix(path, pathSuffixLogin) || strings.HasSuffix(path, pathSuffixAdd)
	return isLoginOrAdd && strings.HasPrefix(response.Header.Get(headerContentType), textPlainMediaType)
}

func peekPlainBody(response *http.Response, path string, probe *callProbe) (*http.Response, error) {
	payload, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		return nil, err
	}
	rejected := strings.TrimSpace(string(payload)) == loginRejectedBody
	if strings.HasSuffix(path, pathSuffixLogin) {
		probe.loginRejected = probe.loginRejected || rejected
	} else {
		probe.addRejected = rejected
	}
	response.Body = io.NopCloser(bytes.NewReader(payload))
	return response, nil
}

// cappedBody échoue dès que la réponse dépasse sa limite, au lieu de la tronquer en silence.
type cappedBody struct {
	source    io.ReadCloser
	remaining int64
	probe     *callProbe
}

func (body *cappedBody) Read(buffer []byte) (int, error) {
	if body.remaining <= 0 {
		var probeByte [1]byte
		if count, _ := body.source.Read(probeByte[:]); count > 0 {
			body.probe.tooLarge = true
			return 0, errResponseTooLarge
		}
		return 0, io.EOF
	}
	if int64(len(buffer)) > body.remaining {
		buffer = buffer[:body.remaining]
	}
	count, err := body.source.Read(buffer)
	body.remaining -= int64(count)
	return count, err
}

func (body *cappedBody) Close() error { return body.source.Close() }
