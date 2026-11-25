package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"necromanciarr/internal/libraryprovider"
	"necromanciarr/internal/sources"
	"necromanciarr/internal/store"
	"necromanciarr/internal/torrentcheck"
	"necromanciarr/internal/torznab"
)

const (
	gibibyte           = int64(1) << 30
	indexerPieceLength = int64(64) << 20
	// sealedTracker n'a pas d'entrée trackers (le .torrent est scellé) ; coveredTrackerName en a une.
	sealedTracker = "Tracker Z"
	otherIndexer  = sources.ID("torznab:b")
)

// matchIndexer est un indexeur factice : capacités fixes, mêmes résultats pour toute recherche, .torrent par GUID.
type matchIndexer struct {
	mutex     sync.Mutex
	caps      torznab.Caps
	results   torznab.Results
	torrents  map[string][]byte
	capsCalls int
	queries   []torznab.Query
	downloads []string
}

func (indexer *matchIndexer) Caps(context.Context) (torznab.Caps, error) {
	indexer.mutex.Lock()
	defer indexer.mutex.Unlock()
	indexer.capsCalls++
	return indexer.caps, nil
}

func (indexer *matchIndexer) Search(_ context.Context, query torznab.Query) (torznab.Results, error) {
	indexer.mutex.Lock()
	defer indexer.mutex.Unlock()
	indexer.queries = append(indexer.queries, query)
	return indexer.results, nil
}

func (indexer *matchIndexer) Download(_ context.Context, item torznab.Item) ([]byte, error) {
	indexer.mutex.Lock()
	defer indexer.mutex.Unlock()
	indexer.downloads = append(indexer.downloads, item.GUID)
	return bytes.Clone(indexer.torrents[item.GUID]), nil
}

func (indexer *matchIndexer) requests() int {
	indexer.mutex.Lock()
	defer indexer.mutex.Unlock()
	return indexer.capsCalls + len(indexer.queries) + len(indexer.downloads)
}

// offer ajoute un résultat de cette taille annoncée servi par ce .torrent.
func (indexer *matchIndexer) offer(guid string, size int64, raw []byte) {
	indexer.torrents[guid] = raw
	indexer.results.Items = append(indexer.results.Items, torznab.Item{GUID: guid, Title: "Release " + guid, Size: size})
}

func indexerMovieCaps() torznab.Caps {
	return torznab.Caps{Modes: map[string][]string{"search": {"q"}, "movie": {"q", "imdbid", "tmdbid"}}, Categories: []int{2000, 5000}}
}

// indexerTorrent est un .torrent v1 valide aux fichiers de ces tailles, annonce avec passkey comme un indexeur le sert.
func indexerTorrent(t *testing.T, name string, sizes ...int64) []byte {
	t.Helper()
	info := metainfo.Info{Name: name, PieceLength: indexerPieceLength}
	var total int64
	for fileIndex, size := range sizes {
		total += size
		info.Files = append(info.Files, metainfo.FileInfo{Path: []string{fmt.Sprintf("file%d.mkv", fileIndex)}, Length: size})
	}
	info.Pieces = bytes.Repeat([]byte("A"), int((total+indexerPieceLength-1)/indexerPieceLength)*20)
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := bencode.Marshal(metainfo.MetaInfo{InfoBytes: infoBytes, Announce: "https://tracker-z.example.com/" + fallbackPasskey + "/announce"})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// matchProvider fait du faux fournisseur de Match une sources.ProviderSource.
type matchProvider struct{ *fakeProvider }

func (matchProvider) Torrent(context.Context, string, string) ([]byte, error) {
	return nil, libraryprovider.ErrNotFound
}

func (matchProvider) Nfos(context.Context, string, string) ([]libraryprovider.Nfo, error) {
	return nil, nil
}

type indexerMatchCase struct {
	t        *testing.T
	store    *store.Store
	provider *fakeProvider
	indexers map[sources.ID]*matchIndexer
	order    []sources.ID
	trackers map[sources.ID]string
	limits   sources.Limits
	sealer   *sources.Sealer
}

func newIndexerMatchCase(t *testing.T) *indexerMatchCase {
	t.Helper()
	return &indexerMatchCase{
		t: t, store: openTestStore(t), provider: &fakeProvider{}, indexers: map[sources.ID]*matchIndexer{},
		order: []sources.ID{sources.ProviderID}, trackers: map[sources.ID]string{}, sealer: testSealer(t),
		limits: sources.Limits{MaxQueriesPerRun: 30, MaxDownloadsPerRun: 10, MaxQueriesPerDay: 200, MaxDownloadsPerDay: 50},
	}
}

func (testCase *indexerMatchCase) addIndexer(identifier sources.ID, tracker string) *matchIndexer {
	indexer := &matchIndexer{caps: indexerMovieCaps(), torrents: map[string][]byte{}}
	testCase.indexers[identifier] = indexer
	testCase.order = append(testCase.order, identifier)
	testCase.trackers[identifier] = tracker
	return indexer
}

func (testCase *indexerMatchCase) addFile(path string, size int64, metadata store.MediaMetadata) store.MediaFile {
	testCase.t.Helper()
	ctx := context.Background()
	if err := testCase.store.UpsertMediaFile(ctx, store.MediaFile{Path: path, Size: size, ModTime: fixedNow}); err != nil {
		testCase.t.Fatal(err)
	}
	file := mediaFilesByPath(testCase.t, testCase.store)[path]
	if err := testCase.store.SetMediaFileMetadata(ctx, file.ID, metadata); err != nil {
		testCase.t.Fatal(err)
	}
	return file
}

func (testCase *indexerMatchCase) addMovie(path string, size int64, tmdbID int, title string) store.MediaFile {
	return testCase.addFile(path, size, store.MediaMetadata{TMDBID: tmdbID, Title: title, Year: 2001})
}

func (testCase *indexerMatchCase) run() Result {
	testCase.t.Helper()
	result, err := Run(context.Background(), StageMatch, testCase.deps())
	if err != nil {
		testCase.t.Fatalf("valeurObtenue %v, valeurAttendue a successful Match", err)
	}
	return result
}

func (testCase *indexerMatchCase) deps() Deps {
	testCase.t.Helper()
	indexers, limits := map[sources.ID]sources.IndexerSource{}, map[sources.ID]sources.Limits{}
	for identifier, indexer := range testCase.indexers {
		indexers[identifier], limits[identifier] = indexer, testCase.limits
	}
	chain, err := sources.NewChain(matchProvider{testCase.provider}, indexers, testCase.order, limits, testCase.store, testCase.sealer,
		func() time.Time { return fixedNow })
	if err != nil {
		testCase.t.Fatal(err)
	}
	chain.SetFallbackTrackers(testCase.trackers)
	return Deps{Store: testCase.store, Sources: chain, Now: func() time.Time { return fixedNow }, Announces: sources.NewResolver(testTrackers())}
}

func (testCase *indexerMatchCase) candidates() []store.Candidate {
	testCase.t.Helper()
	candidates, err := testCase.store.ListCandidatesByState(context.Background())
	if err != nil {
		testCase.t.Fatal(err)
	}
	return candidates
}

func infohashOfTorrent(t *testing.T, raw []byte) string {
	t.Helper()
	parsed, err := torrentcheck.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.InfoHash
}

func TestMatch_searchesIndexersOnlyForUncoveredGroups(t *testing.T) {
	testCase := newIndexerMatchCase(t)
	testCase.addMovie("/srv/media/covered.mkv", 3*gibibyte, 603, "Covered")
	testCase.addMovie("/srv/media/missing.mkv", 4*gibibyte, 604, "Missing")
	testCase.provider.releases = []libraryprovider.Release{release("aa11", "tracker-p", 603, 3*gibibyte)}
	indexer := testCase.addIndexer(indexerSameTracker, sealedTracker)

	result := testCase.run()

	if result.CandidatesCreated != 1 || len(indexer.queries) != 2 {
		t.Fatalf("valeurObtenue %+v queries %+v, valeurAttendue the provider candidate and two searches", result, indexer.queries)
	}
	for _, query := range indexer.queries {
		if query.TMDBID == 603 || strings.Contains(query.Text, "Covered") {
			t.Fatalf("valeurObtenue query %+v, valeurAttendue no search for the covered group", query)
		}
	}
}

func TestMatch_dropsTorznabTorrentWithoutExactSize(t *testing.T) {
	testCase := newIndexerMatchCase(t)
	size := 4 * gibibyte
	testCase.addMovie("/srv/media/missing.mkv", size, 604, "Missing")
	indexer := testCase.addIndexer(indexerSameTracker, sealedTracker)
	indexer.offer("near", size, indexerTorrent(t, "near", size+1))

	result := testCase.run()

	if result.Reasons[ReasonTorznabNoExactSize] == 0 || result.Failed != 0 || len(testCase.candidates()) != 0 {
		t.Fatalf("valeurObtenue %+v candidates %d, valeurAttendue torznab_no_exact_size and nothing stored", result, len(testCase.candidates()))
	}
}

func TestMatch_prefersIDQueryWhenCapsAllow(t *testing.T) {
	testCase := newIndexerMatchCase(t)
	size := 4 * gibibyte
	testCase.addMovie("/srv/media/missing.mkv", size, 604, "Missing")
	indexer := testCase.addIndexer(indexerSameTracker, sealedTracker)
	indexer.offer("win", size, indexerTorrent(t, "win", size))

	testCase.run()

	if len(indexer.queries) != 1 || indexer.queries[0].Mode != "movie" || indexer.queries[0].TMDBID != 604 || indexer.queries[0].Text != "" {
		t.Fatalf("valeurObtenue %+v, valeurAttendue one t=movie&tmdbid=604 query", indexer.queries)
	}
}

func TestMatch_textQueryForSeriesUsesSeasonEpisode(t *testing.T) {
	testCase := newIndexerMatchCase(t)
	testCase.addFile("/srv/media/show/e02.mkv", gibibyte, store.MediaMetadata{TVDBID: 81189, Title: "Show", Season: 1, Episode: 2})
	indexer := testCase.addIndexer(indexerSameTracker, sealedTracker)
	indexer.caps = torznab.Caps{Modes: map[string][]string{"search": {"q"}}}

	testCase.run()

	if len(indexer.queries) != 1 || indexer.queries[0].Text != "Show S01E02" {
		t.Fatalf("valeurObtenue %+v, valeurAttendue the text query Show S01E02", indexer.queries)
	}
}

func TestMatch_movieUpperSizeBound(t *testing.T) {
	testCase := newIndexerMatchCase(t)
	size := 4 * gibibyte
	testCase.addMovie("/srv/media/missing.mkv", size, 604, "Missing")
	indexer := testCase.addIndexer(indexerSameTracker, sealedTracker)
	tooBig := size + size/100 + 64<<20 + 1
	indexer.offer("too-big", tooBig, indexerTorrent(t, "big", size, tooBig-size))

	testCase.run()

	if len(indexer.downloads) != 0 {
		t.Fatalf("valeurObtenue downloads %v, valeurAttendue the oversized item dropped before download", indexer.downloads)
	}
}

func TestMatch_savesTorznabCandidateAtFetched(t *testing.T) {
	testCase := newIndexerMatchCase(t)
	size := 4 * gibibyte
	local := testCase.addMovie("/srv/media/missing.mkv", size, 604, "Missing")
	indexer := testCase.addIndexer(indexerSameTracker, sealedTracker)
	served := indexerTorrent(t, "win", size, 1000)
	indexer.offer("win", size+1000, served)

	result := testCase.run()

	candidates := testCase.candidates()
	if len(candidates) != 1 {
		t.Fatalf("valeurObtenue %d candidates, valeurAttendue one", len(candidates))
	}
	stored := candidates[0]
	if stored.State != store.CandidateFetched || stored.Source != string(indexerSameTracker) || stored.Tracker != sealedTracker ||
		stored.Infohash != infohashOfTorrent(t, served) || !stored.HasSealed || result.CandidatesCreated != 1 {
		t.Fatalf("valeurObtenue %+v (result %+v), valeurAttendue fetched from %s on %q, sealed", stored, result, indexerSameTracker, sealedTracker)
	}
	clearCopy, err := testCase.store.CandidateTorrent(context.Background(), stored.ID)
	if err != nil || len(clearCopy) == 0 || bytes.Contains(clearCopy, []byte(fallbackPasskey)) {
		t.Fatalf("valeurObtenue clear copy err %v, valeurAttendue a stored copy without passkey", err)
	}
	sealed, _, err := testCase.store.CandidateSealed(context.Background(), stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	unsealed, err := testCase.sealer.Unseal(sealed, bindingOf(stored))
	if err != nil || !bytes.Equal(unsealed, served) {
		t.Fatalf("valeurObtenue unseal err %v, valeurAttendue the served torrent bound to the stored row", err)
	}
	files, err := testCase.store.ListCandidateFiles(context.Background(), stored.ID)
	if err != nil || len(files) != 2 || files[0].MediaFileID != local.ID {
		t.Fatalf("valeurObtenue files %+v err %v, valeurAttendue the first torrent file associated with the local file", files, err)
	}
	expectedReport := sources.IndexerReport{Queries: 2, Downloads: 1}
	if gotReport := result.Indexers[string(indexerSameTracker)]; gotReport != expectedReport {
		t.Fatalf("valeurObtenue report %+v, valeurAttendue %+v (caps and one search, one download)", gotReport, expectedReport)
	}
	encoded, err := json.Marshal(result)
	if err != nil || !strings.Contains(string(encoded), `"indexers":{"torznab:a":{"torznab_queries":2,"torznab_downloads":1,`) {
		t.Fatalf("valeurObtenue %s, valeurAttendue the indexers report by source id", encoded)
	}
}

// Deux indexeurs du même tracker servent le même .torrent : un seul candidat, le second résultat est écarté.
func TestMatch_sameInfohashSameTrackerIsOneCandidate(t *testing.T) {
	testCase := newIndexerMatchCase(t)
	sizeOne, sizeTwo := 3*gibibyte, 4*gibibyte
	testCase.addMovie("/srv/media/edition-one.mkv", sizeOne, 604, "Missing")
	testCase.addMovie("/srv/media/edition-two.mkv", sizeTwo, 604, "Missing")
	served := indexerTorrent(t, "partial", sizeOne, 2*gibibyte)
	first := testCase.addIndexer(indexerSameTracker, sealedTracker)
	second := testCase.addIndexer(otherIndexer, sealedTracker)
	first.offer("partial", 5*gibibyte, served)
	second.offer("partial", 5*gibibyte, served)

	result := testCase.run()

	candidates := testCase.candidates()
	if len(candidates) != 1 || len(second.downloads) == 0 || candidates[0].Source != string(indexerSameTracker) || result.CandidatesCreated != 1 {
		t.Fatalf("valeurObtenue %+v (second downloads %v), valeurAttendue one candidate from the first indexer", candidates, second.downloads)
	}
}

func TestMatch_providerCandidateKeepsProviderSource(t *testing.T) {
	testCase := newIndexerMatchCase(t)
	sizeOne, sizeTwo := 3*gibibyte, 4*gibibyte
	testCase.addMovie("/srv/media/edition-one.mkv", sizeOne, 604, "Missing")
	testCase.addMovie("/srv/media/edition-two.mkv", sizeTwo, 604, "Missing")
	served := indexerTorrent(t, "partial", sizeOne, 2*gibibyte)
	fromProvider := release(infohashOfTorrent(t, served), sealedTracker, 604, sizeOne, 2*gibibyte)
	testCase.provider.releases = []libraryprovider.Release{fromProvider}
	indexer := testCase.addIndexer(indexerSameTracker, sealedTracker)
	indexer.offer("partial", 5*gibibyte, served)

	testCase.run()

	candidates := testCase.candidates()
	if len(indexer.downloads) == 0 || len(candidates) != 1 {
		t.Fatalf("valeurObtenue %d candidates downloads %v, valeurAttendue the indexer searched and one candidate", len(candidates), indexer.downloads)
	}
	if stored := candidates[0]; stored.Source != store.SourceProvider || stored.HasSealed || stored.State != store.CandidateMatched {
		t.Fatalf("valeurObtenue %+v, valeurAttendue the provider candidate unchanged, without sealed copy", stored)
	}
}

func TestMatch_configCoveredTrackerHasNoSealedBlob(t *testing.T) {
	testCase := newIndexerMatchCase(t)
	size := 4 * gibibyte
	testCase.addMovie("/srv/media/missing.mkv", size, 604, "Missing")
	indexer := testCase.addIndexer(indexerSameTracker, coveredTrackerName)
	indexer.offer("win", size, indexerTorrent(t, "win", size))

	testCase.run()

	candidates := testCase.candidates()
	if len(candidates) != 1 || candidates[0].HasSealed || candidates[0].State != store.CandidateFetched {
		t.Fatalf("valeurObtenue %+v, valeurAttendue a fetched candidate without sealed copy", candidates)
	}
}

func TestMatch_withoutSecretKeyCallsNoIndexer(t *testing.T) {
	testCase := newIndexerMatchCase(t)
	testCase.sealer = nil
	testCase.addMovie("/srv/media/one.mkv", 3*gibibyte, 604, "One")
	testCase.addMovie("/srv/media/two.mkv", 4*gibibyte, 605, "Two")
	indexer := testCase.addIndexer(indexerSameTracker, sealedTracker)

	result := testCase.run()

	if result.Reasons[ReasonSecretKeyMissing] != 1 || indexer.requests() != 0 || result.Failed != 0 {
		t.Fatalf("valeurObtenue %+v requests %d, valeurAttendue secret_key_missing once and no request", result, indexer.requests())
	}
}

func TestMatch_budgetExhaustedIsNotFailure(t *testing.T) {
	testCase := newIndexerMatchCase(t)
	testCase.limits.MaxQueriesPerRun = 1
	testCase.addMovie("/srv/media/missing.mkv", 4*gibibyte, 604, "Missing")
	testCase.addMovie("/srv/media/other.mkv", 3*gibibyte, 605, "Other")
	testCase.addIndexer(indexerSameTracker, sealedTracker)

	result := testCase.run()

	if result.Reasons[ReasonIndexerBudgetExhausted] != 1 || result.Failed != 0 || !result.Indexers[string(indexerSameTracker)].BudgetExhausted {
		t.Fatalf("valeurObtenue %+v, valeurAttendue indexer_budget_exhausted without failure", result)
	}
}

func TestMatch_skipsV2OnlyIndexerTorrent(t *testing.T) {
	testCase := newIndexerMatchCase(t)
	size := int64(5)
	testCase.addMovie("/srv/media/missing.mkv", size, 604, "Missing")
	indexer := testCase.addIndexer(indexerSameTracker, sealedTracker)
	indexer.offer("v2", size, []byte("d4:infod6:lengthi5e12:meta versioni2e4:name5:b.mkv12:piece lengthi16384eee"))
	indexer.offer("win", size, indexerTorrent(t, "win", size))

	result := testCase.run()

	if result.Reasons[ReasonV2Only] != 1 || len(testCase.candidates()) != 1 {
		t.Fatalf("valeurObtenue %+v, valeurAttendue v2_only counted and the next item kept", result)
	}
}

func TestMatch_withoutIndexerReportsNoIndexers(t *testing.T) {
	testCase := newIndexerMatchCase(t)
	testCase.addMovie("/srv/media/missing.mkv", 4*gibibyte, 604, "Missing")

	result := testCase.run()

	if result.Indexers != nil || len(result.Reasons) != 0 {
		t.Fatalf("valeurObtenue %+v, valeurAttendue the provider-only result", result)
	}
}

func indexerTVCaps() torznab.Caps {
	return torznab.Caps{Modes: map[string][]string{"search": {"q"}, "tvsearch": {"q", "tvdbid", "season", "ep"}}, Categories: []int{5000}}
}

// Le pack du fournisseur couvre E01 ; le même pack servi par un indexeur sans tracker ne prouve aucun fichier encore
// découvert (E02) : écarté, pas de seconde copie ni de litige, et l'indexeur continue par la recherche texte.
func TestMatch_indexerCopyOfAPackCoveringNoUncoveredFileIsDiscarded(t *testing.T) {
	testCase := newIndexerMatchCase(t)
	episodeOne, episodeTwo := gibibyte, 2*gibibyte
	testCase.addFile("/srv/media/show/e01.mkv", episodeOne, store.MediaMetadata{TVDBID: 81189, Title: "Show", Season: 1, Episode: 1})
	testCase.addFile("/srv/media/show/e02.mkv", episodeTwo, store.MediaMetadata{TVDBID: 81189, Title: "Show", Season: 1, Episode: 2})
	pack := indexerTorrent(t, "pack", episodeOne, 3*gibibyte)
	fromProvider := release(infohashOfTorrent(t, pack), "tracker-p", 0, episodeOne, 3*gibibyte)
	fromProvider.TVDBID = 81189
	testCase.provider.releases = []libraryprovider.Release{fromProvider}
	indexer := testCase.addIndexer(indexerSameTracker, "")
	indexer.caps = indexerTVCaps()
	indexer.offer("pack", 4*gibibyte, pack)

	result := testCase.run()

	if candidates := testCase.candidates(); len(candidates) != 1 || candidates[0].Source != store.SourceProvider {
		t.Fatalf("valeurObtenue %+v, valeurAttendue the provider candidate only", candidates)
	}
	if result.Reasons[ReasonTorznabNoExactSize] != 1 || len(indexer.queries) != 2 {
		t.Fatalf("valeurObtenue %+v queries %+v, valeurAttendue the pack refused once and the text query sent", result, indexer.queries)
	}
}

// Un candidat d'indexeur devenu stale est retrouvé : même ligne, génération suivante, de nouveau fetched.
func TestMatch_revivesAStaleIndexerCandidate(t *testing.T) {
	testCase := newIndexerMatchCase(t)
	size := 4 * gibibyte
	local := testCase.addMovie("/srv/media/missing.mkv", size, 604, "Missing")
	indexer := testCase.addIndexer(indexerSameTracker, sealedTracker)
	indexer.offer("win", size, indexerTorrent(t, "win", size))
	testCase.run()
	if _, err := testCase.store.InvalidateCandidatesForFile(context.Background(), local.ID, fixedNow); err != nil {
		t.Fatal(err)
	}
	stale := testCase.candidates()[0]

	result := testCase.run()

	revived := testCase.candidates()
	if len(revived) != 1 || stale.State != store.CandidateStale || revived[0].ID != stale.ID || revived[0].State != store.CandidateFetched ||
		revived[0].Generation != stale.Generation+1 || !revived[0].HasSealed || result.CandidatesUpdated != 1 {
		t.Fatalf("valeurObtenue %+v (result %+v), valeurAttendue the same candidate fetched again at the next generation", revived, result)
	}
}
