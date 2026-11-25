package sftpfs_test

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"

	"necromanciarr/internal/config"
	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/fsystest"
	"necromanciarr/internal/fsys/sftpfs"
	"necromanciarr/internal/fsys/sftpfs/sftptest"
)

// gnuVolume démarre un serveur à l'exécution distante GNU (sauf option contraire) et joint un volume dont la
// bibliothèque est libraryDir et le dossier de liens linksDir.
func gnuVolume(t *testing.T, configure func(*config.FSProvider), options ...sftptest.Option) (*sftptest.Server, *sftpfs.Volume) {
	t.Helper()
	server := sftptest.Start(t, append([]sftptest.Option{sftptest.WithGNUExec()}, options...)...)
	sftptest.Mkdir(t, localPathOf(server, libraryDir))
	fsystest.WriteLibraryFixture(t, localPathOf(server, libraryDir))
	sftptest.Mkdir(t, localPathOf(server, linksDir))
	provider := pinnedProvider(t, server)
	if configure != nil {
		configure(&provider)
	}
	deps := testDeps()
	deps.LibraryRoots = []string{libraryDir}
	return server, dialVolume(t, provider, deps)
}

// requireRealInodes saute un test qui attend des inodes réels du disque hôte (lien dur compris).
func requireRealInodes(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("le faux hôte GNU n'a pas d'inode réel sous Windows")
	}
}

func probeLinksRoot(t *testing.T, volume *sftpfs.Volume) fsys.Capabilities {
	t.Helper()
	capabilities, err := volume.ProbeCapabilities(t.Context(), openLinksRoot(t, volume))
	if err != nil {
		t.Fatalf("ProbeCapabilities: %v", err)
	}
	return capabilities
}

func warningsWithCode(volume *sftpfs.Volume, code string) []sftpfs.Warning {
	return slices.DeleteFunc(volume.Warnings(), func(warning sftpfs.Warning) bool { return warning.Code != code })
}

func assertSFTPOnly(t *testing.T, capabilities fsys.Capabilities, expectedReason string) {
	t.Helper()
	if capabilities.RemoteStat || len(capabilities.RemoteHash) > 0 {
		t.Errorf("valeurObtenue RemoteStat %v RemoteHash %v, valeurAttendue tout faux (lecture SFTP seule)", capabilities.RemoteStat, capabilities.RemoteHash)
	}
	if valeurObtenue := capabilities.Reasons["remoteExec"]; valeurObtenue != expectedReason {
		t.Errorf("valeurObtenue raison %q, valeurAttendue %q", valeurObtenue, expectedReason)
	}
}

func TestCapabilities_gnuHostGivesRemoteStatAndHashes(t *testing.T) {
	requireRealInodes(t)
	_, volume := gnuVolume(t, nil)

	capabilities := probeLinksRoot(t, volume)

	if !capabilities.RemoteStat || !slices.Equal(capabilities.RemoteHash, []string{"sha1", "md5"}) {
		t.Fatalf("valeurObtenue RemoteStat %v RemoteHash %v, valeurAttendue true [sha1 md5]", capabilities.RemoteStat, capabilities.RemoteHash)
	}
	if _, stillMissing := capabilities.Reasons["fileIdentity"]; stillMissing {
		t.Errorf("valeurObtenue %v, valeurAttendue aucune raison d'absence d'identité une fois stat distant prouvé", capabilities.Reasons)
	}
	if len(volume.Warnings()) != 0 {
		t.Errorf("valeurObtenue %+v, valeurAttendue aucun avertissement", volume.Warnings())
	}
}

func TestCapabilities_execDeniedFallsBackToSFTPOnly(t *testing.T) {
	_, volume := gnuVolume(t, nil, sftptest.DenyExec())

	first := probeLinksRoot(t, volume)
	second := probeLinksRoot(t, volume)
	for _, name := range []string{fsystest.FixtureMovieName, fsystest.FixtureNoteName} {
		info, err := volume.Stat(libraryDir + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, known := volume.FileID(info); known {
			t.Errorf("%s : valeurObtenue une identité, valeurAttendue inconnue en SFTP seul", name)
		}
	}

	assertSFTPOnly(t, first, sftpfs.ExecReasonDenied)
	assertSFTPOnly(t, second, sftpfs.ExecReasonDenied)
	warnings := warningsWithCode(volume, sftpfs.CodeRemoteExecUnavailable)
	if len(warnings) != 1 || warnings[0].Path != "seedbox" || warnings[0].Count != 1 {
		t.Fatalf("valeurObtenue %+v, valeurAttendue un seul avertissement remote_exec_unavailable pour le volume", volume.Warnings())
	}
}

func TestCapabilities_execOffNeverExecsAndNeverWarns(t *testing.T) {
	server, volume := gnuVolume(t, func(provider *config.FSProvider) { provider.Exec = config.SFTPExecOff })

	capabilities := probeLinksRoot(t, volume)

	assertSFTPOnly(t, capabilities, sftpfs.ExecReasonOff)
	if sent := commandsSent(server); len(sent) != 0 || len(volume.Warnings()) != 0 {
		t.Fatalf("valeurObtenue %q et %+v, valeurAttendue aucune commande et aucun avertissement", sent, volume.Warnings())
	}
}

// inconsistentOutputs réécrit la sortie de stat (ou d'une empreinte) comme un système de fichiers qui ment.
var inconsistentOutputs = map[string]func(command string, stdout []byte) []byte{
	"inode nul": zeroFileInodes,
	"date décalée": func(command string, stdout []byte) []byte {
		return rewriteStatField(4, "1")(command, stdout)
	},
	"lien dur vu comme deux fichiers": func(command string, stdout []byte) []byte {
		if !strings.Contains(command, "'stat'") {
			return stdout
		}
		// Enregistrements de la sonde : le fichier, le même fichier, son lien dur, la racine ; seul le lien dur change.
		const partnerRecord = 2
		records := bytes.Split(stdout, []byte{0})
		if len(records) <= partnerRecord {
			return stdout
		}
		fields := bytes.Split(records[partnerRecord], []byte{'\t'})
		if len(fields) == 5 {
			fields[1] = append(fields[1], '7')
			records[partnerRecord] = bytes.Join(fields, []byte{'\t'})
		}
		return bytes.Join(records, []byte{0})
	},
	"empreinte fausse": func(command string, stdout []byte) []byte {
		if !strings.Contains(command, "sum'") {
			return stdout
		}
		return bytes.Replace(stdout, stdout[:1], []byte{'0'}, 1)
	},
}

// zeroFileInodes rend un inode nul pour chaque fichier de la sonde, la racine (dernier enregistrement) gardant le
// sien : seul le contrôle « inode non nul » peut alors refuser.
func zeroFileInodes(command string, stdout []byte) []byte {
	if !strings.Contains(command, "'stat'") {
		return stdout
	}
	records := bytes.Split(stdout, []byte{0})
	lastRecord := len(records) - 2
	for recordIndex := range lastRecord {
		fields := bytes.Split(records[recordIndex], []byte{'\t'})
		if len(fields) == 5 {
			fields[1] = []byte("0")
			records[recordIndex] = bytes.Join(fields, []byte{'\t'})
		}
	}
	return bytes.Join(records, []byte{0})
}

// rewriteStatField remplace le champ fieldIndex de chaque enregistrement stat par value.
func rewriteStatField(fieldIndex int, value string) func(command string, stdout []byte) []byte {
	return func(command string, stdout []byte) []byte {
		if !strings.Contains(command, "'stat'") {
			return stdout
		}
		records := bytes.Split(stdout, []byte{0})
		for recordIndex := range records {
			fields := bytes.Split(records[recordIndex], []byte{'\t'})
			if len(fields) == 5 {
				fields[fieldIndex] = []byte(value)
				records[recordIndex] = bytes.Join(fields, []byte{'\t'})
			}
		}
		return bytes.Join(records, []byte{0})
	}
}

func TestCapabilities_execInconsistentWithSFTPFallsBack(t *testing.T) {
	requireRealInodes(t)
	for label, filter := range inconsistentOutputs {
		t.Run(label, func(t *testing.T) {
			_, volume := gnuVolume(t, nil, sftptest.WithExecOutputFilter(filter))

			capabilities := probeLinksRoot(t, volume)

			assertSFTPOnly(t, capabilities, sftpfs.ExecReasonInconsistent)
			if len(warningsWithCode(volume, sftpfs.CodeRemoteExecUnavailable)) != 1 {
				t.Fatalf("valeurObtenue %+v, valeurAttendue un avertissement remote_exec_unavailable", volume.Warnings())
			}
		})
	}
}

func TestCapabilities_withoutHardlinkOrReplaceNothingVerifiesRemoteStat(t *testing.T) {
	_, volume := gnuVolume(t, nil, sftptest.WithoutExtension("hardlink@openssh.com"), sftptest.WithoutExtension("posix-rename@openssh.com"))

	capabilities := probeLinksRoot(t, volume)

	assertSFTPOnly(t, capabilities, sftpfs.ExecReasonInconsistent)
}

func TestExecRequired_refusesStagesWhenProbeFails(t *testing.T) {
	failures := map[string]sftptest.Option{
		"exec refusé":            sftptest.DenyExec(),
		"stat incohérent (date)": sftptest.WithExecOutputFilter(rewriteStatField(4, "1")),
	}
	for label, failure := range failures {
		t.Run(label, func(t *testing.T) {
			server, volume := gnuVolume(t, func(provider *config.FSProvider) { provider.Exec = config.SFTPExecRequired }, failure)
			root := openLinksRoot(t, volume)

			_, err := volume.ProbeCapabilities(t.Context(), root)

			if !errors.Is(err, fsys.ErrRemoteExecRequired) || !errors.Is(err, sftpfs.ErrExecRequiredUnavailable) {
				t.Fatalf("valeurObtenue %v, valeurAttendue fsys.ErrRemoteExecRequired", err)
			}
			capabilities, err := volume.Capabilities(t.Context())
			if err != nil || capabilities.Probed() {
				t.Fatalf("valeurObtenue %+v (%v), valeurAttendue aucune sonde gardée", capabilities, err)
			}
			if entries, _ := os.ReadDir(localPathOf(server, linksDir)); len(entries) != 0 {
				t.Fatalf("valeurObtenue %d entrées, valeurAttendue un dossier de liens vide après le refus", len(entries))
			}
		})
	}
}

func TestFileID_knownOnlyOnceRemoteStatIsProven(t *testing.T) {
	requireRealInodes(t)
	server, volume := gnuVolume(t, nil)
	movie := libraryDir + "/" + fsystest.FixtureMovieName
	twin := libraryDir + "/twin.mkv"
	sftptest.Link(t, localPathOf(server, movie), localPathOf(server, twin))
	movieInfo, twinInfo, noteInfo := statAll(t, volume, movie, twin, libraryDir+"/"+fsystest.FixtureNoteName)
	if _, known := volume.FileID(movieInfo); known {
		t.Fatal("valeurObtenue une identité connue avant la sonde, valeurAttendue inconnue")
	}

	probeLinksRoot(t, volume)
	movieID, movieKnown := volume.FileID(movieInfo)
	execsAfterFirst := len(server.ExecSessions())
	againID, againKnown := volume.FileID(movieInfo)
	twinID, twinKnown := volume.FileID(twinInfo)
	noteID, noteKnown := volume.FileID(noteInfo)

	isSameFile := movieKnown && twinKnown && movieID == twinID && againKnown && againID == movieID
	if !isSameFile || !noteKnown || noteID == movieID || movieID.Inode == 0 {
		t.Fatalf("valeurObtenue %+v %+v %+v %+v, valeurAttendue le lien dur identique, l'autre fichier différent", movieID, againID, twinID, noteID)
	}
	if len(server.ExecSessions()) != execsAfterFirst+3 {
		t.Errorf("valeurObtenue %d sessions, valeurAttendue une lecture fraîche par appel (aucun cache)", len(server.ExecSessions())-execsAfterFirst)
	}
}

func statAll(t *testing.T, volume *sftpfs.Volume, names ...string) (fs.FileInfo, fs.FileInfo, fs.FileInfo) {
	t.Helper()
	infos := make([]fs.FileInfo, 0, len(names))
	for _, name := range names {
		info, err := volume.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		infos = append(infos, info)
	}
	return infos[0], infos[1], infos[2]
}

func TestFileID_unknownForAnUnfollowedSymlinkAndOutsideTheRoots(t *testing.T) {
	requireRealInodes(t)
	server, volume := gnuVolume(t, nil)
	probeLinksRoot(t, volume)
	sftptest.Mkdir(t, localPathOf(server, outsideDir))
	writeServedFile(t, server, outsideDir+"/elsewhere.mkv", []byte("x"))
	before := len(server.ExecSessions())

	linkInfo, err := volume.Lstat(libraryDir + "/" + fsystest.FixtureLinkName)
	if err != nil {
		t.Fatal(err)
	}
	outsideInfo, err := volume.Stat(outsideDir + "/elsewhere.mkv")
	if err != nil {
		t.Fatal(err)
	}

	for _, info := range []fs.FileInfo{linkInfo, outsideInfo} {
		if _, known := volume.FileID(info); known {
			t.Errorf("%s : valeurObtenue une identité, valeurAttendue inconnue", info.Name())
		}
	}
	if len(server.ExecSessions()) != before {
		t.Errorf("valeurObtenue %d lignes exec, valeurAttendue aucune", len(server.ExecSessions())-before)
	}
}

func TestFileIDs_vanishedFileFailsOnlyItself(t *testing.T) {
	requireRealInodes(t)
	var vanishingHostPath string
	removeBeforeStat := sftptest.BeforeExec(func(command string) {
		if strings.Contains(command, "'stat'") && vanishingHostPath != "" {
			_ = sftptest.RemoveFile(vanishingHostPath)
		}
	})
	server, volume := gnuVolume(t, nil, removeBeforeStat)
	probeLinksRoot(t, volume)
	writeServedFile(t, server, libraryDir+"/vanishing.mkv", []byte("v"))
	kept, vanishing, _ := statAll(t, volume, libraryDir+"/"+fsystest.FixtureMovieName, libraryDir+"/vanishing.mkv", libraryDir+"/"+fsystest.FixtureNoteName)
	vanishingHostPath = localPathOf(server, libraryDir+"/vanishing.mkv")

	identities := volume.FileIDs(t.Context(), []fs.FileInfo{kept, vanishing})

	if !identities[0].Known || identities[1].Known {
		t.Fatalf("valeurObtenue %+v, valeurAttendue le fichier restant connu, le disparu inconnu", identities)
	}
	failures := warningsWithCode(volume, sftpfs.CodeRemoteExecFailed)
	if len(failures) != 1 || failures[0].Count != 1 {
		t.Fatalf("valeurObtenue %+v, valeurAttendue remote_exec_failed compté une fois (un élément)", volume.Warnings())
	}
}
