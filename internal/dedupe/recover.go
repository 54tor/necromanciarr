package dedupe

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"

	"necromanciarr/internal/fsys"
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
// la taille attendue, le remplacement est terminé ; sinon seule l'entrée temporaire est retirée. Toute écriture passe
// par la racine d'écriture de la racine torrent, ouverte par registry à la première entrée trouvée (une racine qui
// recouvre la bibliothèque est refusée, fsys.ErrWriteInLibrary). Les dossiers symboliques ne sont pas suivis et
// aucun autre fichier n'est touché. À lancer avant Plan, jamais pendant un Apply.
func Recover(ctx context.Context, torrentRoots []string, registry *fsys.Registry) (RecoverResult, error) {
	files := NewFiles(registry)
	roots, err := resolveRoots(files, torrentRoots)
	if err != nil {
		return RecoverResult{}, err
	}
	var result RecoverResult
	for _, root := range roots {
		actions, err := recoverRoot(ctx, files, root)
		result.Actions = append(result.Actions, actions...)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func recoverRoot(ctx context.Context, files Files, root string) ([]RecoverAction, error) {
	var actions []RecoverAction
	var writeRoot fsys.WriteRoot
	defer func() {
		if writeRoot != nil {
			_ = writeRoot.Close()
		}
	}()
	err := files.Walk(ctx, root, func(path string, entry fs.DirEntry) error {
		if entry.IsDir() || !strings.Contains(entry.Name(), temporaryMarker) {
			return nil
		}
		if writeRoot == nil {
			opened, err := files.WriteRoot(root)
			if err != nil {
				return err
			}
			writeRoot = opened
		}
		actions = append(actions, recoverEntry(files, writeRoot, root, path))
		return nil
	})
	return actions, err
}

func recoverEntry(files Files, writeRoot fsys.WriteRoot, root, temporaryPath string) RecoverAction {
	name := Base(temporaryPath)
	markerIndex := strings.LastIndex(name, temporaryMarker)
	torrentPath := Join(Dir(temporaryPath), name[:markerIndex])
	action := RecoverAction{TemporaryPath: temporaryPath, TorrentPath: torrentPath}
	temporaryRelative, err := Below(root, temporaryPath)
	if err != nil {
		action.Err = err
		return action
	}
	_, err = files.Lstat(torrentPath)
	switch {
	case markerIndex == 0:
		action.Reason = fmt.Errorf("%w: no torrent file name in %s", ErrNotIdentical, name)
	case err == nil:
		action.Reason = ErrTorrentFilePresent
	case !errors.Is(err, fs.ErrNotExist):
		action.Err = err
		return action
	default:
		action.Reason = checkRecoverable(files, temporaryPath, name[markerIndex+len(temporaryMarker):])
	}
	if action.Reason != nil {
		action.Err = removeTemporary(writeRoot, temporaryRelative)
		return action
	}
	torrentRelative, err := Below(root, torrentPath)
	if err == nil {
		err = writeRoot.Replace(temporaryRelative, torrentRelative)
	}
	action.Err = err
	action.Completed = err == nil
	return action
}

// checkRecoverable exige que l'entrée mène à un fichier régulier de la taille inscrite dans son nom.
func checkRecoverable(files Files, temporaryPath, suffix string) error {
	sizeText, _, _ := strings.Cut(suffix, temporarySeparator)
	expectedSize, err := strconv.ParseInt(sizeText, 10, 64)
	if err != nil {
		return fmt.Errorf("%w: no expected size in %s", ErrNotIdentical, temporaryPath)
	}
	info, err := files.Stat(temporaryPath)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNotIdentical, err)
	}
	if !info.Mode().IsRegular() || info.Size() != expectedSize {
		return fmt.Errorf("%w: %s does not resolve to a %d-byte file", ErrNotIdentical, temporaryPath, expectedSize)
	}
	return nil
}
