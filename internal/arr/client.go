// Package arr interroge Radarr et Sonarr pour associer chaque fichier à son identifiant TMDB ou TVDB.
package arr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// KindRadarr et KindSonarr sont les valeurs acceptées pour le type d'instance (insensible à la casse).
	KindRadarr = "radarr"
	KindSonarr = "sonarr"

	// requestTimeout borne chaque appel HTTP pour qu'une instance muette ne bloque pas le pipeline.
	requestTimeout = 30 * time.Second
	apiKeyHeader   = "X-Api-Key"
)

// ErrUnknownKind signale un type d'instance qui n'est ni radarr ni sonarr.
var ErrUnknownKind = errors.New("arr: unknown kind")

// ErrAccessDenied signale une clé d'API refusée par l'instance (HTTP 401 ou 403).
var ErrAccessDenied = errors.New("arr: access denied")

// Item est un fichier connu d'une instance ; TMDBID (Radarr) ou TVDBID (Sonarr) vaut 0 s'il ne s'applique pas.
type Item struct {
	Path   string
	TMDBID int
	TVDBID int
}

// Client interroge une instance Radarr ou Sonarr en lecture seule.
type Client struct {
	kind       string
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// New crée un client ; kind est passé en minuscules et l'éventuel / final de baseURL est retiré.
func New(kind, baseURL, apiKey string) *Client {
	return &Client{
		kind:       strings.ToLower(kind),
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: requestTimeout},
	}
}

// Ping vérifie que l'instance répond et accepte la clé, par un appel authentifié minimal (GET /api/v3/system/status).
func (client *Client) Ping(ctx context.Context) error {
	var status map[string]any
	return client.getJSON(ctx, "/api/v3/system/status", nil, &status)
}

// Items renvoie les fichiers présents de l'instance ; pour Radarr, les films sans fichier sont ignorés.
func (client *Client) Items(ctx context.Context) ([]Item, error) {
	switch client.kind {
	case KindRadarr:
		return client.radarrItems(ctx)
	case KindSonarr:
		return client.sonarrItems(ctx)
	}
	return nil, fmt.Errorf("%w %q", ErrUnknownKind, client.kind)
}

type radarrMovie struct {
	TMDBID    int `json:"tmdbId"`
	MovieFile *struct {
		Path string `json:"path"`
	} `json:"movieFile"`
}

func (client *Client) radarrItems(ctx context.Context) ([]Item, error) {
	var movies []radarrMovie
	if err := client.getJSON(ctx, "/api/v3/movie", nil, &movies); err != nil {
		return nil, err
	}
	var items []Item
	for _, movie := range movies {
		if movie.MovieFile == nil || movie.MovieFile.Path == "" {
			continue
		}
		items = append(items, Item{Path: movie.MovieFile.Path, TMDBID: movie.TMDBID})
	}
	return items, nil
}

type sonarrSeries struct {
	ID     int `json:"id"`
	TVDBID int `json:"tvdbId"`
}

type sonarrEpisodeFile struct {
	Path string `json:"path"`
}

func (client *Client) sonarrItems(ctx context.Context) ([]Item, error) {
	var seriesList []sonarrSeries
	if err := client.getJSON(ctx, "/api/v3/series", nil, &seriesList); err != nil {
		return nil, err
	}
	var items []Item
	for _, series := range seriesList {
		query := url.Values{"seriesId": {fmt.Sprint(series.ID)}}
		var episodeFiles []sonarrEpisodeFile
		if err := client.getJSON(ctx, "/api/v3/episodefile", query, &episodeFiles); err != nil {
			return nil, err
		}
		for _, episodeFile := range episodeFiles {
			items = append(items, Item{Path: episodeFile.Path, TVDBID: series.TVDBID})
		}
	}
	return items, nil
}

// getJSON retire l'erreur réseau enveloppée (errors.Unwrap) car url.Error embarque l'URL complète de la requête.
func (client *Client) getJSON(ctx context.Context, endpoint string, query url.Values, target any) error {
	requestURL := client.baseURL + endpoint
	if query != nil {
		requestURL += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return fmt.Errorf("arr %s: build request: %w", endpoint, errors.Unwrap(err))
	}
	request.Header.Set(apiKeyHeader, client.apiKey)

	response, err := client.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("arr %s: request failed: %w", endpoint, errors.Unwrap(err))
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return fmt.Errorf("arr %s: %w (HTTP %d)", endpoint, ErrAccessDenied, response.StatusCode)
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("arr %s: unexpected status %d", endpoint, response.StatusCode)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		return fmt.Errorf("arr %s: decode: %w", endpoint, err)
	}
	return nil
}
