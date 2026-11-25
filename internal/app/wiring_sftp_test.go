package app

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"necromanciarr/internal/config"
	"necromanciarr/internal/doctor"
	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/local"
	"necromanciarr/internal/fsys/sftpfs"
	"necromanciarr/internal/fsys/sftpfs/sftptest"
	"necromanciarr/internal/pipeline"
)

// Adresses du serveur de test : bibliothèque et dossier de liens voisins, comme sur une seedbox.
const (
	seedboxName    = "seedbox"
	seedboxLibrary = "/library"
	seedboxLinks   = "/links"
)

// writeWaitTimeout borne l'attente de la fermeture d'une connexion vue par le serveur.
const writeWaitTimeout = 2 * time.Second

func startSeedbox(t *testing.T) *sftptest.Server {
	t.Helper()
	server := sftptest.Start(t)
	sftptest.WriteFile(t, filepath.Join(server.Root, "library", "movie.mkv"), []byte("library bytes"), 0o644)
	sftptest.Mkdir(t, filepath.Join(server.Root, "links"))
	return server
}

func seedboxSettings(t *testing.T, provider config.FSProvider) config.Config {
	t.Helper()
	return config.Config{
		ConfigDir: t.TempDir(), DBPath: "necromanciarr.db", ReadBytesPerSecond: 1 << 20, LinkType: "hardlink", CrossSeedMode: "off",
		LibraryRoots: []string{"sftp://" + seedboxName + seedboxLibrary}, LinkDirs: []string{"sftp://" + seedboxName + seedboxLinks},
		FSProviders: []config.FSProvider{provider},
	}
}

func seedboxLocation(remotePath string) fsys.Location {
	return fsys.Location{Volume: seedboxName, Path: remotePath}
}

func buildTestRegistry(t *testing.T, settings config.Config, deps sftpfs.Deps) (*fsys.Registry, *sftpVolumes) {
	t.Helper()
	registry, closer, err := buildRegistry(context.Background(), settings, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closer.Close() })
	volumes, isSet := closer.(*sftpVolumes)
	if !isSet {
		t.Fatalf("valeurObtenue closer %T, valeurAttendue the sftp volume set", closer)
	}
	return registry, volumes
}

// writeRequestsUnder rend les requêtes qui écrivent sous dir (méthodes d'écriture de sftptest).
func writeRequestsUnder(server *sftptest.Server, dir string) []sftptest.Request {
	writeMethods := []string{"Put", "Open", "Setstat", "Mkdir", "Rmdir", "Remove", "Rename", "PosixRename", "Link", "Symlink"}
	var writes []sftptest.Request
	for _, request := range server.Requests() {
		touches := request.Path == dir || strings.HasPrefix(request.Path, dir+"/") ||
			request.Target == dir || strings.HasPrefix(request.Target, dir+"/")
		if slices.Contains(writeMethods, request.Method) && touches {
			writes = append(writes, request)
		}
	}
	return writes
}

func TestBuildRegistry_probesTheLinkDirBeforeReturningItsFirstRoot(t *testing.T) {
	server := startSeedbox(t)
	openedStore := openTestStore(t)
	registry, volumes := buildTestRegistry(t, seedboxSettings(t, server.Provider(t, seedboxName)), sftpfs.Deps{})
	volumes.saveProbesTo(openedStore)

	root, err := registry.WriteRoot(seedboxLocation(seedboxLinks))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	probeWrites := writeRequestsUnder(server, seedboxLinks)
	stored, _, found, err := openedStore.VolumeCapabilities(context.Background(), seedboxName)

	if len(probeWrites) == 0 {
		t.Fatal("valeurObtenue no request in the link folder, valeurAttendue the capability probe before the root is returned")
	}
	if err != nil || !found || !stored.Hardlink || !stored.AtomicReplace {
		t.Fatalf("valeurObtenue %+v found %v err %v, valeurAttendue the probed capabilities stored", stored, found, err)
	}
	if writes := writeRequestsUnder(server, seedboxLibrary); len(writes) != 0 {
		t.Fatalf("valeurObtenue %+v, valeurAttendue no write under the library", writes)
	}
}

func TestBuildRegistry_refusesTheLibraryRootAsALinkDir(t *testing.T) {
	server := startSeedbox(t)
	registry, _ := buildTestRegistry(t, seedboxSettings(t, server.Provider(t, seedboxName)), sftpfs.Deps{})

	_, err := registry.WriteRoot(seedboxLocation(seedboxLibrary))

	if !errors.Is(err, fsys.ErrWriteInLibrary) {
		t.Fatalf("valeurObtenue %v, valeurAttendue fsys.ErrWriteInLibrary", err)
	}
	if writes := writeRequestsUnder(server, seedboxLibrary); len(writes) != 0 {
		t.Fatalf("valeurObtenue %+v, valeurAttendue no write under the library", writes)
	}
}

// La défense propre du volume : même demandée par un registre qui ignore la bibliothèque, une racine qui la recouvre
// est refusée par le volume avant toute requête, parce que la configuration lui a donné ses racines de bibliothèque.
func TestBuildRegistry_givesEachSFTPVolumeItsLibraryRoots(t *testing.T) {
	server := startSeedbox(t)
	_, volumes := buildTestRegistry(t, seedboxSettings(t, server.Provider(t, seedboxName)), sftpfs.Deps{})
	careless, err := fsys.NewRegistry(map[string]fsys.Volume{seedboxName: volumes.volumes[seedboxName]},
		[]fsys.Location{seedboxLocation("/elsewhere")})
	if err != nil {
		t.Fatal(err)
	}

	root, err := careless.WriteRoot(seedboxLocation(seedboxLibrary))

	if root != nil || !errors.Is(err, fsys.ErrWriteInLibrary) {
		t.Fatalf("valeurObtenue %v, valeurAttendue fsys.ErrWriteInLibrary from the volume's own library roots", err)
	}
	if writes := writeRequestsUnder(server, seedboxLibrary); len(writes) != 0 {
		t.Fatalf("valeurObtenue %+v, valeurAttendue no write under the library", writes)
	}
}

// countingLimiter compte les octets que l'on attend sur lui.
type countingLimiter struct{ waited atomic.Int64 }

func (limiter *countingLimiter) WaitN(_ context.Context, byteCount int) error {
	limiter.waited.Add(int64(byteCount))
	return nil
}

// Option A (C10) : le plafond global est payé par torrentcheck, dedupe et hashsource ; le volume ne le paie jamais une
// seconde fois.
func TestBuildRegistry_sftpReadsNeverChargeTheGlobalLimiter(t *testing.T) {
	server := startSeedbox(t)
	global := &countingLimiter{}
	registry, _ := buildTestRegistry(t, seedboxSettings(t, server.Provider(t, seedboxName)), sftpfs.Deps{Limiter: global})
	view, name, err := registry.Read(seedboxLocation(seedboxLibrary + "/movie.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	file, err := view.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })

	buffer := make([]byte, 4)
	if _, err := file.ReadAt(buffer, 0); err != nil {
		t.Fatal(err)
	}

	if valeurObtenue := global.waited.Load(); valeurObtenue != 0 {
		t.Fatalf("valeurObtenue %d bytes charged by the volume, valeurAttendue 0", valeurObtenue)
	}
}

func TestBuildRegistry_closerClosesEverySFTPConnection(t *testing.T) {
	server := startSeedbox(t)
	registry, closer, err := buildRegistry(context.Background(), seedboxSettings(t, server.Provider(t, seedboxName)), sftpfs.Deps{})
	if err != nil {
		t.Fatal(err)
	}
	view, name, err := registry.Read(seedboxLocation(seedboxLibrary))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := view.Stat(name); err != nil {
		t.Fatal(err)
	}
	openBeforeClose := server.OpenConnections()

	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(writeWaitTimeout)
	for server.OpenConnections() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if valeurObtenue := server.OpenConnections(); openBeforeClose != 1 || valeurObtenue != 0 {
		t.Fatalf("valeurObtenue %d then %d open connections, valeurAttendue 1 then 0", openBeforeClose, valeurObtenue)
	}
}

// otherHostKeyLine épingle pour address une clé générée qui n'est pas celle du serveur.
func otherHostKeyLine(t *testing.T, address string) string {
	t.Helper()
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPublicKey, err := ssh.NewPublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	return knownhosts.Line([]string{knownhosts.Normalize(address)}, sshPublicKey)
}

func TestBuildRegistry_wrongKnownHostsKeepsTheHostKeyCodeAndWritesNothing(t *testing.T) {
	server := startSeedbox(t)
	provider := server.Provider(t, seedboxName)
	sftptest.WriteFile(t, provider.KnownHosts, []byte(otherHostKeyLine(t, server.Address)+"\n"), 0o600)
	registry, _ := buildTestRegistry(t, seedboxSettings(t, provider), sftpfs.Deps{})

	_, err := registry.WriteRoot(seedboxLocation(seedboxLinks))

	if valeurObtenue := sftpfs.Code(err); valeurObtenue != sftpfs.CodeHostKeyMismatch || !errors.Is(err, fsys.ErrRemoteUnavailable) {
		t.Fatalf("valeurObtenue code %q (err %v), valeurAttendue %q", valeurObtenue, err, sftpfs.CodeHostKeyMismatch)
	}
	if requests := server.Requests(); len(requests) != 0 {
		t.Fatalf("valeurObtenue %+v, valeurAttendue no SFTP request behind a refused host key", requests)
	}
}

// closedAddress rend une adresse de boucle locale où plus rien n'écoute.
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

func TestNewFromConfig_unreachableSeedboxLetsTheAppStartAndLocalStagesRun(t *testing.T) {
	server := startSeedbox(t)
	provider := server.Provider(t, seedboxName)
	provider.Address = closedAddress(t)
	settings := seedboxSettings(t, provider)
	localLibrary := t.TempDir()
	if err := os.WriteFile(filepath.Join(localLibrary, "local.mkv"), []byte("local bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	settings.LibraryRoots = append([]string{localLibrary}, settings.LibraryRoots...)

	service, closer, err := NewFromConfig(context.Background(), settings)
	if err != nil {
		t.Fatalf("valeurObtenue %v, valeurAttendue the service built without reaching the seedbox", err)
	}
	t.Cleanup(func() { _ = closer.Close() })
	result, err := service.RunStage(context.Background(), pipeline.StageScan)

	if err != nil || result.Processed != 1 || result.Reasons[pipeline.ReasonRemoteUnavailable] != 1 {
		t.Fatalf("valeurObtenue %+v err %v, valeurAttendue the local file scanned and the seedbox counted remote_unavailable", result, err)
	}
	if _, canPing := closer.(interface{ Ping(context.Context) error }); !canPing {
		t.Fatalf("valeurObtenue closer %T, valeurAttendue the store's health probe kept", closer)
	}
}

// innerVolume nomme le champ embarqué autrement que la méthode Volume qu'il apporte.
type innerVolume = fsys.Volume

// resettingVolume compte les ouvertures d'étape d'un volume distant.
type resettingVolume struct {
	innerVolume
	resets atomic.Int32
}

func (volume *resettingVolume) ResetForStage() { volume.resets.Add(1) }

func TestDoctor_givesEachRemoteVolumeItsReconnect(t *testing.T) {
	volume := &resettingVolume{innerVolume: local.New()}
	registry, err := fsys.NewRegistry(map[string]fsys.Volume{"": volume}, []fsys.Location{{Path: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	service := New(Deps{Stages: pipeline.Deps{Registry: registry}})

	if _, err := service.Doctor(context.Background(), DoctorOptions{CheckIDs: []string{doctor.IDLibraryRoots}}); err != nil {
		t.Fatal(err)
	}

	if valeurObtenue := volume.resets.Load(); valeurObtenue != 1 {
		t.Fatalf("valeurObtenue %d resets, valeurAttendue 1", valeurObtenue)
	}
}

func TestBuildRegistry_admitsLinkDirsAndTorrentRootsForRemoteCommands(t *testing.T) {
	server := startSeedbox(t)
	settings := seedboxSettings(t, server.Provider(t, seedboxName))
	settings.Dedupe.TorrentRoots = []string{"sftp://" + seedboxName + "/torrents", "/local/torrents"}
	_, volumes := buildTestRegistry(t, settings, sftpfs.Deps{})
	volume := volumes.volumes[seedboxName]

	for _, admitted := range []string{seedboxLibrary + "/movie.mkv", seedboxLinks + "/movie.mkv", "/torrents/movie.mkv"} {
		_, err := volume.Exec(context.Background(), sftpfs.CommandStat, []string{admitted})
		if errors.Is(err, sftpfs.ErrExecPathOutsideRoots) || !errors.Is(err, sftpfs.ErrExecDisabled) {
			t.Errorf("%s : valeurObtenue %v, valeurAttendue admis puis exec refusé par le serveur de test", admitted, err)
		}
	}
	for _, refused := range []string{"/local/torrents/movie.mkv", "/elsewhere/movie.mkv"} {
		if _, err := volume.Exec(context.Background(), sftpfs.CommandStat, []string{refused}); !errors.Is(err, sftpfs.ErrExecPathOutsideRoots) {
			t.Errorf("%s : valeurObtenue %v, valeurAttendue ErrExecPathOutsideRoots", refused, err)
		}
	}
}
