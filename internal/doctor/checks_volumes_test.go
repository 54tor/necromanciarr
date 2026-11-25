package doctor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"necromanciarr/internal/config"
	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/local"
	"necromanciarr/internal/fsys/sftpfs"
	"necromanciarr/internal/fsys/sftpfs/sftptest"
	"necromanciarr/internal/linker"
)

const (
	seedboxName    = "seedbox"
	seedboxLibrary = "/library"
	seedboxLinks   = "/links"
	sftpFlagWrite  = 0x2
)

// sftpWorld est un serveur de test, son volume paresseux et le registre qui le sert, comme l'application les câble.
type sftpWorld struct {
	server   *sftptest.Server
	volume   *sftpfs.Volume
	provider config.FSProvider
	registry *fsys.Registry
	settings config.Config
	deps     Deps
}

func startSeedbox(t *testing.T, options ...sftptest.Option) *sftptest.Server {
	t.Helper()
	server := sftptest.Start(t, options...)
	sftptest.WriteFile(t, filepath.Join(server.Root, "library", "movie.mkv"), []byte("library bytes"), 0o644)
	sftptest.Mkdir(t, filepath.Join(server.Root, "links"))
	return server
}

// newSFTPWorld câble le fournisseur sur server ; mutate règle le fournisseur et la configuration avant la construction.
func newSFTPWorld(t *testing.T, server *sftptest.Server, mutate func(*config.FSProvider, *config.Config)) *sftpWorld {
	t.Helper()
	provider := server.Provider(t, seedboxName)
	settings := config.Config{
		LibraryRoots: []string{"sftp://" + seedboxName + seedboxLibrary}, LinkDirs: []string{"sftp://" + seedboxName + seedboxLinks},
		LinkType: "hardlink",
	}
	if mutate != nil {
		mutate(&provider, &settings)
	}
	settings.FSProviders = []config.FSProvider{provider}
	return buildWorld(t, server, provider, settings)
}

func buildWorld(t *testing.T, server *sftptest.Server, provider config.FSProvider, settings config.Config) *sftpWorld {
	t.Helper()
	unsetAgent := func(string) (string, bool) { return "", false }
	volume := sftpfs.NewLazy(provider, sftpfs.Deps{LibraryRoots: []string{seedboxLibrary}, LookupEnv: unsetAgent})
	t.Cleanup(func() { _ = volume.Close() })
	world := worldOn(t, server, volume, volume, provider, settings)
	world.volume = volume
	return world
}

// worldOn pose volume dans le registre ; remote est ce que doctor lit du volume (parfois un volume qui décore le vrai).
func worldOn(t *testing.T, server *sftptest.Server, volume fsys.Volume, remote RemoteVolume, provider config.FSProvider, settings config.Config) *sftpWorld {
	t.Helper()
	probe := func(root fsys.WriteRoot) error {
		sftpVolume, isSFTP := remote.(*sftpfs.Volume)
		if !isSFTP {
			return nil
		}
		_, err := sftpVolume.ProbeCapabilities(context.Background(), root)
		return err
	}
	libraryRoots := []fsys.Location{{Volume: seedboxName, Path: seedboxLibrary}}
	registry, err := fsys.NewRegistry(map[string]fsys.Volume{"": local.New(), seedboxName: volume}, libraryRoots,
		fsys.WithWriteRootCheck(seedboxName, probe))
	if err != nil {
		t.Fatal(err)
	}
	return &sftpWorld{
		server: server, provider: provider, registry: registry, settings: settings,
		deps: Deps{Registry: registry, RemoteVolumes: map[string]RemoteVolume{seedboxName: remote}},
	}
}

// run exécute le contrôle id de la configuration du monde et rend son résultat.
func (world *sftpWorld) run(t *testing.T, id string) Outcome {
	t.Helper()
	for _, check := range Build(world.settings, world.deps) {
		if check.ID == id {
			return Run(context.Background(), []Check{check}).Checks[0]
		}
	}
	t.Fatalf("valeurObtenue no check %q, valeurAttendue it to be built", id)
	return Outcome{}
}

func codeAndStatus(outcome Outcome) string {
	return fmt.Sprintf("%s/%s", outcome.Result.Status, outcome.Result.Code)
}

// writeRequests rend les requêtes d'écriture reçues par le serveur (une ouverture n'écrit qu'avec le drapeau d'écriture).
func writeRequests(server *sftptest.Server) []sftptest.Request {
	var writes []sftptest.Request
	for _, request := range server.Requests() {
		isOpen := request.Method == "Open"
		isWrite := slices.Contains([]string{"Put", "Setstat", "Mkdir", "Rmdir", "Remove", "Rename", "PosixRename", "Link", "Symlink"}, request.Method) ||
			(isOpen && request.Flags&sftpFlagWrite != 0)
		if isWrite {
			writes = append(writes, request)
		}
	}
	return writes
}

func requestsOf(server *sftptest.Server, method string) int {
	count := 0
	for _, request := range server.Requests() {
		if request.Method == method {
			count++
		}
	}
	return count
}

func renderedText(t *testing.T, outcome Outcome) string {
	t.Helper()
	return detailOf(outcome.Result) + " | " + hintOf(outcome.Result)
}

func TestDoctor_sftpAnswersAndShowsAddressAndUser(t *testing.T) {
	world := newSFTPWorld(t, startSeedbox(t), nil)

	outcome := world.run(t, "sftp:seedbox")

	text := renderedText(t, outcome)
	if codeAndStatus(outcome) != "ok/sftp_ok" || !strings.Contains(text, world.provider.Address) || !strings.Contains(text, "user@") {
		t.Fatalf("valeurObtenue %s %q, valeurAttendue ok/sftp_ok naming the address and the user", codeAndStatus(outcome), text)
	}
}

func TestDoctor_sftpRefusesWindowsServer(t *testing.T) {
	world := newSFTPWorld(t, startSeedbox(t, sftptest.WindowsRoot()), nil)

	outcome := world.run(t, "sftp:seedbox")

	if codeAndStatus(outcome) != "fail/remote_windows_unsupported" {
		t.Fatalf("valeurObtenue %s %q, valeurAttendue fail/remote_windows_unsupported", codeAndStatus(outcome), renderedText(t, outcome))
	}
}

func TestDoctor_sftpDoesNotMistakeDirectoriesNamedLikeADriveForWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a folder named C: cannot exist on a Windows host")
	}
	server := startSeedbox(t)
	for _, name := range []string{"C", "c", "C:", "D"} {
		sftptest.Mkdir(t, filepath.Join(server.Root, name))
	}
	world := newSFTPWorld(t, server, func(_ *config.FSProvider, settings *config.Config) {
		settings.LibraryRoots = []string{"sftp://seedbox/C", "sftp://seedbox/c"}
	})

	outcome := world.run(t, "sftp:seedbox")

	if codeAndStatus(outcome) != "ok/sftp_ok" {
		t.Fatalf("valeurObtenue %s %q, valeurAttendue ok/sftp_ok: a Unix directory named /C is not a drive", codeAndStatus(outcome), renderedText(t, outcome))
	}
}

func TestIsWindowsDrivePath_needsTheColonForm(t *testing.T) {
	cases := map[string]bool{
		"/C:": true, "/C:/": true, "/c:/Users/media": true, "/Z:/x": true,
		"/": false, "/C": false, "/c": false, "/C/": false, "/C/media": false, "/c/Users": false, "/C:x": false, "/CD:/": false,
		"C:/": false, "": false, "/library/C:/x": false,
	}
	for candidate, expectedValue := range cases {
		if valeurObtenue := isWindowsDrivePath(candidate); valeurObtenue != expectedValue {
			t.Errorf("isWindowsDrivePath(%q): valeurObtenue %v, valeurAttendue %v", candidate, valeurObtenue, expectedValue)
		}
	}
}

func TestDoctor_sftpRefusesAWindowsPathUnderAConfiguredRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a folder named C: cannot exist on a Windows host")
	}
	server := startSeedbox(t)
	sftptest.Mkdir(t, filepath.Join(server.Root, "C:", "media"))
	world := newSFTPWorld(t, server, func(_ *config.FSProvider, settings *config.Config) {
		settings.LibraryRoots = []string{"sftp://seedbox/C:/media"}
	})

	outcome := world.run(t, "sftp:seedbox")

	if codeAndStatus(outcome) != "fail/remote_windows_unsupported" {
		t.Fatalf("valeurObtenue %s, valeurAttendue fail/remote_windows_unsupported for a /C:/ root", codeAndStatus(outcome))
	}
}

func otherHostKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshKey, err := ssh.NewPublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	return sshKey
}

func TestDoctor_sftpMapsConnectionErrorsFromTheirTypes(t *testing.T) {
	weakKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	weakBlock, err := ssh.MarshalPrivateKey(weakKey, "")
	if err != nil {
		t.Fatal(err)
	}
	scenarios := []struct {
		name         string
		serverOption []sftptest.Option
		mutate       func(t *testing.T, server *sftptest.Server, provider *config.FSProvider)
		expectedCode string
	}{
		{"unknown host", nil, func(t *testing.T, _ *sftptest.Server, provider *config.FSProvider) {
			sftptest.WriteFile(t, provider.KnownHosts, []byte(knownhosts.Line([]string{"other.example.com:22"}, otherHostKey(t))+"\n"), 0o600)
		}, "fail/host_key_unknown"},
		{"pinned key differs", nil, func(t *testing.T, server *sftptest.Server, provider *config.FSProvider) {
			sftptest.WriteFile(t, provider.KnownHosts, []byte(knownhosts.Line([]string{knownhosts.Normalize(server.Address)}, otherHostKey(t))+"\n"), 0o600)
		}, "fail/host_key_mismatch"},
		{"known_hosts missing", nil, func(t *testing.T, _ *sftptest.Server, provider *config.FSProvider) {
			provider.KnownHosts = filepath.Join(t.TempDir(), "absent")
		}, "fail/known_hosts_unreadable"},
		{"wrong password", []sftptest.Option{sftptest.WithPassword("the-right-one")}, func(_ *testing.T, _ *sftptest.Server, provider *config.FSProvider) {
			provider.IdentityFile, provider.Password = "", "wrong-password"
		}, "fail/auth_refused"},
		{"rsa key too weak", nil, func(t *testing.T, _ *sftptest.Server, provider *config.FSProvider) {
			provider.IdentityFile = filepath.Join(t.TempDir(), "id_rsa")
			sftptest.WriteFile(t, provider.IdentityFile, pem.EncodeToMemory(weakBlock), 0o600)
		}, "fail/identity_key_too_weak"},
		{"identity is not a key", nil, func(t *testing.T, _ *sftptest.Server, provider *config.FSProvider) {
			provider.IdentityFile = filepath.Join(t.TempDir(), "id_junk")
			sftptest.WriteFile(t, provider.IdentityFile, []byte("not a private key at all"), 0o600)
		}, "fail/identity_file_unusable"},
		{"nothing listens", nil, func(t *testing.T, _ *sftptest.Server, provider *config.FSProvider) {
			provider.Address = closedAddress(t)
		}, "fail/remote_unavailable"},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			server := startSeedbox(t, scenario.serverOption...)
			world := newSFTPWorld(t, server, func(provider *config.FSProvider, _ *config.Config) {
				scenario.mutate(t, server, provider)
			})

			outcome := world.run(t, "sftp:seedbox")

			if codeAndStatus(outcome) != scenario.expectedCode {
				t.Fatalf("valeurObtenue %s %q, valeurAttendue %s", codeAndStatus(outcome), renderedText(t, outcome), scenario.expectedCode)
			}
		})
	}
}

func closedAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func TestDoctor_sftpWarnsForIdentityPermissionsAndMissingAgent(t *testing.T) {
	server := startSeedbox(t)
	world := newSFTPWorld(t, server, func(provider *config.FSProvider, _ *config.Config) {
		keyContent, err := os.ReadFile(provider.IdentityFile)
		if err != nil {
			t.Fatal(err)
		}
		readable := filepath.Join(t.TempDir(), "id_ed25519")
		sftptest.WriteFile(t, readable, keyContent, 0o644)
		provider.IdentityFile, provider.Agent = readable, true
	})

	outcome := world.run(t, "sftp:seedbox")

	text := renderedText(t, outcome)
	if outcome.Result.Status != Warn || !strings.Contains(text, "readable or writable by group or others") || !strings.Contains(text, "agent is unavailable") {
		t.Fatalf("valeurObtenue %s %q, valeurAttendue a warning naming the key permissions and the missing agent", codeAndStatus(outcome), text)
	}
	if outcome.Result.Code != "identity_file_permissions" && outcome.Result.Code != "agent_unavailable" {
		t.Fatalf("valeurObtenue code %q, valeurAttendue identity_file_permissions or agent_unavailable", outcome.Result.Code)
	}
}

func TestDoctor_sftpLibraryWarnsMaybeWritableWithoutWriting(t *testing.T) {
	scenarios := []struct {
		name         string
		mode         os.FileMode
		expectedCode string
	}{
		{"owner is the account", 0o755, "warn/library_maybe_writable"},
		{"group can write", 0o575, "warn/library_maybe_writable"},
		{"others can write", 0o557, "warn/library_maybe_writable"},
		{"nobody writes", 0o555, "ok/root_ok"},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			server := startSeedbox(t)
			if err := os.Chmod(filepath.Join(server.Root, "library"), scenario.mode); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(filepath.Join(server.Root, "library"), 0o700) })
			world := newSFTPWorld(t, server, nil)

			outcome := world.run(t, "sftp-library:seedbox")

			if codeAndStatus(outcome) != scenario.expectedCode {
				t.Fatalf("valeurObtenue %s %q, valeurAttendue %s", codeAndStatus(outcome), renderedText(t, outcome), scenario.expectedCode)
			}
			if writes := writeRequests(server); len(writes) != 0 {
				t.Fatalf("valeurObtenue %d write requests %+v, valeurAttendue none: doctor never writes in a library", len(writes), writes)
			}
			if scenario.expectedCode != "ok/root_ok" && !strings.Contains(hintOf(outcome.Result), "SFTP account that is read-only on the library") {
				t.Fatalf("valeurObtenue hint %q, valeurAttendue the read-only account recommendation", hintOf(outcome.Result))
			}
		})
	}
}

func TestDoctor_sftpLibraryReportsUnreadableAndMediaLessRoots(t *testing.T) {
	server := startSeedbox(t)
	sftptest.WriteFile(t, filepath.Join(server.Root, "docs", "notes.txt"), []byte("x"), 0o644)
	if err := os.Chmod(filepath.Join(server.Root, "docs"), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(server.Root, "docs"), 0o700) })
	scenarios := map[string]struct {
		roots        []string
		expectedCode string
	}{
		"no media":   {[]string{"sftp://seedbox/docs"}, "warn/no_media_in_first_2000"},
		"absent":     {[]string{"sftp://seedbox/absent"}, "fail/library_root_unreadable"},
		"a file":     {[]string{"sftp://seedbox/library/movie.mkv"}, "fail/library_root_unreadable"},
		"none there": {[]string{"/home/u/local-library"}, "skipped/no_library_root"},
	}
	for name, scenario := range scenarios {
		t.Run(name, func(t *testing.T) {
			world := newSFTPWorld(t, server, func(_ *config.FSProvider, settings *config.Config) { settings.LibraryRoots = scenario.roots })

			outcome := world.run(t, "sftp-library:seedbox")

			if codeAndStatus(outcome) != scenario.expectedCode {
				t.Fatalf("valeurObtenue %s %q, valeurAttendue %s", codeAndStatus(outcome), renderedText(t, outcome), scenario.expectedCode)
			}
		})
	}
}

// countingVolume compte les entrées que les listages du volume rendent.
type volumeBase = fsys.Volume

type countingVolume struct {
	volumeBase
	entries *atomic.Int64
}

func (counting countingVolume) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, err := counting.volumeBase.ReadDir(name)
	counting.entries.Add(int64(len(entries)))
	return entries, err
}

func TestDoctor_sftpLibraryListsAtMostTheFirst2000Entries(t *testing.T) {
	server := startSeedbox(t)
	const directories, filesPerDirectory = 60, 50
	for directory := range directories {
		for file := range filesPerDirectory {
			sftptest.WriteFile(t, filepath.Join(server.Root, "big", fmt.Sprintf("d%02d", directory), fmt.Sprintf("f%02d.txt", file)), []byte("x"), 0o644)
		}
	}
	provider := server.Provider(t, seedboxName)
	settings := config.Config{LibraryRoots: []string{"sftp://seedbox/big"}, FSProviders: []config.FSProvider{provider}}
	volume := sftpfs.NewLazy(provider, sftpfs.Deps{LibraryRoots: []string{"/big"}})
	t.Cleanup(func() { _ = volume.Close() })
	counted := &atomic.Int64{}
	world := worldOn(t, server, countingVolume{volumeBase: volume, entries: counted}, volume, provider, settings)

	outcome := world.run(t, "sftp-library:seedbox")

	totalEntries := directories*filesPerDirectory + directories
	if valeurObtenue := counted.Load(); valeurObtenue == 0 || valeurObtenue > maxWalkEntries+filesPerDirectory+directories {
		t.Fatalf("valeurObtenue %d entries listed of %d, valeurAttendue at most %d", valeurObtenue, totalEntries, maxWalkEntries+filesPerDirectory+directories)
	}
	if codeAndStatus(outcome) != "warn/no_media_in_first_2000" {
		t.Fatalf("valeurObtenue %s, valeurAttendue warn/no_media_in_first_2000", codeAndStatus(outcome))
	}
}

func TestDoctor_sftpExecReportsFallback(t *testing.T) {
	scenarios := []struct {
		name         string
		options      []sftptest.Option
		mutate       func(*config.FSProvider, *config.Config)
		expectedCode string
		expectedText string
	}{
		{"denied in auto", nil, nil, "warn/remote_exec_unavailable", "refuses remote commands"},
		{"inconsistent output", []sftptest.Option{sftptest.WithGNUExec(), sftptest.WithExecOutputFilter(func(command string, stdout []byte) []byte {
			if strings.Contains(command, "stat") {
				return []byte("garbage")
			}
			return stdout
		})}, nil, "warn/remote_exec_unavailable", ""},
		{
			"login shell is not posix",
			[]sftptest.Option{sftptest.WithExec(func(string, []byte) ([]byte, []byte, int) { return []byte("fish"), nil, 0 })},
			nil,
			"warn/remote_exec_unavailable", "login shell is not POSIX",
		},
		{
			"required and denied", nil, func(provider *config.FSProvider, _ *config.Config) { provider.Exec = config.SFTPExecRequired },
			"fail/remote_exec_required_failed", "refuses remote commands",
		},
		{
			"turned off", nil, func(provider *config.FSProvider, _ *config.Config) { provider.Exec = config.SFTPExecOff },
			"ok/exec_off", "turned off",
		},
		{"working", []sftptest.Option{sftptest.WithGNUExec()}, nil, "ok/exec_ok", "nice and ionice are present"},
		{"no ionice", []sftptest.Option{sftptest.WithGNUExec(), sftptest.WithoutGNUProgram("ionice")}, nil, "ok/exec_ok", "nice or ionice is missing"},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			server := startSeedbox(t, scenario.options...)
			world := newSFTPWorld(t, server, scenario.mutate)

			outcome := world.run(t, "sftp-exec:seedbox")

			text := renderedText(t, outcome)
			announcesWorkingExec := strings.Contains(text, "remote commands available")
			if announcesWorkingExec && !strings.HasPrefix(scenario.expectedCode, "ok/exec_ok") {
				t.Fatalf("valeurObtenue %q, valeurAttendue no « remote commands available » when the probe failed", text)
			}
			if codeAndStatus(outcome) != scenario.expectedCode || !strings.Contains(text, scenario.expectedText) {
				t.Fatalf("valeurObtenue %s %q, valeurAttendue %s containing %q", codeAndStatus(outcome), text, scenario.expectedCode, scenario.expectedText)
			}
			if scenario.name == "turned off" && requestsOf(server, "Exec") != 0 {
				t.Fatalf("valeurObtenue %d exec requests, valeurAttendue none with exec off", requestsOf(server, "Exec"))
			}
		})
	}
}

func TestDoctor_sftpExecOnlyLibraryRootsLeavesCapabilitiesUnprobedWithoutWriting(t *testing.T) {
	server := startSeedbox(t, sftptest.WithGNUExec())
	world := newSFTPWorld(t, server, func(_ *config.FSProvider, settings *config.Config) { settings.LinkDirs = nil })

	outcome := world.run(t, "sftp-exec:seedbox")

	text := renderedText(t, outcome)
	if codeAndStatus(outcome) != "ok/exec_ok" || !strings.Contains(text, "capabilities are not probed") {
		t.Fatalf("valeurObtenue %s %q, valeurAttendue ok/exec_ok with the not_probed note", codeAndStatus(outcome), text)
	}
	if writes := writeRequests(server); len(writes) != 0 {
		t.Fatalf("valeurObtenue %d write requests %+v, valeurAttendue none", len(writes), writes)
	}
}

func TestDoctor_sftpExecNeverProbesInALibraryRoot(t *testing.T) {
	server := startSeedbox(t, sftptest.WithGNUExec())
	world := newSFTPWorld(t, server, func(_ *config.FSProvider, settings *config.Config) {
		settings.LinkDirs = []string{"sftp://seedbox/library"}
	})

	outcome := world.run(t, "sftp-exec:seedbox")

	if writes := writeRequests(server); len(writes) != 0 {
		t.Fatalf("valeurObtenue %s with %d write requests %+v, valeurAttendue none: a library root is never opened for writing", codeAndStatus(outcome), len(writes), writes)
	}
}

func TestDoctor_sftpExecProbesTheFirstLinkFolderThroughTheRegistry(t *testing.T) {
	server := startSeedbox(t, sftptest.WithGNUExec())
	world := newSFTPWorld(t, server, nil)

	outcome := world.run(t, "sftp-exec:seedbox")

	capabilities, err := world.volume.Capabilities(context.Background())
	if err != nil || !capabilities.RemoteStat || codeAndStatus(outcome) != "ok/exec_ok" {
		t.Fatalf("valeurObtenue %s, capabilities %+v err %v, valeurAttendue exec_ok with remote stat proven by the link folder probe", codeAndStatus(outcome), capabilities, err)
	}
	for _, write := range writeRequests(server) {
		if !strings.HasPrefix(write.Path, seedboxLinks+"/") && write.Path != seedboxLinks {
			t.Fatalf("valeurObtenue write %+v outside %s, valeurAttendue probe entries in the link folder only", write, seedboxLinks)
		}
	}
}

func TestDoctor_linkFeasibilityComparesRemoteFilesystemIdentity(t *testing.T) {
	scenarios := []struct {
		name         string
		options      []sftptest.Option
		expectedCode string
	}{
		{"same fsid", nil, CodeSameVolume},
		{"no statvfs", []sftptest.Option{sftptest.WithoutStatVFS()}, CodeVolumeIdentityUnknown},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			world := newSFTPWorld(t, startSeedbox(t, scenario.options...), nil)
			deps := world.deps.withDefaults()
			deps.SameVolume = sameVolumeIn(world.registry)

			_, code, _, _ := compareVolumes("sftp://seedbox/library", "sftp://seedbox/links", []linker.Kind{linker.Hardlink}, deps)

			if code != scenario.expectedCode {
				t.Fatalf("valeurObtenue %s, valeurAttendue %s", code, scenario.expectedCode)
			}
		})
	}
}

func TestDoctor_linkFeasibilityTreatsAnotherProviderAsAnotherFilesystem(t *testing.T) {
	world := newSFTPWorld(t, startSeedbox(t), nil)
	deps := world.deps.withDefaults()
	deps.SameVolume = func(string, string) (bool, error) { return false, nil }

	status, code, _, _ := compareVolumes("sftp://seedbox/library", "sftp://other/links", []linker.Kind{linker.Hardlink}, deps)

	if status != Fail || code != CodeVolumesDiffer {
		t.Fatalf("valeurObtenue %s/%s, valeurAttendue fail/%s", status, code, CodeVolumesDiffer)
	}
}

func TestDoctor_pathMappingsFailsForUnmappedRemoteLinkDir(t *testing.T) {
	scenarios := []struct {
		name         string
		rules        []config.PathRule
		expectedFail bool
	}{
		{"no rule at all", nil, true},
		{"a rule of another folder", []config.PathRule{{From: "sftp://seedbox/elsewhere", To: "/downloads"}}, true},
		{"a rule that stays remote", []config.PathRule{{From: "sftp://seedbox/links", To: "sftp://seedbox/links"}}, true},
		{"covered", []config.PathRule{{From: "sftp://seedbox/links", To: "/downloads/links"}}, false},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			world := newSFTPWorld(t, startSeedbox(t), func(_ *config.FSProvider, settings *config.Config) { settings.PathRules = scenario.rules })

			outcome := world.run(t, IDPathMappings)

			isFailure := outcome.Result.Status == Fail && outcome.Result.Code == "client_path_unmapped"
			if isFailure != scenario.expectedFail {
				t.Fatalf("valeurObtenue %s %q, valeurAttendue unmapped failure %v", codeAndStatus(outcome), renderedText(t, outcome), scenario.expectedFail)
			}
		})
	}
}

func TestDoctor_pathMappingsLeavesLocalLinkDirsAlone(t *testing.T) {
	settings := config.Config{LinkDirs: []string{"/home/u/links"}}

	outcome := Run(context.Background(), selectCheck(t, Build(settings, Deps{}), IDPathMappings)).Checks[0]

	if codeAndStatus(outcome) != "skipped/no_path_mapping" {
		t.Fatalf("valeurObtenue %s, valeurAttendue skipped/no_path_mapping for a local link dir", codeAndStatus(outcome))
	}
}

func selectCheck(t *testing.T, checks []Check, id string) []Check {
	t.Helper()
	for _, check := range checks {
		if check.ID == id {
			return []Check{check}
		}
	}
	t.Fatalf("valeurObtenue no check %q", id)
	return nil
}

func TestDoctor_redactsSFTPSecrets(t *testing.T) {
	const password, passphrase, keyMarker = "S3cretPassw0rd-sftp", "Sup3rPassphrase-sftp", "PRIVATE-KEY-CONTENT-MARKER"
	server := startSeedbox(t, sftptest.WithPassword("another-password"))
	scenarios := map[string]func(t *testing.T, provider *config.FSProvider){
		"key that cannot be used": func(t *testing.T, provider *config.FSProvider) {
			provider.IdentityFile = filepath.Join(t.TempDir(), "id_junk")
			sftptest.WriteFile(t, provider.IdentityFile, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\n"+keyMarker+"\n"), 0o600)
			provider.Password, provider.IdentityPassphrase = password, passphrase
		},
		"working connection": func(_ *testing.T, provider *config.FSProvider) {
			provider.Password, provider.IdentityPassphrase = password, passphrase
		},
		"refused password": func(_ *testing.T, provider *config.FSProvider) {
			provider.IdentityFile, provider.Password, provider.IdentityPassphrase = "", password, passphrase
		},
		"password inside the address": func(t *testing.T, provider *config.FSProvider) {
			provider.Address = closedAddress(t)
			provider.Password = password
		},
	}
	for name, mutate := range scenarios {
		t.Run(name, func(t *testing.T) {
			world := newSFTPWorld(t, server, func(provider *config.FSProvider, _ *config.Config) { mutate(t, provider) })

			var checks []Check
			for _, id := range []string{"sftp:seedbox", "sftp-library:seedbox", "sftp-exec:seedbox"} {
				checks = append(checks, selectCheck(t, Build(world.settings, world.deps), id)...)
			}
			report := Run(context.Background(), checks)

			rendered, err := MarshalJSONReport(report, englishTranslator)
			if err != nil {
				t.Fatal(err)
			}
			french, err := MarshalJSONReport(report, frenchTranslator)
			if err != nil {
				t.Fatal(err)
			}
			for _, output := range []string{string(rendered), string(french)} {
				for _, secret := range []string{password, passphrase, keyMarker, "another-password"} {
					if strings.Contains(output, secret) {
						t.Fatalf("valeurObtenue the secret %q in %s, valeurAttendue none", secret, output)
					}
				}
				for _, control := range output {
					if control < ' ' && control != '\n' && control != '\t' {
						t.Fatalf("valeurObtenue control character %q in the output", control)
					}
				}
			}
		})
	}
}

func TestDoctor_everySFTPReasonCodeIsLabelledAndDocumented(t *testing.T) {
	codes := []string{
		"hostile_response", "short_read", "not_probed", "extension_missing", "probe_failed", "server_copy_unsupported_by_client",
		"exec_output_too_large", "exec_path_outside_roots", "exec_size_mismatch", "link_dir_world_writable", "symlink_order_unverified",
		"host_key_unknown", "host_key_mismatch", "auth_refused", "known_hosts_unreadable", "identity_key_too_weak", "identity_file_unusable",
		"identity_file_permissions", "agent_unavailable", "remote_windows_unsupported", "remote_exec_unavailable", "exec_off", "exec_denied",
		"login_shell_not_posix", "exec_probe_failed", "exec_program_missing", "exec_inconsistent", "library_root_unreadable",
		"no_media_in_first_2000", "library_maybe_writable", "volume_identity_unknown",
	}
	for _, path := range []string{"06-limits-and-troubleshooting.md", filepath.Join("en", "06-limits-and-troubleshooting.md")} {
		documentation, err := os.ReadFile(filepath.Join("..", "..", "docs", "reference", path))
		if err != nil {
			t.Fatal(err)
		}
		for _, code := range codes {
			if !strings.Contains(string(documentation), "`"+code+"`") {
				t.Errorf("%s: code %q is not documented", path, code)
			}
		}
	}
	for _, translator := range []interface{ ReasonLabel(string) string }{frenchTranslator, englishTranslator} {
		for _, code := range codes {
			if label := translator.ReasonLabel(code); label == "" || label == code {
				t.Errorf("code %q has no translated reason label (got %q)", code, label)
			}
		}
	}
}
