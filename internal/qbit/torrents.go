package qbit

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Masterminds/semver"
	qbittorrent "github.com/autobrr/go-qbittorrent"
)

const (
	infoHashV1Length = 40
	infoHashV2Length = 64
	tagSeparator     = ","
	// contentLayoutOriginal garde l'arborescence du torrent, déjà présente sur le disque (jamais root_folder).
	contentLayoutOriginal = "Original"
)

// exportMinimumVersion : torrents/export arrive avec l'API 2.8.11 (qBittorrent 4.5).
var exportMinimumVersion = semver.MustParse("2.8.11")

// AddOptions règle l'ajout d'un torrent : catégorie et tags servent aux filtres de qui et cross-seed,
// SkipChecking reprend skipRecheck de cross-seed. Le torrent est ajouté arrêté : Start est un choix explicite de
// l'appelant, à ne faire qu'après qu'un RecheckAndGuard a confirmé 100 %.
type AddOptions struct {
	Category     string
	Tags         []string
	Start        bool
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

// validateHash n'accepte qu'un info-hash v1 ou v2 en hexadécimal, rendu en minuscules.
func validateHash(infoHash string) (string, error) {
	normalized := strings.ToLower(infoHash)
	hasValidLength := len(normalized) == infoHashV1Length || len(normalized) == infoHashV2Length
	if _, err := hex.DecodeString(normalized); err != nil || !hasValidLength {
		return "", ErrInvalidHash
	}
	return normalized, nil
}

// Add envoie le .torrent ; paused et stopped (qBittorrent 4 et 5) portent la même valeur, si bien qu'un torrent
// n'est jamais démarré par erreur de version. autoTMM=false garde savePath tel quel.
func (client *Client) Add(ctx context.Context, torrent []byte, savePath string, options AddOptions) error {
	if err := client.refuseIfReadOnly(); err != nil {
		return err
	}
	var response *qbittorrent.TorrentAddResponse
	probe, err := client.run(ctx, "add torrent", func(callCtx context.Context) error {
		var addErr error
		response, addErr = client.library.AddTorrentFromMemoryCtx(callCtx, torrent, addFields(savePath, options))
		return addErr
	})
	if err != nil {
		return err
	}
	return checkAddResponse(response, probe)
}

func addFields(savePath string, options AddOptions) map[string]string {
	stopped := strconv.FormatBool(!options.Start)
	fields := map[string]string{
		"savepath":      savePath,
		"autoTMM":       "false",
		"contentLayout": contentLayoutOriginal,
		"paused":        stopped,
		"stopped":       stopped,
		"skip_checking": strconv.FormatBool(options.SkipChecking),
	}
	if options.Category != "" {
		fields["category"] = options.Category
	}
	if len(options.Tags) > 0 {
		fields["tags"] = strings.Join(options.Tags, tagSeparator)
	}
	return fields
}

// checkAddResponse lit « Fails. » (réponse texte) ou success_count et failure_count (JSON, API 2.14 et plus).
func checkAddResponse(response *qbittorrent.TorrentAddResponse, probe *callProbe) error {
	if probe.addRejected {
		return fmt.Errorf("%w: add torrent: rejected by qBittorrent", ErrBadResponse)
	}
	if response == nil || response.FailureCount > 0 || response.SuccessCount == 0 {
		return fmt.Errorf("%w: add torrent: not added", ErrBadResponse)
	}
	return nil
}

// Recheck demande le recontrôle des données d'un torrent ; voir RecheckAndGuard pour l'usage sûr.
func (client *Client) Recheck(ctx context.Context, infoHash string) error {
	return client.hashAction(ctx, "recheck", infoHash, client.library.RecheckCtx)
}

// Pause met un torrent en pause ; la bibliothèque choisit pause ou stop selon la version de l'API.
func (client *Client) Pause(ctx context.Context, infoHash string) error {
	return client.hashAction(ctx, "pause", infoHash, client.library.PauseCtx)
}

// Resume reprend un torrent ; la bibliothèque choisit resume ou start selon la version de l'API.
func (client *Client) Resume(ctx context.Context, infoHash string) error {
	return client.hashAction(ctx, "resume", infoHash, client.library.ResumeCtx)
}

func (client *Client) hashAction(ctx context.Context, verb, infoHash string, action func(context.Context, []string) error) error {
	if err := client.refuseIfReadOnly(); err != nil {
		return err
	}
	normalized, err := validateHash(infoHash)
	if err != nil {
		return err
	}
	_, err = client.run(ctx, verb+" "+normalized, func(callCtx context.Context) error {
		return action(callCtx, []string{normalized})
	})
	return err
}

// Torrents liste tous les torrents du client.
func (client *Client) Torrents(ctx context.Context) ([]TorrentInfo, error) {
	return client.listTorrents(ctx, "list torrents", qbittorrent.TorrentFilterOptions{})
}

// FindTorrent cherche un seul torrent par son hash (requête filtrée, jamais la liste entière) ; present est faux s'il
// est inconnu du client.
func (client *Client) FindTorrent(ctx context.Context, infoHash string) (TorrentInfo, bool, error) {
	torrent, err := client.torrent(ctx, infoHash)
	if errors.Is(err, ErrNotFound) {
		return TorrentInfo{}, false, nil
	}
	if err != nil {
		return TorrentInfo{}, false, err
	}
	return torrent, true, nil
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
	torrents, err := client.listTorrents(ctx, "torrent "+normalized, qbittorrent.TorrentFilterOptions{Hashes: []string{normalized}})
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

func (client *Client) listTorrents(ctx context.Context, operation string, filter qbittorrent.TorrentFilterOptions) ([]TorrentInfo, error) {
	var entries []qbittorrent.Torrent
	_, err := client.run(ctx, operation, func(callCtx context.Context) error {
		var listErr error
		entries, listErr = client.library.GetTorrentsCtx(callCtx, filter)
		return listErr
	})
	if err != nil {
		return nil, err
	}
	torrents := make([]TorrentInfo, 0, len(entries))
	for _, entry := range entries {
		torrents = append(torrents, TorrentInfo{
			Hash: entry.Hash, Name: entry.Name, SavePath: entry.SavePath, ContentPath: entry.ContentPath,
			Category: entry.Category, Tags: splitTags(entry.Tags), State: string(entry.State), Progress: entry.Progress,
			Size: entry.Size,
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
	var entries *qbittorrent.TorrentFiles
	_, err = client.run(ctx, "files of "+normalized, func(callCtx context.Context) error {
		var filesErr error
		entries, filesErr = client.library.GetFilesInformationCtx(callCtx, normalized)
		return filesErr
	})
	if err != nil {
		return nil, err
	}
	if entries == nil {
		return nil, nil
	}
	files := make([]TorrentFile, 0, len(*entries))
	for _, entry := range *entries {
		files = append(files, TorrentFile{Name: entry.Name, Size: entry.Size, Progress: float64(entry.Progress)})
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
	_, err = client.run(ctx, "export capability", func(context.Context) error {
		_, versionErr := client.library.RequiresMinVersion(exportMinimumVersion)
		return versionErr
	})
	if err != nil {
		return nil, err
	}
	var raw []byte
	_, err = client.run(ctx, "export "+normalized, func(callCtx context.Context) error {
		var exportErr error
		raw, exportErr = client.library.ExportTorrentCtx(callCtx, normalized)
		return exportErr
	})
	return raw, err
}

// Version renvoie la version de l'API web du client (« 2.9.3 »), appel qui valide aussi la connexion.
func (client *Client) Version(ctx context.Context) (string, error) {
	var version string
	_, err := client.run(ctx, "web api version", func(callCtx context.Context) error {
		var versionErr error
		version, versionErr = client.library.GetWebAPIVersionCtx(callCtx)
		return versionErr
	})
	return version, err
}
