package sftpfs_test

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/fsystest"
	"necromanciarr/internal/fsys/sftpfs"
	"necromanciarr/internal/fsys/sftpfs/sftptest"
	"necromanciarr/internal/fsys/sftpfs/shellquote"
)

// sameSizedPair pose deux fichiers de même taille et de même date dans la bibliothèque ; rend leurs chemins distants.
func sameSizedPair(t *testing.T, server *sftptest.Server) (string, string) {
	t.Helper()
	first, second := libraryDir+"/first.mkv", libraryDir+"/second.mkv"
	sameModTime := time.Now().Add(-time.Hour).Truncate(time.Second)
	for _, remotePath := range []string{first, second} {
		writeServedFile(t, server, remotePath, []byte("same size bytes"))
		sftptest.SetModTime(t, localPathOf(server, remotePath), sameModTime)
	}
	return first, second
}

func freshID(t *testing.T, volume *sftpfs.Volume, name string) fsys.FileID {
	t.Helper()
	info, err := volume.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	id, known := volume.FileID(info)
	if !known {
		t.Fatalf("%s : valeurObtenue une identité inconnue, valeurAttendue connue", name)
	}
	return id
}

func TestFileID_inodeSwappedAtSameSizeAndDateIsReadAgain(t *testing.T) {
	requireRealInodes(t)
	server, volume := gnuVolume(t, nil)
	probeLinksRoot(t, volume)
	first, second := sameSizedPair(t, server)
	secondID := freshID(t, volume, second)
	beforeSwap := freshID(t, volume, first)

	sftptest.Rename(t, localPathOf(server, second), localPathOf(server, first))
	afterSwap := freshID(t, volume, first)

	if afterSwap == beforeSwap || afterSwap != secondID {
		t.Fatalf("valeurObtenue %+v (avant %+v), valeurAttendue la nouvelle identité %+v : jamais une identité périmée", afterSwap, beforeSwap, secondID)
	}
}

func TestFileID_symlinkRetargetedAtSameSizeAndDateIsReadAgain(t *testing.T) {
	requireRealInodes(t)
	server, volume := gnuVolume(t, nil)
	probeLinksRoot(t, volume)
	first, second := sameSizedPair(t, server)
	link := libraryDir + "/pointer.mkv"
	sftptest.Symlink(t, "first.mkv", localPathOf(server, link))
	firstID, secondID := freshID(t, volume, first), freshID(t, volume, second)
	beforeRetarget := freshID(t, volume, link)

	sftptest.Symlink(t, "second.mkv", localPathOf(server, link))
	afterRetarget := freshID(t, volume, link)

	if beforeRetarget != firstID || afterRetarget != secondID {
		t.Fatalf("valeurObtenue %+v puis %+v, valeurAttendue %+v puis %+v", beforeRetarget, afterRetarget, firstID, secondID)
	}
}

func TestFileID_unknownOnADeviceTheProbeDidNotValidate(t *testing.T) {
	requireRealInodes(t)
	var otherDevice atomic.Bool
	moveToOtherDevice := sftptest.WithExecOutputFilter(func(command string, stdout []byte) []byte {
		if !otherDevice.Load() || !strings.Contains(command, "'stat'") {
			return stdout
		}
		return rewriteStatField(0, "4242")(command, stdout)
	})
	_, volume := gnuVolume(t, nil, moveToOtherDevice)
	probeLinksRoot(t, volume)
	info, err := volume.Stat(libraryDir + "/" + fsystest.FixtureMovieName)
	if err != nil {
		t.Fatal(err)
	}
	if _, known := volume.FileID(info); !known {
		t.Fatal("valeurObtenue inconnue, valeurAttendue connue sur le périphérique sondé")
	}

	otherDevice.Store(true)
	_, known := volume.FileID(info)

	if known {
		t.Fatal("valeurObtenue une identité connue, valeurAttendue inconnue sur un périphérique que la sonde n'a pas validé")
	}
}

func TestCapabilities_transientProbeTimeoutWarnsOncePerSessionAndProbesAgainNextStage(t *testing.T) {
	requireRealInodes(t)
	sftpfs.SetStatExecTimeout(t, 200*time.Millisecond)
	var shellProbes atomic.Int32
	slowFirstShellProbe := sftptest.BeforeExec(func(command string) {
		if command == shellquote.ShellSemanticsProbe && shellProbes.Add(1) == 1 {
			time.Sleep(400 * time.Millisecond)
		}
	})
	_, volume := gnuVolume(t, nil, slowFirstShellProbe)

	firstStage := probeLinksRoot(t, volume)
	volume.ResetForStage()
	secondStage := probeLinksRoot(t, volume)

	assertSFTPOnly(t, firstStage, sftpfs.ExecReasonProbeFailed)
	if !secondStage.RemoteStat {
		t.Fatalf("valeurObtenue %+v, valeurAttendue la sonde refaite à l'étape suivante (verdict passager)", secondStage)
	}
	warnings := warningsWithCode(volume, sftpfs.CodeRemoteExecUnavailable)
	if len(warnings) != 1 || warnings[0].Count != 1 {
		t.Fatalf("valeurObtenue %+v, valeurAttendue un seul avertissement pour la session", volume.Warnings())
	}
}
