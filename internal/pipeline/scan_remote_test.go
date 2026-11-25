package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"necromanciarr/internal/arr"
	"necromanciarr/internal/config"
	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/fsystest"
	"necromanciarr/internal/fsys/local"
	"necromanciarr/internal/linker"
	"necromanciarr/internal/store"
)

// newRemoteScanDeps sert une bibliothèque locale et une bibliothèque distante factice (remoteLibraryPath).
func newRemoteScanDeps(t *testing.T) (Deps, *store.Store, *fsystest.RemoteVolume, string) {
	t.Helper()
	volume := fsystest.NewRemoteVolume(t, remoteVolumeName)
	volume.MkdirAll(t, remoteLibraryPath)
	localLibrary := t.TempDir()
	remoteLibrary := volume.Location(remoteLibraryPath)
	registry, err := fsys.NewRegistry(map[string]fsys.Volume{"": local.New(), remoteVolumeName: volume},
		[]fsys.Location{{Path: localLibrary}, remoteLibrary})
	if err != nil {
		t.Fatal(err)
	}
	mediaStore := openTestStore(t)
	deps := Deps{
		Store: mediaStore, ScanRoot: LibraryScanner(mediaStore, registry), Registry: registry,
		LibraryRoots: []string{localLibrary, remoteLibrary.String()},
	}
	return deps, mediaStore, volume, localLibrary
}

func writeScanFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("media bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestScan_remoteOutageSkipsThatRootAndScansTheOthers(t *testing.T) {
	deps, _, volume, localLibrary := newRemoteScanDeps(t)
	writeScanFile(t, filepath.Join(localLibrary, "local.mkv"))
	volume.Unavailable.Store(true)

	result, err := Run(context.Background(), StageScan, deps)

	if err != nil || result.Processed != 1 || result.Reasons[ReasonRemoteUnavailable] != 1 {
		t.Fatalf("valeurObtenue %+v err %v, valeurAttendue the local file scanned and remote_unavailable counted once", result, err)
	}
}

// Radarr voit la bibliothèque distante sous /movies ; la règle de chemins la ramène à sftp://seedbox/srv/media, la forme
// enregistrée par Scan : le fichier distant reçoit ses identifiants.
func TestScan_remoteLibraryFilesGetTheirArrIDsThroughThePathMapping(t *testing.T) {
	deps, mediaStore, volume, _ := newRemoteScanDeps(t)
	writeScanFile(t, volume.LocalPath(remoteLibraryPath+"/Film (1999)/film.mkv"))
	deps.Mapper = linker.NewMapper([]config.PathRule{{From: "sftp://" + remoteVolumeName + remoteLibraryPath, To: "/movies"}})
	deps.ArrSources = []ItemsSource{fakeArr{items: []arr.Item{{Path: "/movies/Film (1999)/film.mkv", TMDBID: 603}}}}

	result, err := Run(context.Background(), StageScan, deps)
	if err != nil {
		t.Fatal(err)
	}

	stored := mediaFilesByPath(t, mediaStore)["sftp://"+remoteVolumeName+remoteLibraryPath+"/Film (1999)/film.mkv"]
	if result.UnidentifiedFiles != 0 || stored.TMDBID != 603 {
		t.Fatalf("valeurObtenue %+v and TMDB %d, valeurAttendue the remote file identified as 603", result, stored.TMDBID)
	}
}

// Une coupure après deux inventaires ne doit rien effacer : lignes et identifiants restent.
func TestScan_remoteOutageKeepsTheExistingRemoteRows(t *testing.T) {
	deps, mediaStore, volume, _ := newRemoteScanDeps(t)
	remoteFile := "sftp://" + remoteVolumeName + remoteLibraryPath + "/Film (1999)/film.mkv"
	writeScanFile(t, volume.LocalPath(remoteLibraryPath+"/Film (1999)/film.mkv"))
	writeScanFile(t, volume.LocalPath(remoteLibraryPath+"/Other (2001)/other.mkv"))
	deps.Mapper = linker.NewMapper([]config.PathRule{{From: "sftp://" + remoteVolumeName + remoteLibraryPath, To: "/movies"}})
	deps.ArrSources = []ItemsSource{fakeArr{items: []arr.Item{{Path: "/movies/Film (1999)/film.mkv", TMDBID: 603}}}}
	for range 2 {
		if _, err := Run(context.Background(), StageScan, deps); err != nil {
			t.Fatal(err)
		}
	}
	rowsBefore := mediaFilesByPath(t, mediaStore)

	volume.Unavailable.Store(true)
	result, err := Run(context.Background(), StageScan, deps)

	rowsAfter := mediaFilesByPath(t, mediaStore)
	if err != nil || result.Reasons[ReasonRemoteUnavailable] != 1 || result.Reasons[ReasonMissingFileRemoved] != 0 {
		t.Fatalf("valeurObtenue %+v err %v, valeurAttendue remote_unavailable and nothing removed", result, err)
	}
	if len(rowsAfter) != len(rowsBefore) || len(rowsBefore) != 2 || rowsAfter[remoteFile].TMDBID != 603 {
		t.Fatalf("valeurObtenue %d rows (TMDB %d), valeurAttendue the 2 rows kept with TMDB 603", len(rowsAfter), rowsAfter[remoteFile].TMDBID)
	}
}
