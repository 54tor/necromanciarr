package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"necromanciarr/internal/store"
)

// Raisons comptées par l'étape Scan.
const (
	ReasonNoExternalID           = "no_external_id"
	ReasonMissingFileRemoved     = "missing_file_removed"
	ReasonChangedFileInvalidated = "changed_file_invalidated"
	ReasonEmptyRootNotPruned     = "empty_root_not_pruned"
	ReasonArrPathOutsideLibrary  = "arr_path_outside_library"
)

// maxUnidentifiedSamples borne la liste des noms de fichiers sans identifiant renvoyée dans le Result.
const maxUnidentifiedSamples = 10

type externalIDs struct {
	tmdbID, tvdbID int
}

// runScan inventorie les racines, invalide les candidats des fichiers changés ou disparus, puis reporte les
// identifiants TMDB/TVDB de Radarr et Sonarr sur les fichiers de même chemin.
func runScan(ctx context.Context, deps Deps) (Result, error) {
	var result Result
	before, err := deps.Store.ListMediaFiles(ctx)
	if err != nil {
		return result, err
	}
	for rootIndex, root := range deps.LibraryRoots {
		if err := deps.ScanRoot(ctx, root); err != nil {
			return result, fmt.Errorf("scan library root %d: %w", rootIndex, err)
		}
	}
	current, err := refreshInventory(ctx, deps, &result, before)
	if err != nil {
		return result, err
	}
	idsByPath, err := collectArrIDs(ctx, deps)
	if err != nil {
		return result, err
	}
	return result, joinExternalIDs(ctx, deps, &result, current, idsByPath)
}

// refreshInventory invalide les candidats des fichiers changés depuis le dernier inventaire, retire les fichiers
// disparus et renvoie l'inventaire à jour.
func refreshInventory(ctx context.Context, deps Deps, result *Result, before []store.MediaFile) ([]store.MediaFile, error) {
	after, err := deps.Store.ListMediaFiles(ctx)
	if err != nil {
		return nil, err
	}
	if err := invalidateChangedFiles(ctx, deps, result, before, after); err != nil {
		return nil, err
	}
	removedIDs, err := removeVanishedFiles(ctx, deps, result, after)
	if err != nil {
		return nil, err
	}
	var current []store.MediaFile
	for _, file := range after {
		if !removedIDs[file.ID] {
			current = append(current, file)
		}
	}
	return current, nil
}

func invalidateChangedFiles(ctx context.Context, deps Deps, result *Result, before, after []store.MediaFile) error {
	previousByPath := map[string]store.MediaFile{}
	for _, file := range before {
		previousByPath[file.Path] = file
	}
	for _, file := range after {
		previous, known := previousByPath[file.Path]
		changed := known && (previous.Size != file.Size || !previous.ModTime.Equal(file.ModTime))
		if !changed {
			continue
		}
		invalidated, err := deps.Store.InvalidateCandidatesForFile(ctx, file.ID, deps.now())
		if err != nil {
			return err
		}
		result.count(ReasonChangedFileInvalidated, invalidated)
	}
	return nil
}

// removeVanishedFiles retire les fichiers absents du disque, racine par racine ; une racine dont tous les fichiers
// semblent absents (montage décroché, dossier vide) n'est pas élaguée.
func removeVanishedFiles(ctx context.Context, deps Deps, result *Result, files []store.MediaFile) (map[int64]bool, error) {
	removedIDs := map[int64]bool{}
	for _, root := range deps.LibraryRoots {
		underRoot := filesUnder(files, root)
		vanished := vanishedFiles(underRoot)
		if len(vanished) == 0 {
			continue
		}
		if len(vanished) == len(underRoot) {
			result.count(ReasonEmptyRootNotPruned, 1)
			continue
		}
		for _, file := range vanished {
			if err := deps.Store.DeleteMediaFile(ctx, file.ID, deps.now()); err != nil {
				return nil, err
			}
			removedIDs[file.ID] = true
			result.count(ReasonMissingFileRemoved, 1)
		}
	}
	return removedIDs, nil
}

func filesUnder(files []store.MediaFile, root string) []store.MediaFile {
	rootPrefix := strings.TrimSuffix(filepath.Clean(root), string(filepath.Separator)) + string(filepath.Separator)
	var underRoot []store.MediaFile
	for _, file := range files {
		if strings.HasPrefix(file.Path, rootPrefix) {
			underRoot = append(underRoot, file)
		}
	}
	return underRoot
}

// vanishedFiles ne retient que les fichiers dont l'absence est certaine ; toute autre erreur de lecture les garde.
func vanishedFiles(files []store.MediaFile) []store.MediaFile {
	var vanished []store.MediaFile
	for _, file := range files {
		if _, err := os.Lstat(file.Path); errors.Is(err, fs.ErrNotExist) {
			vanished = append(vanished, file)
		}
	}
	return vanished
}

// collectArrIDs indexe les identifiants des instances par chemin, traduit de la vue de l'instance vers la nôtre.
func collectArrIDs(ctx context.Context, deps Deps) (map[string]externalIDs, error) {
	idsByPath := map[string]externalIDs{}
	for sourceIndex, source := range deps.ArrSources {
		items, err := source.Items(ctx)
		if err != nil {
			return nil, fmt.Errorf("arr source %d: %w", sourceIndex, err)
		}
		for _, item := range items {
			localPath := normalizePath(deps.localPath(item.Path))
			known := idsByPath[localPath]
			idsByPath[localPath] = externalIDs{tmdbID: max(known.tmdbID, item.TMDBID), tvdbID: max(known.tvdbID, item.TVDBID)}
		}
	}
	return idsByPath, nil
}

// localPath traduit un chemin externe ; sans règle applicable, le service voit le même chemin que nous.
func (deps Deps) localPath(externalPath string) string {
	if deps.Mapper == nil {
		return externalPath
	}
	translated, err := deps.Mapper.Reverse(externalPath)
	if err != nil {
		return externalPath
	}
	return translated
}

func normalizePath(original string) string {
	return filepath.Clean(filepath.FromSlash(original))
}

// joinExternalIDs reporte les identifiants sur les fichiers ; sans instance configurée, les identifiants connus restent.
func joinExternalIDs(ctx context.Context, deps Deps, result *Result, files []store.MediaFile, idsByPath map[string]externalIDs) error {
	matchedPaths := 0
	for _, file := range files {
		ids, found := idsByPath[normalizePath(file.Path)]
		if found {
			matchedPaths++
		}
		if len(deps.ArrSources) > 0 && (ids.tmdbID != file.TMDBID || ids.tvdbID != file.TVDBID) {
			if err := deps.Store.SetMediaFileIDs(ctx, file.ID, ids.tmdbID, ids.tvdbID); err != nil {
				return err
			}
			file.TMDBID, file.TVDBID = ids.tmdbID, ids.tvdbID
		}
		result.Processed++
		if file.TMDBID == 0 && file.TVDBID == 0 {
			recordUnidentified(result, file)
		}
	}
	result.count(ReasonArrPathOutsideLibrary, len(idsByPath)-matchedPaths)
	return nil
}

func recordUnidentified(result *Result, file store.MediaFile) {
	result.UnidentifiedFiles++
	result.Skipped++
	result.count(ReasonNoExternalID, 1)
	if len(result.UnidentifiedSamples) < maxUnidentifiedSamples {
		result.UnidentifiedSamples = append(result.UnidentifiedSamples, filepath.Base(file.Path))
	}
}
