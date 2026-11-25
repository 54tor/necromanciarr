package hashsource_test

import (
	"context"
	"crypto/md5"  //nolint:gosec // empreinte attendue, jamais une protection
	"crypto/sha1" //nolint:gosec // empreinte attendue, jamais une protection
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/sftpfs"
	"necromanciarr/internal/fsys/sftpfs/sftptest"
	"necromanciarr/internal/hashsource"
)

const (
	execVolumeName  = "seedbox"
	execLibraryDir  = "/library"
	execLinksDir    = "/links"
	execMovieName   = "movie.mkv"
	execMovieBody   = "remote movie bytes"
	execProbeWindow = 5 * time.Second
)

// execPlace : un serveur à l'exécution distante GNU, son volume sondé dans /links et l'adresse du film de la bibliothèque.
type execPlace struct {
	server *sftptest.Server
	volume *sftpfs.Volume
	movie  string
}

func newExecPlace(t *testing.T, options ...sftptest.Option) execPlace {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("le faux hôte GNU n'a pas d'inode réel sous Windows : la sonde y refuse stat distant")
	}
	server := sftptest.Start(t, append([]sftptest.Option{sftptest.WithGNUExec()}, options...)...)
	for _, dir := range []string{execLibraryDir, execLinksDir} {
		sftptest.Mkdir(t, filepath.Join(server.Root, strings.TrimPrefix(dir, "/")))
	}
	sftptest.WriteFile(t, filepath.Join(server.Root, "library", execMovieName), []byte(execMovieBody), 0o644)
	volume, err := sftpfs.Dial(t.Context(), server.Provider(t, execVolumeName), sftpfs.Deps{
		LibraryRoots: []string{execLibraryDir}, LookupEnv: func(string) (string, bool) { return "", false },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = volume.Close() })
	registry, err := fsys.NewRegistry(map[string]fsys.Volume{execVolumeName: volume},
		[]fsys.Location{{Volume: execVolumeName, Path: execLibraryDir}})
	if err != nil {
		t.Fatal(err)
	}
	root, err := registry.WriteRoot(fsys.Location{Volume: execVolumeName, Path: execLinksDir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	probeContext, cancel := context.WithTimeout(t.Context(), execProbeWindow)
	defer cancel()
	if _, err := volume.ProbeCapabilities(probeContext, root); err != nil {
		t.Fatal(err)
	}
	movie := fsys.Location{Volume: execVolumeName, Path: execLibraryDir + "/" + execMovieName}.String()
	return execPlace{server: server, volume: volume, movie: movie}
}

func hexOf(sum []byte) string { return hex.EncodeToString(sum) }

// hashCommandsSent compte les lignes d'empreinte reçues (sondes de version exclues).
func hashCommandsSent(server *sftptest.Server) int {
	count := 0
	for _, session := range server.ExecSessions() {
		isHashBatch := strings.Contains(session.Command, "sum' '-z'")
		if isHashBatch {
			count++
		}
	}
	return count
}

func TestHashsource_sftpExecReturnsRemoteSHA1(t *testing.T) {
	place := newExecPlace(t)
	source := hashsource.NewSFTPExec(map[string]*sftpfs.Volume{execVolumeName: place.volume})
	sha1Sum := sha1.Sum([]byte(execMovieBody)) //nolint:gosec // voir l'import
	md5Sum := md5.Sum([]byte(execMovieBody))   //nolint:gosec // voir l'import
	before := hashCommandsSent(place.server)

	gotSHA1, sha1Err := source.Hash(t.Context(), place.movie, hashsource.SHA1)
	gotMD5, md5Err := source.Hash(t.Context(), place.movie, hashsource.MD5)

	expectedSHA1 := hashsource.Result{Value: hexOf(sha1Sum[:]), Origin: hashsource.Backend, Available: true}
	expectedMD5 := hashsource.Result{Value: hexOf(md5Sum[:]), Origin: hashsource.Backend, Available: true}
	if sha1Err != nil || md5Err != nil || gotSHA1 != expectedSHA1 || gotMD5 != expectedMD5 {
		t.Fatalf("valeurObtenue %+v (%v), %+v (%v) ; valeurAttendue %+v, %+v", gotSHA1, sha1Err, gotMD5, md5Err, expectedSHA1, expectedMD5)
	}
	if valeurObtenue := hashCommandsSent(place.server) - before; valeurObtenue != 2 {
		t.Errorf("valeurObtenue %d lots d'empreinte, valeurAttendue 2", valeurObtenue)
	}
}

func TestHashsource_sftpExecIsNeverUsedForAVolumeWithoutRemoteHash(t *testing.T) {
	place := newExecPlace(t, sftptest.DenyExec())
	source := hashsource.NewSFTPExec(map[string]*sftpfs.Volume{execVolumeName: place.volume})

	got, err := source.Hash(t.Context(), place.movie, hashsource.SHA1)

	if err != nil || got.Available {
		t.Fatalf("valeurObtenue %+v (%v), valeurAttendue aucune empreinte, sans erreur", got, err)
	}
	if hashCommandsSent(place.server) != 0 {
		t.Fatalf("valeurObtenue %d lots d'empreinte, valeurAttendue aucun", hashCommandsSent(place.server))
	}
}

func TestHashsource_sftpExecLeavesLocalPathsAndSHA256Alone(t *testing.T) {
	place := newExecPlace(t)
	source := hashsource.NewSFTPExec(map[string]*sftpfs.Volume{execVolumeName: place.volume})

	for _, request := range []struct {
		path      string
		algorithm hashsource.Algorithm
	}{{"/srv/media/movie.mkv", hashsource.SHA1}, {place.movie, hashsource.SHA256}, {"sftp://other/library/movie.mkv", hashsource.SHA1}} {
		got, err := source.Hash(t.Context(), request.path, request.algorithm)
		if err != nil || got.Available {
			t.Errorf("%s %s : valeurObtenue %+v (%v), valeurAttendue aucune empreinte", request.path, request.algorithm, got, err)
		}
	}
}

func TestHashsource_sftpExecFailureIsCountedAndTyped(t *testing.T) {
	var vanishing string
	place := newExecPlace(t, sftptest.BeforeExec(func(command string) {
		if strings.Contains(command, "sha1sum' '-z'") && vanishing != "" {
			_ = os.Remove(vanishing)
		}
	}))
	vanishing = filepath.Join(place.server.Root, "library", execMovieName)
	source := hashsource.NewSFTPExec(map[string]*sftpfs.Volume{execVolumeName: place.volume})

	_, err := source.Hash(t.Context(), place.movie, hashsource.SHA1)

	if !errors.Is(err, hashsource.ErrRemoteExecFailed) {
		t.Fatalf("valeurObtenue %v, valeurAttendue hashsource.ErrRemoteExecFailed", err)
	}
	failures := slices.DeleteFunc(place.volume.Warnings(), func(warning sftpfs.Warning) bool { return warning.Code != sftpfs.CodeRemoteExecFailed })
	if len(failures) != 1 || failures[0].Count != 1 {
		t.Fatalf("valeurObtenue %+v, valeurAttendue remote_exec_failed compté une fois", place.volume.Warnings())
	}
}

// recordingSource note les chemins qu'on lui demande.
type recordingSource struct{ asked []string }

func (source *recordingSource) Hash(_ context.Context, path string, _ hashsource.Algorithm) (hashsource.Result, error) {
	source.asked = append(source.asked, path)
	return hashsource.Result{}, nil
}

func TestHashsource_byVolumeSendsRemotePathsToTheRemoteSource(t *testing.T) {
	local, remote := &recordingSource{}, &recordingSource{}
	router := hashsource.ByVolume(local, remote)

	for _, path := range []string{"/srv/media/a.mkv", "sftp://seedbox/library/b.mkv"} {
		if _, err := router.Hash(t.Context(), path, hashsource.MD5); err != nil {
			t.Fatal(err)
		}
	}
	nilRemote, err := hashsource.ByVolume(local, nil).Hash(t.Context(), "sftp://seedbox/library/c.mkv", hashsource.MD5)

	if !slices.Equal(local.asked, []string{"/srv/media/a.mkv"}) || !slices.Equal(remote.asked, []string{"sftp://seedbox/library/b.mkv"}) {
		t.Fatalf("valeurObtenue local %q distant %q, valeurAttendue chaque chemin à la source de son volume", local.asked, remote.asked)
	}
	if err != nil || nilRemote.Available {
		t.Fatalf("valeurObtenue %+v (%v), valeurAttendue aucune empreinte sans source distante", nilRemote, err)
	}
}
