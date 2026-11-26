package config

import (
	"encoding/json"
	"flag"
	"maps"
	"os"
	"reflect"
	"slices"
	"testing"
)

const backendSecretsFixture = "../../web/src/lib/backendSecrets.json"

var updateBackendSecrets = flag.Bool("update", false, "réécrit web/src/lib/backendSecrets.json depuis les tables du serveur")

// backendSecrets est ce que l'interface doit connaître du serveur : champs scellés, réglages et listes.
type backendSecrets struct {
	Elements    map[string][]string `json:"elements"`
	Settings    []string            `json:"settings"`
	SettingKeys []string            `json:"settingKeys"`
	Lists       []string            `json:"lists"`
}

func currentBackendSecrets() backendSecrets {
	elements := map[string][]string{}
	for _, list := range slices.Sorted(maps.Keys(secretFieldsByList)) {
		elements[list] = slices.Sorted(slices.Values(secretFieldsByList[list]))
	}
	return backendSecrets{
		Elements: elements, Settings: slices.Sorted(slices.Values(secretSettings)),
		SettingKeys: slices.Sorted(slices.Values(settingKeys)), Lists: ListNames(),
	}
}

func TestBackendSecretsFixture(t *testing.T) {
	valeurAttendue := currentBackendSecrets()
	if *updateBackendSecrets {
		writeBackendSecrets(t, valeurAttendue)
	}
	var valeurObtenue backendSecrets
	encoded, err := os.ReadFile(backendSecretsFixture)
	if err == nil {
		err = json.Unmarshal(encoded, &valeurObtenue)
	}

	if err != nil || !reflect.DeepEqual(valeurObtenue, valeurAttendue) {
		t.Fatalf("valeurObtenue %+v (%v), valeurAttendue %+v : régénérer avec "+
			"go test ./internal/config/ -run TestBackendSecretsFixture -update", valeurObtenue, err, valeurAttendue)
	}
}

func writeBackendSecrets(t *testing.T, secrets backendSecrets) {
	t.Helper()
	encoded, err := json.MarshalIndent(secrets, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backendSecretsFixture, append(encoded, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
