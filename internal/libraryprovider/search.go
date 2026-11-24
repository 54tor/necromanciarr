package libraryprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

const (
	pageSize         = 100
	defaultMaxPages  = 10
	maxSizesPerQuery = 50 // plafond du contrat pour le paramètre size
)

// Query décrit une recherche ; les filtres renseignés se combinent par ET.
type Query struct {
	TMDBID, TVDBID int
	IMDBID         string
	Text           string
	Tracker        string
	// Sizes sont des tailles exactes de fichier ; au-delà de 50, la recherche est découpée en plusieurs requêtes.
	Sizes []int64
	// Limit borne le nombre de résultats par page ; 0 garde la taille de page par défaut (100).
	Limit int
	// MaxPages borne les pages suivies par requête (10 par défaut).
	MaxPages int
}

// ReleaseFile est un fichier d'un torrent, chemin relatif à la racine.
type ReleaseFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// Release est une release du fournisseur ; Tracker est vide si le fournisseur n'en a pas la notion.
type Release struct {
	Infohash  string        `json:"infohash"`
	Name      string        `json:"name"`
	Tracker   string        `json:"tracker"`
	TotalSize int64         `json:"totalSize"`
	Files     []ReleaseFile `json:"files"`
	TMDBID    int           `json:"tmdb"`
	TVDBID    int           `json:"tvdb"`
	IMDBID    string        `json:"imdb"`
}

type releasePage struct {
	Items []Release `json:"items"`
	Next  *string   `json:"next"`
}

func (query Query) values(sizes []int64, cursor string) url.Values {
	values := url.Values{"limit": {strconv.Itoa(pageSize)}}
	if query.Limit > 0 {
		values.Set("limit", strconv.Itoa(query.Limit))
	}
	if query.TMDBID > 0 {
		values.Set("tmdb", strconv.Itoa(query.TMDBID))
	}
	if query.TVDBID > 0 {
		values.Set("tvdb", strconv.Itoa(query.TVDBID))
	}
	setIfNotEmpty(values, "imdb", query.IMDBID)
	setIfNotEmpty(values, "q", query.Text)
	setIfNotEmpty(values, "tracker", query.Tracker)
	setIfNotEmpty(values, "cursor", cursor)
	for _, size := range sizes {
		values.Add("size", strconv.FormatInt(size, 10))
	}
	return values
}

func setIfNotEmpty(values url.Values, key, value string) {
	if value != "" {
		values.Set(key, value)
	}
}

func chunkSizes(sizes []int64) [][]int64 {
	if len(sizes) == 0 {
		return [][]int64{nil}
	}
	var chunks [][]int64
	for start := 0; start < len(sizes); start += maxSizesPerQuery {
		chunks = append(chunks, sizes[start:min(start+maxSizesPerQuery, len(sizes))])
	}
	return chunks
}

// Search interroge GET /v1/releases ; les résultats des requêtes découpées sont fusionnés par infohash, premier vu d'abord.
func (client *Client) Search(ctx context.Context, query Query) ([]Release, error) {
	maxPages := query.MaxPages
	if maxPages < 1 {
		maxPages = defaultMaxPages
	}
	var releases []Release
	seen := map[string]bool{}
	for _, sizes := range chunkSizes(query.Sizes) {
		found, err := client.searchChunk(ctx, query, sizes, maxPages)
		if err != nil {
			return nil, err
		}
		for _, release := range found {
			if !seen[release.Infohash] {
				seen[release.Infohash] = true
				releases = append(releases, release)
			}
		}
	}
	return releases, nil
}

// searchChunk suit les curseurs ; le curseur n'est qu'un paramètre envoyé à la base configurée, jamais une URL suivie.
func (client *Client) searchChunk(ctx context.Context, query Query, sizes []int64, maxPages int) ([]Release, error) {
	var releases []Release
	cursor := ""
	for pageNumber := 0; pageNumber < maxPages; pageNumber++ {
		body, err := client.getJSON(ctx, client.endpoint(query.values(sizes, cursor), segmentReleases))
		if err != nil {
			return nil, err
		}
		var page releasePage
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("%w: invalid JSON", ErrBadResponse)
		}
		for index := range page.Items {
			if err := normalize(&page.Items[index]); err != nil {
				return nil, err
			}
		}
		releases = append(releases, page.Items...)
		if page.Next == nil || *page.Next == "" {
			break
		}
		cursor = *page.Next
	}
	return releases, nil
}

// Release interroge GET /v1/releases/{infohash} et renvoie la liste complète des fichiers.
func (client *Client) Release(ctx context.Context, infohash, tracker string) (Release, error) {
	if err := validateInfohash(infohash); err != nil {
		return Release{}, err
	}
	body, err := client.getJSON(ctx, client.endpoint(trackerQuery(tracker), segmentReleases, infohash))
	if err != nil {
		return Release{}, err
	}
	var release Release
	if err := json.Unmarshal(body, &release); err != nil {
		return Release{}, fmt.Errorf("%w: invalid JSON", ErrBadResponse)
	}
	if err := normalize(&release); err != nil {
		return Release{}, err
	}
	return release, nil
}

// normalize rejette un infohash invalide et le met en minuscules.
func normalize(release *Release) error {
	if validateInfohash(release.Infohash) != nil {
		return fmt.Errorf("%w: invalid infohash in release", ErrBadResponse)
	}
	release.Infohash = strings.ToLower(release.Infohash)
	return nil
}
