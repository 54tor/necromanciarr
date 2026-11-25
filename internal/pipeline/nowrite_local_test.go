package pipeline

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"necromanciarr/internal/arr"
	"necromanciarr/internal/config"
	"necromanciarr/internal/dedupe"
	"necromanciarr/internal/doctor"
	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/fsystest"
	"necromanciarr/internal/fsys/local"
	"necromanciarr/internal/libraryprovider"
	"necromanciarr/internal/linker"
	"necromanciarr/internal/repair"
	"necromanciarr/internal/store"
)

// Invariant I1 de bout en bout sur le disque local : chaque écrivain passe par un registre dont le volume est un
// enregistreur d'appels ; aucune écriture ne doit viser la bibliothèque, qui doit rester identique octet pour octet.

// agedLibraryTime : les fichiers de dedupe et repair sont anciens, hors de toute fenêtre « en cours d'écriture ».
var agedLibraryTime = time.Unix(1600000000, 0)

// noWriteWorld est l'arbre du test : bibliothèque, dossier de liens, racine torrent de dedupe et dossier de liens à
// réparer, tous hors de la bibliothèque, servis par un registre dont le volume local est enregistré. Les torrents à lier
// naissent de Match (fournisseur factice), à partir de l'inventaire de Scan : aucun n'est posé en base par le test.
type noWriteWorld struct {
	*applyFixture
	recorder      *fsystest.Recorder
	registry      *fsys.Registry
	torrentRoot   string
	repairLinkDir string
	releases      []libraryprovider.Release
	hardHash      string
	softHash      string
	annexHash     string
	brokenLink    store.Link
}

// Tailles distinctes : Match rattache un fichier de release à un média par sa taille.
const (
	hardMovieSize = movieSize + 3
	softMovieSize = movieSize + 5
)

// libraryFileCount compte les fichiers posés dans la bibliothèque (trois films liés, un doublon, une cible renommée).
const libraryFileCount = 5

func newNoWriteWorld(t *testing.T) *noWriteWorld {
	t.Helper()
	world := &noWriteWorld{applyFixture: newApplyFixture(t), recorder: fsystest.NewRecorder(local.New())}
	registry, err := fsys.NewRegistry(map[string]fsys.Volume{"": world.recorder}, []fsys.Location{{Path: world.root}})
	if err != nil {
		t.Fatal(err)
	}
	world.registry, world.verifyFixture.registry, world.productionOpener = registry, registry, true
	world.hardHash = world.publish(t, "Movie.Hard", []torrentEntry{{path: "hard.mkv", content: patternBytes(11, hardMovieSize), localName: "hard.mkv"}})
	world.softHash = world.publish(t, "Movie.Soft", []torrentEntry{{path: "soft.mkv", content: patternBytes(12, softMovieSize), localName: "soft.mkv"}})
	world.annexHash = world.publish(t, "Movie.Annex", []torrentEntry{
		{path: "annex.mkv", content: patternBytes(13, movieSize), localName: "annex.mkv"},
		{path: "annex.nfo", content: patternBytes(14, nfoSize)},
	})
	world.torrentRoot = world.plantDuplicate(t)
	world.repairLinkDir, world.brokenLink = world.plantBrokenLink(t)
	return world
}

// publish pose les médias locaux du torrent dans la bibliothèque, son .torrent chez le fournisseur factice et sa
// release dans la recherche ; rien n'est écrit en base.
func (world *noWriteWorld) publish(t *testing.T, name string, entries []torrentEntry) string {
	t.Helper()
	raw := buildTestTorrent(t, name, entries)
	infohash := infohashOf(t, raw)
	world.fetcher.torrents[infohash] = raw
	releaseFiles := make([]libraryprovider.ReleaseFile, 0, len(entries))
	var total int64
	for _, entry := range entries {
		if entry.localName != "" {
			writeAgedFile(t, world.libraryPath(entry.localName), entry.content)
		}
		size := int64(len(entry.content))
		releaseFiles = append(releaseFiles, libraryprovider.ReleaseFile{Path: name + "/" + entry.path, Size: size})
		total += size
	}
	world.releases = append(world.releases, libraryprovider.Release{
		Infohash: infohash, Name: name, Tracker: testTracker, TotalSize: total, Files: releaseFiles, TMDBID: 603,
	})
	return infohash
}

// plantDuplicate pose, hors de la bibliothèque, une copie d'un fichier de la bibliothèque que dedupe remplacera.
func (world *noWriteWorld) plantDuplicate(t *testing.T) string {
	t.Helper()
	content := patternBytes(21, movieSize)
	torrentRoot := filepath.Join(t.TempDir(), "torrents")
	writeAgedFile(t, filepath.Join(world.root, "duplicate.mkv"), content)
	writeAgedFile(t, filepath.Join(torrentRoot, "duplicate.mkv"), content)
	return torrentRoot
}

// plantBrokenLink pose un lien symbolique vers un nom disparu de la bibliothèque ; le fichier existe sous un autre nom.
func (world *noWriteWorld) plantBrokenLink(t *testing.T) (string, store.Link) {
	t.Helper()
	renamedPath := filepath.Join(world.root, "renamed.mkv")
	writeAgedFile(t, renamedPath, patternBytes(31, movieSize))
	linkDir := filepath.Join(t.TempDir(), "repair-links")
	if err := os.MkdirAll(linkDir, 0o750); err != nil {
		t.Fatal(err)
	}
	oldTarget := filepath.Join(world.root, "old-name.mkv")
	linkPath := filepath.Join(linkDir, "movie.mkv")
	if err := os.Symlink(oldTarget, linkPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	fingerprint, _, err := dedupe.HeadTailFingerprint(t.Context(), nil, nil, renamedPath, movieSize, time.Time{}, true)
	if err != nil {
		t.Fatal(err)
	}
	link := store.Link{
		Kind: "symlink", LinkPath: linkPath, TargetPath: oldTarget, Size: movieSize, State: store.LinkOK,
		Fingerprint: fingerprint, ModTime: agedLibraryTime,
	}
	return linkDir, link
}

func writeAgedFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, agedLibraryTime, agedLibraryTime); err != nil {
		t.Fatal(err)
	}
}

// phase est une étape du test. run rend son Result (zéro hors des étapes du pipeline) et check prouve qu'elle a
// vraiment travaillé. Chaque étape doit laisser au moins un appel enregistré (lecture comprise), sauf Match, qui ne
// lit que la base et le fournisseur (prouvée par le compte de ce qu'elle crée) ; writes exige une écriture enregistrée
// (preuve qu'elle passe par l'enregistreur et non par un accès disque direct).
type phase struct {
	name          string
	writes        bool
	withoutVolume bool
	run           func(t *testing.T) Result
	check         func(result Result) error
}

func (world *noWriteWorld) phases() []phase {
	return []phase{
		{name: "scan", run: world.scan, check: expectAtLeast("processed", libraryFileCount, func(result Result) int { return result.Processed })},
		{name: "match", withoutVolume: true, run: world.match, check: expectAtLeast("candidates created", 3, func(result Result) int { return result.CandidatesCreated })},
		{name: "verify", run: world.verify, check: expectAtLeast("proposals created", 3, func(result Result) int { return result.ProposalsCreated })},
		{name: "link hardlink", writes: true, run: world.linkWith(linker.Hardlink, world.fullProposal(world.hardHash), world.annexProposal)},
		{name: "link symlink", writes: true, run: world.linkWith(linker.Symlink, world.fullProposal(world.softHash))},
		{name: "seed", run: world.seed, check: expectAtLeast("seeded", 3, func(result Result) int { return result.Seeded + result.SeededPausedIncomplete })},
		{name: "force", writes: true, run: world.force},
		{name: "dedupe", writes: true, run: world.dedupe},
		{name: "repair", writes: true, run: world.repair},
		{name: "doctor", writes: true, run: world.doctor},
	}
}

func expectAtLeast(what string, minimum int, field func(Result) int) func(Result) error {
	return func(result Result) error {
		if gotValue := field(result); gotValue < minimum {
			return fmt.Errorf("expectedValue at least %d %s, gotValue %d (result %+v)", minimum, what, gotValue, result)
		}
		return nil
	}
}

// runPhases exécute les étapes dans l'ordre et rend la première qui n'a pas prouvé son travail.
func (world *noWriteWorld) runPhases(t *testing.T, steps []phase) error {
	t.Helper()
	for _, step := range steps {
		callsBefore, writesBefore := len(world.recorder.Calls()), len(world.recorder.WriteCalls())
		result := step.run(t)
		recordedCalls := len(world.recorder.Calls()) - callsBefore
		recordedWrites := len(world.recorder.WriteCalls()) - writesBefore
		if !step.withoutVolume && recordedCalls == 0 {
			return fmt.Errorf("%s: expectedValue calls recorded through the registry, gotValue none", step.name)
		}
		if step.writes && recordedWrites == 0 {
			return fmt.Errorf("%s: expectedValue writes recorded through the registry, gotValue none", step.name)
		}
		if step.check != nil {
			if err := step.check(result); err != nil {
				return fmt.Errorf("%s: %w", step.name, err)
			}
		}
	}
	return nil
}

func (world *noWriteWorld) runEveryWriter(t *testing.T) {
	t.Helper()
	if err := world.runPhases(t, world.phases()); err != nil {
		t.Fatal(err)
	}
}

func (world *noWriteWorld) runStage(t *testing.T, stage Stage, deps Deps) Result {
	t.Helper()
	result, err := Run(t.Context(), stage, deps)
	if err != nil {
		t.Fatalf("%s: %v", stage, err)
	}
	return result
}

func (world *noWriteWorld) scan(t *testing.T) Result {
	t.Helper()
	var items []arr.Item
	for _, name := range []string{"hard.mkv", "soft.mkv", "annex.mkv"} {
		items = append(items, arr.Item{Path: world.libraryPath(name), TMDBID: 603})
	}
	deps := Deps{
		Store: world.store, ScanRoot: LibraryScanner(world.store, world.registry), LibraryRoots: []string{world.root},
		ArrSources: []ItemsSource{fakeArr{items: items}}, Registry: world.registry, Now: func() time.Time { return fixedNow },
	}
	return world.runStage(t, StageScan, deps)
}

func (world *noWriteWorld) match(t *testing.T) Result {
	t.Helper()
	provider := &fakeProvider{honourSizes: true, releases: world.releases}
	deps := Deps{
		Store: world.store, Sources: fakeChain{searcher: provider, fetcher: world.fetcher}, Registry: world.registry,
		Now: func() time.Time { return fixedNow },
	}
	return world.runStage(t, StageMatch, deps)
}

func (world *noWriteWorld) verify(t *testing.T) Result {
	t.Helper()
	return world.runStage(t, StageVerify, world.deps())
}

// linkWith accepte les propositions choisies (lues après Verify) puis lance Link avec cette seule sorte de lien.
func (world *noWriteWorld) linkWith(kind linker.Kind, proposals ...func() store.Proposal) func(t *testing.T) Result {
	return func(t *testing.T) Result {
		t.Helper()
		for _, proposal := range proposals {
			world.accept(proposal().ID)
		}
		world.linkKinds = []linker.Kind{kind}
		result := world.runStage(t, StageLink, world.applyDeps())
		if result.Linked != len(proposals) {
			t.Fatalf("link %s: gotValue %+v, expectedValue %d linked", kind, result, len(proposals))
		}
		return result
	}
}

func (world *noWriteWorld) fullProposal(infohash string) func() store.Proposal {
	return func() store.Proposal { return world.proposalOf(world.candidate(infohash).ID, store.ProposalFull) }
}

func (world *noWriteWorld) annexProposal() store.Proposal {
	return world.proposalOf(world.candidate(world.annexHash).ID, store.ProposalPartialAnnex)
}

func (world *noWriteWorld) seed(t *testing.T) Result {
	t.Helper()
	return world.runStage(t, StageSeed, world.applyDeps())
}

// force isole le média lié du partial_annex par une copie indépendante, comme Force.
func (world *noWriteWorld) force(t *testing.T) Result {
	t.Helper()
	proposal := world.annexProposal()
	linkRoot := world.progressOf(proposal.ID).LinkRoot
	result, err := IsolateStraddlingMedia(t.Context(), world.applyDeps(), proposal, world.candidate(world.annexHash), linkRoot)
	if err != nil || len(result.Replaced) != 1 {
		t.Fatalf("force: gotValue %+v %v, expectedValue the straddling media copied", result, err)
	}
	return Result{}
}

func (world *noWriteWorld) dedupe(t *testing.T) Result {
	t.Helper()
	library := []store.MediaFile{{Path: filepath.Join(world.root, "duplicate.mkv"), Size: movieSize, ModTime: agedLibraryTime}}
	options := dedupe.Options{
		Kinds: []linker.Kind{linker.Hardlink}, TorrentRoots: []string{world.torrentRoot}, Recorder: world.store, Registry: world.registry,
	}
	replacements, err := dedupe.Plan(t.Context(), []string{world.torrentRoot}, library, options)
	if err != nil || len(replacements) != 1 || replacements[0].Kind == 0 {
		t.Fatalf("dedupe plan: gotValue %+v %v, expectedValue one applicable replacement", replacements, err)
	}
	result, err := dedupe.Apply(t.Context(), replacements, options)
	if err != nil || !result.Outcomes[0].Done {
		t.Fatalf("dedupe apply: gotValue %+v %v", result, err)
	}
	return Result{}
}

func (world *noWriteWorld) repair(t *testing.T) Result {
	t.Helper()
	library := []store.MediaFile{{Path: filepath.Join(world.root, "renamed.mkv"), Size: movieSize, ModTime: agedLibraryTime}}
	scanOptions := repair.Options{LibraryRoots: []string{world.root}, AutoApply: true, Registry: world.registry}
	repairs, _, err := repair.Scan(t.Context(), scanOptions, []store.Link{world.brokenLink}, library)
	if err != nil || len(repairs) != 1 {
		t.Fatalf("repair scan: gotValue %+v %v, expectedValue one repair", repairs, err)
	}
	result, err := repair.Apply(t.Context(), repairs, repair.ApplyOptions{Roots: []string{world.repairLinkDir}, Registry: world.registry})
	if err != nil || !result.Outcomes[0].Done {
		t.Fatalf("repair apply: gotValue %+v %v", result, err)
	}
	return Result{}
}

// doctorCheckIDs sont les contrôles qui touchent la bibliothèque ou les dossiers de liens.
var doctorCheckIDs = []string{doctor.IDLibraryRoots, doctor.IDLinks, doctor.IDDedupe, doctor.IDPathMappings}

func (world *noWriteWorld) doctor(t *testing.T) Result {
	t.Helper()
	settings := config.Config{
		LibraryRoots: []string{world.root}, LinkDirs: []string{world.linkDir}, LinkType: "hardlink",
		Dedupe:    config.Dedupe{TorrentRoots: []string{world.torrentRoot}},
		PathRules: []config.PathRule{{From: world.linkDir, To: "/data/links"}},
	}
	var selected []doctor.Check
	for _, check := range doctor.Build(settings, doctor.Deps{Registry: world.registry}) {
		if slices.Contains(doctorCheckIDs, check.ID) {
			selected = append(selected, check)
		}
	}
	report := doctor.Run(t.Context(), selected)
	if len(report.Checks) != len(doctorCheckIDs) {
		t.Fatalf("doctor: gotValue %d checks, expectedValue %d", len(report.Checks), len(doctorCheckIDs))
	}
	for _, outcome := range report.Checks {
		isRefused := outcome.Result.Status == doctor.Fail || (outcome.ID == doctor.IDLibraryRoots && outcome.Result.Status != doctor.OK)
		if isRefused {
			t.Fatalf("doctor %s: gotValue %v %s, expectedValue library-roots ok and no failure", outcome.ID, outcome.Result.Status, outcome.Result.Code)
		}
	}
	return Result{}
}

// treeEntry est l'état d'une entrée de la bibliothèque : type et droits, date de modification, empreinte du contenu
// et identité du fichier (atime, ctime et nombre de liens exclus : un hardlink les change, c'est permis).
type treeEntry struct {
	mode    fs.FileMode
	modTime time.Time
	digest  [sha256.Size]byte
	info    fs.FileInfo
}

func snapshotTree(t *testing.T, root string) map[string]treeEntry {
	t.Helper()
	snapshot := map[string]treeEntry{}
	walkErr := filepath.WalkDir(root, func(entryPath string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(entryPath)
		if err != nil {
			return err
		}
		entry := treeEntry{mode: info.Mode(), modTime: info.ModTime(), info: info}
		if info.Mode().IsRegular() {
			content, readErr := os.ReadFile(entryPath) // #nosec G304 -- chemin de test sous t.TempDir()
			if readErr != nil {
				return readErr
			}
			entry.digest = sha256.Sum256(content)
		}
		relative, err := filepath.Rel(root, entryPath)
		snapshot[relative] = entry
		return err
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
	return snapshot
}

// treeDifferences nomme chaque entrée apparue, disparue ou changée entre deux instantanés.
func treeDifferences(before, after map[string]treeEntry) []string {
	var differences []string
	for name, expectedEntry := range before {
		gotEntry, present := after[name]
		isSame := present && gotEntry.mode == expectedEntry.mode && gotEntry.modTime.Equal(expectedEntry.modTime) &&
			gotEntry.digest == expectedEntry.digest && os.SameFile(gotEntry.info, expectedEntry.info)
		if !isSame {
			differences = append(differences, name)
		}
	}
	for name := range after {
		if _, existed := before[name]; !existed {
			differences = append(differences, name)
		}
	}
	slices.Sort(differences)
	return differences
}

func TestLocal_noWriteCallUnderLibraryRoot_endToEnd(t *testing.T) {
	world := newNoWriteWorld(t)
	before := snapshotTree(t, world.root)

	world.runEveryWriter(t)

	world.recorder.AssertNoWriteUnder(t, world.root)
	if differences := treeDifferences(before, snapshotTree(t, world.root)); len(differences) != 0 {
		t.Fatalf("expectedValue the library tree unchanged, gotValue changes in %v", differences)
	}
}

// libraryWriteCatcher retient l'échec de l'assertion au lieu d'arrêter ce test, qui doit la voir échouer.
type libraryWriteCatcher struct {
	testing.TB
	failures []string
}

func (catcher *libraryWriteCatcher) Errorf(format string, _ ...any) {
	catcher.failures = append(catcher.failures, format)
}

// plantInLibrary est l'étape malveillante : un registre où la bibliothèque n'est pas déclarée laisse ouvrir une
// racine d'écriture sur elle, et l'écriture passe par l'enregistreur.
func (world *noWriteWorld) plantInLibrary(t *testing.T) {
	t.Helper()
	careless, err := fsys.NewRegistry(map[string]fsys.Volume{"": world.recorder}, []fsys.Location{{Path: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	root, err := careless.WriteRoot(fsys.Location{Path: world.root})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	planted, err := root.CreateExclusive("planted.mkv", 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := planted.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLocal_noWriteCallUnderLibraryRoot_catchesAMaliciousWriter(t *testing.T) {
	world := newNoWriteWorld(t)
	before := snapshotTree(t, world.root)
	world.runEveryWriter(t)

	world.plantInLibrary(t)

	catcher := &libraryWriteCatcher{TB: t}
	world.recorder.AssertNoWriteUnder(catcher, world.root)
	if len(catcher.failures) == 0 {
		t.Fatal("expectedValue the write under the library caught by the recorder, gotValue none")
	}
	differences := treeDifferences(before, snapshotTree(t, world.root))
	if !slices.Equal(differences, []string{".", "planted.mkv"}) {
		t.Fatalf("expectedValue the planted file and its directory seen by the snapshot, gotValue %v", differences)
	}
}

// TestLocal_noWriteCallUnderLibraryRoot_failsWhenAStageDidNotRun : une étape remplacée par une étape vide doit faire
// échouer la preuve, sinon le test de bout en bout pourrait passer sans rien exercer.
func TestLocal_noWriteCallUnderLibraryRoot_failsWhenAStageDidNotRun(t *testing.T) {
	for _, skipped := range []string{"scan", "match", "verify", "seed"} {
		t.Run(skipped, func(t *testing.T) {
			world := newNoWriteWorld(t)
			steps := world.phases()
			for stepIndex := range steps {
				if steps[stepIndex].name == skipped {
					steps[stepIndex].run = func(*testing.T) Result { return Result{} }
				}
			}

			err := world.runPhases(t, steps)

			if err == nil || !strings.HasPrefix(err.Error(), skipped+":") {
				t.Fatalf("expectedValue the empty %s stage reported, gotValue %v", skipped, err)
			}
		})
	}
}
