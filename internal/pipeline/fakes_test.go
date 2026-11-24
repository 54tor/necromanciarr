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
	queries     []libraryprovider.Query
}

func (provider *fakeProvider) Search(_ context.Context, query libraryprovider.Query) ([]libraryprovider.Release, error) {
	provider.queries = append(provider.queries, query)
	if provider.failure != nil && len(provider.queries) == provider.failOnCall {
		return nil, provider.failure
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
