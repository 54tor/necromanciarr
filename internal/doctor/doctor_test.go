package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"necromanciarr/internal/config"
	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/fsystest"
	"necromanciarr/internal/hashsource"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/library"
	"necromanciarr/internal/libraryprovider"
	"necromanciarr/internal/linker"
	"necromanciarr/internal/qbit"
	"necromanciarr/internal/store"
)

func fixedCheck(id string, status Status) Check {
	return Check{ID: id, TitleKey: i18n.KeyDoctorTitleConfig, Run: func(context.Context) Result { return Result{Status: status, Code: CodeOK} }}
}

func resultOf(t *testing.T, report Report, id string) Outcome {
	t.Helper()
	for _, named := range report.Checks {
		if named.ID == id {
			return named
		}
	}
	t.Fatalf("check %q missing from report %+v", id, report)
	return Outcome{}
}

func TestRun_reportsWorstStatusAndExitCode(t *testing.T) {
	testCases := []struct {
		name             string
		statuses         []Status
		expectedWorst    Status
		expectedExitCode int
	}{
		{"all ok", []Status{OK, OK}, OK, 0},
		{"warn and skipped do not fail", []Status{OK, Warn, Skipped}, Warn, 0},
		{"one fail fails", []Status{OK, Fail, Warn}, Fail, 1},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var checks []Check
			for index, status := range testCase.statuses {
				checks = append(checks, fixedCheck(string(rune('a'+index)), status))
			}

			report := Run(context.Background(), checks)

			if report.Worst != testCase.expectedWorst || ExitCode(report) != testCase.expectedExitCode {
				t.Fatalf("expectedValue %v/%d, gotValue %v/%d", testCase.expectedWorst, testCase.expectedExitCode, report.Worst, ExitCode(report))
			}
		})
	}
}

func TestRun_contextTimeoutTurnsCheckIntoFailWithoutBlockingOthers(t *testing.T) {
	blocking := Check{ID: "slow", TitleKey: i18n.KeyDoctorTitleConfig, Run: func(ctx context.Context) Result {
		<-ctx.Done()
		return Result{Status: OK, Code: CodeOK}
	}}

	report := Run(context.Background(), []Check{blocking, fixedCheck("next", OK)}, WithTimeout(20*time.Millisecond))

	if resultOf(t, report, "slow").Result.Status != Fail || resultOf(t, report, "next").Result.Status != OK {
		t.Fatalf("gotValue %+v", report)
	}
}

func TestRun_checkPanicIsReportedAsFail(t *testing.T) {
	panicking := Check{ID: "boom", TitleKey: i18n.KeyDoctorTitleConfig, Run: func(context.Context) Result { panic("kaboom") }}

	report := Run(context.Background(), []Check{panicking, fixedCheck("next", OK)})

	boom := resultOf(t, report, "boom")
	if boom.Result.Status != Fail || !strings.Contains(detailOf(boom.Result), "(string)") || resultOf(t, report, "next").Result.Status != OK {
		t.Fatalf("gotValue %+v", report)
	}
}

func TestRun_cancelledContextSkipsRemainingChecks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	report := Run(ctx, []Check{fixedCheck("a", OK)})

	if resultOf(t, report, "a").Result.Status != Skipped {
		t.Fatalf("gotValue %+v", report)
	}
}

type fakeSearcher struct {
	search func(query libraryprovider.Query) ([]libraryprovider.Release, error)
}

func (searcher fakeSearcher) Search(_ context.Context, query libraryprovider.Query) ([]libraryprovider.Release, error) {
	return searcher.search(query)
}

func depsWithSearcher(searcher Searcher) Deps {
	return Deps{NewSearcher: func(config.Config) (Searcher, error) { return searcher, nil }}
}

func TestChecks_libraryApiWarnsWhenSizeFilterIgnored(t *testing.T) {
	ignoring := fakeSearcher{search: func(libraryprovider.Query) ([]libraryprovider.Release, error) {
		return []libraryprovider.Release{{Infohash: "a", Files: []libraryprovider.ReleaseFile{{Path: "x", Size: 42}}}}, nil
	}}
	honoring := fakeSearcher{search: func(query libraryprovider.Query) ([]libraryprovider.Release, error) {
		if len(query.Sizes) == 0 {
			return []libraryprovider.Release{{Infohash: "a", Files: []libraryprovider.ReleaseFile{{Path: "x", Size: 42}}}}, nil
		}
		return []libraryprovider.Release{{Infohash: "b", Files: []libraryprovider.ReleaseFile{{Path: "y", Size: 1}}}}, nil
	}}
	settings := config.Config{LibraryAPI: "http://example.com"}

	gotIgnoring := checkLibraryAPI(context.Background(), settings, depsWithSearcher(ignoring).withDefaults())
	gotHonoring := checkLibraryAPI(context.Background(), settings, depsWithSearcher(honoring).withDefaults())

	if gotIgnoring.Status != Warn || hintOf(gotIgnoring) == "" {
		t.Fatalf("ignoring: gotValue %+v", gotIgnoring)
	}
	if gotHonoring.Status != OK {
		t.Fatalf("honoring: gotValue %+v", gotHonoring)
	}
}

func TestChecks_libraryApiReportsTypedErrorClassWithoutBody(t *testing.T) {
	denied := fakeSearcher{search: func(libraryprovider.Query) ([]libraryprovider.Release, error) {
		return nil, errors.Join(libraryprovider.ErrAccessDenied, errors.New("body with SECRETBODY"))
	}}

	got := checkLibraryAPI(context.Background(), config.Config{LibraryAPI: "http://example.com"}, depsWithSearcher(denied).withDefaults())

	if got.Status != Fail || !strings.Contains(detailOf(got), "access denied") || strings.Contains(detailOf(got), "SECRETBODY") {
		t.Fatalf("gotValue %+v", got)
	}
}

func TestChecks_linkFeasibilityNamesSameVolumeFixOnCrossDevice(t *testing.T) {
	linkDirectory := t.TempDir()
	deps := Deps{
		Probe: func(string) (linker.Capabilities, error) {
			return linker.Capabilities{Hardlink: true, Symlink: true}, nil
		},
		SameVolume: func(string, string) (bool, error) {
			return false, nil
		},
		FilesystemKind: func(string) (linker.FilesystemKind, error) { return linker.Local, nil },
	}.withDefaults()
	settings := config.Config{LibraryRoots: []string{"/home/u/library"}, LinkDirs: []string{linkDirectory}, LinkType: "hardlink"}

	got := checkLinkFeasibility(settings, deps)

	if got.Status != Fail || !strings.Contains(hintOf(got), "same mounted volume") {
		t.Fatalf("gotValue %+v", got)
	}
}

func TestChecks_linkFeasibilityDoesNotProbeInsideLibraryRoot(t *testing.T) {
	libraryRoot, outside := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(libraryRoot, "links"), 0o750); err != nil {
		t.Fatal(err)
	}
	var probed []string
	deps := Deps{
		Probe: func(directory string) (linker.Capabilities, error) {
			probed = append(probed, directory)
			return linker.Capabilities{Hardlink: true}, nil
		},
		SameVolume: func(string, string) (bool, error) { return true, nil },
	}.withDefaults()
	settings := config.Config{
		LibraryRoots: []string{libraryRoot},
		LinkDirs:     []string{filepath.Join(libraryRoot, "links"), outside},
		LinkType:     "hardlink",
	}

	got := checkLinkFeasibility(settings, deps)

	if !reflect.DeepEqual(probed, []string{outside}) {
		t.Fatalf("expectedValue [%s], gotValue %v", outside, probed)
	}
	if got.Status != Fail || !strings.Contains(detailOf(got), "inside a library root") {
		t.Fatalf("gotValue %+v", got)
	}
}

func TestChecks_linkFeasibilityNeverProbesThroughSymlinkIntoLibraryRoot(t *testing.T) {
	libraryRoot, outside := t.TempDir(), t.TempDir()
	linksInsideLibrary := filepath.Join(libraryRoot, "links")
	if err := os.Mkdir(linksInsideLibrary, 0o750); err != nil {
		t.Fatal(err)
	}
	symlinkedDirectory := filepath.Join(outside, "innocent")
	if err := os.Symlink(linksInsideLibrary, symlinkedDirectory); err != nil {
		t.Fatal(err)
	}
	deps := Deps{
		Probe: func(directory string) (linker.Capabilities, error) {
			t.Errorf("Probe must not be called, got %s", directory)
			return linker.Capabilities{}, nil
		},
		SameVolume: func(string, string) (bool, error) { return true, nil },
	}.withDefaults()
	settings := config.Config{LibraryRoots: []string{libraryRoot}, LinkDirs: []string{symlinkedDirectory}, LinkType: "hardlink"}

	got := checkLinkFeasibility(settings, deps)

	if got.Status != Fail || !strings.Contains(detailOf(got), "inside a library root") {
		t.Fatalf("gotValue %+v", got)
	}
}

func TestChecks_linkFeasibilityReportsMissingDirectoryWithoutProbing(t *testing.T) {
	deps := Deps{Probe: func(string) (linker.Capabilities, error) {
		t.Error("Probe must not be called")
		return linker.Capabilities{}, nil
	}}.withDefaults()

	got := checkLinkFeasibility(config.Config{LinkDirs: []string{"/home/u/absent-links"}, LinkType: "hardlink"}, deps)

	if got.Status != Fail || !strings.Contains(detailOf(got), "cannot be resolved") {
		t.Fatalf("gotValue %+v", got)
	}
}

func TestChecks_configDirRefusesDatabaseInsideLibraryRoot(t *testing.T) {
	libraryRoot := t.TempDir()
	opened := false
	deps := Deps{OpenStore: func(string) (io.Closer, error) {
		opened = true
		return nil, errors.New("must not open")
	}}.withDefaults()
	settings := config.Config{ConfigDir: t.TempDir(), DBPath: filepath.Join(libraryRoot, "db.sqlite"), LibraryRoots: []string{libraryRoot}}

	got := checkConfigDir(settings, deps)

	if got.Status != Fail || opened || hintOf(got) == "" {
		t.Fatalf("opened %v, gotValue %+v", opened, got)
	}
}

func TestChecks_libraryRootsFollowSymlinksAndSkipUnreadableSubdirectories(t *testing.T) {
	realRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(realRoot, "film.mkv"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkedRoot := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(realRoot, symlinkedRoot); err != nil {
		t.Fatal(err)
	}
	mediaTarget := filepath.Join(t.TempDir(), "target.bin")
	if err := os.WriteFile(mediaTarget, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkedMediaRoot := t.TempDir()
	if err := os.Symlink(mediaTarget, filepath.Join(linkedMediaRoot, "episode.mkv")); err != nil {
		t.Fatal(err)
	}
	unreadableRoot := t.TempDir()
	lockedDirectory := filepath.Join(unreadableRoot, "locked")
	if err := os.Mkdir(lockedDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unreadableRoot, "z-film.mkv"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lockedDirectory, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(lockedDirectory, 0o750) })

	for name, root := range map[string]string{"symlinked root": symlinkedRoot, "symlinked media file": linkedMediaRoot} {
		if got := checkLibraryRoots(context.Background(), config.Config{LibraryRoots: []string{root}}, Deps{}); got.Status != OK {
			t.Fatalf("%s: gotValue %+v", name, got)
		}
	}
	got := checkLibraryRoots(context.Background(), config.Config{LibraryRoots: []string{unreadableRoot}}, Deps{})
	isRoot := os.Geteuid() == 0
	if got.Status != OK || (!isRoot && !strings.Contains(detailOf(got), "unreadable subdirectories skipped: 1")) {
		t.Fatalf("unreadable subdirectory: gotValue %+v", got)
	}
}

func TestChecks_libraryApiSizeFilterIsNotVerifiableWithoutUsableResults(t *testing.T) {
	testCases := map[string][]libraryprovider.Release{
		"no result":      nil,
		"only file-less": {{Infohash: "a"}},
	}
	for name, releases := range testCases {
		t.Run(name, func(t *testing.T) {
			searcher := fakeSearcher{search: func(libraryprovider.Query) ([]libraryprovider.Release, error) { return releases, nil }}

			got := checkLibraryAPI(context.Background(), config.Config{LibraryAPI: "http://example.com"}, depsWithSearcher(searcher).withDefaults())

			if got.Status != OK || !strings.Contains(detailOf(got), "not verifiable") {
				t.Fatalf("gotValue %+v", got)
			}
		})
	}
}

func TestRun_panicMessageNeverLeaksABareSecret(t *testing.T) {
	const bareSecret = "BAREPANICTOKEN9"
	settings := config.Config{AuthToken: bareSecret, ListenAddr: "127.0.0.1:1", Radarr: []config.ArrService{{Kind: "radarr", BaseURL: "http://example.com", APIKey: "LONGARRKEY1"}}}
	deps := Deps{NewPinger: func(config.ArrService) Pinger { panic("boom " + bareSecret) }}

	report := Run(context.Background(), Build(settings, deps))

	arrResult := resultOf(t, report, "arr:radarr:0")
	rendered, _ := MarshalJSONReport(report, englishTranslator)
	if arrResult.Result.Status != Fail || strings.Contains(string(rendered), bareSecret) || !strings.Contains(detailOf(arrResult.Result), "check panicked") {
		t.Fatalf("gotValue %s", rendered)
	}
}

func TestRun_parentCancellationDuringCheckIsSkipped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	blocking := Check{ID: "slow", TitleKey: i18n.KeyDoctorTitleConfig, Run: func(checkCtx context.Context) Result {
		cancel()
		<-checkCtx.Done()
		return Result{Status: OK, Code: CodeOK}
	}}

	report := Run(ctx, []Check{blocking})

	if resultOf(t, report, "slow").Result.Status != Skipped || ExitCode(report) != 0 {
		t.Fatalf("gotValue %+v", report)
	}
}

func TestChecks_crossSeedSkippedWhenNotConfigured(t *testing.T) {
	got := checkCrossSeed(context.Background(), config.Config{CrossSeedMode: "auto"}, Deps{}.withDefaults())

	if got.Status != Skipped {
		t.Fatalf("gotValue %+v", got)
	}
}

func TestChecks_crossSeedWarnsWhenDaemonUnreachableInDelegateMode(t *testing.T) {
	settings := config.Config{CrossSeedMode: "delegate", CrossSeed: &config.CrossSeedDaemon{BaseURL: "http://127.0.0.1:2468"}}
	deps := Deps{Reachable: func(context.Context, string) bool { return false }}.withDefaults()

	got := checkCrossSeed(context.Background(), settings, deps)

	if got.Status != Warn || !strings.Contains(hintOf(got), "unreachable") {
		t.Fatalf("gotValue %+v", got)
	}
}

func TestChecks_crossSeedUnreachableDaemonHintNamesTheTwoSettings(t *testing.T) {
	settings := config.Config{CrossSeedMode: "auto", CrossSeed: &config.CrossSeedDaemon{BaseURL: "http://localhost:2468"}}
	deps := Deps{Reachable: func(context.Context, string) bool { return false }}.withDefaults()

	got := checkCrossSeed(context.Background(), settings, deps)

	valeurObtenue := hintOf(got)
	if !strings.Contains(valeurObtenue, "crossSeedUrl") || !strings.Contains(valeurObtenue, "crossSeedApiKey") {
		t.Fatalf("valeurObtenue %q, valeurAttendue a hint naming crossSeedUrl and crossSeedApiKey", valeurObtenue)
	}
}

func TestChecks_authFailsOnPublicListenWithoutToken(t *testing.T) {
	testCases := []struct {
		name     string
		settings config.Config
		expected Status
	}{
		{"public without token", config.Config{ListenAddr: "0.0.0.0:8787"}, Fail},
		{"public with token", config.Config{ListenAddr: "0.0.0.0:8787", AuthToken: "t"}, OK},
		{"loopback without token", config.Config{ListenAddr: "127.0.0.1:8787"}, OK},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if gotValue := checkAuth(testCase.settings).Status; gotValue != testCase.expected {
				t.Fatalf("expectedValue %v, gotValue %v", testCase.expected, gotValue)
			}
		})
	}
}

func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		snapshot[path] = info.ModTime().String() + info.Mode().String()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestChecks_libraryRootsAreNeverWritten(t *testing.T) {
	libraryRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(libraryRoot, "movies"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(libraryRoot, "movies", "film.mkv"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, libraryRoot)
	deps := Deps{
		SameVolume: func(string, string) (bool, error) { return true, nil },
		Probe:      func(string) (linker.Capabilities, error) { return linker.Capabilities{}, nil },
	}
	settings := config.Config{LibraryRoots: []string{libraryRoot}, LinkDirs: []string{t.TempDir()}, LinkType: "hardlink"}

	report := Run(context.Background(), Build(settings, deps))

	if resultOf(t, report, IDLibraryRoots).Result.Status != OK {
		t.Fatalf("gotValue %+v", resultOf(t, report, IDLibraryRoots))
	}
	if after := snapshotTree(t, libraryRoot); !reflect.DeepEqual(before, after) {
		t.Fatalf("library root changed: before %v after %v", before, after)
	}
}

func TestChecks_libraryRootsWarnsWhenEmptyAndFailsWhenMissing(t *testing.T) {
	emptyRoot := t.TempDir()

	got := checkLibraryRoots(context.Background(), config.Config{LibraryRoots: []string{emptyRoot, filepath.Join(emptyRoot, "absent")}}, Deps{})

	if got.Status != Fail || !strings.Contains(detailOf(got), "no media file") || !strings.Contains(detailOf(got), "not found") {
		t.Fatalf("gotValue %+v", got)
	}
}

type fakePinger struct{ err error }

func (pinger fakePinger) Ping(context.Context) error { return pinger.err }

type fakeTorrentClient struct {
	version string
	err     error
	count   int
}

func (client fakeTorrentClient) Version(context.Context) (string, error) {
	return client.version, client.err
}

func (client fakeTorrentClient) CountTorrents(context.Context) (int, bool, error) {
	return min(client.count, qbit.TorrentPageSize), client.count >= qbit.TorrentPageSize, nil
}

func TestChecks_arrAndQbittorrentReportServiceState(t *testing.T) {
	settings := config.Config{
		Radarr:         []config.ArrService{{Kind: "radarr", BaseURL: "http://example.com", APIKey: "k"}},
		TorrentClients: []config.TorrentClient{{Type: "qbittorrent", ReadOnly: true, BaseURL: "http://example.com"}},
		CrossSeedImport: config.CrossSeedImport{
			UnsupportedTorrentClients: []config.UnsupportedClient{{Index: 1, Type: "deluge"}},
		},
	}
	deps := Deps{
		NewPinger: func(config.ArrService) Pinger { return fakePinger{} },
		NewTorrentClient: func(config.TorrentClient) (TorrentClient, error) {
			return fakeTorrentClient{version: "2.9.3", count: 3}, nil
		},
	}

	report := Run(context.Background(), Build(settings, deps))

	if resultOf(t, report, "arr:radarr:0").Result.Status != OK {
		t.Fatalf("arr: %+v", resultOf(t, report, "arr:radarr:0"))
	}
	qbitResult := resultOf(t, report, IDQbittorrent)
	for _, expectedFragment := range []string{"API 2.9.3", "torrents: 3", "read-only", `type "deluge"`} {
		if !strings.Contains(detailOf(qbitResult.Result), expectedFragment) {
			t.Fatalf("expectedValue %q in %+v", expectedFragment, qbitResult)
		}
	}
	if qbitResult.Result.Status != Warn {
		t.Fatalf("gotValue %v", qbitResult.Result.Status)
	}
}

func TestChecks_dedupeAndPathMappings(t *testing.T) {
	libraryRoot := t.TempDir()
	settings := config.Config{
		LibraryRoots: []string{libraryRoot},
		Dedupe:       config.Dedupe{TorrentRoots: []string{filepath.Join(libraryRoot, "torrents")}},
		PathRules:    []config.PathRule{{From: "/home/u/absent-a", To: "/home/u/absent-b"}},
	}

	if got := checkDedupe(settings, Deps{}); got.Status != Fail {
		t.Fatalf("dedupe: gotValue %+v", got)
	}
	if got := checkPathMappings(settings, Deps{}); got.Status != Warn {
		t.Fatalf("path mappings: gotValue %+v", got)
	}
	if got := checkDedupe(config.Config{}, Deps{}); got.Status != Skipped {
		t.Fatalf("dedupe empty: gotValue %+v", got)
	}
}

func TestChecks_fsProvidersAdviceBecomesWarnWithModTimeHint(t *testing.T) {
	deps := Deps{DescribeMounts: func(context.Context, []config.FSProvider) ([]hashsource.ProviderMountReport, error) {
		return []hashsource.ProviderMountReport{{Provider: "remote", MountReport: hashsource.MountReport{
			Mount: hashsource.Mount{LocalPath: "/home/u/mnt", Remote: "remote:"}, Advice: []i18n.Message{{Key: i18n.KeyAdviceMountNoHash}},
		}}}, nil
	}}.withDefaults()

	got := checkFSProviders(context.Background(), config.Config{FSProviders: []config.FSProvider{{Name: "remote"}}}, deps)

	if got.Status != Warn || !strings.Contains(hintOf(got), "no hash") || !strings.Contains(hintOf(got), "write cache") {
		t.Fatalf("gotValue %+v", got)
	}
}

func TestChecks_configDirOpensDatabaseReadWrite(t *testing.T) {
	configDir := t.TempDir()
	settings := config.Config{ConfigDir: configDir, DBPath: "necromanciarr.db"}

	got := checkConfigDir(settings, Deps{}.withDefaults())

	if got.Status != OK {
		t.Fatalf("gotValue %+v", got)
	}
	if _, err := os.Stat(filepath.Join(configDir, "necromanciarr.db")); err != nil {
		t.Fatal(err)
	}
	failing := Deps{OpenStore: func(string) (io.Closer, error) { return nil, errors.New("read-only") }}.withDefaults()
	if got := checkConfigDir(settings, failing); got.Status != Fail {
		t.Fatalf("failing: gotValue %+v", got)
	}
}

func TestReport_neverContainsSecrets(t *testing.T) {
	const (
		authSecret     = "AUTHSECRET1"
		providerSecret = "PROVIDERSECRET2"
		arrSecret      = "ARRSECRET3"
		arrPassword    = "ARRPASSWORD4"
		proxySecret    = "QUIPROXYKEY5"
		clientPassword = "CLIENTPASSWORD6"
		daemonSecret   = "DAEMONSECRET7"
		rcPassword     = "RCPASSWORD8"
	)
	settings := config.Config{
		ListenAddr: "0.0.0.0:1", AuthToken: authSecret, LibraryProviderToken: providerSecret,
		LibraryAPI: "http://example.com?token=" + providerSecret,
		Radarr:     []config.ArrService{{Kind: "radarr", BaseURL: "http://example.com/arr", APIKey: arrSecret, Password: arrPassword}},
		TorrentClients: []config.TorrentClient{{
			Type: "qbittorrent", Password: clientPassword,
			BaseURL: "http://qui.example.com/proxy/" + proxySecret + "/qbittorrent",
		}},
		CrossSeed:     &config.CrossSeedDaemon{BaseURL: "http://example.com:2468", APIKey: daemonSecret},
		FSProviders:   []config.FSProvider{{Name: "p", RC: "http://rcuser:" + rcPassword + "@example.com:5572"}},
		CrossSeedMode: "auto", ConfigDir: t.TempDir(), DBPath: "x.db",
	}
	leaky := func(text string) error {
		return errors.New(text + " http://qui.example.com/proxy/" + proxySecret + "/q?apikey=" + arrSecret + " " + providerSecret + " " + authSecret)
	}
	deps := Deps{
		NewSearcher:      func(config.Config) (Searcher, error) { return nil, leaky("provider") },
		NewPinger:        func(config.ArrService) Pinger { return fakePinger{err: leaky("arr " + arrPassword)} },
		NewTorrentClient: func(config.TorrentClient) (TorrentClient, error) { return nil, leaky("qbit " + clientPassword) },
		OpenStore:        func(string) (io.Closer, error) { return nil, leaky("db " + daemonSecret) },
		DescribeMounts: func(context.Context, []config.FSProvider) ([]hashsource.ProviderMountReport, error) {
			return nil, leaky("rc http://rcuser:" + rcPassword + "@example.com:5572")
		},
	}

	report := Run(context.Background(), Build(settings, deps))
	rendered, err := MarshalJSONReport(report, englishTranslator)
	if err != nil {
		t.Fatal(err)
	}

	for _, secret := range []string{authSecret, providerSecret, arrSecret, arrPassword, proxySecret, clientPassword, daemonSecret, rcPassword} {
		if strings.Contains(string(rendered), secret) {
			t.Fatalf("secret %q leaked in %s", secret, rendered)
		}
	}
	if !json.Valid(rendered) || len(report.Checks) < 8 {
		t.Fatalf("unexpected report %s", rendered)
	}
}

func panickingArrDeps(message string) Deps {
	return Deps{NewPinger: func(config.ArrService) Pinger { panic(message) }}
}

func panicSettings(secret string) config.Config {
	return config.Config{
		AuthToken: secret, ListenAddr: "127.0.0.1:1",
		Radarr: []config.ArrService{{Kind: "radarr", BaseURL: "http://example.com", APIKey: "LONGARRKEY1"}},
	}
}

func TestRun_panicSecretStraddlingTheCutIsFullyRedacted(t *testing.T) {
	const secret = "straddle-secret-value"
	message := strings.Repeat("a", 190) + secret

	report := Run(context.Background(), Build(panicSettings(secret), panickingArrDeps(message)))

	detail := detailOf(resultOf(t, report, "arr:radarr:0").Result)
	if strings.Contains(detail, secret[:4]) {
		t.Fatalf("fragment of the secret survived: %q", detail)
	}
}

func TestRun_panicMessageIsCappedAt200Runes(t *testing.T) {
	report := Run(context.Background(), Build(panicSettings("UNRELATEDTOKEN1"), panickingArrDeps(strings.Repeat("x", 500))))

	if gotValue := strings.Count(detailOf(resultOf(t, report, "arr:radarr:0").Result), "x"); gotValue != maxPanicMessageRunes {
		t.Fatalf("expectedValue %d, gotValue %d", maxPanicMessageRunes, gotValue)
	}
}

func spyStoreDeps(opened *bool) Deps {
	return Deps{OpenStore: func(string) (io.Closer, error) {
		*opened = true
		return nil, errors.New("must not open")
	}}.withDefaults()
}

func TestChecks_configDirFailsWithoutOpeningWhenDatabaseDirectoryDoesNotResolve(t *testing.T) {
	opened := false
	settings := config.Config{ConfigDir: t.TempDir(), DBPath: "/home/u/absent-dir/db.sqlite"}

	got := checkConfigDir(settings, spyStoreDeps(&opened))

	if got.Status != Fail || opened {
		t.Fatalf("opened %v, gotValue %+v", opened, got)
	}
}

func TestChecks_configDirRefusesDatabaseFileSymlinkedIntoLibraryRoot(t *testing.T) {
	libraryRoot, configDir := t.TempDir(), t.TempDir()
	target := filepath.Join(libraryRoot, "db.sqlite")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(configDir, "necromanciarr.db")); err != nil {
		t.Fatal(err)
	}
	opened := false
	settings := config.Config{ConfigDir: configDir, DBPath: "necromanciarr.db", LibraryRoots: []string{libraryRoot}}

	got := checkConfigDir(settings, spyStoreDeps(&opened))

	if got.Status != Fail || opened {
		t.Fatalf("opened %v, gotValue %+v", opened, got)
	}
}

func TestChecks_dedupeCatchesTorrentRootSymlinkedIntoLibrary(t *testing.T) {
	libraryRoot, outside := t.TempDir(), t.TempDir()
	symlinkedRoot := filepath.Join(outside, "torrents")
	if err := os.Symlink(libraryRoot, symlinkedRoot); err != nil {
		t.Fatal(err)
	}

	got := checkDedupe(config.Config{LibraryRoots: []string{libraryRoot}, Dedupe: config.Dedupe{TorrentRoots: []string{symlinkedRoot}}}, Deps{})

	if got.Status != Fail {
		t.Fatalf("gotValue %+v", got)
	}
}

func TestChecks_libraryRootsAgreeWithScanOnMediaExtensions(t *testing.T) {
	extensions := []string{".mkv", ".MP4", ".m4a", ".aac", ".ogg", ".opus", ".flac", ".webm", ".nfo", ".txt"}
	for _, extension := range extensions {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "file"+extension), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		scanStore, err := store.Open(filepath.Join(t.TempDir(), "scan.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = scanStore.Close() })
		if err := library.Scan(context.Background(), fsys.Location{Path: root}, fsystest.LocalRegistry(t), scanStore); err != nil {
			t.Fatal(err)
		}
		scannedFiles, err := scanStore.ListMediaFiles(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		expectedValue := len(scannedFiles) == 1
		gotValue := checkLibraryRoots(context.Background(), config.Config{LibraryRoots: []string{root}}, Deps{}).Status == OK
		if gotValue != expectedValue {
			t.Fatalf("%s: expectedValue media=%v (scan), gotValue media=%v (doctor)", extension, expectedValue, gotValue)
		}
	}
}

func TestChecks_crossSeedWarnsWhenConfigJSReadThisToolsVariables(t *testing.T) {
	settings := config.Config{CrossSeedMode: "auto", CrossSeedImport: config.CrossSeedImport{
		Configured: true, ConfigPath: "/home/u/cross-seed/config.js", BlockedEnvironment: []string{"NECROMANCIARR_AUTH_TOKEN"},
	}}

	got := checkCrossSeed(context.Background(), settings, Deps{}.withDefaults())

	if got.Status != Warn || !strings.Contains(detailOf(got)+hintOf(got), "NECROMANCIARR_AUTH_TOKEN") {
		t.Fatalf("gotValue %+v, expectedValue a warning naming the blocked variable", got)
	}
}

func TestChecks_configWarnsWhenTheWorkingDirectoryDotEnvWasRead(t *testing.T) {
	got := checkConfig(config.Config{ConfigDir: "/home/u/config", WorkingDirDotEnv: true})

	if got.Status != Warn || !strings.Contains(detailOf(got)+hintOf(got), ".env") {
		t.Fatalf("gotValue %+v, expectedValue a warning about the working directory .env", got)
	}
}

func TestChecks_qbittorrentCountsFromASinglePage(t *testing.T) {
	settings := config.Config{TorrentClients: []config.TorrentClient{{Type: "qbittorrent", BaseURL: "http://example.com"}}}
	deps := Deps{NewTorrentClient: func(config.TorrentClient) (TorrentClient, error) {
		return fakeTorrentClient{version: "v5.1.0", count: 60_000}, nil
	}}.withDefaults()

	got := checkQbittorrent(context.Background(), settings, deps)

	if got.Status != OK || !strings.Contains(detailOf(got), "torrents: at least 500") {
		t.Fatalf("gotValue %+v, expectedValue a count bounded by one page", got)
	}
}

func TestChecks_qbittorrentShowsTheSeedCategoryAndTags(t *testing.T) {
	settings := config.Config{
		TorrentClients: []config.TorrentClient{{Type: "qbittorrent", BaseURL: "http://example.com"}},
		LinkCategory:   "relinked", SeedTags: []string{"qui", "necromanciarr"},
	}
	deps := Deps{NewTorrentClient: func(config.TorrentClient) (TorrentClient, error) {
		return fakeTorrentClient{version: "v5.1.0", count: 3}, nil
	}}.withDefaults()
	expectedValue := `seeds added with category "relinked" and tags qui, necromanciarr`

	gotValue := checkQbittorrent(context.Background(), settings, deps)

	if gotValue.Status != OK || !strings.Contains(detailOf(gotValue), expectedValue) {
		t.Fatalf("gotValue %+v, expectedValue a detail containing %q", gotValue, expectedValue)
	}
}

func TestChecks_linkFeasibilityOffersCreateLinkDirFixForAMissingLocalDirectory(t *testing.T) {
	missingDirectory := filepath.Join(t.TempDir(), "links")
	deps := Deps{}.withDefaults()

	valeurObtenue := checkLinkFeasibility(config.Config{LinkDirs: []string{missingDirectory}, LinkType: "hardlink"}, deps)

	if valeurObtenue.FixID != FixCreateLinkDir || valeurObtenue.FixTarget != missingDirectory {
		t.Fatalf("valeurObtenue %q %q, valeurAttendue %q %q", valeurObtenue.FixID, valeurObtenue.FixTarget, FixCreateLinkDir, missingDirectory)
	}
}

func TestChecks_linkFeasibilityOffersNoFixForAMissingDirectoryUnderALibraryRoot(t *testing.T) {
	libraryRoot := t.TempDir()
	deps := Deps{}.withDefaults()
	settings := config.Config{LibraryRoots: []string{libraryRoot}, LinkDirs: []string{filepath.Join(libraryRoot, "links")}, LinkType: "hardlink"}

	valeurObtenue := checkLinkFeasibility(settings, deps)

	if valeurObtenue.FixID != "" {
		t.Fatalf("valeurObtenue %q, valeurAttendue no fix", valeurObtenue.FixID)
	}
}
