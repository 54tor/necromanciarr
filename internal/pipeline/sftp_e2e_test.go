package pipeline

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"necromanciarr/internal/arr"
	"necromanciarr/internal/config"
	"necromanciarr/internal/dedupe"
	"necromanciarr/internal/doctor"
	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/local"
	"necromanciarr/internal/fsys/sftpfs"
	"necromanciarr/internal/fsys/sftpfs/sftptest"
	"necromanciarr/internal/hashsource"
	"necromanciarr/internal/libraryprovider"
	"necromanciarr/internal/linker"
	"necromanciarr/internal/qbit"
	"necromanciarr/internal/repair"
	"necromanciarr/internal/store"
)

// Invariant I1 de bout en bout sur SFTP : bibliothèque, dossier de liens, racine torrent de dedupe et dossier de liens à
// réparer sont voisins sur un même serveur en processus. Chaque étape passe par un vrai volume sftpfs ; le journal des
// requêtes du serveur ne doit contenir aucune écriture sous la racine canonique de la bibliothèque, et l'arbre de la
// bibliothèque doit rester identique (contenu, droits, date, inode ; atime, ctime et nombre de liens exclus).

// Chemins vus par le serveur, par Radarr (/movies) et par qBittorrent (/downloads/links).
const (
	sftpVolumeName = "seedbox"
	sftpLibraryDir = "/library"
	sftpLinkDir    = "/links"
	sftpTorrentDir = "/torrents"
	sftpRepairDir  = "/repair-links"
	arrLibraryDir  = "/movies"
	clientLinkDir  = "/downloads/links"
)

// sftpLibraryFileCount compte les fichiers posés dans la bibliothèque distante.
const sftpLibraryFileCount = 5

// sftpReadMethods ne lisent que leur chemin. sftpWrittenPaths donne, par méthode d'écriture, les chemins qu'elle
// modifie : la cible d'un lien dur seulement (sa source, lue, est le côté permis d'un hardlink, §4.C.6), le lien créé
// d'un lien symbolique (le texte de sa cible désigne légitimement un fichier de la bibliothèque : lien symlink, lien
// réparé ; tel qu'appliqué par le serveur, donc même avec des arguments inversés), les deux chemins d'un renommage.
// Setstat couvre aussi Fsetstat (chmod à la création) et la troncature. Exec porte une ligne de commande : seules
// les lignes constantes de la table (sftpfs.GuardLines) sont admises, toute autre est une violation.
var (
	sftpReadMethods  = []string{"Get", "Stat", "Lstat", "List", "Readlink", "RealPath", "StatVFS"}
	sftpWrittenPaths = map[string]func(sftptest.Request) []string{
		"Put":         pathOnly,
		"Open":        pathOnly,
		"Setstat":     pathOnly,
		"Mkdir":       pathOnly,
		"Rmdir":       pathOnly,
		"Remove":      pathOnly,
		"Rename":      bothPaths,
		"PosixRename": bothPaths,
		"Link":        func(request sftptest.Request) []string { return []string{request.Target} },
		"Symlink":     pathOnly,
	}
	sftpForbiddenMethods = []string{}
	sftpCommandMethods   = []string{"Exec"}
)

func pathOnly(request sftptest.Request) []string  { return []string{request.Path} }
func bothPaths(request sftptest.Request) []string { return []string{request.Path, request.Target} }

// isUnderRoot compare par segments : « /library-sibling » n'est pas sous « /library ».
func isUnderRoot(name, root string) bool {
	return name == root || root == "/" || strings.HasPrefix(name, root+"/")
}

// requestViolations rend chaque requête qui écrit sous libraryRoot, lit la bibliothèque autrement qu'en lecture
// seule, porte une méthode interdite, une requête étendue que pkg/sftp ne sert pas (aucune n'est classée) ou une
// méthode que la classification ne connaît pas.
func requestViolations(requests []sftptest.Request, libraryRoot string) []string {
	var violations []string
	for _, request := range requests {
		writtenPaths, isWrite := sftpWrittenPaths[request.Method]
		switch {
		case slices.Contains(sftpForbiddenMethods, request.Method):
			violations = append(violations, fmt.Sprintf("forbidden %+v", request))
		case slices.Contains(sftpCommandMethods, request.Method):
			if !slices.Contains(sftpfs.GuardLines(), request.Path) {
				violations = append(violations, fmt.Sprintf("command outside the table %+v", request))
			}
		case isWrite && slices.ContainsFunc(writtenPaths(request), func(name string) bool { return isUnderRoot(name, libraryRoot) }):
			violations = append(violations, fmt.Sprintf("write %+v", request))
		case request.Method == "Get" && isUnderRoot(request.Path, libraryRoot) && request.Flags != sftptest.FlagRead:
			violations = append(violations, fmt.Sprintf("open flags %+v", request))
		case strings.HasPrefix(request.Method, sftptest.ExtendedMethodPrefix):
			violations = append(violations, fmt.Sprintf("unclassified-extended %+v", request))
		case !isWrite && !slices.Contains(sftpReadMethods, request.Method):
			violations = append(violations, fmt.Sprintf("unclassified %+v", request))
		}
	}
	return violations
}

// followsLinks : Setstat et une ouverture non exclusive suivent, côté serveur, un lien symbolique final et écrivent
// dans le fichier d'un lien dur ; leur chemin seul ne dit donc pas quel fichier elles touchent.
func followsLinks(request sftptest.Request) bool {
	isOpen := request.Method == "Put" || request.Method == "Open"
	return request.Method == "Setstat" || (isOpen && request.Flags&sftptest.FlagExclusive == 0)
}

// maxVirtualLinks borne la résolution des liens symboliques sous la racine servie, comme sftptest.
const maxVirtualLinks = 40

// resolveOnServer suit les liens symboliques du dernier segment comme le serveur : une cible qui commence par « / » repart de la
// racine servie (les dossiers parents du test sont de vrais dossiers).
func (world *sftpWorld) resolveOnServer(remotePath string) string {
	current := remotePath
	for range maxVirtualLinks {
		target, err := os.Readlink(world.serverPath(current))
		if err != nil {
			return world.serverPath(current)
		}
		if !path.IsAbs(target) {
			target = path.Join(path.Dir(current), target)
		}
		current = target
	}
	return world.serverPath(current)
}

// inodeViolations rend chaque écriture qui suit les liens et touche, côté serveur, une entrée de la bibliothèque
// (même fichier qu'une entrée de library), ou dont l'entrée a disparu sans porter un nom d'outil (sonde, temporaire).
func (world *sftpWorld) inodeViolations(requests []sftptest.Request, library map[string]treeEntry) []string {
	var violations []string
	for _, request := range requests {
		if !followsLinks(request) {
			continue
		}
		info, err := os.Stat(world.resolveOnServer(request.Path))
		isToolEntry := strings.Contains(path.Base(request.Path), toolEntryMarker)
		switch {
		case err != nil && !isToolEntry:
			violations = append(violations, fmt.Sprintf("unverifiable %+v", request))
		case err == nil && slices.ContainsFunc(slices.Collect(maps.Values(library)), func(entry treeEntry) bool { return os.SameFile(entry.info, info) }):
			violations = append(violations, fmt.Sprintf("library inode %+v", request))
		}
	}
	return violations
}

// toolEntryMarker : les sondes et temporaires de l'outil portent « .necromanciarr- » dans leur nom.
const toolEntryMarker = ".necromanciarr-"

func countWrites(requests []sftptest.Request) int {
	writes := 0
	for _, request := range requests {
		if _, isWrite := sftpWrittenPaths[request.Method]; isWrite {
			writes++
		}
	}
	return writes
}

// sftpWorld est l'arbre servi par le serveur, le registre de production (volume sftpfs paresseux, sonde avant toute
// racine d'écriture comme app.sftpVolumes.ensureProbed, que ce paquet ne peut importer) et les faux services.
type sftpWorld struct {
	*applyFixture
	server       *sftptest.Server
	volume       *sftpfs.Volume
	mapper       *linker.Mapper
	releases     []libraryprovider.Release
	hardHash     string
	softHash     string
	annexHash    string
	brokenLink   store.Link
	localLibrary string
	// preferredKinds est l'ordre des sortes de liens de Link (vide : hardlink, le linkType par défaut).
	preferredKinds []linker.Kind
}

func (world *sftpWorld) serverPath(remotePath string) string {
	return filepath.Join(world.server.Root, filepath.FromSlash(strings.TrimPrefix(remotePath, "/")))
}

func sftpStored(remotePath string) string {
	return fsys.Location{Volume: sftpVolumeName, Path: remotePath}.String()
}

func noEnvironment(string) (string, bool) { return "", false }

// probeEveryRoot reprend le contrôle posé par l'application sur chaque racine d'écriture sftp.
func probeEveryRoot(volume *sftpfs.Volume) func(fsys.WriteRoot) error {
	return func(root fsys.WriteRoot) error {
		_, err := volume.ProbeCapabilities(context.Background(), root)
		return err
	}
}

func newSFTPWorld(t *testing.T, options ...sftptest.Option) *sftpWorld {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the served tree needs symlinks planted on a Unix host")
	}
	server := sftptest.Start(t, options...)
	world := &sftpWorld{applyFixture: newApplyFixture(t), server: server}
	for _, dir := range []string{sftpLibraryDir, sftpLinkDir, sftpTorrentDir, sftpRepairDir} {
		sftptest.Mkdir(t, world.serverPath(dir))
	}
	world.localLibrary = world.serverPath(sftpLibraryDir)
	world.volume = sftpfs.NewLazy(server.Provider(t, sftpVolumeName), sftpfs.Deps{LibraryRoots: []string{sftpLibraryDir}, LookupEnv: noEnvironment})
	t.Cleanup(func() { _ = world.volume.Close() })
	registry, err := fsys.NewRegistry(map[string]fsys.Volume{"": local.New(), sftpVolumeName: world.volume},
		[]fsys.Location{{Volume: sftpVolumeName, Path: sftpLibraryDir}}, fsys.WithWriteRootCheck(sftpVolumeName, probeEveryRoot(world.volume)))
	if err != nil {
		t.Fatal(err)
	}
	world.mapper = linker.NewMapper([]config.PathRule{
		{From: sftpStored(sftpLibraryDir), To: arrLibraryDir}, {From: sftpStored(sftpLinkDir), To: clientLinkDir},
	})
	world.root, world.linkDir, world.registry, world.productionOpener = world.localLibrary, sftpStored(sftpLinkDir), registry, true
	world.applyFixture.mapper = world.mapper
	world.hardHash = world.publish(t, "Movie.Hard", []torrentEntry{{path: "hard.mkv", content: patternBytes(11, hardMovieSize), localName: "hard.mkv"}})
	world.softHash = world.publish(t, "Movie.Soft", []torrentEntry{{path: "soft.mkv", content: patternBytes(12, softMovieSize), localName: "soft.mkv"}})
	world.annexHash = world.publish(t, "Movie.Annex", []torrentEntry{
		{path: "annex.mkv", content: patternBytes(13, movieSize), localName: "annex.mkv"},
		{path: "annex.nfo", content: patternBytes(14, nfoSize)},
	})
	world.plantDuplicate(t)
	world.plantBrokenLink(t)
	return world
}

// publish pose les médias du torrent dans la bibliothèque distante, son .torrent chez le fournisseur factice et sa
// release dans la recherche ; rien n'est écrit en base.
func (world *sftpWorld) publish(t *testing.T, name string, entries []torrentEntry) string {
	t.Helper()
	raw := buildTestTorrent(t, name, entries)
	infohash := infohashOf(t, raw)
	world.fetcher.torrents[infohash] = raw
	releaseFiles := make([]libraryprovider.ReleaseFile, 0, len(entries))
	var total int64
	for _, entry := range entries {
		if entry.localName != "" {
			writeAgedFile(t, world.serverPath(sftpLibraryDir+"/"+entry.localName), entry.content)
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

func (world *sftpWorld) plantDuplicate(t *testing.T) {
	t.Helper()
	content := patternBytes(21, movieSize)
	writeAgedFile(t, world.serverPath(sftpLibraryDir+"/duplicate.mkv"), content)
	writeAgedFile(t, world.serverPath(sftpTorrentDir+"/duplicate.mkv"), content)
}

// plantBrokenLink pose un lien symbolique vers un nom disparu de la bibliothèque ; le fichier existe sous un autre nom.
func (world *sftpWorld) plantBrokenLink(t *testing.T) {
	t.Helper()
	renamedPath := world.serverPath(sftpLibraryDir + "/renamed.mkv")
	writeAgedFile(t, renamedPath, patternBytes(31, movieSize))
	oldTarget := sftpLibraryDir + "/old-name.mkv"
	if err := os.Symlink(oldTarget, world.serverPath(sftpRepairDir+"/movie.mkv")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	fingerprint, _, err := dedupe.HeadTailFingerprint(t.Context(), nil, nil, renamedPath, movieSize, time.Time{}, true)
	if err != nil {
		t.Fatal(err)
	}
	world.brokenLink = store.Link{
		Kind: "symlink", LinkPath: sftpStored(sftpRepairDir + "/movie.mkv"), TargetPath: sftpStored(oldTarget), Size: movieSize,
		State: store.LinkOK, Fingerprint: fingerprint, ModTime: agedLibraryTime, Volume: sftpVolumeName,
	}
}

// canonicalLibraryRoot est la racine de bibliothèque telle que le serveur la résout (RealPath), jamais un littéral.
func (world *sftpWorld) canonicalLibraryRoot(t *testing.T) string {
	t.Helper()
	return canonicalOnServer(t, world.registry, sftpLibraryDir)
}

func canonicalOnServer(t *testing.T, registry *fsys.Registry, remotePath string) string {
	t.Helper()
	view, name, err := registry.Read(fsys.Location{Volume: sftpVolumeName, Path: remotePath})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := view.Canonical(name)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func (world *sftpWorld) stageDeps() Deps {
	deps := world.applyDeps()
	deps.LibraryRoots, deps.Mapper, deps.ClientMapper = []string{sftpStored(sftpLibraryDir)}, world.mapper, world.mapper
	return deps
}

// sftpPhase est une étape du test : run rend son Result et check prouve qu'elle a vraiment travaillé. Chaque étape
// doit laisser au moins une requête au serveur, sauf Match (base et fournisseur seulement) ; writes en exige une
// d'écriture.
type sftpPhase struct {
	name          string
	writes        bool
	withoutServer bool
	run           func(t *testing.T) (Result, error)
	check         func(result Result) error
}

func (world *sftpWorld) phases() []sftpPhase {
	return []sftpPhase{
		{name: "scan", run: world.scan, check: expectAtLeast("processed", sftpLibraryFileCount, func(result Result) int { return result.Processed })},
		{name: "match", withoutServer: true, run: world.match, check: expectAtLeast("candidates created", 3, func(result Result) int { return result.CandidatesCreated })},
		{name: "verify", run: world.verify, check: expectAtLeast("proposals created", 3, func(result Result) int { return result.ProposalsCreated })},
		{name: "link", writes: true, run: world.link, check: expectAtLeast("linked", 3, func(result Result) int { return result.Linked })},
		{name: "seed", run: world.seed, check: expectAtLeast("seeded", 3, func(result Result) int { return result.Seeded + result.SeededPausedIncomplete })},
		{name: "force", run: world.force},
		{name: "dedupe dry-run", run: world.dedupe(true)},
		{name: "dedupe", writes: true, run: world.dedupe(false)},
		{name: "repair", writes: true, run: world.repair},
		{name: "doctor", run: world.doctor},
	}
}

// runPhases exécute les étapes dans l'ordre et rend la première qui n'a pas prouvé son travail.
func (world *sftpWorld) runPhases(t *testing.T, steps []sftpPhase) error {
	t.Helper()
	for _, step := range steps {
		requestsBefore := len(world.server.Requests())
		result, err := step.run(t)
		if err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
		sent := world.server.Requests()[requestsBefore:]
		switch writes := countWrites(sent); {
		case !step.withoutServer && len(sent) == 0:
			return fmt.Errorf("%s: expectedValue requests received by the server, gotValue none", step.name)
		case step.writes && writes == 0:
			return fmt.Errorf("%s: expectedValue write requests received by the server, gotValue none", step.name)
		}
		if step.check != nil {
			if err := step.check(result); err != nil {
				return fmt.Errorf("%s: %w", step.name, err)
			}
		}
	}
	return nil
}

func (world *sftpWorld) runEveryPhase(t *testing.T) {
	t.Helper()
	if err := world.runPhases(t, world.phases()); err != nil {
		t.Fatal(err)
	}
}

func (world *sftpWorld) scan(t *testing.T) (Result, error) {
	t.Helper()
	var items []arr.Item
	for _, name := range []string{"hard.mkv", "soft.mkv", "annex.mkv"} {
		items = append(items, arr.Item{Path: arrLibraryDir + "/" + name, TMDBID: 603})
	}
	deps := world.stageDeps()
	deps.ScanRoot, deps.ArrSources = LibraryScanner(world.store, world.registry), []ItemsSource{fakeArr{items: items}}
	result, err := Run(t.Context(), StageScan, deps)
	if err == nil && result.UnidentifiedFiles != sftpLibraryFileCount-len(items) {
		err = fmt.Errorf("expectedValue the remote files identified through the arr path mapping, gotValue %+v", result)
	}
	return result, err
}

func (world *sftpWorld) match(t *testing.T) (Result, error) {
	t.Helper()
	deps := world.stageDeps()
	deps.Sources = fakeChain{searcher: &fakeProvider{honourSizes: true, releases: world.releases}, fetcher: world.fetcher}
	return Run(t.Context(), StageMatch, deps)
}

func (world *sftpWorld) verify(t *testing.T) (Result, error) {
	t.Helper()
	return Run(t.Context(), StageVerify, world.deps())
}

// link accepte les deux full et le partial_annex lus après Verify, puis lie par hardlink (linkType par défaut).
func (world *sftpWorld) link(t *testing.T) (Result, error) {
	t.Helper()
	for _, infohash := range []string{world.hardHash, world.softHash} {
		world.accept(world.proposalOf(world.candidate(infohash).ID, store.ProposalFull).ID)
	}
	world.accept(world.annexProposal().ID)
	world.linkKinds = []linker.Kind{linker.Hardlink}
	if len(world.preferredKinds) > 0 {
		world.linkKinds = world.preferredKinds
	}
	return Run(t.Context(), StageLink, world.stageDeps())
}

func (world *sftpWorld) annexProposal() store.Proposal {
	return world.proposalOf(world.candidate(world.annexHash).ID, store.ProposalPartialAnnex)
}

func (world *sftpWorld) seed(t *testing.T) (Result, error) {
	t.Helper()
	for _, infohash := range []string{world.hardHash, world.softHash} {
		if _, chosen := world.client.guardResults[infohash]; !chosen {
			world.client.guardResults[infohash] = qbit.RecheckResult{Progress: 1, Complete: true}
		}
	}
	return Run(t.Context(), StageSeed, world.stageDeps())
}

// force tente d'isoler le média à cheval du partial_annex : sans copie côté serveur, le refus server_copy_unavailable
// tombe avant toute écriture.
func (world *sftpWorld) force(t *testing.T) (Result, error) {
	t.Helper()
	proposal := world.annexProposal()
	linkRoot := world.progressOf(proposal.ID).LinkRoot
	_, err := IsolateStraddlingMedia(t.Context(), world.stageDeps(), proposal, world.candidate(world.annexHash), linkRoot)
	var refusal *ForceRefusal
	if !errors.As(err, &refusal) || refusal.Reason != ReasonServerCopyUnavailable {
		return Result{}, fmt.Errorf("expectedValue the server_copy_unavailable refusal, gotValue %w", err)
	}
	return Result{}, nil
}

// planDedupe planifie le remplacement du doublon de /torrents par un lien vers la bibliothèque.
func (world *sftpWorld) planDedupe(t *testing.T, dryRun bool) ([]dedupe.Replacement, dedupe.Options, error) {
	t.Helper()
	library := []store.MediaFile{{Path: sftpStored(sftpLibraryDir + "/duplicate.mkv"), Size: movieSize, ModTime: agedLibraryTime}}
	remoteHashes := hashsource.NewSFTPExec(map[string]*sftpfs.Volume{sftpVolumeName: world.volume})
	options := dedupe.Options{
		Kinds: []linker.Kind{linker.Hardlink}, TorrentRoots: []string{sftpStored(sftpTorrentDir)}, Recorder: world.store,
		Registry: world.registry, DryRun: dryRun, Backend: hashsource.ByVolume(nil, remoteHashes),
	}
	replacements, err := dedupe.Plan(t.Context(), options.TorrentRoots, library, options)
	return replacements, options, err
}

func (world *sftpWorld) dedupe(dryRun bool) func(t *testing.T) (Result, error) {
	return func(t *testing.T) (Result, error) {
		t.Helper()
		replacements, options, err := world.planDedupe(t, dryRun)
		if err != nil || len(replacements) != 1 || replacements[0].Kind == 0 {
			return Result{}, fmt.Errorf("plan: gotValue %+v %w, expectedValue one applicable replacement", replacements, err)
		}
		requestsBeforeApply := len(world.server.Requests())
		result, err := dedupe.Apply(t.Context(), replacements, options)
		isApplied := err == nil && len(result.Outcomes) == 1 && result.Outcomes[0].Done != dryRun
		if !isApplied {
			return Result{}, fmt.Errorf("apply (dry run %v): gotValue %+v %w", dryRun, result, err)
		}
		if writes := countWrites(world.server.Requests()[requestsBeforeApply:]); dryRun && writes != 0 {
			return Result{}, fmt.Errorf("apply dry run: expectedValue no write request, gotValue %d", writes)
		}
		return Result{}, nil
	}
}

func (world *sftpWorld) repair(t *testing.T) (Result, error) {
	t.Helper()
	library := []store.MediaFile{{Path: sftpStored(sftpLibraryDir + "/renamed.mkv"), Size: movieSize, ModTime: agedLibraryTime}}
	scanOptions := repair.Options{LibraryRoots: []string{sftpStored(sftpLibraryDir)}, AutoApply: true, Registry: world.registry}
	repairs, _, err := repair.Scan(t.Context(), scanOptions, []store.Link{world.brokenLink}, library)
	if err != nil || len(repairs) != 1 {
		return Result{}, fmt.Errorf("scan: gotValue %+v %w, expectedValue one repair", repairs, err)
	}
	result, err := repair.Apply(t.Context(), repairs, repair.ApplyOptions{Roots: []string{sftpStored(sftpRepairDir)}, Registry: world.registry})
	if err != nil || !result.Outcomes[0].Done {
		return Result{}, fmt.Errorf("apply: gotValue %+v %w", result, err)
	}
	return Result{}, nil
}

func (world *sftpWorld) doctor(t *testing.T) (Result, error) {
	t.Helper()
	settings := config.Config{
		LibraryRoots: []string{sftpStored(sftpLibraryDir)}, LinkDirs: []string{sftpStored(sftpLinkDir)}, LinkType: "hardlink",
		Dedupe: config.Dedupe{TorrentRoots: []string{sftpStored(sftpTorrentDir)}},
		PathRules: []config.PathRule{
			{From: sftpStored(sftpLinkDir), To: clientLinkDir}, {From: sftpStored(sftpTorrentDir), To: "/downloads/torrents"},
		},
	}
	var selected []doctor.Check
	for _, check := range doctor.Build(settings, doctor.Deps{Registry: world.registry}) {
		if slices.Contains(doctorCheckIDs, check.ID) {
			selected = append(selected, check)
		}
	}
	report := doctor.Run(t.Context(), selected)
	if len(report.Checks) != len(doctorCheckIDs) {
		return Result{}, fmt.Errorf("gotValue %d checks, expectedValue %d", len(report.Checks), len(doctorCheckIDs))
	}
	for _, outcome := range report.Checks {
		isRefused := outcome.Result.Status == doctor.Fail || (outcome.ID == doctor.IDLibraryRoots && outcome.Result.Status != doctor.OK)
		if isRefused {
			return Result{}, fmt.Errorf("%s: gotValue %v %s, expectedValue library-roots ok and no failure", outcome.ID, outcome.Result.Status, outcome.Result.Code)
		}
	}
	return Result{}, nil
}

func TestSFTPRequestClasses_coverEverySftptestMethodOnce(t *testing.T) {
	classified := slices.Concat(sftpReadMethods, slices.Collect(maps.Keys(sftpWrittenPaths)), sftpForbiddenMethods, sftpCommandMethods)
	slices.Sort(classified)
	valeurAttendue := slices.Clone(sftptest.Methods)
	slices.Sort(valeurAttendue)

	if !slices.Equal(classified, valeurAttendue) {
		t.Fatalf("valeurObtenue %v, valeurAttendue each of %v classified exactly once", classified, valeurAttendue)
	}
}

func TestSFTP_noWriteRequestUnderLibraryRoot_endToEnd(t *testing.T) {
	world := newSFTPWorld(t, sftptest.WithGNUExec())
	before := snapshotTree(t, world.localLibrary)

	world.runEveryPhase(t)

	world.assertLibraryUntouched(t, before)
	world.assertOnlyTableCommands(t)
}

// assertOnlyTableCommands : l'exécution distante a servi (stat et empreintes en lots), chaque ligne reçue est une
// ligne constante de la table et aucune ne porte d'octet d'un chemin de la bibliothèque (ils passent par stdin).
func (world *sftpWorld) assertOnlyTableCommands(t *testing.T) {
	t.Helper()
	capabilities, err := world.volume.Capabilities(t.Context())
	if err != nil || !capabilities.RemoteStat || len(capabilities.RemoteHash) == 0 {
		t.Fatalf("valeurObtenue %+v (%v), valeurAttendue stat et empreintes distants prouvés par la sonde", capabilities, err)
	}
	hashedLibraryFile := false
	for _, session := range world.server.ExecSessions() {
		if !slices.Contains(sftpfs.GuardLines(), session.Command) || strings.Contains(session.Command, sftpLibraryDir) {
			t.Errorf("valeurObtenue %q, valeurAttendue une ligne constante de la table, sans chemin", session.Command)
		}
		isHashBatch := strings.Contains(session.Command, "sum' '-z'")
		hashedLibraryFile = hashedLibraryFile || (isHashBatch && strings.Contains(string(session.Stdin), sftpLibraryDir+"/"))
	}
	if !hashedLibraryFile {
		t.Fatal("valeurAttendue l'empreinte du doublon de la bibliothèque calculée par l'hôte (dedupe), valeurObtenue aucune")
	}
}

// Link par lien symbolique d'abord : la garde d'ordre des arguments passe par Link ; le texte de la cible désigne la
// bibliothèque (côté lecture), le lien créé reste sous /links.
func TestSFTP_noWriteRequestUnderLibraryRoot_endToEndWithSymlinkLinks(t *testing.T) {
	world := newSFTPWorld(t)
	world.preferredKinds = []linker.Kind{linker.Symlink, linker.Hardlink}
	before := snapshotTree(t, world.localLibrary)

	world.runEveryPhase(t)

	symlinkIntoLibrary := func(request sftptest.Request) bool {
		return request.Method == "Symlink" && isUnderRoot(request.Path, sftpLinkDir) && isUnderRoot(request.Target, sftpLibraryDir)
	}
	if !slices.ContainsFunc(world.server.Requests(), symlinkIntoLibrary) {
		t.Fatal("expectedValue Link to create symbolic links under /links that point into the library")
	}
	world.assertLibraryUntouched(t, before)
}

func (world *sftpWorld) assertLibraryUntouched(t *testing.T, before map[string]treeEntry) {
	t.Helper()
	if violations := requestViolations(world.server.Requests(), world.canonicalLibraryRoot(t)); len(violations) != 0 {
		t.Fatalf("expectedValue no write request under the library, gotValue %v", violations)
	}
	if violations := world.inodeViolations(world.server.Requests(), before); len(violations) != 0 {
		t.Fatalf("expectedValue no write through a library inode, gotValue %v", violations)
	}
	if differences := treeDifferences(before, snapshotTree(t, world.localLibrary)); len(differences) != 0 {
		t.Fatalf("expectedValue the library tree unchanged, gotValue changes in %v", differences)
	}
}

// plantInLibrary est l'étape malveillante : un volume et un registre qui ignorent tous deux la bibliothèque laissent
// ouvrir une racine d'écriture sur elle (le volume du monde la refuserait : il connaît ses racines de bibliothèque).
func (world *sftpWorld) plantInLibrary(t *testing.T) {
	t.Helper()
	careless := sftpfs.NewLazy(world.server.Provider(t, sftpVolumeName), sftpfs.Deps{LookupEnv: noEnvironment})
	t.Cleanup(func() { _ = careless.Close() })
	registry, err := fsys.NewRegistry(map[string]fsys.Volume{sftpVolumeName: careless}, []fsys.Location{{Volume: sftpVolumeName, Path: "/elsewhere"}})
	if err != nil {
		t.Fatal(err)
	}
	root, err := registry.WriteRoot(fsys.Location{Volume: sftpVolumeName, Path: sftpLibraryDir})
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
	if err := root.MkdirAll("planted-dir", 0o750); err != nil {
		t.Fatal(err)
	}
	if err := root.Symlink("planted.mkv", "planted-link"); err != nil {
		t.Fatal(err)
	}
}

// plantedMethods sont les écritures de plantInLibrary que l'assertion doit voir, chacune sur son chemin planté.
var plantedMethods = map[string]string{"Mkdir": "/planted-dir", "Symlink": "/planted-link"}

func TestSFTP_noWriteRequestUnderLibraryRoot_catchesAMaliciousWriter(t *testing.T) {
	world := newSFTPWorld(t)
	before := snapshotTree(t, world.localLibrary)
	world.runEveryPhase(t)

	world.plantInLibrary(t)

	violations := requestViolations(world.server.Requests(), world.canonicalLibraryRoot(t))
	if !slices.ContainsFunc(violations, func(violation string) bool { return strings.Contains(violation, sftpLibraryDir+"/planted.mkv") }) {
		t.Fatalf("expectedValue the planted file caught by the request assertion, gotValue %v", violations)
	}
	for method, plantedPath := range plantedMethods {
		caught := func(violation string) bool {
			return strings.Contains(violation, "Method:"+method+" Path:"+sftpLibraryDir+plantedPath+" ")
		}
		if !slices.ContainsFunc(violations, caught) {
			t.Fatalf("expectedValue the planted %s caught by the request assertion, gotValue %v", method, violations)
		}
	}
	differences := treeDifferences(before, snapshotTree(t, world.localLibrary))
	if !slices.Equal(differences, []string{".", "planted-dir", "planted-link", "planted.mkv"}) {
		t.Fatalf("expectedValue the planted entries and their directory seen by the snapshot, gotValue %v", differences)
	}
}

// La racine de l'assertion est celle que le serveur résout : un alias de la bibliothèque (lien symbolique) donnerait un
// préfixe que les requêtes, émises sur le chemin canonique, ne portent jamais.
func TestSFTP_noWriteRequestUnderLibraryRoot_checksTheCanonicalRootBySegments(t *testing.T) {
	world := newSFTPWorld(t)
	if err := os.Symlink(sftpLibraryDir, world.serverPath("/library-alias")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	planted := []sftptest.Request{{Method: "Put", Path: sftpLibraryDir + "/planted.mkv"}}
	sibling := []sftptest.Request{{Method: "Put", Path: sftpLibraryDir + "-sibling/planted.mkv"}}

	canonical := canonicalOnServer(t, world.registry, "/library-alias")

	if canonical != sftpLibraryDir {
		t.Fatalf("valeurObtenue %q, valeurAttendue the alias resolved to %s", canonical, sftpLibraryDir)
	}
	if missed := requestViolations(planted, "/library-alias"); len(missed) != 0 {
		t.Fatalf("valeurObtenue %v, valeurAttendue the raw alias blind to the write (the trap this test pins)", missed)
	}
	if caught := requestViolations(planted, canonical); len(caught) != 1 {
		t.Fatalf("valeurObtenue %v, valeurAttendue the write caught under the canonical root", caught)
	}
	if siblingCaught := requestViolations(sibling, canonical); len(siblingCaught) != 0 {
		t.Fatalf("valeurObtenue %v, valeurAttendue a sibling directory outside the library", siblingCaught)
	}
}

// TestSFTP_noWriteRequestUnderLibraryRoot_failsWhenAStageDidNotRun : une étape remplacée par une étape vide doit faire
// échouer la preuve, sinon le test de bout en bout pourrait passer sans rien exercer.
func TestSFTP_noWriteRequestUnderLibraryRoot_failsWhenAStageDidNotRun(t *testing.T) {
	for _, skipped := range []string{"scan", "match", "verify", "link", "seed", "force", "dedupe dry-run", "dedupe", "repair", "doctor"} {
		t.Run(skipped, func(t *testing.T) {
			world := newSFTPWorld(t)
			steps := world.phases()
			for stepIndex := range steps {
				if steps[stepIndex].name == skipped {
					steps[stepIndex].run = func(*testing.T) (Result, error) { return Result{}, nil }
				}
			}

			err := world.runPhases(t, steps)

			if err == nil || !strings.HasPrefix(err.Error(), skipped+":") {
				t.Fatalf("expectedValue the empty %s stage reported, gotValue %v", skipped, err)
			}
		})
	}
}

// Seed d'un lien distant (règles C5) : ajout en pause sans skip_checking, recontrôle du client, reprise seulement à
// 100 % ; le full encore incomplet et le partial_annex restent en pause.
func TestSFTP_endToEndSeedsWithClientRecheck(t *testing.T) {
	world := newSFTPWorld(t)
	world.skipRecheck = true
	world.client.guardResults[world.softHash] = qbit.RecheckResult{Progress: 0.5}
	steps := world.phases()[:5]
	if err := world.runPhases(t, steps); err != nil {
		t.Fatal(err)
	}

	for _, infohash := range []string{world.hardHash, world.softHash, world.annexHash} {
		added, present := world.client.added[infohash]
		if !present || added.options.SkipChecking || added.options.Start || !strings.HasPrefix(added.savePath, clientLinkDir+"/") {
			t.Fatalf("%s: valeurObtenue %+v, valeurAttendue a paused add under %s without skip_checking", infohash, added, clientLinkDir)
		}
	}
	if valeurObtenue := world.client.resumed; !slices.Equal(valeurObtenue, []string{world.hardHash}) {
		t.Fatalf("valeurObtenue resumed %v, valeurAttendue only the torrent the client confirmed at 100 %%", valeurObtenue)
	}
	guarded := slices.Sorted(slices.Values(world.client.guardCalls))
	if valeurAttendue := slices.Sorted(slices.Values([]string{world.hardHash, world.softHash})); !slices.Equal(guarded, valeurAttendue) {
		t.Fatalf("valeurObtenue rechecks %v, valeurAttendue the two full torrents rechecked by the client", guarded)
	}
}

// writesOutsideProbes rend les écritures qui ne sont pas une entrée de sonde directement sous dir.
func writesOutsideProbes(requests []sftptest.Request, dir string) []sftptest.Request {
	var outside []sftptest.Request
	for _, request := range requests {
		writtenPaths, isWrite := sftpWrittenPaths[request.Method]
		if !isWrite {
			continue
		}
		for _, written := range writtenPaths(request) {
			isProbeEntry := path.Dir(written) == dir && strings.HasPrefix(path.Base(written), ".necromanciarr-probe-")
			if !isProbeEntry {
				outside = append(outside, request)
				break
			}
		}
	}
	return outside
}

// Une session neuve (aucun Link avant) : le plan sonde d'abord le dossier torrent, puis lit le remplacement atomique
// prouvé ; seules les entrées de sonde y sont écrites.
func TestSFTP_freshSessionDedupePlanProbesBeforeReadingAtomicReplace(t *testing.T) {
	world := newSFTPWorld(t)

	replacements, _, err := world.planDedupe(t, true)

	if err != nil || len(replacements) != 1 || replacements[0].Kind == 0 {
		t.Fatalf("valeurObtenue %+v %v, valeurAttendue one applicable replacement on a fresh session", replacements, err)
	}
	if outside := writesOutsideProbes(world.server.Requests(), sftpTorrentDir); len(outside) != 0 {
		t.Fatalf("valeurObtenue %+v, valeurAttendue only probe entries written in %s", outside, sftpTorrentDir)
	}
}

func TestSFTP_freshSessionDedupePlanWithoutPosixRenameIsTruthfullyRefused(t *testing.T) {
	world := newSFTPWorld(t, sftptest.WithoutExtension("posix-rename@openssh.com"))

	replacements, _, err := world.planDedupe(t, true)

	if err != nil || len(replacements) != 1 || !errors.Is(replacements[0].Reason, fsys.ErrNoAtomicReplace) {
		t.Fatalf("valeurObtenue %+v %v, valeurAttendue the replacement refused with fsys.ErrNoAtomicReplace", replacements, err)
	}
	if !slices.ContainsFunc(world.server.Requests(), func(request sftptest.Request) bool { return request.Method == "StatVFS" }) {
		t.Fatal("valeurAttendue the volume probed before the refusal")
	}
}

var errProbeRefused = errors.New("probe refused")

// Une sonde en échec se dit telle quelle : le refus ne prétend jamais que le serveur manque de posix-rename.
func TestSFTP_freshSessionDedupePlanReportsAFailedProbeAsSuch(t *testing.T) {
	world := newSFTPWorld(t)
	registry, err := fsys.NewRegistry(map[string]fsys.Volume{"": local.New(), sftpVolumeName: world.volume},
		[]fsys.Location{{Volume: sftpVolumeName, Path: sftpLibraryDir}},
		fsys.WithWriteRootCheck(sftpVolumeName, func(fsys.WriteRoot) error { return errProbeRefused }))
	if err != nil {
		t.Fatal(err)
	}
	world.registry = registry

	replacements, _, err := world.planDedupe(t, true)

	if err != nil || len(replacements) != 1 {
		t.Fatalf("valeurObtenue %+v %v, valeurAttendue one replacement", replacements, err)
	}
	reason := replacements[0].Reason
	if replacements[0].Kind != 0 || !errors.Is(reason, errProbeRefused) || errors.Is(reason, fsys.ErrNoAtomicReplace) {
		t.Fatalf("valeurObtenue %v, valeurAttendue the probe failure, never fsys.ErrNoAtomicReplace", reason)
	}
}

// Un lien dur de la bibliothèque sous /links, et un lien symbolique planté vers elle : une ouverture en écriture ou un
// Setstat par ces chemins écrirait la bibliothèque sans que leur chemin soit dessous.
func TestSFTP_noWriteRequestUnderLibraryRoot_flagsWritesThroughLibraryInodes(t *testing.T) {
	world := newSFTPWorld(t)
	before := snapshotTree(t, world.localLibrary)
	if err := world.runPhases(t, world.phases()[:4]); err != nil {
		t.Fatal(err)
	}
	hardlinkPath := world.linkPathOf(t, world.hardHash)
	if err := os.Symlink(sftpLibraryDir+"/soft.mkv", world.serverPath(sftpLinkDir+"/planted-link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	writes := []sftptest.Request{
		{Method: "Put", Path: hardlinkPath, Flags: sftptest.FlagWrite | sftptest.FlagTruncate},
		{Method: "Setstat", Path: sftpLinkDir + "/planted-link"},
		{Method: "Open", Path: sftpLinkDir + "/vanished.mkv", Flags: sftptest.FlagRead | sftptest.FlagWrite},
		{Method: "Put", Path: hardlinkPath, Flags: sftptest.FlagWrite | sftptest.FlagCreate | sftptest.FlagExclusive},
	}

	valeurObtenue := world.inodeViolations(writes, before)

	valeurAttendue := []string{"library inode", "library inode", "unverifiable"}
	if len(valeurObtenue) != len(valeurAttendue) {
		t.Fatalf("valeurObtenue %v, valeurAttendue %v (the exclusive create, which never follows, is not flagged)", valeurObtenue, valeurAttendue)
	}
	for violationIndex, prefix := range valeurAttendue {
		if !strings.HasPrefix(valeurObtenue[violationIndex], prefix) {
			t.Fatalf("valeurObtenue %v, valeurAttendue %v", valeurObtenue, valeurAttendue)
		}
	}
}

// linkPathOf rend le chemin, vu par le serveur, du lien du film de ce torrent.
func (world *sftpWorld) linkPathOf(t *testing.T, infohash string) string {
	t.Helper()
	for _, link := range world.linksOf() {
		if strings.Contains(link.LinkPath, infohash) && strings.HasSuffix(link.LinkPath, ".mkv") {
			location, err := fsys.StoredLocation(link.LinkPath)
			if err != nil {
				t.Fatal(err)
			}
			return location.Path
		}
	}
	t.Fatalf("no recorded link for %s", infohash)
	return ""
}

func TestSFTP_requestViolations_refusesUnknownExtendedRequests(t *testing.T) {
	unknown := []sftptest.Request{{Method: sftptest.ExtendedMethodPrefix + "fsync@openssh.com"}}

	valeurObtenue := requestViolations(unknown, sftpLibraryDir)

	if len(valeurObtenue) != 1 || !strings.HasPrefix(valeurObtenue[0], "unclassified-extended") {
		t.Fatalf("valeurObtenue %v, valeurAttendue the unknown extended request refused as unclassified-extended", valeurObtenue)
	}
}
