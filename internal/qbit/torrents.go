package qbit

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	addRejectedBody   = "Fails."
	uploadFileName    = "upload.torrent"
	infoHashV1Length  = 40
	infoHashV2Length  = 64
	tagSeparator      = ","
	endpointAdd       = "torrents/add"
	endpointInfo      = "torrents/info"
	endpointFiles     = "torrents/files"
	endpointExport    = "torrents/export"
	endpointRecheck   = "torrents/recheck"
	verbPauseV4       = "torrents/pause"
	verbResumeV4      = "torrents/resume"
	verbStopV5        = "torrents/stop"
	verbStartV5       = "torrents/start"
	fieldPausedV4     = "paused"
	fieldStoppedV5    = "stopped"
	fieldSkipChecking = "skip_checking"
)

// AddOptions règle l'ajout d'un torrent : catégorie et tags servent aux filtres de qui et cross-seed, Paused à la
// revue avant reprise, SkipChecking reprend skipRecheck de cross-seed.
type AddOptions struct {
	Category     string
	Tags         []string
	Paused       bool
	SkipChecking bool
}

// TorrentInfo est un torrent tel que listé par qBittorrent ; Progress va de 0 à 1.
type TorrentInfo struct {
	Hash        string
	Name        string
	SavePath    string
	ContentPath string
	Category    string
	Tags        []string
	State       string
	Progress    float64
	Size        int64
}

// TorrentFile est un fichier d'un torrent ; Name est relatif au dossier d'enregistrement.
type TorrentFile struct {
	Name     string
	Size     int64
	Progress float64
}

type infoEntry struct {
	Hash        string  `json:"hash"`
	Name        string  `json:"name"`
	SavePath    string  `json:"save_path"`
	ContentPath string  `json:"content_path"`
	Category    string  `json:"category"`
	Tags        string  `json:"tags"`
	State       string  `json:"state"`
	Progress    float64 `json:"progress"`
	Size        int64   `json:"size"`
}

type fileEntry struct {
	Name     string  `json:"name"`
	Size     int64   `json:"size"`
	Progress float64 `json:"progress"`
}

// validateHash n'accepte qu'un info-hash v1 ou v2 en hexadécimal, rendu en minuscules.
func validateHash(infoHash string) (string, error) {
	normalized := strings.ToLower(infoHash)
	hasValidLength := len(normalized) == infoHashV1Length || len(normalized) == infoHashV2Length
	if _, err := hex.DecodeString(normalized); err != nil || !hasValidLength {
		return "", ErrInvalidHash
	}
	return normalized, nil
}

// Add envoie le .torrent en multipart ; root_folder n'est jamais envoyé : l'arborescence est déjà sur le disque et
// le nom du dossier racine vient du torrent. autoTMM=false garde savePath tel quel.
func (client *Client) Add(ctx context.Context, torrent []byte, savePath string, options AddOptions) error {
	if err := client.refuseIfReadOnly(); err != nil {
		return err
	}
	stopStart, err := client.usesStopStart(ctx)
	if err != nil {
		return err
	}
	body, err := addBody(torrent, savePath, options, stopStart)
	if err != nil {
		return err
	}
	payload, err := client.call(ctx, http.MethodPost, endpointAdd, nil, body, maxSmallBytes)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(payload)) == addRejectedBody {
		return fmt.Errorf("%w: torrent rejected by qBittorrent", ErrBadResponse)
	}
	return nil
}

func addBody(torrent []byte, savePath string, options AddOptions, stopStart bool) (*requestBody, error) {
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	part, err := writer.CreateFormFile("torrents", uploadFileName)
	if err != nil {
		return nil, fmt.Errorf("qbit: building add request: %w", err)
	}
	if _, err := part.Write(torrent); err != nil {
		return nil, fmt.Errorf("qbit: building add request: %w", err)
	}
	pausedField := fieldPausedV4
	if stopStart {
		pausedField = fieldStoppedV5
	}
	fields := [][2]string{
		{"savepath", savePath},
		{"autoTMM", "false"},
		{pausedField, strconv.FormatBool(options.Paused)},
		{fieldSkipChecking, strconv.FormatBool(options.SkipChecking)},
	}
	if options.Category != "" {
		fields = append(fields, [2]string{"category", options.Category})
	}
	if len(options.Tags) > 0 {
		fields = append(fields, [2]string{"tags", strings.Join(options.Tags, tagSeparator)})
	}
	for _, field := range fields {
		if err := writer.WriteField(field[0], field[1]); err != nil {
			return nil, fmt.Errorf("qbit: building add request: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("qbit: building add request: %w", err)
	}
	return &requestBody{contentType: writer.FormDataContentType(), data: buffer.Bytes()}, nil
}

// Recheck demande le recontrôle des données d'un torrent ; voir RecheckAndGuard pour l'usage sûr.
func (client *Client) Recheck(ctx context.Context, infoHash string) error {
	return client.hashAction(ctx, endpointRecheck, infoHash)
}

// Pause met un torrent en pause (stop sur qBittorrent 5).
func (client *Client) Pause(ctx context.Context, infoHash string) error {
	stopStart, err := client.usesStopStartForWrite(ctx)
	if err != nil {
		return err
	}
	if stopStart {
		return client.hashAction(ctx, verbStopV5, infoHash)
	}
	return client.hashAction(ctx, verbPauseV4, infoHash)
}

// Resume reprend un torrent (start sur qBittorrent 5).
func (client *Client) Resume(ctx context.Context, infoHash string) error {
	stopStart, err := client.usesStopStartForWrite(ctx)
	if err != nil {
		return err
	}
	if stopStart {
		return client.hashAction(ctx, verbStartV5, infoHash)
	}
	return client.hashAction(ctx, verbResumeV4, infoHash)
}

// usesStopStartForWrite refuse avant de sonder la version : un client readonly n'émet aucune requête d'écriture.
func (client *Client) usesStopStartForWrite(ctx context.Context) (bool, error) {
	if err := client.refuseIfReadOnly(); err != nil {
		return false, err
	}
	return client.usesStopStart(ctx)
}

func (client *Client) hashAction(ctx context.Context, endpoint, infoHash string) error {
	if err := client.refuseIfReadOnly(); err != nil {
		return err
	}
	normalized, err := validateHash(infoHash)
	if err != nil {
		return err
	}
	_, err = client.call(ctx, http.MethodPost, endpoint, nil, formBody(url.Values{"hashes": {normalized}}), maxSmallBytes)
	return err
}

// Torrents liste tous les torrents du client.
func (client *Client) Torrents(ctx context.Context) ([]TorrentInfo, error) {
	return client.listTorrents(ctx, nil)
}

// Progress renvoie l'avancement (0 à 1) d'un torrent ; ErrNotFound s'il est inconnu.
func (client *Client) Progress(ctx context.Context, infoHash string) (float64, error) {
	torrent, err := client.torrent(ctx, infoHash)
	if err != nil {
		return 0, err
	}
	return torrent.Progress, nil
}

func (client *Client) torrent(ctx context.Context, infoHash string) (TorrentInfo, error) {
	normalized, err := validateHash(infoHash)
	if err != nil {
		return TorrentInfo{}, err
	}
	torrents, err := client.listTorrents(ctx, url.Values{"hashes": {normalized}})
	if err != nil {
		return TorrentInfo{}, err
	}
	for _, torrent := range torrents {
		if strings.EqualFold(torrent.Hash, normalized) {
			return torrent, nil
		}
	}
	return TorrentInfo{}, fmt.Errorf("%w: %s", ErrNotFound, normalized)
}

func (client *Client) listTorrents(ctx context.Context, query url.Values) ([]TorrentInfo, error) {
	payload, err := client.call(ctx, http.MethodGet, endpointInfo, query, nil, maxListBytes)
	if err != nil {
		return nil, err
	}
	var entries []infoEntry
	if err := json.Unmarshal(payload, &entries); err != nil {
		return nil, fmt.Errorf("%w: %s: invalid json", ErrBadResponse, endpointInfo)
	}
	torrents := make([]TorrentInfo, 0, len(entries))
	for _, entry := range entries {
		torrents = append(torrents, TorrentInfo{
			Hash: entry.Hash, Name: entry.Name, SavePath: entry.SavePath, ContentPath: entry.ContentPath,
			Category: entry.Category, Tags: splitTags(entry.Tags), State: entry.State, Progress: entry.Progress, Size: entry.Size,
		})
	}
	return torrents, nil
}

// splitTags lit la liste « a, b » renvoyée par torrents/info.
func splitTags(raw string) []string {
	var tags []string
	for _, tag := range strings.Split(raw, tagSeparator) {
		if trimmed := strings.TrimSpace(tag); trimmed != "" {
			tags = append(tags, trimmed)
		}
	}
	return tags
}

// Files liste les fichiers d'un torrent ; ErrNotFound s'il est inconnu.
func (client *Client) Files(ctx context.Context, infoHash string) ([]TorrentFile, error) {
	normalized, err := validateHash(infoHash)
	if err != nil {
		return nil, err
	}
	payload, err := client.call(ctx, http.MethodGet, endpointFiles, url.Values{"hash": {normalized}}, nil, maxListBytes)
	if err != nil {
		return nil, err
	}
	var entries []fileEntry
	if err := json.Unmarshal(payload, &entries); err != nil {
		return nil, fmt.Errorf("%w: %s: invalid json", ErrBadResponse, endpointFiles)
	}
	files := make([]TorrentFile, 0, len(entries))
	for _, entry := range entries {
		files = append(files, TorrentFile(entry))
	}
	return files, nil
}

// ExportTorrent renvoie le .torrent brut (qBittorrent 4.5 ou plus, sinon ErrUnsupported) ; un 404 sur une version
// qui connaît l'export veut dire torrent inconnu (ErrNotFound).
func (client *Client) ExportTorrent(ctx context.Context, infoHash string) ([]byte, error) {
	normalized, err := validateHash(infoHash)
	if err != nil {
		return nil, err
	}
	version, err := client.apiVersion(ctx)
	if err != nil {
		return nil, err
	}
	if !version.atLeast(exportVersion) {
		return nil, fmt.Errorf("%w: %s needs qBittorrent 4.5 or later", ErrUnsupported, endpointExport)
	}
	return client.call(ctx, http.MethodGet, endpointExport, url.Values{"hash": {normalized}}, nil, maxListBytes)
}
