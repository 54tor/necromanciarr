package qbit

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"
)

// defaultRecheckTimeout couvre le recontrôle d'un gros torrent sur disque lent ; au-delà, le torrent reste en pause.
const defaultRecheckTimeout = time.Hour

// Seuls ces états, avec un avancement de 1, autorisent la reprise après recontrôle.
var completeStates = []string{"pausedUP", "stoppedUP", "uploading", "stalledUP", "queuedUP", "forcedUP"}

// RecheckResult est l'issue d'un recontrôle gardé : Resumed n'est vrai que si Complete l'est.
type RecheckResult struct {
	Progress float64
	Complete bool
	Resumed  bool
}

// RecheckAndGuard met le torrent en pause, le recontrôle puis ne le reprend que s'il est complet. Après un
// remplacement par lien, le client pointe vers un fichier de la bibliothèque : reprendre un torrent incomplet y
// téléchargerait des pièces. Tout autre cas (incomplet, délai dépassé, contexte annulé) le laisse en pause.
func (client *Client) RecheckAndGuard(ctx context.Context, infoHash string, timeout time.Duration) (RecheckResult, error) {
	if err := client.refuseIfReadOnly(); err != nil {
		return RecheckResult{}, err
	}
	normalized, err := validateHash(infoHash)
	if err != nil {
		return RecheckResult{}, err
	}
	if err := client.Pause(ctx, normalized); err != nil {
		return RecheckResult{}, fmt.Errorf("qbit: pause before recheck of %s: %w", normalized, err)
	}
	if err := client.Recheck(ctx, normalized); err != nil {
		return RecheckResult{}, fmt.Errorf("qbit: recheck of %s: %w", normalized, err)
	}
	torrent, err := client.waitForCheckEnd(ctx, normalized, timeout)
	result := RecheckResult{Progress: torrent.Progress}
	if err != nil {
		return result, err
	}
	result.Complete = isComplete(torrent)
	if !result.Complete {
		return result, nil
	}
	if err := client.Resume(ctx, normalized); err != nil {
		return result, fmt.Errorf("qbit: resume of %s after recheck: %w", normalized, err)
	}
	result.Resumed = true
	return result, nil
}

func isComplete(torrent TorrentInfo) bool {
	return torrent.Progress == 1 && slices.Contains(completeStates, torrent.State)
}

func isChecking(state string) bool {
	return strings.HasPrefix(state, "checking") || state == "queuedForChecking"
}

// waitForCheckEnd n'accepte un état final qu'après avoir vu le recontrôle en cours : juste après la demande,
// l'ancien état (complet) est encore affiché et ferait reprendre à tort.
func (client *Client) waitForCheckEnd(ctx context.Context, infoHash string, timeout time.Duration) (TorrentInfo, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(client.pollInterval)
	defer ticker.Stop()
	checkingSeen := false
	for {
		torrent, err := client.torrent(ctx, infoHash)
		if err != nil {
			return TorrentInfo{}, err
		}
		if isChecking(torrent.State) {
			checkingSeen = true
		} else if checkingSeen {
			return torrent, nil
		}
		select {
		case <-ctx.Done():
			return torrent, ctx.Err()
		case <-deadline.C:
			return torrent, fmt.Errorf("%w: %s", ErrRecheckTimeout, infoHash)
		case <-ticker.C:
		}
	}
}

// RecheckPath satisfait dedupe.Rechecker : chaque torrent dont un fichier est path est recontrôlé avec
// RecheckAndGuard ; un torrent incomplet reste en pause et donne ErrIncompleteAfterRecheck avec son hash.
// path est comparé tel quel aux chemins vus par qBittorrent (save_path + nom de fichier).
func (client *Client) RecheckPath(ctx context.Context, filePath string) error {
	if err := client.refuseIfReadOnly(); err != nil {
		return err
	}
	hashes, err := client.torrentsCovering(ctx, filePath)
	if err != nil {
		return err
	}
	if len(hashes) == 0 {
		return fmt.Errorf("%w: no torrent covers the path", ErrNotFound)
	}
	var failures []error
	for _, hash := range hashes {
		result, err := client.RecheckAndGuard(ctx, hash, defaultRecheckTimeout)
		switch {
		case err != nil:
			failures = append(failures, fmt.Errorf("qbit: torrent %s: %w", hash, err))
		case !result.Complete:
			failures = append(failures, fmt.Errorf("%w: %s (progress %.4f)", ErrIncompleteAfterRecheck, hash, result.Progress))
		}
	}
	return errors.Join(failures...)
}

func (client *Client) torrentsCovering(ctx context.Context, filePath string) ([]string, error) {
	target := normalizePath(filePath)
	torrents, err := client.Torrents(ctx)
	if err != nil {
		return nil, err
	}
	var hashes []string
	for _, torrent := range torrents {
		savePath := normalizePath(torrent.SavePath)
		if !strings.HasPrefix(target, strings.TrimSuffix(savePath, "/")+"/") {
			continue
		}
		covers, err := client.torrentHasFile(ctx, torrent.Hash, savePath, target)
		if err != nil {
			return nil, err
		}
		if covers {
			hashes = append(hashes, torrent.Hash)
		}
	}
	return hashes, nil
}

func (client *Client) torrentHasFile(ctx context.Context, infoHash, savePath, target string) (bool, error) {
	files, err := client.Files(ctx, infoHash)
	if err != nil {
		return false, err
	}
	for _, file := range files {
		if path.Join(savePath, normalizePath(file.Name)) == target {
			return true, nil
		}
	}
	return false, nil
}

// normalizePath compare en séparateurs « / » : qBittorrent peut renvoyer des chemins Windows.
func normalizePath(rawPath string) string {
	return path.Clean(strings.ReplaceAll(rawPath, `\`, "/"))
}
