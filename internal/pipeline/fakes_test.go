package pipeline

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"necromanciarr/internal/arr"
	"necromanciarr/internal/libraryprovider"
	"necromanciarr/internal/store"
)

var fixedNow = time.Unix(1700000000, 0).UTC()

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	openedStore, err := store.Open(filepath.Join(t.TempDir(), "necromanciarr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = openedStore.Close() })
	return openedStore
}

type fakeArr struct {
	items []arr.Item
}

func (source fakeArr) Items(context.Context) ([]arr.Item, error) {
	return source.items, nil
}

// fakeProvider imite le fournisseur : filtre par identifiant, et par taille seulement si honourSizes.
type fakeProvider struct {
	releases    []libraryprovider.Release
	honourSizes bool
	failOnCall  int
	failure     error
	// alwaysFailTVDB fait échouer chaque recherche de cet identifiant TVDB (erreur ordinaire, l'étape continue).
	alwaysFailTVDB int
	queries        []libraryprovider.Query
}

var errFakeSearch = errors.New("fake provider: unreadable response")

func (provider *fakeProvider) Search(_ context.Context, query libraryprovider.Query) ([]libraryprovider.Release, error) {
	provider.queries = append(provider.queries, query)
	if provider.failure != nil && len(provider.queries) == provider.failOnCall {
		return nil, provider.failure
	}
	if provider.alwaysFailTVDB != 0 && query.TVDBID == provider.alwaysFailTVDB {
		return nil, errFakeSearch
	}
	var found []libraryprovider.Release
	for _, release := range provider.releases {
		sameID := (query.TMDBID != 0 && release.TMDBID == query.TMDBID) || (query.TVDBID != 0 && release.TVDBID == query.TVDBID)
		if sameID && (!provider.honourSizes || hasAnySize(release, query.Sizes)) {
			found = append(found, release)
		}
	}
	return found, nil
}

func hasAnySize(release libraryprovider.Release, sizes []int64) bool {
	for _, file := range release.Files {
		if slices.Contains(sizes, file.Size) {
			return true
		}
	}
	return false
}

var errFakeNoRule = errors.New("fake mapper: no rule")

// prefixMapper traduit la vue du client (clientPrefix) vers la nôtre (localPrefix).
type prefixMapper struct {
	localPrefix, clientPrefix string
}

func (mapper prefixMapper) Reverse(original string) (string, error) {
	rest, found := strings.CutPrefix(original, mapper.clientPrefix)
	if !found {
		return "", errFakeNoRule
	}
	return mapper.localPrefix + rest, nil
}

func release(infohash, tracker string, tmdbID int, sizes ...int64) libraryprovider.Release {
	files := make([]libraryprovider.ReleaseFile, len(sizes))
	var total int64
	for fileIndex, size := range sizes {
		files[fileIndex] = libraryprovider.ReleaseFile{Path: "Release/file" + string(rune('a'+fileIndex)) + ".mkv", Size: size}
		total += size
	}
	return libraryprovider.Release{Infohash: infohash, Name: "Release " + infohash, Tracker: tracker, TotalSize: total, Files: files, TMDBID: tmdbID}
}

func addMediaFile(t *testing.T, mediaStore *store.Store, path string, size int64, tmdbID int) {
	t.Helper()
	if err := mediaStore.UpsertMediaFile(context.Background(), store.MediaFile{Path: path, Size: size, ModTime: fixedNow, TMDBID: tmdbID}); err != nil {
		t.Fatal(err)
	}
}

func matchDeps(mediaStore *store.Store, provider *fakeProvider) Deps {
	return Deps{Store: mediaStore, Provider: provider, Now: func() time.Time { return fixedNow }}
}

func candidatesByHash(t *testing.T, mediaStore *store.Store) map[string]store.Candidate {
	t.Helper()
	candidates, err := mediaStore.ListCandidatesByState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byHash := map[string]store.Candidate{}
	for _, candidate := range candidates {
		byHash[candidate.Infohash] = candidate
	}
	return byHash
}

// lockStealingStore laisse un autre détenteur reprendre le verrou à la première prolongation, ou fait échouer toutes
// les prolongations si refreshFailure est posé.
type lockStealingStore struct {
	*store.Store
	refreshFailure error
}

var errFakeRefresh = errors.New("fake store: refresh failed")

func (stealing *lockStealingStore) RefreshRunLock(ctx context.Context, holder string, expiresAt time.Time) error {
	if stealing.refreshFailure != nil {
		return stealing.refreshFailure
	}
	if err := stealing.ReleaseRunLock(ctx, holder); err != nil {
		return err
	}
	if err := stealing.AcquireRunLock(ctx, "thief", "verify", fixedNow, fixedNow.Add(time.Hour)); err != nil {
		return err
	}
	return stealing.Store.RefreshRunLock(ctx, holder, expiresAt)
}

// blockingProvider répond au premier appel puis attend l'annulation du contexte.
type blockingProvider struct {
	firstReleases []libraryprovider.Release
	calls         int
}

func (provider *blockingProvider) Search(ctx context.Context, _ libraryprovider.Query) ([]libraryprovider.Release, error) {
	provider.calls++
	if provider.calls == 1 {
		return provider.firstReleases, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func addEpisodeFile(t *testing.T, mediaStore *store.Store, path string, size int64, tvdbID int) {
	t.Helper()
	if err := mediaStore.UpsertMediaFile(context.Background(), store.MediaFile{Path: path, Size: size, ModTime: fixedNow, TVDBID: tvdbID}); err != nil {
		t.Fatal(err)
	}
}
