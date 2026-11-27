package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"necromanciarr/internal/config"
)

const localVolumeName = "nas"

// localVolumeSettings déclare un volume local nommé sur volumeRoot, avec bibliothèque et dossier de liens dessous.
func localVolumeSettings(t *testing.T, linkDirs ...string) (config.Config, string) {
	t.Helper()
	volumeRoot := t.TempDir()
	library := filepath.Join(volumeRoot, "library")
	if err := os.MkdirAll(library, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, linkDir := range linkDirs {
		if err := os.MkdirAll(linkDir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	provider := config.FSProvider{Type: config.FSProviderTypeLocal, Name: localVolumeName, Paths: []string{volumeRoot, filepath.Join(volumeRoot, "other")}}
	return config.Config{
		ConfigDir: t.TempDir(), DBPath: "necromanciarr.db", ReadBytesPerSecond: 1 << 20, LinkType: "hardlink", CrossSeedMode: "off",
		LibraryRoots: []string{library}, LinkDirs: linkDirs, FSProviders: []config.FSProvider{provider},
	}, volumeRoot
}

func TestVolumes_listShowsALocalVolumeWithItsPaths(t *testing.T) {
	settings, volumeRoot := localVolumeSettings(t)
	service := seedboxService(t, settings)
	valeurAttendue := VolumeRow{Volume: localVolumeName, Type: VolumeTypeLocal, Address: volumeRoot + ", " + filepath.Join(volumeRoot, "other"), State: "ok"}

	rows, err := service.ListVolumes(context.Background())

	if err != nil || len(rows) != 2 || rows[1].Volume != valeurAttendue.Volume || rows[1].Type != valeurAttendue.Type ||
		rows[1].Address != valeurAttendue.Address || rows[1].State != valeurAttendue.State {
		t.Fatalf("valeurObtenue %+v (%v), valeurAttendue la ligne %+v", rows, err, valeurAttendue)
	}
}

func TestVolumes_probeOfALocalVolumeMeasuresTheLinksInItsLinkDir(t *testing.T) {
	settings, volumeRoot := localVolumeSettings(t)
	settings, linkDir := withLinkDir(t, settings, filepath.Join(volumeRoot, "links"))
	service := seedboxService(t, settings)

	result, err := service.ProbeVolume(context.Background(), localVolumeName)

	if err != nil || result.Result != VolumeProbeOK || result.Capabilities == nil || result.ProbedAt == nil {
		t.Fatalf("valeurObtenue %+v (%v), valeurAttendue une sonde réussie", result, err)
	}
	if entries, readErr := os.ReadDir(linkDir); readErr != nil || len(entries) != 0 {
		t.Fatalf("valeurObtenue %v (%v), valeurAttendue aucun fichier laissé", entries, readErr)
	}
	rows, err := service.ListVolumes(context.Background())
	if err != nil || rows[1].Capabilities == nil || !rows[1].ProbedAt.Equal(*result.ProbedAt) {
		t.Fatalf("valeurObtenue %+v (%v), valeurAttendue la sonde enregistrée et listée", rows, err)
	}
}

func TestVolumes_probeOfALocalVolumeWithoutLinkDirUnderItWritesNothing(t *testing.T) {
	outsideLinkDir := filepath.Join(t.TempDir(), "links")
	settings, volumeRoot := localVolumeSettings(t, outsideLinkDir)
	service := seedboxService(t, settings)

	result, err := service.ProbeVolume(context.Background(), localVolumeName)

	if err != nil || result.Result != VolumeProbeFailed || result.Error != ReasonNoLinkDir {
		t.Fatalf("valeurObtenue %+v (%v), valeurAttendue l'échec %s", result, err, ReasonNoLinkDir)
	}
	for _, folder := range []string{outsideLinkDir, filepath.Join(volumeRoot, "library")} {
		if entries, readErr := os.ReadDir(folder); readErr != nil || len(entries) != 0 {
			t.Fatalf("%s: valeurObtenue %v (%v), valeurAttendue rien d'écrit", folder, entries, readErr)
		}
	}
}

func withLinkDir(t *testing.T, settings config.Config, linkDir string) (config.Config, string) {
	t.Helper()
	if err := os.MkdirAll(linkDir, 0o700); err != nil {
		t.Fatal(err)
	}
	settings.LinkDirs = []string{linkDir}
	return settings, linkDir
}
