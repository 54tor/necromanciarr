package dedupe

import (
	"context"
	"crypto/md5" //nolint:gosec // empreinte attendue de md5sum, jamais une protection
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/fsystest"
	"necromanciarr/internal/fsys/local"
	"necromanciarr/internal/hashsource"
	"necromanciarr/internal/i18n"
)

// fakeExecHashes imite sftp-exec : l'empreinte d'un chemin sftp:// est calculée sur le fichier qui le sert, sans
// passer par le limiteur ; failing rend hashsource.ErrRemoteExecFailed.
type fakeExecHashes struct {
	volume  *fsystest.RemoteVolume
	failing atomic.Bool
	// failingPath : seul ce chemin échoue (vide : aucun).
	failingPath string
	calls       atomic.Int64
}

func (source *fakeExecHashes) Hash(_ context.Context, stored string, algorithm hashsource.Algorithm) (hashsource.Result, error) {
	source.calls.Add(1)
	if !strings.HasPrefix(stored, "sftp://") || algorithm != hashsource.MD5 {
		return hashsource.Result{}, nil
	}
	if source.failing.Load() || stored == source.failingPath {
		return hashsource.Result{}, hashsource.ErrRemoteExecFailed
	}
	location, err := fsys.ParseLocation(stored)
	if err != nil {
		return hashsource.Result{}, err
	}
	content, err := os.ReadFile(source.volume.LocalPath(location.Path))
	if err != nil {
		return hashsource.Result{}, err
	}
	sum := md5.Sum(content) //nolint:gosec // voir l'import
	return hashsource.Result{Value: hex.EncodeToString(sum[:]), Origin: hashsource.Backend, Available: true}, nil
}

// fingerprintBytes : empreintes début et fin des deux fichiers, que toute preuve relit d'abord.
const fingerprintBytes = 2 * 2 * headTailSize

func hasWarning(replacement Replacement, key i18n.Key) bool {
	return slices.ContainsFunc(replacement.Warnings, func(warning i18n.Message) bool { return warning.Key == key })
}

func TestDedupe_remoteExecHashProvesWithoutReadingAndAnnouncesRemoteHashBytes(t *testing.T) {
	layout := newRemoteLayout(t)
	backend := &fakeExecHashes{volume: layout.volume}
	options := layout.options()
	options.Backend = backend
	size := int64(len(layout.content))

	replacement := layout.planOne(t, options)

	isRemoteHashProposal := replacement.Method == MethodHash && replacement.BytesToRead == 0 &&
		replacement.RemoteHashBytes == 2*size && hasWarning(replacement, i18n.KeyWarningDedupeRemoteHashBytes) &&
		!hasWarning(replacement, i18n.KeyWarningDedupeFullRemoteRead)
	if !isRemoteHashProposal {
		t.Fatalf("gotValue %+v, expectedValue a hash proof read on the host (%d bytes announced), nothing over the network", replacement, 2*size)
	}
	result, err := Apply(context.Background(), []Replacement{replacement}, options)
	if err != nil || !result.Outcomes[0].Done || result.Outcomes[0].BytesRead != fingerprintBytes {
		t.Fatalf("gotValue %v %+v, expectedValue done with only the head and tail fingerprints read (%d bytes)", err, result, fingerprintBytes)
	}
}

func TestDedupe_remoteExecFailureAtPlanUsesAFullReadAndSaysSo(t *testing.T) {
	layout := newRemoteLayout(t)
	backend := &fakeExecHashes{volume: layout.volume}
	backend.failing.Store(true)
	options := layout.options()
	options.Backend = backend
	size := int64(len(layout.content))

	replacement := layout.planOne(t, options)

	isFullRead := replacement.Method == MethodFull && replacement.BytesToRead == 2*size && replacement.RemoteHashBytes == 0
	if !isFullRead || !hasWarning(replacement, i18n.KeyWarningDedupeRemoteExecFailed) || !hasWarning(replacement, i18n.KeyWarningDedupeFullRemoteRead) {
		t.Fatalf("gotValue %+v, expectedValue a full read announced with remote_exec_failed", replacement)
	}
}

func TestDedupe_remoteSideWithoutHashIsNeverReadLocally(t *testing.T) {
	layout := newRemoteLayout(t)
	backend := &fakeExecHashes{volume: layout.volume, failingPath: layout.libraryPath}
	options := layout.options()
	options.Backend = backend

	replacement := layout.planOne(t, options)

	if replacement.Method != MethodFull || !hasWarning(replacement, i18n.KeyWarningDedupeRemoteExecFailed) {
		t.Fatalf("gotValue %+v, expectedValue a full read: a remote file without hash cannot be hashed by a local read", replacement)
	}
}

func TestDedupe_remoteExecFailureMidStageFallsBackToAFullReadNeverSampled(t *testing.T) {
	// Au-delà de 10 Mio, une preuve par échantillons ne relirait pas les fichiers en entier.
	layout := newRemoteLayoutOfSize(t, 16*mebibyte)
	backend := &fakeExecHashes{volume: layout.volume}
	options := layout.options()
	options.Backend = backend
	size := int64(len(layout.content))
	replacement := layout.planOne(t, options)
	if replacement.Method != MethodHash {
		t.Fatalf("gotValue %s, expectedValue a hash proof planned", replacement.Method)
	}
	backend.failing.Store(true)

	result, err := Apply(context.Background(), []Replacement{replacement}, options)

	if err != nil || !result.Outcomes[0].Done || result.Outcomes[0].BytesRead < 2*size {
		t.Fatalf("gotValue %v %+v, expectedValue the replacement proven by a full read of both files (%d bytes at least)", err, result, 2*size)
	}
}

func TestDedupe_remoteExecFailureWithVerifyHashNeverReads(t *testing.T) {
	layout := newRemoteLayout(t)
	backend := &fakeExecHashes{volume: layout.volume}
	options := layout.options()
	options.Backend, options.Verify = backend, VerifyHash
	replacement := layout.planOne(t, options)
	backend.failing.Store(true)

	result, err := Apply(context.Background(), []Replacement{replacement}, options)

	outcome := result.Outcomes[0]
	if !errors.Is(err, ErrHashUnavailable) || outcome.Done || outcome.BytesRead != fingerprintBytes {
		t.Fatalf("gotValue %v %+v, expectedValue refused (hash_unavailable) after the fingerprints only", err, result)
	}
}

// identities rend l'information de stored et son identité selon le registre.
func identityOf(t *testing.T, files Files, stored string) fs.FileInfo {
	t.Helper()
	info, err := files.Lstat(stored)
	if err != nil {
		t.Fatal(err)
	}
	if _, known := files.FileID(stored, info); !known {
		t.Skip("no inode on this platform")
	}
	return info
}

func TestFiles_remoteIdentityNeverMatchesAnotherVolume(t *testing.T) {
	seedbox := fsystest.NewRemoteVolume(t, "seedbox")
	other := fsystest.NewRemoteVolume(t, "other")
	for _, volume := range []*fsystest.RemoteVolume{seedbox, other} {
		volume.FileIDKnown.Store(true)
		volume.RemoteStat.Store(true)
	}
	writeAged(t, seedbox.LocalPath(remoteLibraryDir+"/movie.mkv"), patterned(mebibyte, 3))
	if err := os.MkdirAll(other.LocalPath(remoteLibraryDir), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(seedbox.LocalPath(remoteLibraryDir+"/movie.mkv"), other.LocalPath(remoteLibraryDir+"/movie.mkv")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(seedbox.LocalPath(remoteLibraryDir+"/movie.mkv"), seedbox.LocalPath(remoteLibraryDir+"/twin.mkv")); err != nil {
		t.Fatal(err)
	}
	registry, err := fsys.NewRegistry(map[string]fsys.Volume{"": local.New(), "seedbox": seedbox, "other": other},
		[]fsys.Location{seedbox.Location(remoteLibraryDir), other.Location(remoteLibraryDir)})
	if err != nil {
		t.Fatal(err)
	}
	files := NewFiles(registry)
	remoteMovie := seedbox.Location(remoteLibraryDir + "/movie.mkv").String()
	remoteTwin := seedbox.Location(remoteLibraryDir + "/twin.mkv").String()
	otherMovie := other.Location(remoteLibraryDir + "/movie.mkv").String()
	localBacking := seedbox.LocalPath(remoteLibraryDir + "/movie.mkv")
	movieInfo, twinInfo := identityOf(t, files, remoteMovie), identityOf(t, files, remoteTwin)
	otherInfo, localInfo := identityOf(t, files, otherMovie), identityOf(t, files, localBacking)
	remoteID, _ := files.FileID(remoteMovie, movieInfo)
	localID, _ := files.FileID(localBacking, localInfo)
	if remoteID != localID {
		t.Skip("the fake remote does not share the local identity of its backing file")
	}

	if !files.SameFile(remoteMovie, movieInfo, remoteTwin, twinInfo) {
		t.Errorf("gotValue different, expectedValue a hard link of the same remote volume seen as the same file")
	}
	if files.SameFile(remoteMovie, movieInfo, localBacking, localInfo) {
		t.Errorf("gotValue same file, expectedValue a remote identity never equal to a local one, even with equal numbers")
	}
	if files.SameFile(remoteMovie, movieInfo, otherMovie, otherInfo) {
		t.Errorf("gotValue same file, expectedValue identities of two providers never compared")
	}
	if _, reliable := ReliableID(remoteMovie, movieInfo); reliable {
		t.Errorf("gotValue a reliable identity, expectedValue never for a remote path (no destructive proof)")
	}
}
