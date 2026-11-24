package pipeline

import (
	"bytes"
	"context"
	"crypto/sha1" // #nosec G505 -- SHA-1 imposé par le format BitTorrent v1
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"necromanciarr/internal/crossseed"
	"necromanciarr/internal/libraryprovider"
	"necromanciarr/internal/store"
	"necromanciarr/internal/torrentcheck"
)

// verifyPieceLength est volontairement minuscule : quelques dizaines d'octets suffisent à couvrir plusieurs pièces.
const verifyPieceLength = 16

// torrentEntry est un fichier du torrent de test ; localName vide signifie qu'il manque à la bibliothèque. localContent
// remplace le contenu local (fichier corrompu) ; twinLocalName ajoute une copie locale identique (association ambiguë).
type torrentEntry struct {
	path          string
	content       []byte
	localName     string
	localContent  []byte
	twinLocalName string
	// padding fait de l'entrée un fichier de remplissage BEP 47 : listé par le fournisseur, ignoré par torrentcheck.
	padding bool
}

func patternBytes(seed byte, size int) []byte {
	content := make([]byte, size)
	for position := range content {
		content[position] = seed + byte(position*7)
	}
	return content
}

func concatenatedPieceHashes(data []byte) []byte {
	var hashes []byte
	for start := 0; start < len(data); start += verifyPieceLength {
		sum := sha1.Sum(data[start:min(start+verifyPieceLength, len(data))]) // #nosec G401 -- SHA-1 imposé par BitTorrent v1
		hashes = append(hashes, sum[:]...)
	}
	return hashes
}

func encodeTestTorrent(t *testing.T, info metainfo.Info) []byte {
	t.Helper()
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := bencode.Marshal(metainfo.MetaInfo{InfoBytes: infoBytes, Announce: "http://tracker.example.com/announce"})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func buildTestTorrent(t *testing.T, name string, entries []torrentEntry) []byte {
	t.Helper()
	var concatenated []byte
	info := metainfo.Info{Name: name, PieceLength: verifyPieceLength}
	for _, entry := range entries {
		concatenated = append(concatenated, entry.content...)
		fileInfo := metainfo.FileInfo{Path: []string{entry.path}, Length: int64(len(entry.content))}
		if entry.padding {
			fileInfo.Attr = "p"
		}
		info.Files = append(info.Files, fileInfo)
	}
	info.Pieces = concatenatedPieceHashes(concatenated)
	return encodeTestTorrent(t, info)
}

// fakeFetcher sert des .torrent et des .nfo par infohash et compte les appels.
type fakeFetcher struct {
	mutex        sync.Mutex
	torrents     map[string][]byte
	torrentErrs  map[string]error
	nfos         map[string][]libraryprovider.Nfo
	torrentCalls int
	nfoCalls     int
}

func newFakeFetcher() *fakeFetcher {
	return &fakeFetcher{torrents: map[string][]byte{}, torrentErrs: map[string]error{}, nfos: map[string][]libraryprovider.Nfo{}}
}

func (fetcher *fakeFetcher) Torrent(_ context.Context, infohash, _ string) ([]byte, error) {
	fetcher.mutex.Lock()
	defer fetcher.mutex.Unlock()
	fetcher.torrentCalls++
	if err := fetcher.torrentErrs[infohash]; err != nil {
		return nil, err
	}
	raw, known := fetcher.torrents[infohash]
	if !known {
		return nil, libraryprovider.ErrNotFound
	}
	return raw, nil
}

func (fetcher *fakeFetcher) Nfos(_ context.Context, infohash, _ string) ([]libraryprovider.Nfo, error) {
	fetcher.mutex.Lock()
	defer fetcher.mutex.Unlock()
	fetcher.nfoCalls++
	nfos, known := fetcher.nfos[infohash]
	if !known {
		return nil, libraryprovider.ErrNotFound
	}
	return nfos, nil
}

// countingOpener ouvre les vrais fichiers en lecture seule, compte les octets lus et peut échouer après un plafond.
type countingOpener struct {
	mutex          sync.Mutex
	bytesRead      int64
	failAfterBytes int64
}

var errInjectedRead = errors.New("injected read failure")

func (opener *countingOpener) open(path string) (LocalFile, error) {
	file, err := os.Open(path) // #nosec G304 -- chemin de test sous t.TempDir()
	if err != nil {
		return nil, err
	}
	return &countingFile{file: file, opener: opener}, nil
}

func (opener *countingOpener) total() int64 {
	opener.mutex.Lock()
	defer opener.mutex.Unlock()
	return opener.bytesRead
}

type countingFile struct {
	file   *os.File
	opener *countingOpener
}

func (counting *countingFile) ReadAt(buffer []byte, offset int64) (int, error) {
	counting.opener.mutex.Lock()
	defer counting.opener.mutex.Unlock()
	isOverLimit := counting.opener.failAfterBytes > 0 && counting.opener.bytesRead+int64(len(buffer)) > counting.opener.failAfterBytes
	if isOverLimit {
		return 0, errInjectedRead
	}
	readCount, err := counting.file.ReadAt(buffer, offset)
	counting.opener.bytesRead += int64(readCount)
	return readCount, err
}

func (counting *countingFile) Close() error { return counting.file.Close() }

// verifyFixture tient une bibliothèque réelle sous t.TempDir(), un store et un fournisseur factice.
type verifyFixture struct {
	t         *testing.T
	store     *store.Store
	root      string
	fetcher   *fakeFetcher
	opener    *countingOpener
	crossSeed crossseed.Behavior
	limiter   torrentcheck.Limiter
	// readBudget plafonne les octets lus pour départager les copies (0 = sans limite, comme Deps.ReadBudget).
	readBudget int64
	// partialCap est Deps.PartialMaxMissingPercent (nil = sans plafond).
	partialCap *float64
	// productionOpener laisse Deps.OpenLocal nil : l'étape ouvre alors avec openReadOnly.
	productionOpener bool
}

func newVerifyFixture(t *testing.T) *verifyFixture {
	t.Helper()
	return &verifyFixture{
		t: t, store: openTestStore(t), root: t.TempDir(), fetcher: newFakeFetcher(), opener: &countingOpener{},
		crossSeed: crossseed.Delegate,
	}
}

// writeLocal écrit le fichier de bibliothèque s'il n'existe pas encore et l'inventorie avec sa vraie taille et sa vraie date.
func (fixture *verifyFixture) writeLocal(name string, content []byte) store.MediaFile {
	fixture.t.Helper()
	localPath := filepath.Join(fixture.root, name)
	if _, err := os.Stat(localPath); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(localPath, content, 0o600); err != nil {
			fixture.t.Fatal(err)
		}
	}
	info, err := os.Stat(localPath)
	if err != nil {
		fixture.t.Fatal(err)
	}
	ctx := context.Background()
	if err := fixture.store.UpsertMediaFile(ctx, store.MediaFile{Path: localPath, Size: info.Size(), ModTime: info.ModTime(), TMDBID: 603}); err != nil {
		fixture.t.Fatal(err)
	}
	return mediaFilesByPath(fixture.t, fixture.store)[localPath]
}

// addCandidate enregistre le torrent chez le fournisseur factice et le candidat associé aux fichiers locaux, puis regroupe.
func (fixture *verifyFixture) addCandidate(name, tracker string, entries []torrentEntry) store.Candidate {
	fixture.t.Helper()
	raw := buildTestTorrent(fixture.t, name, entries)
	return fixture.addCandidateWithTorrent(name, tracker, entries, raw)
}

func (fixture *verifyFixture) addCandidateWithTorrent(name, tracker string, entries []torrentEntry, raw []byte) store.Candidate {
	fixture.t.Helper()
	infohash := infohashOf(fixture.t, raw)
	var files []store.CandidateFile
	var total int64
	for fileIndex, entry := range entries {
		total += int64(len(entry.content))
		files = append(files, fixture.candidateRows(name, fileIndex, entry)...)
	}
	ctx := context.Background()
	if _, _, err := fixture.store.SaveCandidate(ctx, store.Candidate{Infohash: infohash, Tracker: tracker, Name: name, TotalSize: total}, files, fixedNow); err != nil {
		fixture.t.Fatal(err)
	}
	if err := fixture.store.RegroupCandidates(ctx); err != nil {
		fixture.t.Fatal(err)
	}
	fixture.fetcher.torrents[infohash] = raw
	return fixture.candidate(infohash)
}

func (fixture *verifyFixture) candidateRows(name string, fileIndex int, entry torrentEntry) []store.CandidateFile {
	base := store.CandidateFile{FileIndex: fileIndex, Path: name + "/" + entry.path, Size: int64(len(entry.content))}
	localContent := entry.content
	if entry.localContent != nil {
		localContent = entry.localContent
	}
	var rows []store.CandidateFile
	for _, localName := range []string{entry.localName, entry.twinLocalName} {
		if localName == "" {
			continue
		}
		local := fixture.writeLocal(localName, localContent)
		row := base
		row.MediaFileID, row.MediaSize, row.MediaModTime, row.Ambiguous = local.ID, local.Size, local.ModTime, entry.twinLocalName != ""
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return []store.CandidateFile{base}
	}
	return rows
}

// infohashOf calcule l'infohash sans passer par Parse, qui refuse les torrents v2 seuls.
func infohashOf(t *testing.T, raw []byte) string {
	t.Helper()
	metaInfo, err := metainfo.Load(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return metaInfo.HashInfoBytes().HexString()
}

func (fixture *verifyFixture) candidate(infohash string) store.Candidate {
	fixture.t.Helper()
	candidate, known := candidatesByHash(fixture.t, fixture.store)[infohash]
	if !known {
		fixture.t.Fatalf("candidate %s not found", infohash)
	}
	return candidate
}

func (fixture *verifyFixture) deps() Deps {
	deps := Deps{
		Store: fixture.store, Fetcher: fixture.fetcher, OpenLocal: fixture.opener.open, CrossSeed: fixture.crossSeed,
		ReadLimiter: fixture.limiter, ReadBudget: fixture.readBudget, ProgressEvery: 1,
		PartialMaxMissingPercent: fixture.partialCap, Now: func() time.Time { return fixedNow },
	}
	if fixture.productionOpener {
		deps.OpenLocal = nil
	}
	return deps
}

// run exécute l'étape et vérifie, après chaque exécution, que la bibliothèque n'a pas bougé.
func (fixture *verifyFixture) run(ctx context.Context) (Result, error) {
	before := snapshotLibrary(fixture.t, fixture.root)
	result, err := Run(ctx, StageVerify, fixture.deps())
	assertLibraryUnchanged(fixture.t, before, snapshotLibrary(fixture.t, fixture.root))
	return result, err
}

func (fixture *verifyFixture) mustRun() Result {
	fixture.t.Helper()
	result, err := fixture.run(context.Background())
	if err != nil {
		fixture.t.Fatalf("verify: %v (result %+v)", err, result)
	}
	return result
}

func (fixture *verifyFixture) verification(candidateID int64) (store.Verification, bool) {
	fixture.t.Helper()
	verification, found, err := fixture.store.LoadVerification(context.Background(), candidateID)
	if err != nil {
		fixture.t.Fatal(err)
	}
	return verification, found
}

func (fixture *verifyFixture) proposals() []store.Proposal {
	fixture.t.Helper()
	proposals, err := fixture.store.ListProposals(context.Background(), "")
	if err != nil {
		fixture.t.Fatal(err)
	}
	return proposals
}

func (fixture *verifyFixture) proposalsOf(candidateID int64) []store.Proposal {
	fixture.t.Helper()
	var owned []store.Proposal
	for _, proposal := range fixture.proposals() {
		if proposal.CandidateID == candidateID {
			owned = append(owned, proposal)
		}
	}
	return owned
}

func kindsOf(proposals []store.Proposal) []store.ProposalKind {
	var kinds []store.ProposalKind
	for _, proposal := range proposals {
		kinds = append(kinds, proposal.Kind)
	}
	slices.Sort(kinds)
	return kinds
}

func decodeDetails(t *testing.T, proposal store.Proposal) ProposalDetails {
	t.Helper()
	var details ProposalDetails
	if err := json.Unmarshal(proposal.Details, &details); err != nil {
		t.Fatalf("details %s: %v", proposal.Details, err)
	}
	return details
}

// assertLibraryUnchanged compare deux instantanés : contenu, date et inode de chaque fichier, et aucun fichier créé.
func assertLibraryUnchanged(t *testing.T, before, after map[string]librarySnapshot) {
	t.Helper()
	if len(after) != len(before) {
		t.Fatalf("library entries changed: gotValue %d, expectedValue %d", len(after), len(before))
	}
	for name, expectedValue := range before {
		gotValue, present := after[name]
		if !present || !bytes.Equal(gotValue.content, expectedValue.content) {
			t.Fatalf("library file %s content changed", name)
		}
		if !gotValue.info.ModTime().Equal(expectedValue.info.ModTime()) || !os.SameFile(gotValue.info, expectedValue.info) {
			t.Fatalf("library file %s mtime or inode changed", name)
		}
	}
}

type librarySnapshot struct {
	content []byte
	info    os.FileInfo
}

func snapshotLibrary(t *testing.T, root string) map[string]librarySnapshot {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := map[string]librarySnapshot{}
	for _, entry := range entries {
		entryPath := filepath.Join(root, entry.Name())
		content, err := os.ReadFile(entryPath) // #nosec G304 -- chemin de test sous t.TempDir()
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(entryPath)
		if err != nil {
			t.Fatal(err)
		}
		snapshot[entry.Name()] = librarySnapshot{content: content, info: info}
	}
	return snapshot
}

// cancellingLimiter annule le contexte de l'étape à sa première sollicitation, comme un arrêt demandé en pleine lecture ;
// letReadThrough laisse passer cette lecture, pour que l'annulation tombe sur l'enregistrement qui suit.
type cancellingLimiter struct {
	cancel         context.CancelFunc
	calls          int
	letReadThrough bool
}

func (limiter *cancellingLimiter) WaitN(ctx context.Context, _ int) error {
	limiter.calls++
	limiter.cancel()
	if limiter.letReadThrough && limiter.calls == 1 {
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}

var _ io.ReaderAt = (*countingFile)(nil)
