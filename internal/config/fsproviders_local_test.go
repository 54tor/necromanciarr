package config

import (
	"slices"
	"strings"
	"testing"
)

func localProviderJSON(name string, paths ...string) string {
	quoted := make([]string, 0, len(paths))
	for _, path := range paths {
		quoted = append(quoted, `"`+jsonText(path)+`"`)
	}
	return `{"type": "local", "name": "` + name + `", "paths": [` + strings.Join(quoted, ", ") + `]}`
}

func TestFsProviders_parsesALocalVolumeWithItsCleanedPaths(t *testing.T) {
	content := `{"fsProviders": [` + localProviderJSON("nas", absPath("srv", "media")+string([]byte{'/'}), absPath("srv", "links")) + `]}`
	valeurAttendue := []string{absPath("srv", "media"), absPath("srv", "links")}

	loadedConfig, err := loadFromJSON(t, content, nil)
	if err != nil {
		t.Fatal(err)
	}
	provider := loadedConfig.FSProviders[0]

	if provider.Type != FSProviderTypeLocal || provider.Name != "nas" || !slices.Equal(provider.Paths, valeurAttendue) {
		t.Fatalf("valeurObtenue %+v, valeurAttendue un volume local nas sur %v", provider, valeurAttendue)
	}
}

func TestFsProviders_refusesAnInvalidLocalVolume(t *testing.T) {
	scenarios := map[string]string{
		"no paths":       `{"fsProviders": [{"type": "local", "name": "nas", "paths": []}]}`,
		"missing paths":  `{"fsProviders": [{"type": "local", "name": "nas"}]}`,
		"relative path":  `{"fsProviders": [` + localProviderJSON("nas", "srv/media") + `]}`,
		"parent segment": `{"fsProviders": [` + localProviderJSON("nas", absPath("srv")+"/../etc") + `]}`,
		"bad name":       `{"fsProviders": [` + localProviderJSON("Nas", absPath("srv")) + `]}`,
		"unknown key":    `{"fsProviders": [{"type": "local", "name": "nas", "paths": ["` + jsonText(absPath("srv")) + `"], "readBytesPerSecond": 1}]}`,
		"duplicate name": `{"fsProviders": [` + localProviderJSON("nas", absPath("srv", "a")) + `, ` + localProviderJSON("nas", absPath("srv", "b")) + `]}`,
		"path twice":     `{"fsProviders": [` + localProviderJSON("nas", absPath("srv", "a")) + `, ` + localProviderJSON("other", absPath("srv", "a")) + `]}`,
		"path repeated":  `{"fsProviders": [` + localProviderJSON("nas", absPath("srv", "a"), absPath("srv", "a")) + `]}`,
		"reference":      `{"fsProviders": [` + localProviderJSON("nas", "${MEDIA}") + `]}`,
	}
	for name, content := range scenarios {
		_, err := loadFromJSON(t, content, nil)

		if err == nil {
			t.Errorf("%s: valeurObtenue nil, valeurAttendue un refus", name)
		}
	}
}

func TestFsProviders_acceptsNestedLocalPaths(t *testing.T) {
	content := `{"fsProviders": [` + localProviderJSON("nas", absPath("srv")) + `, ` + localProviderJSON("inner", absPath("srv", "inner")) + `]}`

	_, err := loadFromJSON(t, content, nil)
	if err != nil {
		t.Fatalf("valeurObtenue %v, valeurAttendue des chemins imbriqués acceptés", err)
	}
}

func TestLibraryRoots_refusesASFTPLocationNamingALocalVolume(t *testing.T) {
	content := `{"fsProviders": [` + localProviderJSON("nas", absPath("srv")) + `], "libraryRoots": ["sftp://nas/media"]}`

	_, err := loadFromJSON(t, content, nil)

	if err == nil || !strings.Contains(err.Error(), "does not expose a filesystem") {
		t.Fatalf("valeurObtenue %v, valeurAttendue un refus du volume local en sftp://", err)
	}
}
