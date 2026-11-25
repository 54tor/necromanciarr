package sftpfs_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/time/rate"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/sftpfs"
	"necromanciarr/internal/fsys/sftpfs/sftptest"
)

const (
	// readFileBytes dépasse plusieurs paquets SFTP (32 Kio) : pkg/sftp lit alors par requêtes concurrentes.
	readFileBytes = 300 << 10
	maxNameBytes  = 4096
	// hostileSize dépasse 2^62 : aucune taille de fichier réelle n'en approche.
	hostileSize = 1<<62 + 1
)

// countingLimiter compte les octets attendus et la plus grosse attente ; refusal fait échouer chaque attente.
type countingLimiter struct {
	mutex   sync.Mutex
	waited  int
	largest int
	refusal error
}

func (limiter *countingLimiter) WaitN(_ context.Context, byteCount int) error {
	limiter.mutex.Lock()
	defer limiter.mutex.Unlock()
	if limiter.refusal != nil {
		return limiter.refusal
	}
	limiter.waited += byteCount
	limiter.largest = max(limiter.largest, byteCount)
	return nil
}

func (limiter *countingLimiter) totals() (waited, largest int) {
	limiter.mutex.Lock()
	defer limiter.mutex.Unlock()
	return limiter.waited, limiter.largest
}

func readVolume(t *testing.T, deps sftpfs.Deps, options ...sftptest.Option) (*sftptest.Server, *sftpfs.Volume) {
	t.Helper()
	server := sftptest.Start(t, options...)
	return server, dialVolume(t, pinnedProvider(t, server), deps)
}

func writeServedFile(t *testing.T, server *sftptest.Server, remotePath string, content []byte) {
	t.Helper()
	sftptest.WriteFile(t, filepath.Join(server.Root, filepath.FromSlash(strings.TrimPrefix(remotePath, "/"))), content, 0o644)
}

func patternBytes(length int) []byte {
	content := make([]byte, length)
	for byteIndex := range content {
		content[byteIndex] = byte(byteIndex % 251)
	}
	return content
}

func openRemote(t *testing.T, volume *sftpfs.Volume, remotePath string) fsys.File {
	t.Helper()
	file, err := volume.Open(remotePath)
	if err != nil {
		t.Fatalf("Open(%q): %v", remotePath, err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func TestSFTPOpen_sendsReadFlagOnly(t *testing.T) {
	server, volume := readVolume(t, testDeps())
	writeServedFile(t, server, "/library/movie.mkv", []byte("movie"))

	file := openRemote(t, volume, "/library/movie.mkv")
	if _, err := file.ReadAt(make([]byte, 5), 0); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}

	opens := []sftptest.Request{}
	for _, request := range server.Requests() {
		switch request.Method {
		case "Get", "Put", "Open":
			opens = append(opens, request)
		case "Setstat", "Mkdir", "Remove", "Rename", "PosixRename", "Symlink", "Link", "Rmdir":
			t.Errorf("valeurObtenue write request %+v, valeurAttendue none", request)
		}
	}
	valeurAttendue := []sftptest.Request{{Method: "Get", Path: "/library/movie.mkv", Flags: sftptest.FlagRead}}
	if !slices.Equal(opens, valeurAttendue) {
		t.Fatalf("valeurObtenue %+v, valeurAttendue %+v (SSH_FXF_READ only)", opens, valeurAttendue)
	}
}

func TestSFTPOpen_fileExposesOnlyReadMethods(t *testing.T) {
	server, volume := readVolume(t, testDeps())
	writeServedFile(t, server, "/movie.mkv", []byte("movie"))

	var file any = openRemote(t, volume, "/movie.mkv")

	forbidden := map[string]bool{}
	_, forbidden["Write"] = file.(interface{ Write([]byte) (int, error) })
	_, forbidden["WriteAt"] = file.(io.WriterAt)
	_, forbidden["ReadFrom"] = file.(io.ReaderFrom)
	_, forbidden["WriteTo"] = file.(io.WriterTo)
	_, forbidden["*sftp.File"] = file.(*sftp.File)
	_, forbidden["Truncate"] = file.(interface{ Truncate(int64) error })
	_, forbidden["Chmod"] = file.(interface{ Chmod(fs.FileMode) error })
	_, forbidden["Chown"] = file.(interface{ Chown(int, int) error })
	_, forbidden["Sync"] = file.(interface{ Sync() error })
	for method, exposed := range forbidden {
		if exposed {
			t.Errorf("valeurObtenue %s reachable by type assertion, valeurAttendue read methods only", method)
		}
	}
	_, isReader := file.(io.ReadSeeker)
	if !isReader {
		t.Errorf("valeurObtenue no Read/Seek, valeurAttendue ReadAt, Read, Seek, Close and Stat")
	}
}

func TestSFTPOpen_refusesWhatIsNotARegularFile(t *testing.T) {
	server, volume := readVolume(t, testDeps())
	writeServedFile(t, server, "/dir/file", []byte("x"))

	if _, err := volume.Open("/dir"); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("valeurObtenue %v, valeurAttendue fs.ErrInvalid for a directory", err)
	}
}

func TestSFTPReader_respectsReadLimiter(t *testing.T) {
	global := &countingLimiter{}
	deps := testDeps()
	deps.Limiter = global
	server, volume := readVolume(t, deps)
	provider := &countingLimiter{}
	sftpfs.SetProviderLimiter(volume, provider)
	content := patternBytes(readFileBytes)
	writeServedFile(t, server, "/movie.mkv", content)

	file := openRemote(t, volume, "/movie.mkv")
	buffer := make([]byte, len(content))
	readCount, err := file.ReadAt(buffer, 0)
	if err != nil || !bytes.Equal(buffer[:readCount], content) {
		t.Fatalf("ReadAt: %d bytes, err %v", readCount, err)
	}

	for name, limiter := range map[string]*countingLimiter{"provider": provider, "global": global} {
		waited, largest := limiter.totals()
		if waited != readCount {
			t.Errorf("%s: valeurObtenue %d bytes waited, valeurAttendue %d (bytes read)", name, waited, readCount)
		}
		if largest > sftpfs.LimiterChunkBytes {
			t.Errorf("%s: valeurObtenue a wait of %d bytes, valeurAttendue at most %d (burst)", name, largest, sftpfs.LimiterChunkBytes)
		}
	}
}

func TestSFTPReader_waitsBeforeReading(t *testing.T) {
	refusal := errors.New("limiter refused")
	deps := testDeps()
	deps.Limiter = &countingLimiter{refusal: refusal}
	server, volume := readVolume(t, deps)
	writeServedFile(t, server, "/movie.mkv", []byte("movie"))

	file := openRemote(t, volume, "/movie.mkv")
	buffer := make([]byte, 5)
	readCount, err := file.ReadAt(buffer, 0)
	if !errors.Is(err, refusal) || readCount != 0 || buffer[0] != 0 {
		t.Fatalf("valeurObtenue %d bytes, err %v, valeurAttendue nothing read when the limiter refuses", readCount, err)
	}
}

func TestSFTPReader_concurrentReadersShareTheLimiters(t *testing.T) {
	global := &countingLimiter{}
	deps := testDeps()
	deps.Limiter = global
	server, volume := readVolume(t, deps)
	provider := &countingLimiter{}
	sftpfs.SetProviderLimiter(volume, provider)
	const readerCount = 4
	content := patternBytes(readFileBytes)
	writeServedFile(t, server, "/movie.mkv", content)

	var group sync.WaitGroup
	readTotals := make([]int, readerCount)
	for readerIndex := range readerCount {
		group.Go(func() {
			file, err := volume.Open("/movie.mkv")
			if err != nil {
				t.Errorf("Open: %v", err)
				return
			}
			defer func() { _ = file.Close() }()
			readTotals[readerIndex], err = file.ReadAt(make([]byte, len(content)), 0)
			if err != nil {
				t.Errorf("ReadAt: %v", err)
			}
		})
	}
	group.Wait()

	totalRead := 0
	for _, readTotal := range readTotals {
		totalRead += readTotal
	}
	for name, limiter := range map[string]*countingLimiter{"provider": provider, "global": global} {
		if waited, _ := limiter.totals(); waited != totalRead || totalRead != readerCount*len(content) {
			t.Errorf("%s: valeurObtenue %d bytes waited for %d read, valeurAttendue %d", name, waited, totalRead, readerCount*len(content))
		}
	}
}

func TestSFTPReader_providerLimiterFollowsReadBytesPerSecond(t *testing.T) {
	server := sftptest.Start(t)
	unlimited := dialVolume(t, pinnedProvider(t, server), testDeps())
	if sftpfs.ProviderLimiter(unlimited) != nil {
		t.Errorf("valeurObtenue a provider limiter, valeurAttendue none when readBytesPerSecond is 0")
	}
	provider := pinnedProvider(t, server)
	provider.ReadBytesPerSecond = 1 << 20
	if sftpfs.ProviderLimiter(dialVolume(t, provider, testDeps())) == nil {
		t.Errorf("valeurObtenue no provider limiter, valeurAttendue one for readBytesPerSecond")
	}
}

func TestSFTPReader_readWithoutAnyLimiter(t *testing.T) {
	server, volume := readVolume(t, testDeps())
	writeServedFile(t, server, "/movie.mkv", []byte("movie"))

	file := openRemote(t, volume, "/movie.mkv")
	valeurObtenue, err := io.ReadAll(file.(io.Reader))
	if err != nil || string(valeurObtenue) != "movie" {
		t.Fatalf("valeurObtenue %q (err %v), valeurAttendue movie", valeurObtenue, err)
	}
}

func TestSFTPFile_staleHandleNeverBreaksANewSession(t *testing.T) {
	server, volume := readVolume(t, testDeps())
	writeServedFile(t, server, "/movie.mkv", []byte("movie"))
	file := openRemote(t, volume, "/movie.mkv")

	server.DropConnections()
	waitForState(t, volume, sftpfs.StateBroken)
	if _, err := volume.Stat("/movie.mkv"); err != nil {
		t.Fatalf("Stat after reconnect: %v", err)
	}
	if _, err := file.ReadAt(make([]byte, 5), 0); !errors.Is(err, fsys.ErrRemoteUnavailable) {
		t.Fatalf("valeurObtenue %v, valeurAttendue ErrRemoteUnavailable for a handle of the lost session", err)
	}
	if valeurObtenue := volume.State(); valeurObtenue != sftpfs.StateOK {
		t.Fatalf("valeurObtenue %q, valeurAttendue the new session kept ok", valeurObtenue)
	}
}

func TestSFTPReadDir_dropsNamesWithSlashOrDotDot(t *testing.T) {
	_, volume := readVolume(t, testDeps(), sftptest.WithListing("/library",
		sftptest.Entry{Name: "zeta"},
		sftptest.Entry{Name: "a/b"},
		sftptest.Entry{Name: ".."},
		sftptest.Entry{Name: "../"},
		sftptest.Entry{Name: "x/.."},
		sftptest.Entry{Name: "x\x00y"},
		sftptest.Entry{Name: ""},
		sftptest.Entry{Name: strings.Repeat("n", maxNameBytes+1)},
		sftptest.Entry{Name: "alpha", Size: 1},
		sftptest.Entry{Name: "alpha", Size: 2},
		sftptest.Entry{Name: "huge", Size: hostileSize},
		sftptest.Entry{Name: "negative", Size: -1},
		sftptest.Entry{Name: "bad\xffutf8\u202e"},
	))

	entries, err := volume.ReadDir("/library")
	if err != nil {
		t.Fatal(err)
	}
	valeurObtenue := []string{}
	for _, entry := range entries {
		valeurObtenue = append(valeurObtenue, entry.Name())
	}
	// pkg/sftp réduit chaque nom reçu à path.Base : « a/b » arrive « b », « ../ » et « x/.. » arrivent « .. »,
	// « » arrive « . ». Le filtre de sftpfs écarte ce qui reste dangereux ; les octets invalides et les
	// caractères de contrôle restent des noms de fichiers Linux possibles.
	valeurAttendue := []string{"alpha", "b", "bad\xffutf8\u202e", "zeta"}
	if !slices.Equal(valeurObtenue, valeurAttendue) {
		t.Fatalf("valeurObtenue %q, valeurAttendue %q", valeurObtenue, valeurAttendue)
	}
	info, err := entries[0].Info()
	if err != nil || info.Size() != 1 {
		t.Fatalf("valeurObtenue %v (err %v), valeurAttendue the first alpha kept", info, err)
	}
}

func TestSFTPReadDir_listingFilterDropsRawHostileNames(t *testing.T) {
	for _, hostileName := range []string{"", ".", "..", "a/b", "/", "x\x00y", strings.Repeat("n", maxNameBytes+1)} {
		if sftpfs.KeepListingName(hostileName) {
			t.Errorf("valeurObtenue %q kept, valeurAttendue dropped", hostileName)
		}
	}
	for _, validName := range []string{"movie.mkv", ".hidden", "...", "bad\xff", "tab\tname", strings.Repeat("n", maxNameBytes)} {
		if !sftpfs.KeepListingName(validName) {
			t.Errorf("valeurObtenue %q dropped, valeurAttendue kept", validName)
		}
	}
}

func TestSFTPReadDir_refusesAListingBeyondTheCeiling(t *testing.T) {
	sftpfs.SetMaxListingEntries(t, 2)
	_, volume := readVolume(t, testDeps(), sftptest.WithListing("/library",
		sftptest.Entry{Name: "one"}, sftptest.Entry{Name: "two"}, sftptest.Entry{Name: "three"}))

	if _, err := volume.ReadDir("/library"); !errors.Is(err, sftpfs.ErrHostileResponse) {
		t.Fatalf("valeurObtenue %v, valeurAttendue ErrHostileResponse", err)
	}
}

func TestSFTPReadlink_refusesOverlongTarget(t *testing.T) {
	_, volume := readVolume(t, testDeps(),
		sftptest.WithReadlinkTarget("/library/long", "/"+strings.Repeat("t", maxNameBytes)),
		sftptest.WithReadlinkTarget("/library/limit", strings.Repeat("t", maxNameBytes)),
		sftptest.WithReadlinkTarget("/library/nul", "a\x00b"))

	if _, err := volume.Readlink("/library/long"); !errors.Is(err, sftpfs.ErrHostileResponse) {
		t.Errorf("valeurObtenue %v, valeurAttendue ErrHostileResponse for a 4097-byte target", err)
	}
	if _, err := volume.Readlink("/library/nul"); !errors.Is(err, sftpfs.ErrHostileResponse) {
		t.Errorf("valeurObtenue %v, valeurAttendue ErrHostileResponse for a target holding NUL", err)
	}
	if target, err := volume.Readlink("/library/limit"); err != nil || len(target) != maxNameBytes {
		t.Errorf("valeurObtenue %d bytes (err %v), valeurAttendue a 4096-byte target accepted", len(target), err)
	}
}

func TestSFTPStat_refusesAbsurdSizes(t *testing.T) {
	_, volume := readVolume(t, testDeps(), sftptest.WithListing("/library",
		sftptest.Entry{Name: "huge", Size: hostileSize}, sftptest.Entry{Name: "negative", Size: -1}))

	for _, remotePath := range []string{"/library/huge", "/library/negative"} {
		if _, err := volume.Stat(remotePath); !errors.Is(err, sftpfs.ErrHostileResponse) {
			t.Errorf("Stat(%q): valeurObtenue %v, valeurAttendue ErrHostileResponse", remotePath, err)
		}
		if _, err := volume.Lstat(remotePath); !errors.Is(err, sftpfs.ErrHostileResponse) {
			t.Errorf("Lstat(%q): valeurObtenue %v, valeurAttendue ErrHostileResponse", remotePath, err)
		}
	}
}

func TestSFTPStat_mapsMissingEntriesToNotExist(t *testing.T) {
	_, volume := readVolume(t, testDeps())

	_, err := volume.Stat("/missing")
	if !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), strconv.Quote("/missing")) {
		t.Fatalf("valeurObtenue %v, valeurAttendue fs.ErrNotExist naming the quoted path", err)
	}
}

func TestSFTP_errorsMapToFsSentinels(t *testing.T) {
	cases := map[string]struct {
		cause          error
		valeurAttendue error
	}{
		"not exist":     {os.ErrNotExist, fs.ErrNotExist},
		"permission":    {os.ErrPermission, fs.ErrPermission},
		"no such path":  {&sftp.StatusError{Code: uint32(sftp.ErrSSHFxNoSuchFile)}, fs.ErrNotExist},
		"denied status": {&sftp.StatusError{Code: uint32(sftp.ErrSSHFxPermissionDenied)}, fs.ErrPermission},
		"unsupported":   {&sftp.StatusError{Code: uint32(sftp.ErrSSHFxOpUnsupported)}, errors.ErrUnsupported},
	}
	for name, testCase := range cases {
		valeurObtenue := sftpfs.MapError("stat", "/x\x1b[31m", testCase.cause)
		if !errors.Is(valeurObtenue, testCase.valeurAttendue) {
			t.Errorf("%s: valeurObtenue %v, valeurAttendue %v", name, valeurObtenue, testCase.valeurAttendue)
		}
		if strings.ContainsRune(valeurObtenue.Error(), '\x1b') {
			t.Errorf("%s: valeurObtenue a raw control character in %q", name, valeurObtenue.Error())
		}
	}
	if sftpfs.MapError("read", "/x", io.EOF) != io.EOF { //nolint:errorlint // contrat de io.Reader : io.EOF comparé par identité
		t.Errorf("valeurAttendue io.EOF returned unwrapped")
	}
}

func TestSFTPCanonical_returnsTheCleanRealPath(t *testing.T) {
	server, volume := readVolume(t, testDeps())
	writeServedFile(t, server, "/srv/media/movie.mkv", []byte("x"))

	valeurObtenue, err := volume.Canonical("/srv/./media/../media/")
	if err != nil || valeurObtenue != "/srv/media" {
		t.Fatalf("valeurObtenue %q (err %v), valeurAttendue /srv/media", valeurObtenue, err)
	}
	if fsys.RemotePaths().Clean(valeurObtenue) != valeurObtenue {
		t.Fatalf("valeurObtenue %q, valeurAttendue equal to its own Clean", valeurObtenue)
	}
}

func TestSFTPCanonical_brokenVolumeCannotCanonicalize(t *testing.T) {
	_, volume := readVolume(t, testDeps())
	_ = volume.Close()

	_, err := volume.Canonical("/srv")
	if !errors.Is(err, fsys.ErrCannotCanonicalize) || !errors.Is(err, fsys.ErrRemoteUnavailable) {
		t.Fatalf("valeurObtenue %v, valeurAttendue ErrCannotCanonicalize wrapping ErrRemoteUnavailable", err)
	}
}

func TestSFTP_volumeIDFromStatVFS(t *testing.T) {
	_, volume := readVolume(t, testDeps())
	id, known, err := volume.VolumeID("/")
	if err != nil || !known || id != "fsid:0000000000000001" {
		t.Fatalf("valeurObtenue %q known %v (err %v), valeurAttendue fsid:0000000000000001", id, known, err)
	}

	_, withoutStatVFS := readVolume(t, testDeps(), sftptest.WithoutStatVFS())
	if id, known, err := withoutStatVFS.VolumeID("/"); err != nil || known || id != "" {
		t.Fatalf("valeurObtenue %q known %v (err %v), valeurAttendue unknown without statvfs", id, known, err)
	}

	_, zeroFsid := readVolume(t, testDeps(), sftptest.WithStatVFS(func(reply *sftp.StatVFS) { reply.Fsid = 0 }))
	if _, known, err := zeroFsid.VolumeID("/"); err != nil || known {
		t.Fatalf("valeurObtenue known %v (err %v), valeurAttendue unknown for fsid 0", known, err)
	}
}

func TestSFTP_freeSpaceUnknownWithoutStatVFS(t *testing.T) {
	_, volume := readVolume(t, testDeps(), sftptest.WithoutStatVFS())

	if _, err := volume.FreeSpace("/"); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("valeurObtenue %v, valeurAttendue errors.ErrUnsupported (linker: ErrFreeSpaceUnknown)", err)
	}
}

func TestSFTP_freeSpaceFromStatVFS(t *testing.T) {
	_, volume := readVolume(t, testDeps())

	valeurObtenue, err := volume.FreeSpace("/")
	if valeurAttendue := uint64(1<<19) * 4096; err != nil || valeurObtenue != valeurAttendue {
		t.Fatalf("valeurObtenue %d (err %v), valeurAttendue %d", valeurObtenue, err, valeurAttendue)
	}
}

func TestSFTP_freeSpaceRefusesHostileStatVFS(t *testing.T) {
	cases := map[string]func(*sftp.StatVFS){
		"zero fragment size": func(reply *sftp.StatVFS) { reply.Frsize = 0 },
		"overflow":           func(reply *sftp.StatVFS) { reply.Bavail, reply.Frsize = 1<<60, 1<<10 },
		"beyond 2^62":        func(reply *sftp.StatVFS) { reply.Bavail, reply.Frsize = 1<<51, 1<<12 },
	}
	for name, mutate := range cases {
		_, volume := readVolume(t, testDeps(), sftptest.WithStatVFS(mutate))
		if _, err := volume.FreeSpace("/"); !errors.Is(err, sftpfs.ErrHostileResponse) {
			t.Errorf("%s: valeurObtenue %v, valeurAttendue ErrHostileResponse", name, err)
		}
	}
}

func TestSFTP_identityAndSyntax(t *testing.T) {
	server, volume := readVolume(t, testDeps())
	writeServedFile(t, server, "/movie.mkv", []byte("x"))
	info, err := volume.Stat("/movie.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if _, known := volume.FileID(info); known {
		t.Errorf("valeurObtenue a known FileID, valeurAttendue unknown without ssh exec stat")
	}
	if volume.Volume() != "seedbox" {
		t.Errorf("valeurObtenue %q, valeurAttendue the provider name", volume.Volume())
	}
	if volume.Paths() != fsys.RemotePaths() {
		t.Errorf("valeurAttendue POSIX remote paths")
	}
	capabilities, err := volume.Capabilities(context.Background())
	if err != nil || capabilities.FileIdentity || capabilities.RemoteStat || capabilities.ServerCopy || !capabilities.VolumeIdentity {
		t.Errorf("valeurObtenue %+v (err %v), valeurAttendue VolumeIdentity only", capabilities, err)
	}
	_, withoutStatVFS := readVolume(t, testDeps(), sftptest.WithoutStatVFS())
	if capabilities, err := withoutStatVFS.Capabilities(context.Background()); err != nil || capabilities.VolumeIdentity {
		t.Errorf("valeurObtenue %+v (err %v), valeurAttendue no VolumeIdentity without statvfs", capabilities, err)
	}
}

func TestSFTP_refusesRelativeOrNULNames(t *testing.T) {
	_, volume := readVolume(t, testDeps())

	for _, name := range []string{"relative", "/a\x00b", ""} {
		if _, err := volume.Stat(name); !errors.Is(err, fs.ErrInvalid) {
			t.Errorf("Stat(%q): valeurObtenue %v, valeurAttendue fs.ErrInvalid", name, err)
		}
	}
}

// TestSources_neverUseFilepath : un chemin distant est toujours POSIX ; filepath le casserait sous Windows.
func TestSources_neverUseFilepath(t *testing.T) {
	sourceFiles, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fileSet := token.NewFileSet()
	for _, sourceFile := range sourceFiles {
		if strings.HasSuffix(sourceFile, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fileSet, sourceFile, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imported := range parsed.Imports {
			if imported.Path.Value == strconv.Quote("path/filepath") {
				t.Errorf("%s imports path/filepath", sourceFile)
			}
		}
		source, err := os.ReadFile(sourceFile)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(source, []byte("PathSeparator")) || bytes.Contains(source, []byte("IsPathSeparator")) {
			t.Errorf("%s uses the local path separator", sourceFile)
		}
	}
}

// waitForRequest attend que le serveur ait reçu une requête de cette méthode.
func waitForRequest(t *testing.T, server *sftptest.Server, method string) {
	t.Helper()
	deadline := time.Now().Add(settleTimeout)
	for time.Now().Before(deadline) {
		for _, request := range server.Requests() {
			if request.Method == method {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("valeurAttendue a %s request", method)
}

func assertRemoteUnavailableNotEndOfFile(t *testing.T, label string, err error) {
	t.Helper()
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("%s: valeurObtenue %v matching end of file, valeurAttendue a lost connection only", label, err)
	}
	if !errors.Is(err, fsys.ErrRemoteUnavailable) {
		t.Errorf("%s: valeurObtenue %v, valeurAttendue ErrRemoteUnavailable", label, err)
	}
}

func TestSFTP_lostConnectionIsNeverEndOfFile(t *testing.T) {
	operations := map[string]func(volume *sftpfs.Volume) error{
		"Stat": func(volume *sftpfs.Volume) error { _, err := volume.Stat("/"); return err },
		"List": func(volume *sftpfs.Volume) error { _, err := volume.ReadDir("/"); return err },
	}
	for method, operation := range operations {
		server, volume := readVolume(t, testDeps(), sftptest.StallMethod(method))
		outcome := make(chan error, 1)
		go func() { outcome <- operation(volume) }()
		waitForRequest(t, server, method)

		server.DropConnections()

		lostErr := <-outcome
		assertRemoteUnavailableNotEndOfFile(t, method, lostErr)
		waitForState(t, volume, sftpfs.StateBroken)
	}
}

func TestSFTPReader_lostConnectionIsNeverEndOfFile(t *testing.T) {
	server, volume := readVolume(t, testDeps())
	writeServedFile(t, server, "/movie.mkv", patternBytes(readFileBytes))
	file := openRemote(t, volume, "/movie.mkv")

	server.DropConnections()
	_, err := file.ReadAt(make([]byte, readFileBytes), 0)

	assertRemoteUnavailableNotEndOfFile(t, "ReadAt", err)
	waitForState(t, volume, sftpfs.StateBroken)
}

func TestSFTPReader_closeReleasesALimiterWait(t *testing.T) {
	server, volume := readVolume(t, testDeps())
	sftpfs.SetProviderLimiter(volume, rate.NewLimiter(1, sftpfs.LimiterChunkBytes))
	writeServedFile(t, server, "/movie.mkv", patternBytes(2*sftpfs.LimiterChunkBytes))
	file := openRemote(t, volume, "/movie.mkv")
	outcome := make(chan error, 1)
	go func() {
		_, err := file.ReadAt(make([]byte, 2*sftpfs.LimiterChunkBytes), 0)
		outcome <- err
	}()
	time.Sleep(100 * time.Millisecond)

	_ = volume.Close()

	select {
	case err := <-outcome:
		if !errors.Is(err, fsys.ErrRemoteUnavailable) {
			t.Fatalf("valeurObtenue %v, valeurAttendue ErrRemoteUnavailable", err)
		}
	case <-time.After(time.Second):
		t.Fatal("valeurObtenue a limiter wait still blocked 1 s after Close, valeurAttendue released")
	}
}

func TestSFTPReader_staleHandlePaysNoTokens(t *testing.T) {
	global := &countingLimiter{}
	deps := testDeps()
	deps.Limiter = global
	server, volume := readVolume(t, deps)
	provider := &countingLimiter{}
	sftpfs.SetProviderLimiter(volume, provider)
	writeServedFile(t, server, "/movie.mkv", []byte("movie"))
	file := openRemote(t, volume, "/movie.mkv")
	server.DropConnections()
	waitForState(t, volume, sftpfs.StateBroken)

	_, err := file.ReadAt(make([]byte, 5), 0)

	if !errors.Is(err, fsys.ErrRemoteUnavailable) {
		t.Fatalf("valeurObtenue %v, valeurAttendue ErrRemoteUnavailable", err)
	}
	for name, limiter := range map[string]*countingLimiter{"provider": provider, "global": global} {
		if waited, _ := limiter.totals(); waited != 0 {
			t.Errorf("%s: valeurObtenue %d bytes waited, valeurAttendue 0 for a stale handle", name, waited)
		}
	}
}

func TestSFTPReader_negativeOffsetIsInvalid(t *testing.T) {
	server, volume := readVolume(t, testDeps())
	writeServedFile(t, server, "/movie.mkv", []byte("movie"))
	file := openRemote(t, volume, "/movie.mkv")

	if _, err := file.ReadAt(make([]byte, 5), -1); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("valeurObtenue %v, valeurAttendue fs.ErrInvalid", err)
	}
}

func TestSFTP_transportFailuresAreConnectionLosses(t *testing.T) {
	for label, testCase := range map[string]struct {
		err            error
		valeurAttendue bool
	}{
		"send header EOF":  {fmt.Errorf("failed to send packet: %w", io.EOF), true},
		"send payload EOF": {fmt.Errorf("failed to send packet payload: %w", io.EOF), true},
		"connection lost":  {fmt.Errorf("%w: %w", sftp.ErrSSHFxConnectionLost, io.EOF), true},
		"closed pipe":      {io.ErrClosedPipe, true},
		"closed network":   {fmt.Errorf("write: %w", net.ErrClosed), true},
		"exit missing":     {&ssh.ExitMissingError{}, true},
		"end of file":      {io.EOF, false},
		"missing entry":    {os.ErrNotExist, false},
		"text only":        {errors.New("failed to send packet: EOF"), false},
		"path naming a send failure": {
			&fs.PathError{Op: "remove", Path: "/links/failed to send packet: EOF", Err: fs.ErrNotExist}, false,
		},
		"path naming a loss": {&fs.PathError{Op: "lstat", Path: "/links/connection lost", Err: fs.ErrNotExist}, false},
		"path wrapping a send failure": {
			&fs.PathError{Op: "remove", Path: "/links/x", Err: fmt.Errorf("failed to send packet: %w", io.EOF)}, false,
		},
		"unconfined name": {
			fmt.Errorf("%w: sftpfs: %q is a symlink: failed to send packet: %w", fsys.ErrUnconfined, "connection lost", fs.ErrInvalid), false,
		},
		"wrapped send failure": {fmt.Errorf("sftpfs: stat: %w", fmt.Errorf("failed to send packet: %w", io.EOF)), true},
		"joined send failure":  {errors.Join(fs.ErrNotExist, fmt.Errorf("failed to send packet payload: %w", io.ErrClosedPipe)), true},
	} {
		if valeurObtenue := sftpfs.IsTransportFailure(testCase.err); valeurObtenue != testCase.valeurAttendue {
			t.Errorf("%s: valeurObtenue %v, valeurAttendue %v", label, valeurObtenue, testCase.valeurAttendue)
		}
	}
}

// assertShortReadKeepsTheSession : une fin de fichier avant la taille, fstat réussi, n'est ni io.EOF ni une session
// cassée : ErrShortRead (sous ErrRemoteUnavailable), session gardée.
func assertShortReadKeepsTheSession(t *testing.T, label string, volume *sftpfs.Volume, err error) {
	t.Helper()
	assertRemoteUnavailableNotEndOfFile(t, label, err)
	if !errors.Is(err, sftpfs.ErrShortRead) {
		t.Errorf("%s: valeurObtenue %v, valeurAttendue ErrShortRead", label, err)
	}
	if valeurObtenue := volume.State(); valeurObtenue != sftpfs.StateOK {
		t.Errorf("%s: valeurObtenue state %q, valeurAttendue the live session kept", label, valeurObtenue)
	}
}

func TestSFTPReader_endOfFileBeforeTheKnownSizeIsAShortReadOnALiveSession(t *testing.T) {
	server, volume := readVolume(t, testDeps(), sftptest.WithTruncatedReads("/movie.mkv", 1000))
	writeServedFile(t, server, "/movie.mkv", patternBytes(readFileBytes))
	file := openRemote(t, volume, "/movie.mkv")

	_, err := file.ReadAt(make([]byte, readFileBytes), 0)

	assertShortReadKeepsTheSession(t, "ReadAt", volume, err)
}

func TestSFTPReader_sequentialEndOfFileBeforeTheKnownSizeIsAShortRead(t *testing.T) {
	server, volume := readVolume(t, testDeps(), sftptest.WithTruncatedReads("/movie.mkv", 4))
	writeServedFile(t, server, "/movie.mkv", []byte("0123456789"))
	file := openRemote(t, volume, "/movie.mkv")
	reader, isReader := file.(io.Reader)
	if !isReader {
		t.Fatal("valeurAttendue a Read method")
	}

	_, err := io.ReadAll(reader)

	assertShortReadKeepsTheSession(t, "Read", volume, err)
}

func TestSFTPReader_readReachingTheSizeIsEndOfFile(t *testing.T) {
	server, volume := readVolume(t, testDeps())
	writeServedFile(t, server, "/movie.mkv", []byte("0123456789"))
	file := openRemote(t, volume, "/movie.mkv")

	readCount, err := file.ReadAt(make([]byte, 10), 5)

	if readCount != 5 || !errors.Is(err, io.EOF) || errors.Is(err, fsys.ErrRemoteUnavailable) {
		t.Errorf("valeurObtenue %d bytes, %v, valeurAttendue 5 bytes and a real io.EOF", readCount, err)
	}
	if valeurObtenue := volume.State(); valeurObtenue != sftpfs.StateOK {
		t.Errorf("valeurObtenue %q, valeurAttendue the session kept", valeurObtenue)
	}
}

func TestSFTP_sendFailureBeforeTheSessionEndIsNoticedIsALostConnection(t *testing.T) {
	_, volume := readVolume(t, testDeps())

	err := sftpfs.ClassifyOnLiveSession(volume, fmt.Errorf("failed to send packet: %w", io.EOF))

	assertRemoteUnavailableNotEndOfFile(t, "Stat", err)
	waitForState(t, volume, sftpfs.StateBroken)
}
