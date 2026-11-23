package libraryprovider

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	httpTimeout        = 30 * time.Second
	defaultConcurrency = 4
	defaultMaxPages    = 10
	pageSize           = 100
	maxJSONBytes       = 16 << 20
	maxTorrentBytes    = 32 << 20
	jsonAccept         = "application/ld+json"
)

var (
	ErrAccessDenied = errors.New("library-provider: access denied")
	ErrNotFound     = errors.New("library-provider: release not found")
	ErrUnavailable  = errors.New("library-provider: unavailable")
	ErrBadResponse  = errors.New("library-provider: unreadable response")
)

type Query struct {
	TMDBID, TVDBID int
	Text, Tracker  string
	MaxPages       int
}

type ReleaseFile struct {
	Path string
	Size int64
}

type Release struct {
	Tracker, InfoHash string
	TotalSize         int64
	Files             []ReleaseFile
}

type Nfo struct {
	Label string
	Data  []byte
}

type Client struct {
	MaxInFlight int

	base          *url.URL
	token         string
	httpClient    *http.Client
	cache         *cache
	semaphore     chan struct{}
	semaphoreOnce sync.Once
}

func New(baseURL, token string) (*Client, error) {
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || baseURL == "" || parsed.Host == "" {
		return nil, errors.New("library-provider: base URL is required")
	}
	return &Client{
		MaxInFlight: defaultConcurrency,
		base:        parsed,
		token:       token,
		httpClient:  &http.Client{Timeout: httpTimeout},
		cache:       newCache(),
	}, nil
}

func (client *Client) acquire(ctx context.Context) (func(), error) {
	client.semaphoreOnce.Do(func() {
		limit := client.MaxInFlight
		if limit < 1 {
			limit = defaultConcurrency
		}
		client.semaphore = make(chan struct{}, limit)
	})
	select {
	case client.semaphore <- struct{}{}:
		return func() { <-client.semaphore }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, ctx.Err())
	}
}

func (client *Client) get(ctx context.Context, target string, accept string, limit int64) ([]byte, error) {
	release, err := client.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid request", ErrBadResponse)
	}
	request.Header.Set("Authorization", "Bearer "+client.token)
	request.Header.Set("Accept", accept)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: request failed", ErrUnavailable)
	}
	defer response.Body.Close()
	if statusErr := statusError(response.StatusCode); statusErr != nil {
		return nil, statusErr
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit))
	if err != nil {
		return nil, fmt.Errorf("%w: body read failed", ErrUnavailable)
	}
	return body, nil
}

func statusError(status int) error {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return fmt.Errorf("%w (HTTP %d)", ErrAccessDenied, status)
	case status == http.StatusNotFound:
		return ErrNotFound
	case status >= 500 || status == http.StatusTooManyRequests || status == http.StatusRequestTimeout:
		return fmt.Errorf("%w (HTTP %d)", ErrUnavailable, status)
	case status >= 400:
		return fmt.Errorf("%w (HTTP %d)", ErrBadResponse, status)
	}
	return nil
}

func (client *Client) endpoint(path string, query url.Values) string {
	target := *client.base
	target.Path = strings.TrimRight(target.Path, "/") + path
	target.RawQuery = query.Encode()
	return target.String()
}

func (client *Client) torrentPath(tracker, infoHash, suffix string) string {
	return "/torrents/" + url.PathEscape(tracker) + "/" + url.PathEscape(infoHash) + suffix
}

type releasePage struct {
	Member []struct {
		Torrents []struct {
			Name     string `json:"name"`
			InfoHash string `json:"infoHash"`
		} `json:"torrents"`
		Files []struct {
			Path string `json:"path"`
			Size int64  `json:"size"`
		} `json:"files"`
		TotalSize int64 `json:"totalSize"`
	} `json:"member"`
	View struct {
		Next string `json:"next"`
	} `json:"view"`
}

func (page releasePage) releases(tracker string) []Release {
	var releases []Release
	for _, member := range page.Member {
		files := make([]ReleaseFile, 0, len(member.Files))
		for _, file := range member.Files {
			files = append(files, ReleaseFile{Path: file.Path, Size: file.Size})
		}
		for _, torrent := range member.Torrents {
			if tracker != "" && torrent.Name != tracker {
				continue
			}
			releases = append(releases, Release{Tracker: torrent.Name, InfoHash: torrent.InfoHash, TotalSize: member.TotalSize, Files: files})
		}
	}
	return releases
}

func (query Query) values() url.Values {
	values := url.Values{"limit": {strconv.Itoa(pageSize)}}
	switch {
	case query.TMDBID > 0:
		values.Set("q", "tmdb:"+strconv.Itoa(query.TMDBID))
	case query.TVDBID > 0:
		values.Set("q", "tvdb:"+strconv.Itoa(query.TVDBID))
	case query.Text != "":
		values.Set("q", query.Text)
	}
	if query.Tracker != "" {
		values.Set("tracker", query.Tracker)
	}
	return values
}

func (client *Client) nextPageURL(next string) (string, error) {
	resolved, err := client.base.Parse(next)
	if err != nil || resolved.Host != client.base.Host || resolved.Scheme != client.base.Scheme {
		return "", fmt.Errorf("%w: unexpected pagination link", ErrBadResponse)
	}
	return resolved.String(), nil
}

func (client *Client) Search(ctx context.Context, query Query) ([]Release, error) {
	maxPages := query.MaxPages
	if maxPages < 1 {
		maxPages = defaultMaxPages
	}
	cacheKey := "search|" + client.endpoint("/releases", query.values()) + "|" + strconv.Itoa(maxPages)
	if cached, found := client.cache.get(cacheKey); found {
		return cached.value.([]Release), cached.err
	}
	var releases []Release
	target := client.endpoint("/releases", query.values())
	for pageNumber := 0; pageNumber < maxPages && target != ""; pageNumber++ {
		body, err := client.get(ctx, target, jsonAccept, maxJSONBytes)
		if err != nil {
			return nil, err
		}
		var page releasePage
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("%w: invalid JSON", ErrBadResponse)
		}
		releases = append(releases, page.releases(query.Tracker)...)
		target = ""
		if page.View.Next != "" {
			if target, err = client.nextPageURL(page.View.Next); err != nil {
				return nil, err
			}
		}
	}
	client.cache.put(cacheKey, releases, nil)
	return releases, nil
}

func (client *Client) Torrent(ctx context.Context, tracker, infoHash string) ([]byte, error) {
	target := client.endpoint(client.torrentPath(tracker, infoHash, "/download"), url.Values{"noAnnounce": {"1"}})
	body, err := client.get(ctx, target, "application/x-bittorrent", maxTorrentBytes)
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(body, []byte("d")) {
		return nil, fmt.Errorf("%w: not a bencode dictionary", ErrBadResponse)
	}
	return body, nil
}

type nfosPayload struct {
	Nfos map[string][]struct {
		Nfo     string `json:"nfo"`
		Partial bool   `json:"partial"`
	} `json:"nfos"`
	ReleaseNfo string `json:"releaseNfo"`
}

func (client *Client) Nfos(ctx context.Context, tracker, infoHash string) ([]Nfo, error) {
	cacheKey := "nfos|" + tracker + "|" + infoHash
	if cached, found := client.cache.get(cacheKey); found {
		if cached.err != nil {
			return nil, cached.err
		}
		return cached.value.([]Nfo), nil
	}
	body, err := client.get(ctx, client.endpoint(client.torrentPath(tracker, infoHash, "/nfos"), nil), jsonAccept, maxJSONBytes)
	if errors.Is(err, ErrNotFound) {
		client.cache.put(cacheKey, nil, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	var payload nfosPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("%w: invalid JSON", ErrBadResponse)
	}
	nfos := deduplicate(collectNfos(payload))
	client.cache.put(cacheKey, nfos, nil)
	return nfos, nil
}

func collectNfos(payload nfosPayload) []Nfo {
	var nfos []Nfo
	slugs := make([]string, 0, len(payload.Nfos))
	for slug := range payload.Nfos {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	for _, slug := range slugs {
		entries := payload.Nfos[slug]
		for entryIndex, entry := range entries {
			data, err := decodeBase64(entry.Nfo)
			if err != nil || len(data) == 0 {
				continue
			}
			label := "api:" + slug
			if len(entries) > 1 {
				label += "#" + strconv.Itoa(entryIndex+1)
			}
			if entry.Partial {
				label += "(partial)"
			}
			nfos = append(nfos, Nfo{Label: label, Data: data})
		}
	}
	if data, err := decodeBase64(payload.ReleaseNfo); err == nil && len(data) > 0 {
		nfos = append(nfos, Nfo{Label: "api:releaseNfo", Data: data})
	}
	return nfos
}

func decodeBase64(encoded string) ([]byte, error) {
	return base64.RawStdEncoding.DecodeString(strings.TrimRight(strings.TrimSpace(encoded), "="))
}

func deduplicate(nfos []Nfo) []Nfo {
	seen := map[[sha1.Size]byte]bool{}
	var unique []Nfo
	for _, nfo := range nfos {
		digest := sha1.Sum(nfo.Data)
		if seen[digest] {
			continue
		}
		seen[digest] = true
		unique = append(unique, nfo)
	}
	return unique
}
