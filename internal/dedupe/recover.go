package dedupe

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrTorrentFilePresent : le fichier que le lien temporaire devait remplacer est toujours là, le lien est retiré.
var ErrTorrentFilePresent = errors.New("dedupe: torrent file still present")

// RecoverAction rend compte d'une entrée temporaire laissée par un Apply interrompu : Completed dit que le renommage
// a été terminé ; sinon l'entrée a été retirée pour Reason. Err signale l'échec de l'action elle-même.
type RecoverAction struct {
	TemporaryPath string
	TorrentPath   string
	Completed     bool
	Reason        error
	Err           error
}

// RecoverResult liste les actions de Recover dans l'ordre du parcours.
type RecoverResult struct {
	Actions []RecoverAction
}

// Recover reprend les entrées « *.necromanciarr-tmp-* » laissées sous torrentRoots par un Apply interrompu (un VFS
// peut supprimer la destination avant de renommer). Si le fichier torrent manque et que l'entrée mène à un fichier de
// la taille attendue, le renommage est terminé ; sinon seule l'entrée temporaire est retirée. Les dossiers symboliques
// ne sont pas suivis et les fichiers de la bibliothèque ne sont jamais touchés. À lancer avant Plan, jamais pendant
// un Apply.
func Recover(ctx context.Context, torrentRoots []string) (RecoverResult, error) {
	roots, err := resolveRoots(torrentRoots)
	if err != nil {
		return RecoverResult{}, err
	}
	var result RecoverResult
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if !entry.IsDir() && strings.Contains(entry.Name(), temporaryMarker) {
				result.Actions = append(result.Actions, recoverEntry(path))
			}
			return nil
		})
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func recoverEntry(temporaryPath string) RecoverAction {
	name := filepath.Base(temporaryPath)
	markerIndex := strings.LastIndex(name, temporaryMarker)
	torrentPath := filepath.Join(filepath.Dir(temporaryPath), name[:markerIndex])
	action := RecoverAction{TemporaryPath: temporaryPath, TorrentPath: torrentPath}
	_, err := os.Lstat(torrentPath)
	switch {
	case markerIndex == 0:
		action.Reason = fmt.Errorf("%w: no torrent file name in %s", ErrNotIdentical, name)
	case err == nil:
		action.Reason = ErrTorrentFilePresent
	case !errors.Is(err, fs.ErrNotExist):
		action.Err = err
		return action
	default:
		action.Reason = checkRecoverable(temporaryPath, name[markerIndex+len(temporaryMarker):])
	}
	if action.Reason != nil {
		action.Err = removeTemporary(temporaryPath)
		return action
	}
	action.Err = os.Rename(temporaryPath, torrentPath)
	action.Completed = action.Err == nil
	return action
}

// checkRecoverable exige que l'entrée mène à un fichier régulier de la taille inscrite dans son nom.
func checkRecoverable(temporaryPath, suffix string) error {
	sizeText, _, _ := strings.Cut(suffix, temporarySeparator)
	expectedSize, err := strconv.ParseInt(sizeText, 10, 64)
	if err != nil {
		return fmt.Errorf("%w: no expected size in %s", ErrNotIdentical, temporaryPath)
	}
	info, err := os.Stat(temporaryPath)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNotIdentical, err)
	}
	if !info.Mode().IsRegular() || info.Size() != expectedSize {
		return fmt.Errorf("%w: %s does not resolve to a %d-byte file", ErrNotIdentical, temporaryPath, expectedSize)
	}
	return nil
}
