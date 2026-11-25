package pipeline

import (
	"crypto/sha256"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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
// réparer, tous hors de la bibliothèque, servis par un registre dont le volume local est enregistré.
type noWriteWorld struct {
	*applyFixture
	recorder       *fsystest.Recorder
	registry       *fsys.Registry
	torrentRoot    string
	repairLinkDir  string
	hardCandidate  store.Candidate
	softCandidate  store.Candidate
	annexCandidate store.Candidate
	brokenLink     store.Link
}

func newNoWriteWorld(t *testing.T) *noWriteWorld {
	t.Helper()
	world := &noWriteWorld{applyFixture: newApplyFixture(t), recorder: fsystest.NewRecorder(local.New())}
	registry, err := fsys.NewRegistry(map[string]fsys.Volume{"": world.recorder}, []fsys.Location{{Path: world.root}})
	if err != nil {
		t.Fatal(err)
	}
	world.registry, world.verifyFixture.registry, world.productionOpener = registry, registry, true
	world.hardCandidate = world.addCandidate("Movie.Hard", "", []torrentEntry{{path: "hard.mkv", content: patternBytes(11, movieSize), localName: "hard.mkv"}})
	world.softCandidate = world.addCandidate("Movie.Soft", "", []torrentEntry{{path: "soft.mkv", content: patternBytes(12, movieSize), localName: "soft.mkv"}})
	world.annexCandidate = world.addCandidate("Movie.Annex", "", []torrentEntry{
		{path: "annex.mkv", content: patternBytes(13, movieSize), localName: "annex.mkv"},
		{path: "annex.nfo", content: patternBytes(14, nfoSize)},
	})
	world.torrentRoot = world.plantDuplicate(t)
	world.repairLinkDir, world.brokenLink = world.plantBrokenLink(t)
	return world
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

// phase est un écrivain du test ; writes dit s'il doit laisser au moins une écriture enregistrée (preuve qu'il
// passe bien par l'enregistreur et non par un accès disque direct).
type phase struct {
	name   string
	writes bool
	run    func(t *testing.T)
}

func (world *noWriteWorld) phases() []phase {
	return []phase{
		{name: "scan", run: world.scan},
		{name: "match", run: world.match},
		{name: "verify", run: world.verify},
		{name: "link hardlink", writes: true, run: world.linkWith(linker.Hardlink, world.fullProposal(world.hardCandidate), world.annexProposal)},
		{name: "link symlink", writes: true, run: world.linkWith(linker.Symlink, world.fullProposal(world.softCandidate))},
		{name: "seed", run: world.seed},
		{name: "force", writes: true, run: world.force},
		{name: "dedupe", writes: true, run: world.dedupe},
		{name: "repair", writes: true, run: world.repair},
		{name: "doctor", writes: true, run: world.doctor},
	}
}

func (world *noWriteWorld) runEveryWriter(t *testing.T) {
	t.Helper()
	for _, step := range world.phases() {
		before := len(world.recorder.WriteCalls())
		step.run(t)
		recordedWrites := len(world.recorder.WriteCalls()) - before
		if step.writes && recordedWrites == 0 {
			t.Fatalf("%s: expectedValue writes recorded through the registry, gotValue none", step.name)
		}
	}
}

func (world *noWriteWorld) scan(t *testing.T) {
	t.Helper()
	var items []arr.Item
	for _, name := range []string{"hard.mkv", "soft.mkv", "annex.mkv"} {
		items = append(items, arr.Item{Path: world.libraryPath(name), TMDBID: 603})
	}
	deps := Deps{
		Store: world.store, ScanRoot: LibraryScanner(world.store, world.registry), LibraryRoots: []string{world.root},
		ArrSources: []ItemsSource{fakeArr{items: items}}, Registry: world.registry, Now: func() time.Time { return fixedNow },
	}
	if _, err := Run(t.Context(), StageScan, deps); err != nil {
		t.Fatalf("scan: %v", err)
	}
}

func (world *noWriteWorld) match(t *testing.T) {
	t.Helper()
	provider := &fakeProvider{honourSizes: true, releases: []libraryprovider.Release{release(world.hardCandidate.Infohash, testTracker, 603, movieSize)}}
	deps := matchDeps(world.store, provider)
	deps.Registry = world.registry
	if _, err := Run(t.Context(), StageMatch, deps); err != nil {
		t.Fatalf("match: %v", err)
	}
}

func (world *noWriteWorld) verify(t *testing.T) {
	t.Helper()
	if _, err := Run(t.Context(), StageVerify, world.deps()); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

// linkWith accepte les propositions choisies (lues après Verify) puis lance Link avec cette seule sorte de lien.
func (world *noWriteWorld) linkWith(kind linker.Kind, proposals ...func() store.Proposal) func(t *testing.T) {
	return func(t *testing.T) {
		t.Helper()
		for _, proposal := range proposals {
			world.accept(proposal().ID)
		}
		world.linkKinds = []linker.Kind{kind}
		result, err := Run(t.Context(), StageLink, world.applyDeps())
		if err != nil || result.Linked != len(proposals) {
			t.Fatalf("link %s: gotValue %+v %v, expectedValue %d linked", kind, result, err, len(proposals))
		}
	}
}

func (world *noWriteWorld) fullProposal(candidate store.Candidate) func() store.Proposal {
	return func() store.Proposal { return world.proposalOf(candidate.ID, store.ProposalFull) }
}

func (world *noWriteWorld) annexProposal() store.Proposal {
	return world.proposalOf(world.annexCandidate.ID, store.ProposalPartialAnnex)
}

func (world *noWriteWorld) seed(t *testing.T) {
	t.Helper()
	if _, err := Run(t.Context(), StageSeed, world.applyDeps()); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// force isole le média lié du partial_annex par une copie indépendante, comme Force.
func (world *noWriteWorld) force(t *testing.T) {
	t.Helper()
	proposal := world.annexProposal()
	linkRoot := world.progressOf(proposal.ID).LinkRoot
	result, err := IsolateStraddlingMedia(t.Context(), world.applyDeps(), proposal, world.annexCandidate, linkRoot)
	if err != nil || len(result.Replaced) != 1 {
		t.Fatalf("force: gotValue %+v %v, expectedValue the straddling media copied", result, err)
	}
}

func (world *noWriteWorld) dedupe(t *testing.T) {
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
}

func (world *noWriteWorld) repair(t *testing.T) {
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
}

// doctorCheckIDs sont les contrôles qui touchent la bibliothèque ou les dossiers de liens.
var doctorCheckIDs = []string{doctor.IDLibraryRoots, doctor.IDLinks, doctor.IDDedupe, doctor.IDPathMappings}

func (world *noWriteWorld) doctor(t *testing.T) {
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
