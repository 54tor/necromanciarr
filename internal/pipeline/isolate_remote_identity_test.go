package pipeline

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/sftpfs"
	"necromanciarr/internal/fsys/sftpfs/sftptest"
	"necromanciarr/internal/linker"
)

// TestSharesLibraryFile_seesAnInodeSwappedAtSameSizeAndDate : preuve de concept de la revue C16. Le fichier du dossier
// de liens est d'abord une copie indépendante, puis remplacé (même taille, même date) par un lien dur vers le fichier
// de la bibliothèque. Une identité distante périmée dirait « non partagé » et Force sauterait l'isolation : le client
// écrirait dans la bibliothèque.
func TestSharesLibraryFile_seesAnInodeSwappedAtSameSizeAndDate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("le faux hôte GNU n'a pas d'inode réel sous Windows")
	}
	server := sftptest.Start(t, sftptest.WithGNUExec())
	hostPath := func(remotePath string) string {
		return filepath.Join(server.Root, filepath.FromSlash(strings.TrimPrefix(remotePath, "/")))
	}
	for _, dir := range []string{sftpLibraryDir, sftpLinkDir + "/torrent"} {
		sftptest.Mkdir(t, hostPath(dir))
	}
	source, destination := sftpLibraryDir+"/movie.mkv", sftpLinkDir+"/torrent/movie.mkv"
	sameModTime := time.Now().Add(-time.Hour).Truncate(time.Second)
	for _, remotePath := range []string{source, destination} {
		sftptest.WriteFile(t, hostPath(remotePath), []byte("movie bytes"), 0o644)
		sftptest.SetModTime(t, hostPath(remotePath), sameModTime)
	}
	volume, err := sftpfs.Dial(t.Context(), server.Provider(t, sftpVolumeName), sftpfs.Deps{LibraryRoots: []string{sftpLibraryDir}, LookupEnv: noEnvironment})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = volume.Close() })
	registry, err := fsys.NewRegistry(map[string]fsys.Volume{sftpVolumeName: volume}, []fsys.Location{{Volume: sftpVolumeName, Path: sftpLibraryDir}})
	if err != nil {
		t.Fatal(err)
	}
	root, err := registry.WriteRoot(fsys.Location{Volume: sftpVolumeName, Path: sftpLinkDir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	if capabilities, err := volume.ProbeCapabilities(t.Context(), root); err != nil || !capabilities.RemoteStat {
		t.Fatalf("valeurObtenue %+v (%v), valeurAttendue stat distant prouvé", capabilities, err)
	}
	operation := linker.Op{Source: source, Destination: destination}

	sharedBeforeSwap, beforeErr := sharesLibraryFile(volume, operation)
	if err := sftptest.RemoveFile(hostPath(destination)); err != nil {
		t.Fatal(err)
	}
	sftptest.Link(t, hostPath(source), hostPath(destination))
	sharedAfterSwap, afterErr := sharesLibraryFile(volume, operation)

	if beforeErr != nil || afterErr != nil || sharedBeforeSwap || !sharedAfterSwap {
		t.Fatalf("valeurObtenue %v puis %v (%v, %v), valeurAttendue une copie non partagée puis le lien dur vu partagé",
			sharedBeforeSwap, sharedAfterSwap, beforeErr, afterErr)
	}
}
