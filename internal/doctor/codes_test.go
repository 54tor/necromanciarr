package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"necromanciarr/internal/arr"
	"necromanciarr/internal/config"
	"necromanciarr/internal/config/configtest"
	"necromanciarr/internal/hashsource"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/libraryprovider"
	"necromanciarr/internal/linker"
	"necromanciarr/internal/qbit"
)

var (
	frenchTranslator  = i18n.New(i18n.French, nil)
	englishTranslator = i18n.New(i18n.English, nil)
	codePattern       = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

func detailOf(result Result) string {
	return joinMessages(englishTranslator, result.Detail, result.Details)
}

func hintOf(result Result) string { return joinMessages(englishTranslator, result.Hint, result.Hints) }

// codeScenario rassemble une configuration et des faux qui font parcourir une série de branches des contrôles.
type codeScenario struct {
	name     string
	settings config.Config
	deps     Deps
}

type erroringSearcher struct{ err error }

func (searcher erroringSearcher) Search(context.Context, libraryprovider.Query) ([]libraryprovider.Release, error) {
	return nil, searcher.err
}

func codeScenarios(t *testing.T) []codeScenario {
	t.Helper()
	libraryRoot, emptyRoot, linkDirectory := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(libraryRoot, "film.mkv"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	healthyDeps := Deps{
		NewPinger: func(config.ArrService) Pinger { return fakePinger{} },
		NewTorrentClient: func(config.TorrentClient) (TorrentClient, error) {
			return fakeTorrentClient{version: "5", count: 2}, nil
		},
		Probe: func(string) (linker.Capabilities, error) {
			return linker.Capabilities{Hardlink: true, Symlink: true}, nil
		},
		SameVolume: func(string, string) (bool, error) { return true, nil },
		Reachable:  func(context.Context, string) bool { return true },
		DescribeMounts: func(context.Context, []config.FSProvider) ([]hashsource.ProviderMountReport, error) {
			return []hashsource.ProviderMountReport{{Provider: "remote", MountReport: hashsource.MountReport{
				Mount: hashsource.Mount{LocalPath: "/home/u/mnt", Remote: "remote:"}, Advice: []i18n.Message{{Key: i18n.KeyAdviceMountNoHash}},
			}}}, nil
		},
		NewSearcher: func(config.Config) (Searcher, error) {
			return erroringSearcher{err: libraryprovider.ErrAccessDenied}, nil
		},
	}
	failingDeps := Deps{
		NewPinger: func(config.ArrService) Pinger { return fakePinger{err: errors.New("connection refused")} },
		NewTorrentClient: func(config.TorrentClient) (TorrentClient, error) {
			return nil, errors.New("bad url")
		},
		Probe:      func(string) (linker.Capabilities, error) { return linker.Capabilities{}, errors.New("read-only") },
		SameVolume: func(string, string) (bool, error) { return false, errors.New("stat failed") },
		Reachable:  func(context.Context, string) bool { return false },
		OpenStore:  func(string) (io.Closer, error) { return nil, errors.New("read-only") },
		DescribeMounts: func(context.Context, []config.FSProvider) ([]hashsource.ProviderMountReport, error) {
			return nil, errors.New("rc down")
		},
		NewSearcher: func(config.Config) (Searcher, error) { return nil, errors.New("bad config") },
	}
	richSettings := config.Config{
		ConfigDir: t.TempDir(), DBPath: "x.db", ListenAddr: "0.0.0.0:1", AuthToken: "t",
		LibraryRoots: []string{libraryRoot, emptyRoot, filepath.Join(emptyRoot, "absent")}, LibraryAPI: "http://example.com",
		LinkDirs: []string{linkDirectory, filepath.Join(libraryRoot, "links"), "/home/u/absent-links"}, LinkType: "symlink",
		Radarr:         []config.ArrService{{Kind: "radarr", BaseURL: "http://example.com", APIKey: "k"}},
		TorrentClients: []config.TorrentClient{{Type: "qbittorrent", BaseURL: "http://example.com", ReadOnly: true}},
		CrossSeedMode:  "delegate", CrossSeed: &config.CrossSeedDaemon{BaseURL: "http://example.com:2468"},
		CrossSeedImport: config.CrossSeedImport{
			Configured: true, BlockedEnvironment: []string{"NECROMANCIARR_AUTH_TOKEN"},
			UnsupportedTorrentClients: []config.UnsupportedClient{{Index: 1, Type: "deluge"}},
		},
		FSProviders: []config.FSProvider{{Name: "remote"}}, WorkingDirDotEnv: true,
		PathRules: []config.PathRule{{From: "/home/u/absent-a", To: "/home/u/absent-b"}},
		Dedupe:    config.Dedupe{TorrentRoots: []string{filepath.Join(libraryRoot, "torrents"), "/home/u/absent-torrents"}},
	}
	return []codeScenario{
		{"empty configuration", config.Config{ConfigDir: "/home/u/absent-config", ListenAddr: "0.0.0.0:1"}, Deps{}},
		{"short tracker value", config.Config{Trackers: []config.Tracker{{Key: "tracker-a", ShortVariables: []string{"UID"}}}}, Deps{}},
		{"healthy fakes", richSettings, healthyDeps},
		{"failing fakes", richSettings, failingDeps},
		{"invalid cross-seed mode", config.Config{CrossSeedMode: "bogus", CrossSeed: &config.CrossSeedDaemon{BaseURL: "http://example.com"}}, healthyDeps},
		{"loopback listen", config.Config{ListenAddr: "127.0.0.1:1", CrossSeedImport: config.CrossSeedImport{Configured: true}, CrossSeedMode: "auto"}, healthyDeps},
		{"database in library", config.Config{ConfigDir: t.TempDir(), DBPath: filepath.Join(libraryRoot, "db.sqlite"), LibraryRoots: []string{libraryRoot}}, healthyDeps},
		{"database directory unresolved", config.Config{ConfigDir: t.TempDir(), DBPath: "/home/u/absent-dir/db.sqlite"}, healthyDeps},
		{
			"different volumes",
			config.Config{LibraryRoots: []string{libraryRoot}, LinkDirs: []string{linkDirectory}, LinkType: "symlink"},
			Deps{Probe: healthyDeps.Probe, SameVolume: func(string, string) (bool, error) { return false, nil }},
		},
		{
			"link kinds refused",
			config.Config{LinkDirs: []string{linkDirectory}, LinkType: "hardlink"},
			Deps{Probe: func(string) (linker.Capabilities, error) { return linker.Capabilities{Symlink: true}, nil }},
		},
		{
			"qbittorrent timeout",
			config.Config{TorrentClients: []config.TorrentClient{{Type: "qbittorrent", BaseURL: "http://example.com"}}},
			Deps{NewTorrentClient: func(config.TorrentClient) (TorrentClient, error) {
				return fakeTorrentClient{err: qbit.ErrAuthFailed}, nil
			}},
		},
		{
			"arr rejected and timed out",
			config.Config{Radarr: []config.ArrService{{Kind: "radarr"}}, Sonarr: []config.ArrService{{Kind: "sonarr"}}},
			Deps{NewPinger: func(service config.ArrService) Pinger {
				if service.Kind == "radarr" {
					return fakePinger{err: errors.Join(errors.New("x"), arr.ErrAccessDenied)}
				}
				return fakePinger{err: context.DeadlineExceeded}
			}},
		},
	}
}

func TestDoctor_everyResultHasStableCode(t *testing.T) {
	seenCodes := map[string]bool{}
	for _, scenario := range codeScenarios(t) {
		report := Run(context.Background(), Build(scenario.settings, scenario.deps))
		for _, outcome := range report.Checks {
			code := outcome.Result.Code
			if !codePattern.MatchString(code) {
				t.Errorf("%s / %s: gotValue code %q, expectedValue it to match %s", scenario.name, outcome.ID, code, codePattern)
			}
			seenCodes[code] = true
			if outcome.TitleKey == "" {
				t.Errorf("%s / %s: empty title key", scenario.name, outcome.ID)
			}
		}
	}
	if len(seenCodes) < 25 {
		t.Errorf("scenarios reach %d distinct codes, expectedValue at least 25: %v", len(seenCodes), seenCodes)
	}
}

func TestDoctor_everyResultLiteralNamesItsCode(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			literal, isLiteral := node.(*ast.CompositeLit)
			if !isLiteral || !isNamed(literal.Type, "Result") || len(literal.Elts) == 0 {
				return true
			}
			if _, keyed := literal.Elts[0].(*ast.KeyValueExpr); keyed && !hasKey(literal, "Code") {
				t.Errorf("%s: Result literal without Code", file)
			}
			return true
		})
	}
}

func isNamed(expression ast.Expr, name string) bool {
	identifier, isIdentifier := expression.(*ast.Ident)
	return isIdentifier && identifier.Name == name
}

func hasKey(literal *ast.CompositeLit, key string) bool {
	for _, element := range literal.Elts {
		if pair, isPair := element.(*ast.KeyValueExpr); isPair && isNamed(pair.Key, key) {
			return true
		}
	}
	return false
}

func TestDoctor_everyCodeIsDocumented(t *testing.T) {
	documentation, err := os.ReadFile(filepath.Join("..", "..", "docs", "reference", "06-limits-and-troubleshooting.md"))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), "codes.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	declared := 0
	ast.Inspect(parsed, func(node ast.Node) bool {
		valueSpec, isSpec := node.(*ast.ValueSpec)
		if !isSpec {
			return true
		}
		for _, value := range valueSpec.Values {
			literal, isLiteral := value.(*ast.BasicLit)
			if !isLiteral || literal.Kind != token.STRING {
				continue
			}
			code, _ := strconv.Unquote(literal.Value)
			declared++
			if !strings.Contains(string(documentation), "`"+code+"`") {
				t.Errorf("code %q is not documented in 06-limits-and-troubleshooting.md", code)
			}
		}
		return true
	})
	if declared < 25 {
		t.Errorf("codes.go declares %d codes, expectedValue at least 25", declared)
	}
}

func TestDoctor_jsonCodesIdenticalInBothLanguages(t *testing.T) {
	scenarios := codeScenarios(t)
	scenario := scenarios[1]
	report := Run(context.Background(), Build(scenario.settings, scenario.deps))

	french := decodeRendered(t, report, frenchTranslator)
	english := decodeRendered(t, report, englishTranslator)

	if len(french) != len(english) || len(french) < 8 {
		t.Fatalf("gotValue %d and %d checks", len(french), len(english))
	}
	titlesDiffering := 0
	for index := range french {
		for _, stableField := range []string{"id", "status", "code"} {
			if french[index][stableField] != english[index][stableField] {
				t.Errorf("check %d field %s: fr %v, en %v, expectedValue identical", index, stableField, french[index][stableField], english[index][stableField])
			}
		}
		if french[index]["detail"] == english[index]["detail"] {
			t.Errorf("check %v: detail identical in both languages: %v", french[index]["id"], french[index]["detail"])
		}
		if french[index]["title"] != english[index]["title"] {
			titlesDiffering++
		}
	}
	if titlesDiffering == 0 {
		t.Error("no title differs between languages")
	}
}

func decodeRendered(t *testing.T, report Report, translator i18n.Translator) []map[string]any {
	t.Helper()
	raw, err := MarshalJSONReport(report, translator)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Worst  string
		Checks []map[string]any
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("%v in %s", err, raw)
	}
	return decoded.Checks
}

func TestDoctor_panicResultIsTranslatedAndRedacted(t *testing.T) {
	const secret = "PANICSECRETVALUE"
	settings := config.Config{AuthToken: secret, ListenAddr: "127.0.0.1:1", Radarr: []config.ArrService{{Kind: "radarr", BaseURL: "http://example.com"}}}
	deps := Deps{NewPinger: func(config.ArrService) Pinger { panic("boom " + secret) }}

	report := Run(context.Background(), Build(settings, deps))

	for language, expectedFragment := range map[i18n.Language]string{i18n.French: "paniqué", i18n.English: "panicked"} {
		rendered, err := MarshalJSONReport(report, i18n.New(language, nil))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(rendered), secret) || !strings.Contains(string(rendered), expectedFragment) || !strings.Contains(string(rendered), `"code": "panic"`) {
			t.Errorf("%s: gotValue %s, expectedValue code panic, %q and no secret", language, rendered, expectedFragment)
		}
	}
}

func TestDoctor_knownSecretInAParamIsNeverRendered(t *testing.T) {
	const secret = "KNOWNSECRETVALUE9"
	settings := config.Config{Radarr: []config.ArrService{{Kind: "radarr", BaseURL: "http://example.com", APIKey: secret}}}
	deps := Deps{NewPinger: func(config.ArrService) Pinger { return fakePinger{err: errors.New("refused for key " + secret)} }}

	report := Run(context.Background(), Build(settings, deps))

	for _, translator := range []i18n.Translator{frenchTranslator, englishTranslator} {
		rendered, _ := MarshalJSONReport(report, translator)
		if strings.Contains(string(rendered), secret) {
			t.Errorf("%s: secret leaked in %s", translator.Language(), rendered)
		}
	}
}

func TestRender_titleStaysInTheJSONAndCodesAreNeverTranslated(t *testing.T) {
	report := Report{Checks: []Outcome{{
		ID: "config", TitleKey: i18n.KeyDoctorTitleConfig,
		Result: Result{Status: Warn, Code: CodeWorkingDirDotEnv, Detail: i18n.Message{Key: i18n.KeyDoctorConfigDotEnv}},
	}}, Worst: Warn}

	rendered := Render(report, frenchTranslator)

	named := rendered.Checks[0]
	if named.ID != "config" || named.Status != Warn || named.Code != CodeWorkingDirDotEnv || named.Title == "" || named.Title == string(i18n.KeyDoctorTitleConfig) {
		t.Fatalf("gotValue %+v", named)
	}
}

func TestDoctor_redactsTrackerPasskeyInPanicAndDetail(t *testing.T) {
	const passkey = `fake"pass&key/0000`
	configDir := t.TempDir()
	content := `{"radarr": ["http://example.com:7878?apikey=k"], "trackers": {"tracker-a": "https://tracker-a.example.com/announce?pk=${PASSKEY_TRACKER_A}"}}`
	settings, err := config.LoadWith(nil, func(name string) (string, bool) { return passkey, name == "PASSKEY_TRACKER_A" }, configDir, configtest.Opener(t, content))
	if err != nil {
		t.Fatal(err)
	}
	leak := "announce udp://tracker-a.example.com/announce?pk=" + passkey + " bare " + strings.ToUpper(passkey)
	panicking := Deps{NewPinger: func(config.ArrService) Pinger { panic("boom " + leak) }}
	failing := Deps{NewPinger: func(config.ArrService) Pinger { return fakePinger{err: errors.New("refused: " + leak)} }}

	for label, deps := range map[string]Deps{"panic": panicking, "detail": failing} {
		rendered, _ := MarshalJSONReport(Run(context.Background(), Build(settings, deps)), englishTranslator)

		for _, form := range []string{passkey, strings.ToUpper(passkey), `fake\"pass`, `pass&key`, "pass&key"} {
			if strings.Contains(string(rendered), form) {
				t.Fatalf("%s: gotValue %q in %s, expectedValue no form of the passkey", label, form, rendered)
			}
		}
	}
}

func TestConfigLoadResult_namesTheSourceOfEachList(t *testing.T) {
	settings := config.Config{ConfigDir: "/home/u/config", Origins: []config.ValueOrigin{
		{Key: "radarr", ItemID: "a", Origin: "crossseed"},
		{Key: "sonarr", ItemID: "b", Origin: "crossseed"}, {Key: "sonarr", ItemID: "c", Origin: "added"},
		{Key: "linkDirs", Origin: "default"},
	}}
	valeurAttendue := map[string]string{"radarr": "cross-seed", "sonarr": "database", "torrent_clients": "none", "link_dirs": "none"}

	valeurObtenue := configLoadResult(settings).Detail.Params

	for parametre, sourceAttendue := range valeurAttendue {
		if valeurObtenue[parametre] != sourceAttendue {
			t.Fatalf("valeurObtenue %v, valeurAttendue %v", valeurObtenue, valeurAttendue)
		}
	}
}
