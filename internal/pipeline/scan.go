package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"necromanciarr/internal/arr"
	"necromanciarr/internal/store"
)

// Raisons comptées par l'étape Scan.
const (
	ReasonNoExternalID           = "no_external_id"
	ReasonMissingFileRemoved     = "missing_file_removed"
	ReasonChangedFileInvalidated = "changed_file_invalidated"
	ReasonEmptyRootNotPruned     = "empty_root_not_pruned"
	ReasonArrPathOutsideLibrary  = "arr_path_outside_library"
	ReasonArrPathsMatchedNothing = "arr_paths_matched_nothing"
	ReasonConflictingIDs         = "conflicting_ids"
)

// maxUnidentifiedSamples borne la liste des noms de fichiers sans identifiant renvoyée dans le Result.
const maxUnidentifiedSamples = 10

type externalIDs struct {
	tmdbID, tvdbID int
}

// runScan inventorie les racines, invalide tout candidat d'un fichier changé ou disparu, puis reporte les
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
	idsByPath, keepKnownIDs, err := collectArrIDs(ctx, deps, &result, current)
	if err != nil {
		return result, err
	}
	return result, joinExternalIDs(ctx, deps, &result, current, idsByPath, keepKnownIDs)
}

// refreshInventory invalide tout candidat d'un fichier changé depuis le dernier inventaire, retire les fichiers
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

// collectArrIDs indexe les identifiants des instances par chemin, traduit de la vue de l'instance vers la nôtre ; deux
// instances en désaccord gardent la première valeur vue (raison conflicting_ids). keepKnownIDs est vrai si une instance
// a renvoyé des fichiers sans qu'aucun chemin ne corresponde à la bibliothèque alors que des fichiers avaient déjà des
// identifiants : règle de chemins fausse ou montage absent, les effacer serait une perte.
func collectArrIDs(ctx context.Context, deps Deps, result *Result, files []store.MediaFile) (idsByPath map[string]store.MediaMetadata, keepKnownIDs bool, err error) {
	libraryPaths := map[string]bool{}
	anyIdentified := false
	for _, file := range files {
		libraryPaths[normalizePath(file.Path)] = true
		anyIdentified = anyIdentified || file.TMDBID != 0 || file.TVDBID != 0
	}
	idsByPath = map[string]store.MediaMetadata{}
	for sourceIndex, source := range deps.ArrSources {
		items, err := source.Items(ctx)
		if err != nil {
			return nil, false, fmt.Errorf("arr source %d: %w", sourceIndex, err)
		}
		matchedItems := 0
		for _, item := range items {
			localPath := normalizePath(deps.localPath(item.Path))
			if libraryPaths[localPath] {
				matchedItems++
			}
			mergeMetadata(result, idsByPath, localPath, item)
		}
		if len(items) > 0 && matchedItems == 0 && anyIdentified {
			result.count(ReasonArrPathsMatchedNothing, 1)
			keepKnownIDs = true
		}
	}
	return idsByPath, keepKnownIDs, nil
}

// mergeMetadata garde, champ par champ, la première valeur non vide vue pour le chemin.
func mergeMetadata(result *Result, metadataByPath map[string]store.MediaMetadata, localPath string, item arr.Item) {
	known := metadataByPath[localPath]
	merged := known
	if known.TMDBID == 0 {
		merged.TMDBID = item.TMDBID
	}
	if known.TVDBID == 0 {
		merged.TVDBID = item.TVDBID
	}
	if known.IMDBID == "" {
		merged.IMDBID = item.IMDBID
	}
	if known.Title == "" {
		merged.Title, merged.Year = item.Title, item.Year
	}
	if known.Season == 0 && known.Episode == 0 {
		merged.Season, merged.Episode = item.Season, item.Episode
	}
	tmdbConflict := known.TMDBID != 0 && item.TMDBID != 0 && known.TMDBID != item.TMDBID
	tvdbConflict := known.TVDBID != 0 && item.TVDBID != 0 && known.TVDBID != item.TVDBID
	if tmdbConflict || tvdbConflict {
		result.count(ReasonConflictingIDs, 1)
	}
	metadataByPath[localPath] = merged
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

// joinExternalIDs reporte les identifiants sur les fichiers ; sans instance configurée, ou si keepKnownIDs, un
// fichier absent des instances garde ses identifiants.
func joinExternalIDs(ctx context.Context, deps Deps, result *Result, files []store.MediaFile, idsByPath map[string]store.MediaMetadata, keepKnownIDs bool) error {
	matchedPaths := 0
	for _, file := range files {
		metadata, found := idsByPath[normalizePath(file.Path)]
		if found {
			matchedPaths++
		}
		mayClear := len(deps.ArrSources) > 0 && !keepKnownIDs
		if (found || mayClear) && metadata != metadataOf(file) {
			if err := deps.Store.SetMediaFileMetadata(ctx, file.ID, metadata); err != nil {
				return err
			}
			file.TMDBID, file.TVDBID = metadata.TMDBID, metadata.TVDBID
		}
		result.Processed++
		if file.TMDBID == 0 && file.TVDBID == 0 {
			recordUnidentified(result, file)
		}
	}
	result.count(ReasonArrPathOutsideLibrary, len(idsByPath)-matchedPaths)
	return nil
}

func metadataOf(file store.MediaFile) store.MediaMetadata {
	return store.MediaMetadata{
		TMDBID: file.TMDBID, TVDBID: file.TVDBID, IMDBID: file.IMDBID, Title: file.Title,
		Year: file.Year, Season: file.Season, Episode: file.Episode,
	}
}

func recordUnidentified(result *Result, file store.MediaFile) {
	result.UnidentifiedFiles++
	result.Skipped++
	result.count(ReasonNoExternalID, 1)
	if len(result.UnidentifiedSamples) < maxUnidentifiedSamples {
		result.UnidentifiedSamples = append(result.UnidentifiedSamples, filepath.Base(file.Path))
	}
}
