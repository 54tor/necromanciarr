package doctor

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"

	"necromanciarr/internal/config"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/libraryprovider"
	"necromanciarr/internal/torrentcheck"
)

const (
	maxWalkEntries  = 2000
	probeSizeFilter = int64(1)
)

// checkLibraryRoots ne fait que lire : aucune écriture dans une racine de bibliothèque.
func checkLibraryRoots(ctx context.Context, settings config.Config) Result {
	if len(settings.LibraryRoots) == 0 {
		return Result{
			Status: Warn, Code: CodeNoLibraryRoot,
			Detail: message(i18n.KeyDoctorRootsNone), Hint: message(i18n.KeyDoctorRootsNoneHint),
		}
	}
	var collected findings
	for _, root := range settings.LibraryRoots {
		status, code, detail, hint := inspectLibraryRoot(ctx, root)
		collected.add(status, code, detail, hint)
	}
	return collected.result()
}

func inspectLibraryRoot(ctx context.Context, root string) (Status, string, i18n.Message, i18n.Message) {
	notFound := func() (Status, string, i18n.Message, i18n.Message) {
		return Fail, CodeRootNotFound, message(i18n.KeyDoctorRootNotFound, "root", root), message(i18n.KeyDoctorRootNotFoundHint)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return notFound()
	}
	info, err := os.Stat(resolvedRoot)
	switch {
	case err != nil:
		return notFound()
	case !info.IsDir():
		return Fail, CodeRootNotDir, message(i18n.KeyDoctorRootNotDirectory, "root", root), i18n.Message{}
	case !isReadable(resolvedRoot):
		return Fail, CodeRootUnreadable, message(i18n.KeyDoctorRootNotReadable, "root", root), message(i18n.KeyDoctorRootNotReadableHint)
	}
	walk := findMediaFile(ctx, resolvedRoot)
	skipped := walk.unreadable > 0
	count := strconv.Itoa(walk.unreadable)
	if walk.found {
		key := i18n.KeyDoctorRootMediaFound
		if skipped {
			key = i18n.KeyDoctorRootMediaFoundSkipped
		}
		return OK, CodeRootOK, message(key, "root", root, "count", count), i18n.Message{}
	}
	key := i18n.KeyDoctorRootNoMedia
	if skipped {
		key = i18n.KeyDoctorRootNoMediaSkipped
	}
	return Warn, CodeRootEmpty, message(key, "root", root, "entries", strconv.Itoa(walk.entries), "count", count),
		message(i18n.KeyDoctorRootNoMediaHint)
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
		// Même définition du média que Scan et Verify : doctor ne doit pas annoncer un média que Scan ignorerait.
		if isFileLike && torrentcheck.IsMediaPath(path) {
			walk.found = true
			return fs.SkipAll
		}
		return nil
	})
	return walk
}

func checkLibraryAPI(ctx context.Context, settings config.Config, deps Deps) Result {
	if settings.LibraryAPI == "" {
		return Result{
			Status: Skipped, Code: CodeLibraryAPIUnset,
			Detail: message(i18n.KeyDoctorAPINotConfigured), Hint: message(i18n.KeyDoctorAPINotConfiguredHint),
		}
	}
	searcher, err := deps.NewSearcher(settings)
	if err != nil {
		return Result{
			Status: Fail, Code: CodeProviderClientFailed,
			Detail: message(i18n.KeyDoctorAPIClientFailed, "error", err.Error()), Hint: message(i18n.KeyDoctorAPIClientFailedHint),
		}
	}
	probe := libraryprovider.Query{Limit: 1, MaxPages: 1}
	if _, err := searcher.Search(ctx, probe); err != nil {
		return providerFailure(err)
	}
	return checkSizeFilter(ctx, searcher)
}

// checkSizeFilter : un fournisseur qui ignore le filtre size renvoie des releases dont aucun fichier n'a cette taille.
// Les releases sans liste de fichiers ne prouvent rien : sans release exploitable, le filtre n'est pas vérifiable.
func checkSizeFilter(ctx context.Context, searcher Searcher) Result {
	releases, err := searcher.Search(ctx, libraryprovider.Query{Sizes: []int64{probeSizeFilter}, Limit: 1, MaxPages: 1})
	if err != nil {
		return providerFailure(err)
	}
	usable := 0
	for _, release := range releases {
		if len(release.Files) == 0 {
			continue
		}
		usable++
		if !hasFileOfSize(release, probeSizeFilter) {
			return Result{
				Status: Warn, Code: CodeSizeFilterIgnored,
				Detail: message(i18n.KeyDoctorSizeFilterIgnored), Hint: message(i18n.KeyDoctorSizeFilterIgnoredHint),
			}
		}
	}
	if usable == 0 {
		return Result{Status: OK, Code: CodeSizeFilterUnverifiable, Detail: message(i18n.KeyDoctorSizeFilterUnverifiable)}
	}
	return Result{Status: OK, Code: CodeOK, Detail: message(i18n.KeyDoctorAPIOK)}
}

func hasFileOfSize(release libraryprovider.Release, size int64) bool {
	return slices.ContainsFunc(release.Files, func(file libraryprovider.ReleaseFile) bool { return file.Size == size })
}

// providerFailure ne reprend que la classe de l'erreur, jamais son texte ni un corps de réponse.
func providerFailure(err error) Result {
	var rateLimited *libraryprovider.RateLimitedError
	code, detail, hint := CodeProviderRequestFailed, message(i18n.KeyDoctorProviderRequestFailed), message(i18n.KeyDoctorProviderGenericHint)
	switch {
	case errors.As(err, &rateLimited):
		code, detail = CodeProviderRateLimited, message(i18n.KeyDoctorProviderRateLimited, "retry_after", rateLimited.RetryAfter.String())
	case errors.Is(err, libraryprovider.ErrAccessDenied):
		code, detail, hint = CodeProviderAccessDenied, message(i18n.KeyDoctorProviderAccessDenied), message(i18n.KeyDoctorProviderTokenHint)
	case errors.Is(err, libraryprovider.ErrNotFound):
		code, detail, hint = CodeProviderNotFound, message(i18n.KeyDoctorProviderNotFound), message(i18n.KeyDoctorProviderPathHint)
	case errors.Is(err, libraryprovider.ErrUnavailable):
		code, detail = CodeProviderUnavailable, message(i18n.KeyDoctorProviderUnavailable)
	case errors.Is(err, libraryprovider.ErrBadResponse):
		code, detail = CodeProviderBadResponse, message(i18n.KeyDoctorProviderBadResponse)
	case errors.Is(err, context.DeadlineExceeded):
		code, detail = CodeProviderTimeout, message(i18n.KeyDoctorProviderTimeout)
	}
	return Result{Status: Fail, Code: code, Detail: detail, Hint: hint}
}
