package sftpfs_test

import (
	"context"
	"errors"
	"path"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/fsystest"
	"necromanciarr/internal/fsys/sftpfs"
	"necromanciarr/internal/fsys/sftpfs/sftptest"
)

// Clés et codes de Capabilities.Reasons attendus (contrat machine).
const (
	reasonKeySymlink       = "symlink"
	reasonKeyHardlink      = "hardlink"
	reasonKeyAtomicReplace = "atomicReplace"
	reasonKeyVolumeID      = "volumeIdentity"
	reasonKeyServerCopy    = "serverCopy"
	codeExtensionMissing   = "extension_missing"
	codeProbeFailed        = "probe_failed"
	codeNotProbed          = "not_probed"
	codeServerCopyByClient = "server_copy_unsupported_by_client"
	probeEntryName         = ".necromanciarr-probe-0123456789abcdef0123456789abcdef"
	cancelSettleDelay      = 50 * time.Millisecond
)

func probeLinks(t *testing.T, volume *sftpfs.Volume) (fsys.Capabilities, error) {
	t.Helper()
	return volume.ProbeCapabilities(t.Context(), openLinksRoot(t, volume))
}

func mustProbeLinks(t *testing.T, volume *sftpfs.Volume) fsys.Capabilities {
	t.Helper()
	capabilities, err := probeLinks(t, volume)
	if err != nil {
		t.Fatalf("ProbeCapabilities: %v", err)
	}
	return capabilities
}

func assertLinkDirEmpty(t *testing.T, server *sftptest.Server) {
	t.Helper()
	if valeurObtenue := servedEntries(t, server, linksDir); len(valeurObtenue) != 0 {
		t.Errorf("valeurObtenue %q, valeurAttendue no probe entry left in the link dir", valeurObtenue)
	}
}

func isUnder(remotePath, directory string) bool {
	return remotePath == directory || strings.HasPrefix(remotePath, directory+"/")
}

func TestCapabilities_probesOnlyInLinkDir(t *testing.T) {
	server, volume := writeVolume(t)
	before := len(server.Requests())

	capabilities := mustProbeLinks(t, volume)

	isEverythingProbed := capabilities.Symlink && capabilities.Hardlink && capabilities.AtomicReplace && capabilities.VolumeIdentity
	if !isEverythingProbed {
		t.Errorf("valeurObtenue %+v, valeurAttendue symlink, hardlink, atomic replace and statvfs proven", capabilities)
	}
	for _, request := range server.Requests()[before:] {
		for _, requestPath := range []string{request.Path, request.Target} {
			if requestPath == "" {
				continue
			}
			if !isUnder(requestPath, linksDir) || isUnder(requestPath, libraryDir) {
				t.Errorf("valeurObtenue %+v, valeurAttendue every probe request under %s", request, linksDir)
			}
		}
	}
	assertLinkDirEmpty(t, server)
	if valeurObtenue, err := volume.Capabilities(t.Context()); err != nil || !valeurObtenue.Hardlink || !valeurObtenue.AtomicReplace {
		t.Errorf("valeurObtenue %+v (err %v), valeurAttendue the probed capabilities kept for the session", valeurObtenue, err)
	}
}

func TestCapabilities_refusesARootInsideALibraryRoot(t *testing.T) {
	server := sftptest.Start(t)
	sftptest.Mkdir(t, localPathOf(server, libraryDir+"/sub"))
	deps := testDeps()
	deps.LibraryRoots = []string{libraryDir}
	volume := dialVolume(t, pinnedProvider(t, server), deps)
	for _, directory := range []string{libraryDir, libraryDir + "/sub"} {
		root, err := volume.OpenWriteRoot(directory)
		if err != nil {
			t.Fatalf("OpenWriteRoot(%s): %v", directory, err)
		}
		before := len(server.Requests())

		_, err = volume.ProbeCapabilities(t.Context(), root)

		if !errors.Is(err, fsys.ErrWriteInLibrary) {
			t.Errorf("%s: valeurObtenue %v, valeurAttendue ErrWriteInLibrary", directory, err)
		}
		if requests := server.Requests()[before:]; len(requests) != 0 {
			t.Errorf("%s: valeurObtenue %+v, valeurAttendue no request", directory, requests)
		}
	}
}

func TestCapabilities_detectsReversedSymlinkArguments(t *testing.T) {
	server, volume := writeVolume(t, sftptest.ReversedSymlinkArgs())
	root := openLinksRoot(t, volume)

	capabilities, err := volume.ProbeCapabilities(t.Context(), root)
	if err != nil {
		t.Fatalf("ProbeCapabilities: %v", err)
	}

	if capabilities.Symlink || capabilities.Reasons[reasonKeySymlink] != codeProbeFailed {
		t.Errorf("valeurObtenue %+v, valeurAttendue Symlink false with %s", capabilities, codeProbeFailed)
	}
	if err := root.Symlink("../x", "link"); !errors.Is(err, sftpfs.ErrSymlinkOrderUnverified) {
		t.Errorf("valeurObtenue %v, valeurAttendue ErrSymlinkOrderUnverified", err)
	}
	assertLinkDirEmpty(t, server)
	valeurAttendue := []string{"library", "links", "outside"}
	if valeurObtenue := servedEntries(t, server, "/"); !slices.Equal(valeurObtenue, valeurAttendue) {
		t.Errorf("server root: valeurObtenue %q, valeurAttendue %q", valeurObtenue, valeurAttendue)
	}
}

func TestCapabilities_withoutPosixRenameDisablesReplace(t *testing.T) {
	server, volume := writeVolume(t, sftptest.WithoutExtension("posix-rename@openssh.com"))

	capabilities := mustProbeLinks(t, volume)

	if capabilities.AtomicReplace || capabilities.Reasons[reasonKeyAtomicReplace] != codeExtensionMissing {
		t.Errorf("valeurObtenue %+v, valeurAttendue AtomicReplace false with %s", capabilities, codeExtensionMissing)
	}
	if renames := append(methodsSince(server, 0, "Rename"), methodsSince(server, 0, "PosixRename")...); len(renames) != 0 {
		t.Errorf("valeurObtenue %+v, valeurAttendue no rename request", renames)
	}
	if !capabilities.Hardlink {
		t.Errorf("valeurObtenue %+v, valeurAttendue Hardlink still proven", capabilities)
	}
	assertLinkDirEmpty(t, server)
}

func TestCapabilities_withoutHardlinkDisablesHardlink(t *testing.T) {
	server, volume := writeVolume(t, sftptest.WithoutExtension("hardlink@openssh.com"))

	capabilities := mustProbeLinks(t, volume)

	if capabilities.Hardlink || capabilities.Reasons[reasonKeyHardlink] != codeExtensionMissing {
		t.Errorf("valeurObtenue %+v, valeurAttendue Hardlink false with %s", capabilities, codeExtensionMissing)
	}
	if links := methodsSince(server, 0, "Link"); len(links) != 0 {
		t.Errorf("valeurObtenue %+v, valeurAttendue no Link request", links)
	}
	if !capabilities.AtomicReplace {
		t.Errorf("valeurObtenue %+v, valeurAttendue AtomicReplace still proven", capabilities)
	}
	assertLinkDirEmpty(t, server)
}

func TestCapabilities_withoutStatVFSDisablesVolumeIdentity(t *testing.T) {
	server, volume := writeVolume(t, sftptest.WithoutStatVFS())

	capabilities := mustProbeLinks(t, volume)

	if capabilities.VolumeIdentity || capabilities.Reasons[reasonKeyVolumeID] != codeExtensionMissing {
		t.Errorf("valeurObtenue %+v, valeurAttendue VolumeIdentity false with %s", capabilities, codeExtensionMissing)
	}
	if statvfs := methodsSince(server, 0, "StatVFS"); len(statvfs) != 0 {
		t.Errorf("valeurObtenue %+v, valeurAttendue no StatVFS request", statvfs)
	}
}

func TestCapabilities_advertisedExtensionWhoseTryFailsIsProbeFailed(t *testing.T) {
	for _, scenario := range []struct {
		name, reasonKey string
		option          sftptest.Option
	}{
		{"hardlink refused", reasonKeyHardlink, sftptest.FailMethod("Link", 1)},
		{"posix-rename refused", reasonKeyAtomicReplace, sftptest.FailMethod("PosixRename", 1)},
		{"statvfs impossible", reasonKeyVolumeID, sftptest.WithStatVFS(func(reply *sftp.StatVFS) { reply.Frsize = 0 })},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			server, volume := writeVolume(t, scenario.option)

			capabilities := mustProbeLinks(t, volume)

			if valeurObtenue := capabilities.Reasons[scenario.reasonKey]; valeurObtenue != codeProbeFailed {
				t.Errorf("valeurObtenue %q (%+v), valeurAttendue %s", valeurObtenue, capabilities, codeProbeFailed)
			}
			assertLinkDirEmpty(t, server)
		})
	}
}

func TestCapabilities_serverCopyIsUnsupportedByTheClient(t *testing.T) {
	_, volume := writeVolume(t)

	capabilities := mustProbeLinks(t, volume)

	if capabilities.ServerCopy || capabilities.Reasons[reasonKeyServerCopy] != codeServerCopyByClient {
		t.Errorf("valeurObtenue %+v, valeurAttendue ServerCopy false with %s", capabilities, codeServerCopyByClient)
	}
}

func TestCapabilities_leavesTheLinkDirEmptyAfterEveryFailure(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		option sftptest.Option
	}{
		{"create refused", sftptest.FailMethod("Put", 1)},
		{"second create refused", sftptest.FailMethod("Put", 2)},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			server, volume := writeVolume(t, scenario.option)

			capabilities, err := probeLinks(t, volume)

			if err == nil && (capabilities.Hardlink && capabilities.AtomicReplace) {
				t.Errorf("valeurObtenue %+v, valeurAttendue a failed try reported", capabilities)
			}
			assertLinkDirEmpty(t, server)
		})
	}
}

func TestCapabilities_cancelledContextStillRemovesTheProbeEntries(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	server, volume := writeVolume(t, sftptest.OnRequest("Link", func(sftptest.Request) { cancel() }))
	root := openLinksRoot(t, volume)

	_, err := volume.ProbeCapabilities(ctx, root)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("valeurObtenue %v, valeurAttendue context.Canceled", err)
	}
	assertLinkDirEmpty(t, server)
	if valeurObtenue := volume.State(); valeurObtenue != sftpfs.StateOK {
		t.Errorf("valeurObtenue %s, valeurAttendue the session kept", valeurObtenue)
	}
	if valeurObtenue, err := volume.Capabilities(t.Context()); err != nil || valeurObtenue.Reasons[reasonKeyHardlink] != codeNotProbed {
		t.Errorf("valeurObtenue %+v (err %v), valeurAttendue an interrupted probe never cached", valeurObtenue, err)
	}
}

func TestCapabilities_cachedPerSession(t *testing.T) {
	server, volume := writeVolume(t)
	_ = mustProbeLinks(t, volume)
	root := openLinksRoot(t, volume)
	before := len(server.Requests())

	_, err := volume.ProbeCapabilities(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}

	if requests := server.Requests()[before:]; len(requests) != 0 {
		t.Errorf("valeurObtenue %+v, valeurAttendue the cached result", requests)
	}
	server.DropConnections()
	waitForState(t, volume, sftpfs.StateBroken)
	volume.ResetForStage()
	if valeurObtenue, err := volume.Capabilities(t.Context()); err != nil || valeurObtenue.Hardlink || valeurObtenue.Reasons[reasonKeyHardlink] != codeNotProbed {
		t.Errorf("valeurObtenue %+v (err %v), valeurAttendue a new session unprobed", valeurObtenue, err)
	}
	if valeurObtenue := mustProbeLinks(t, volume); !valeurObtenue.Hardlink {
		t.Errorf("valeurObtenue %+v, valeurAttendue a new probe on the new session", valeurObtenue)
	}
}

func TestCapabilities_staysCheap(t *testing.T) {
	server, volume := writeVolume(t)
	root := openLinksRoot(t, volume)
	before := len(server.Requests())

	if _, err := volume.ProbeCapabilities(t.Context(), root); err != nil {
		t.Fatal(err)
	}

	const maxProbeRequests = 12
	if valeurObtenue := len(server.Requests()[before:]); valeurObtenue > maxProbeRequests {
		t.Errorf("valeurObtenue %d requests %+v, valeurAttendue at most %d", valeurObtenue, server.Requests()[before:], maxProbeRequests)
	}
}

func TestCapabilities_beforeAnyProbeNothingIsProven(t *testing.T) {
	server, volume := writeVolume(t, sftptest.WithoutExtension("hardlink@openssh.com"))

	capabilities, err := volume.Capabilities(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	isNothingProven := !capabilities.Symlink && !capabilities.Hardlink && !capabilities.AtomicReplace && !capabilities.VolumeIdentity && !capabilities.ServerCopy
	if !isNothingProven {
		t.Errorf("valeurObtenue %+v, valeurAttendue nothing reported from the advertised extensions alone", capabilities)
	}
	valeurAttendue := map[string]string{
		reasonKeySymlink: codeNotProbed, reasonKeyHardlink: codeExtensionMissing, reasonKeyAtomicReplace: codeNotProbed,
		reasonKeyVolumeID: codeNotProbed, reasonKeyServerCopy: codeServerCopyByClient,
	}
	for key, code := range valeurAttendue {
		if capabilities.Reasons[key] != code {
			t.Errorf("reason %s: valeurObtenue %q, valeurAttendue %q", key, capabilities.Reasons[key], code)
		}
	}
	if writes := writesSince(server, 0); len(writes) != 0 {
		t.Errorf("valeurObtenue %+v, valeurAttendue no write", writes)
	}
	if _, err := volume.ProbeCapabilities(t.Context(), nil); !errors.Is(err, sftpfs.ErrNoProbeDir) {
		t.Errorf("valeurObtenue %v, valeurAttendue ErrNoProbeDir without a link dir", err)
	}
}

func TestSweepProbes_removesOnlyProbeEntries(t *testing.T) {
	skipWithoutSymlinks(t)
	server, volume := writeVolume(t)
	links := localPathOf(server, linksDir)
	removable := []string{probeEntryName, probeEntryName + "-t", probeEntryName + "-a"}
	kept := []string{
		".necromanciarr-probe-short", ".necromanciarr-probe-0123456789ABCDEF0123456789ABCDEF",
		probeEntryName + "-a.mkv", "keep.mkv", probeEntryName + "-dir",
	}
	sftptest.WriteFile(t, links+"/"+removable[0], []byte("x"), 0o600)
	sftptest.ReplaceWithSymlink(t, links+"/"+removable[1], "/library/"+"movie.mkv")
	sftptest.WriteFile(t, links+"/"+removable[2], nil, 0o600)
	for _, name := range kept[:4] {
		sftptest.WriteFile(t, links+"/"+name, []byte("x"), 0o600)
	}
	sftptest.Mkdir(t, links+"/"+kept[4])
	sftptest.WriteFile(t, links+"/sub/"+probeEntryName, []byte("x"), 0o600)
	root := openLinksRoot(t, volume)

	removed, err := sftpfs.SweepProbes(root)
	if err != nil {
		t.Fatal(err)
	}

	if removed != len(removable) {
		t.Errorf("valeurObtenue %d removed, valeurAttendue %d", removed, len(removable))
	}
	valeurAttendue := append(slices.Clone(kept), "sub")
	slices.Sort(valeurAttendue)
	if valeurObtenue := servedEntries(t, server, linksDir); !slices.Equal(valeurObtenue, valeurAttendue) {
		t.Errorf("valeurObtenue %q, valeurAttendue %q", valeurObtenue, valeurAttendue)
	}
	if valeurObtenue := servedEntries(t, server, linksDir+"/sub"); len(valeurObtenue) != 1 {
		t.Errorf("valeurObtenue %q, valeurAttendue the nested entry untouched (no recursion)", valeurObtenue)
	}
	if valeurObtenue := servedContent(t, server, libraryDir+"/movie.mkv"); valeurObtenue != fsystest.FixtureMovieContent {
		t.Errorf("valeurObtenue %q, valeurAttendue the library target of the removed link intact", valeurObtenue)
	}
}

func TestSweepProbes_refusesARootSwappedForALinkAndRemovesNothing(t *testing.T) {
	skipWithoutSymlinks(t)
	server, volume := writeVolume(t)
	sftptest.WriteFile(t, localPathOf(server, outsideDir+"/"+probeEntryName), []byte("x"), 0o600)
	root := openLinksRoot(t, volume)
	sftptest.ReplaceWithSymlink(t, localPathOf(server, linksDir), outsideDir)

	_, err := sftpfs.SweepProbes(root)

	if !errors.Is(err, fsys.ErrUnconfined) {
		t.Errorf("valeurObtenue %v, valeurAttendue ErrUnconfined", err)
	}
	if valeurObtenue := outsideEntries(t, server); !slices.Equal(valeurObtenue, []string{probeEntryName}) {
		t.Errorf("valeurObtenue %q, valeurAttendue the outside entry untouched", valeurObtenue)
	}
}

func TestCapabilities_rootSwappedDuringTheProbeIsRefusedAndNotCached(t *testing.T) {
	skipWithoutSymlinks(t)
	var server *sftptest.Server
	var planted []string
	// Juste avant statvfs, des entrées aux noms de la sonde sont posées dehors, puis le dossier de liens devient un lien
	// vers elles : un nettoyage par chemin les retirerait.
	hook := func(sftptest.Request) {
		if planted != nil {
			return
		}
		for _, request := range server.Requests() {
			if request.Method == "Put" && strings.HasSuffix(request.Path, "-a") {
				base := path.Base(strings.TrimSuffix(request.Path, "-a"))
				planted = []string{base + "-b", base + "-h"}
			}
		}
		for _, name := range planted {
			sftptest.WriteFile(t, localPathOf(server, outsideDir+"/"+name), []byte("pre-existing"), 0o600)
		}
		sftptest.ReplaceWithSymlink(t, localPathOf(server, linksDir), outsideDir)
	}
	server, volume := writeVolume(t, sftptest.OnRequest("StatVFS", hook))
	root := openLinksRoot(t, volume)

	_, err := volume.ProbeCapabilities(t.Context(), root)

	if !errors.Is(err, fsys.ErrUnconfined) {
		t.Errorf("valeurObtenue %v, valeurAttendue ErrUnconfined after the swap", err)
	}
	if valeurObtenue := outsideEntries(t, server); len(planted) != 2 || !slices.Equal(valeurObtenue, planted) {
		t.Errorf("valeurObtenue %q, valeurAttendue the outside entries %q untouched", valeurObtenue, planted)
	}
	if valeurObtenue, err := volume.Capabilities(t.Context()); err != nil || valeurObtenue.Reasons[reasonKeyHardlink] != codeNotProbed {
		t.Errorf("valeurObtenue %+v (err %v), valeurAttendue nothing cached", valeurObtenue, err)
	}
}

func TestCapabilities_cancelDuringTheSweepKeepsTheSession(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// L'annulation arrive pendant que la suppression est servie : sans contexte détaché, run coupe la session.
	var armed atomic.Bool
	server, volume := writeVolume(t, sftptest.OnRequest("Remove", func(sftptest.Request) {
		if armed.Load() {
			cancel()
			time.Sleep(cancelSettleDelay)
		}
	}))
	for _, tail := range []string{"", "-a"} {
		sftptest.WriteFile(t, localPathOf(server, linksDir+"/"+probeEntryName+tail), []byte("x"), 0o600)
	}
	root := openLinksRoot(t, volume)
	armed.Store(true)

	_, err := volume.ProbeCapabilities(ctx, root)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("valeurObtenue %v, valeurAttendue context.Canceled", err)
	}
	if valeurObtenue := volume.State(); valeurObtenue != sftpfs.StateOK {
		t.Errorf("valeurObtenue %s, valeurAttendue the session kept", valeurObtenue)
	}
}

func TestCapabilities_sweepsLeftoversBeforeProbing(t *testing.T) {
	server, volume := writeVolume(t)
	sftptest.WriteFile(t, localPathOf(server, linksDir+"/"+probeEntryName+"-b"), []byte("crash"), 0o600)

	_ = mustProbeLinks(t, volume)

	assertLinkDirEmpty(t, server)
}
