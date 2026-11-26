package config

import (
	"encoding/json"
	"flag"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
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

// writeBackendSecrets écrit la fixture comme prettier la rend : un tableau tient sur une ligne s'il ne dépasse pas
// prettierWidth colonnes, sinon un élément par ligne.
func writeBackendSecrets(t *testing.T, secrets backendSecrets) {
	t.Helper()
	var lines []string
	lines = append(lines, "{", `  "elements": {`)
	for position, list := range slices.Sorted(maps.Keys(secrets.Elements)) {
		isLast := position == len(secrets.Elements)-1
		lines = append(lines, arrayLines(t, "    ", list, secrets.Elements[list], !isLast)...)
	}
	lines = append(lines, "  },")
	lines = append(lines, arrayLines(t, "  ", "settings", secrets.Settings, true)...)
	lines = append(lines, arrayLines(t, "  ", "settingKeys", secrets.SettingKeys, true)...)
	lines = append(lines, arrayLines(t, "  ", "lists", secrets.Lists, false)...)
	lines = append(lines, "}", "")
	if err := os.WriteFile(backendSecretsFixture, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
}

// prettierWidth reprend printWidth de web/.prettierrc.
const prettierWidth = 100

func arrayLines(t *testing.T, indent, key string, values []string, hasComma bool) []string {
	t.Helper()
	comma := ""
	if hasComma {
		comma = ","
	}
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		quoted = append(quoted, string(encoded))
	}
	singleLine := indent + `"` + key + `": [` + strings.Join(quoted, ", ") + "]" + comma
	if len(singleLine) <= prettierWidth {
		return []string{singleLine}
	}
	lines := []string{indent + `"` + key + `": [`}
	for position, value := range quoted {
		separator := ","
		if position == len(quoted)-1 {
			separator = ""
		}
		lines = append(lines, indent+"  "+value+separator)
	}
	return append(lines, indent+"]"+comma)
}
