package doctor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"necromanciarr/internal/config"
	"necromanciarr/internal/libraryprovider"
)

const (
	maxWalkEntries  = 2000
	probeSizeFilter = int64(1)
)

var mediaExtensions = []string{".mkv", ".mp4", ".avi", ".m4v", ".mov", ".wmv", ".ts", ".m2ts", ".mpg", ".mpeg", ".webm", ".flac", ".mp3"}

// checkLibraryRoots ne fait que lire : aucune écriture dans une racine de bibliothèque.
func checkLibraryRoots(ctx context.Context, settings config.Config) Result {
	if len(settings.LibraryRoots) == 0 {
		return Result{Status: Warn, Detail: "no library root configured", Hint: "set libraryRoots in config.json"}
	}
	var collected findings
	for _, root := range settings.LibraryRoots {
		collected.add(inspectLibraryRoot(ctx, root))
	}
	return collected.result()
}

func inspectLibraryRoot(ctx context.Context, root string) (Status, string, string) {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return Fail, root + ": not found or not accessible", "check the path and the mount"
	}
	info, err := os.Stat(resolvedRoot)
	switch {
	case err != nil:
		return Fail, root + ": not found or not accessible", "check the path and the mount"
	case !info.IsDir():
		return Fail, root + ": not a directory", ""
	case !isReadable(resolvedRoot):
		return Fail, root + ": not readable", "check permissions of the user running necromanciarr"
	}
	walk := findMediaFile(ctx, resolvedRoot)
	note := ""
	if walk.unreadable > 0 {
		note = fmt.Sprintf(" (%d unreadable subdirectories skipped)", walk.unreadable)
	}
	if walk.found {
		return OK, root + ": readable, media file found" + note, ""
	}
	return Warn, fmt.Sprintf("%s: no media file in the first %d entries%s", root, walk.entries, note),
		"the root may be empty or not mounted yet"
}

func isReadable(directory string) bool {
	opened, err := os.Open(directory) // #nosec G304 -- racine configurée par l'opérateur, ouverte en lecture seule
	if err != nil {
		return false
	}
	defer func() { _ = opened.Close() }()
	_, err = opened.Readdirnames(1)
	return err == nil || errors.Is(err, io.EOF)
}

type mediaWalk struct {
	found      bool
	entries    int
	unreadable int
}

// findMediaFile parcourt au plus maxWalkEntries entrées sans ouvrir aucun fichier ; un lien symbolique dont
// l'extension est média compte, un sous-dossier illisible est sauté et dénombré.
func findMediaFile(ctx context.Context, root string) mediaWalk {
	var walk mediaWalk
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		walk.entries++
		if ctx.Err() != nil || walk.entries > maxWalkEntries {
			return fs.SkipAll
		}
		if err != nil {
			walk.unreadable++
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		isFileLike := entry.Type().IsRegular() || entry.Type()&fs.ModeSymlink != 0
		if isFileLike && slices.Contains(mediaExtensions, strings.ToLower(filepath.Ext(path))) {
			walk.found = true
			return fs.SkipAll
		}
		return nil
	})
	return walk
}

func checkLibraryAPI(ctx context.Context, settings config.Config, deps Deps) Result {
	if settings.LibraryAPI == "" {
		return Result{Status: Skipped, Detail: "libraryApi is not configured", Hint: "set LIBRARY_API to enable release lookups"}
	}
	searcher, err := deps.NewSearcher(settings)
	if err != nil {
		return Result{
			Status: Fail, Detail: "cannot build the library provider client: " + err.Error(),
			Hint: "check libraryApi and LIBRARY_PROVIDER_TOKEN",
		}
	}
	probe := libraryprovider.Query{Limit: 1, MaxPages: 1}
	if _, err := searcher.Search(ctx, probe); err != nil {
		return Result{Status: Fail, Detail: "search failed: " + describeProviderError(err), Hint: hintForProviderError(err)}
	}
	return checkSizeFilter(ctx, searcher)
}

// checkSizeFilter : un fournisseur qui ignore le filtre size renvoie des releases dont aucun fichier n'a cette taille.
// Les releases sans liste de fichiers ne prouvent rien : sans release exploitable, le filtre n'est pas vérifiable.
func checkSizeFilter(ctx context.Context, searcher Searcher) Result {
	releases, err := searcher.Search(ctx, libraryprovider.Query{Sizes: []int64{probeSizeFilter}, Limit: 1, MaxPages: 1})
	if err != nil {
		return Result{Status: Fail, Detail: "size filter search failed: " + describeProviderError(err), Hint: hintForProviderError(err)}
	}
	usable := 0
	for _, release := range releases {
		if len(release.Files) == 0 {
			continue
		}
		usable++
		if !hasFileOfSize(release, probeSizeFilter) {
			return Result{
				Status: Warn, Detail: "authentication accepted, but the provider ignores the size filter",
				Hint: "matching by size will download far more candidates than needed; update the provider",
			}
		}
	}
	if usable == 0 {
		return Result{Status: OK, Detail: "authentication accepted, search answers; filtre size non vérifiable (aucun résultat exploitable)"}
	}
	return Result{Status: OK, Detail: "authentication accepted, search answers, size filter honored"}
}

func hasFileOfSize(release libraryprovider.Release, size int64) bool {
	return slices.ContainsFunc(release.Files, func(file libraryprovider.ReleaseFile) bool { return file.Size == size })
}

// describeProviderError ne reprend que la classe de l'erreur, jamais son texte ni un corps de réponse.
func describeProviderError(err error) string {
	var rateLimited *libraryprovider.RateLimitedError
	switch {
	case errors.As(err, &rateLimited):
		return fmt.Sprintf("rate limited (retry after %s)", rateLimited.RetryAfter)
	case errors.Is(err, libraryprovider.ErrAccessDenied):
		return "access denied"
	case errors.Is(err, libraryprovider.ErrNotFound):
		return "not found"
	case errors.Is(err, libraryprovider.ErrUnavailable):
		return "unavailable"
	case errors.Is(err, libraryprovider.ErrBadResponse):
		return "unreadable response (outside the contract)"
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	}
	return "request failed"
}

func hintForProviderError(err error) string {
	switch {
	case errors.Is(err, libraryprovider.ErrAccessDenied):
		return "check LIBRARY_PROVIDER_TOKEN"
	case errors.Is(err, libraryprovider.ErrNotFound):
		return "check the path in libraryApi"
	}
	return "check libraryApi and the provider status"
}
