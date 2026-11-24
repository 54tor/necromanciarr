package qbit

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"
	"time"
)

const (
	// defaultRecheckTimeout couvre le recontrôle d'un gros torrent sur disque lent ; au-delà, le torrent reste en pause.
	defaultRecheckTimeout = time.Hour
	// defaultCheckStartTimeout : sans état checking dans ce délai, le recontrôle est tenu pour non lancé.
	defaultCheckStartTimeout = 30 * time.Second
	// safetyPauseTimeout borne la remise en pause faite même quand le contexte de l'appelant est annulé.
	safetyPauseTimeout = 30 * time.Second
)

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
// téléchargerait des pièces. Incomplet, il est remis en pause (qBittorrent peut l'avoir relancé) ; délai dépassé,
// contexte annulé ou erreur : il est aussi remis en pause.
func (client *Client) RecheckAndGuard(ctx context.Context, infoHash string, timeout time.Duration) (RecheckResult, error) {
	if err := client.refuseIfReadOnly(); err != nil {
		return RecheckResult{}, err
	}
	normalized, err := validateHash(infoHash)
	if err != nil {
		return RecheckResult{}, err
	}
	results, err := client.guardHashes(ctx, []string{normalized}, []bool{true}, timeout)
	if len(results) == 0 {
		return RecheckResult{}, err
	}
	return results[0], err
}

// guardHashes met d'abord tous les torrents en pause, puis les recontrôle un à un, puis ne reprend que ceux à 100 %
// dont resumable est vrai (même index). Une erreur (pause, recontrôle, délai) laisse tous les torrents en pause : leur
// état est alors inconnu.
func (client *Client) guardHashes(ctx context.Context, hashes []string, resumable []bool, timeout time.Duration) ([]RecheckResult, error) {
	for _, hash := range hashes {
		if err := client.Pause(ctx, hash); err != nil {
			return nil, client.pauseAllAfter(ctx, hashes, fmt.Errorf("qbit: pause before recheck of %s: %w", hash, err))
		}
	}
	results := make([]RecheckResult, len(hashes))
	for hashIndex, hash := range hashes {
		torrent, err := client.recheckAndWait(ctx, hash, timeout)
		results[hashIndex].Progress = torrent.Progress
		if err != nil {
			return results, client.pauseAllAfter(ctx, hashes, err)
		}
		results[hashIndex].Complete = isComplete(torrent)
	}
	return results, client.releaseChecked(ctx, hashes, resumable, results)
}

// releaseChecked reprend les torrents à 100 % qui peuvent l'être et remet en pause les autres (pause idempotente) :
// un torrent que l'utilisateur avait laissé en pause le reste.
func (client *Client) releaseChecked(ctx context.Context, hashes []string, resumable []bool, results []RecheckResult) error {
	var failures []error
	for hashIndex, hash := range hashes {
		if !results[hashIndex].Complete || !resumable[hashIndex] {
			if err := client.safetyPause(ctx, hash); err != nil {
				failures = append(failures, fmt.Errorf("qbit: pause of %s after recheck failed: %w", hash, err))
			}
			continue
		}
		if err := client.Resume(ctx, hash); err != nil {
			failures = append(failures, fmt.Errorf("qbit: resume of %s after recheck: %w", hash, err))
			continue
		}
		results[hashIndex].Resumed = true
	}
	return errors.Join(failures...)
}

// pauseAllAfter remet tout en pause après une erreur et joint les échecs de pause à la cause.
func (client *Client) pauseAllAfter(ctx context.Context, hashes []string, cause error) error {
	failures := []error{cause}
	for _, hash := range hashes {
		if err := client.safetyPause(ctx, hash); err != nil {
			failures = append(failures, fmt.Errorf("qbit: safety pause of %s failed: %w", hash, err))
		}
	}
	return errors.Join(failures...)
}

// safetyPause survit à l'annulation du contexte de l'appelant : laisser un torrent tourner serait pire.
func (client *Client) safetyPause(ctx context.Context, hash string) error {
	pauseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), safetyPauseTimeout)
	defer cancel()
	return client.Pause(pauseCtx, hash)
}

func (client *Client) recheckAndWait(ctx context.Context, hash string, timeout time.Duration) (TorrentInfo, error) {
	if err := client.Recheck(ctx, hash); err != nil {
		return TorrentInfo{}, fmt.Errorf("qbit: recheck of %s: %w", hash, err)
	}
	return client.waitForCheckEnd(ctx, hash, timeout)
}

func isComplete(torrent TorrentInfo) bool {
	return torrent.Progress == 1 && slices.Contains(completeStates, torrent.State)
}

func isChecking(state string) bool {
	return strings.HasPrefix(state, "checking") || state == "queuedForChecking"
}

// waitForCheckEnd n'accepte un état final qu'après avoir vu le recontrôle en cours : juste après la demande,
// l'ancien état (complet) est encore affiché et ferait reprendre à tort.
func (client *Client) waitForCheckEnd(ctx context.Context, hash string, timeout time.Duration) (TorrentInfo, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	startDeadline := time.NewTimer(client.checkStartTimeout)
	defer startDeadline.Stop()
	ticker := time.NewTicker(client.pollInterval)
	defer ticker.Stop()
	checkingSeen := false
	for {
		torrent, err := client.torrent(ctx, hash)
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
			return torrent, fmt.Errorf("%w: %s", ErrRecheckTimeout, hash)
		case <-startDeadline.C:
			if !checkingSeen {
				return torrent, fmt.Errorf("%w: %s: checking never started", ErrRecheckTimeout, hash)
			}
		case <-ticker.C:
		}
	}
}

// RecheckPath satisfait dedupe.Rechecker : tous les torrents dont un fichier est path sont mis en pause, puis
// recontrôlés ; un torrent incomplet reste en pause et donne ErrIncompleteAfterRecheck avec son hash. Seul un torrent
// complet qui tournait avant la garde est repris : celui que l'utilisateur avait mis en pause ou arrêté le reste.
// path est comparé tel quel aux chemins vus par qBittorrent (save_path + nom de fichier).
func (client *Client) RecheckPath(ctx context.Context, filePath string) error {
	if err := client.refuseIfReadOnly(); err != nil {
		return err
	}
	covering, err := client.torrentsCovering(ctx, filePath)
	if err != nil {
		return err
	}
	if len(covering) == 0 {
		return fmt.Errorf("%w: no torrent covers the path", ErrNotFound)
	}
	hashes := make([]string, 0, len(covering))
	wasRunning := make([]bool, 0, len(covering))
	for _, match := range covering {
		hashes = append(hashes, match.hash)
		wasRunning = append(wasRunning, isRunning(match.state))
	}
	results, err := client.guardHashes(ctx, hashes, wasRunning, client.recheckTimeout)
	if err != nil {
		return err
	}
	var failures []error
	for hashIndex, result := range results {
		if !result.Complete {
			failures = append(failures, fmt.Errorf("%w: %s (progress %.4f)", ErrIncompleteAfterRecheck, hashes[hashIndex], result.Progress))
		}
	}
	return errors.Join(failures...)
}

// coveringTorrent est un torrent du client qui contient le fichier ; relativeName est son nom sous save_path, state
// son état lu avant toute pause.
type coveringTorrent struct {
	hash         string
	relativeName string
	state        string
}

// notRunningStates : un torrent dans l'un de ces états n'était pas actif avant la garde et ne doit pas être repris.
var notRunningStates = []string{"error", "missingFiles", "unknown"}

// isRunning dit si un torrent tournait (téléchargement, partage, file d'attente, contrôle) avant la garde.
func isRunning(state string) bool {
	isStopped := strings.HasPrefix(state, "paused") || strings.HasPrefix(state, "stopped")
	return !isStopped && !slices.Contains(notRunningStates, state)
}

// torrentsCovering trie par hash pour un ordre stable ; un torrent disparu entre la liste et ses fichiers est
// ignoré, toute autre erreur arrête tout avant la moindre pause.
func (client *Client) torrentsCovering(ctx context.Context, filePath string) ([]coveringTorrent, error) {
	target := normalizePath(filePath)
	torrents, err := client.Torrents(ctx)
	if err != nil {
		return nil, err
	}
	var covering []coveringTorrent
	for _, torrent := range torrents {
		relativeName, isUnderSavePath := strings.CutPrefix(target, strings.TrimSuffix(normalizePath(torrent.SavePath), "/")+"/")
		if !isUnderSavePath {
			continue
		}
		hasFile, err := client.torrentHasFile(ctx, torrent.Hash, relativeName)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if hasFile {
			covering = append(covering, coveringTorrent{hash: strings.ToLower(torrent.Hash), relativeName: relativeName, state: torrent.State})
		}
	}
	sort.Slice(covering, func(left, right int) bool { return covering[left].hash < covering[right].hash })
	return covering, nil
}

func (client *Client) torrentHasFile(ctx context.Context, infoHash, relativeName string) (bool, error) {
	files, err := client.Files(ctx, infoHash)
	if err != nil {
		return false, err
	}
	for _, file := range files {
		if normalizePath(file.Name) == relativeName {
			return true, nil
		}
	}
	return false, nil
}

// normalizePath compare en séparateurs « / » : qBittorrent peut renvoyer des chemins Windows.
func normalizePath(rawPath string) string {
	return path.Clean(strings.ReplaceAll(rawPath, `\`, "/"))
}
