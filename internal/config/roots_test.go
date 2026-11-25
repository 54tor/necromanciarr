package config

import (
	"errors"
	"strings"
	"testing"
)

func loadWithRoots(t *testing.T, content string) error {
	t.Helper()
	configDir := t.TempDir()
	writeFile(t, configDir, configFileName, content)
	_, err := Load(nil, lookupFrom(nil), configDir)
	return err
}

func TestConfig_rootOnUnknownSFTPProviderIsUsageError(t *testing.T) {
	for _, key := range []string{
		`"libraryRoots": ["sftp://seedbox/srv/media"]`,
		`"linkDirs": ["sftp://seedbox/srv/links"]`,
		`"dedupe": {"torrentRoots": ["sftp://seedbox/srv/torrents"]}`,
	} {
		err := loadWithRoots(t, "{"+key+"}")

		if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), `"seedbox"`) {
			t.Fatalf("%s: valeurObtenue %v, valeurAttendue a usage error naming the provider", key, err)
		}
	}
}

func TestConfig_rootOnRcloneProviderIsRefusedAsNotAFilesystem(t *testing.T) {
	err := loadWithRoots(t, `{"libraryRoots": ["sftp://cloud/media"], "fsProviders": [{"type": "rclone", "name": "cloud", "mounts": ["/mnt/cloud=remote:media"]}]}`)

	if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), `"cloud"`) || !strings.Contains(err.Error(), "rclone") {
		t.Fatalf("valeurObtenue %v, valeurAttendue a usage error saying rclone is not a filesystem", err)
	}
}

func TestConfig_refusesRootsThatAreNotLocations(t *testing.T) {
	for _, raw := range []string{"relative/media", "sftp://Bad_Name/media", "sftp://seedbox", "/srv/../media"} {
		err := loadWithRoots(t, `{"libraryRoots": [`+quoteJSON(raw)+`]}`)

		if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), "libraryRoots[0]") {
			t.Fatalf("%q: valeurObtenue %v, valeurAttendue a usage error naming libraryRoots[0]", raw, err)
		}
	}
}

func TestConfig_acceptsRootOnADeclaredFilesystemProvider(t *testing.T) {
	settings := Config{
		LibraryRoots: []string{"sftp://seedbox/srv/media", "/srv/media"},
		LinkDirs:     []string{"sftp://seedbox/srv/links"},
		FSProviders:  []FSProvider{{Type: "sftp", Name: "seedbox"}},
	}

	err := validateLocations(settings, []string{"sftp"})
	if err != nil {
		t.Fatalf("valeurObtenue %v, valeurAttendue no error", err)
	}
}

func TestConfig_overlapIsCheckedOnTheSameVolumeOnly(t *testing.T) {
	providers := []FSProvider{{Type: "sftp", Name: "seedbox"}, {Type: "sftp", Name: "other"}}
	cases := []struct {
		name            string
		libraryRoot     string
		linkDir         string
		expectedOverlap bool
	}{
		{"same volume child", "sftp://seedbox/srv/media", "sftp://seedbox/srv/media/links", true},
		{"same volume parent", "sftp://seedbox/srv/media", "sftp://seedbox/srv", true},
		{"same path on another volume", "sftp://seedbox/srv/media", "sftp://other/srv/media", false},
		{"same path on the local disk", "sftp://seedbox/srv/media", "/srv/media", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			settings := Config{LibraryRoots: []string{testCase.libraryRoot}, LinkDirs: []string{testCase.linkDir}, FSProviders: providers}

			err := validateLocations(settings, []string{"sftp"})
			if err == nil {
				err = validateLinkDirs(settings)
			}

			valeurObtenue := err != nil
			if valeurObtenue != testCase.expectedOverlap {
				t.Fatalf("valeurAttendue overlap %v, valeurObtenue %v", testCase.expectedOverlap, err)
			}
		})
	}
}

func TestConfig_pathsOverlapComparesPerVolume(t *testing.T) {
	cases := []struct {
		first, second   string
		expectedOverlap bool
	}{
		{"/srv/media", "/srv/media/tv", true},
		{"/data/tv", "/data/tvshows", false},
		{"sftp://seedbox/srv", "sftp://seedbox/srv/media", true},
		{"sftp://seedbox/srv/media", "sftp://other/srv/media", false},
		{"/srv/media", "sftp://seedbox/srv/media", false},
		{"relative/dir", "relative/dir/sub", true},
	}
	for _, testCase := range cases {
		valeurObtenue := PathsOverlap(testCase.first, testCase.second)
		if valeurObtenue != testCase.expectedOverlap {
			t.Fatalf("%s / %s: valeurAttendue %v, valeurObtenue %v", testCase.first, testCase.second, testCase.expectedOverlap, valeurObtenue)
		}
	}
}
