package web

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"necromanciarr/internal/app"
)

var updateGolden = flag.Bool("update", false, "réécrit testdata/routes.json depuis la table des routes")

const routesGoldenPath = "testdata/routes.json"

type goldenRoute struct {
	Method   string `json:"method"`
	Path     string `json:"path"`
	Action   string `json:"action"`
	Mutating bool   `json:"mutating"`
}

func currentRoutesGolden(t *testing.T) []byte {
	t.Helper()
	mutatingByName := map[string]bool{}
	for _, action := range app.Actions() {
		mutatingByName[action.Name] = action.Mutating
	}
	golden := make([]goldenRoute, 0, len(apiRoutes))
	for _, apiRoute := range apiRoutes {
		golden = append(golden, goldenRoute{apiRoute.method, apiRoute.path, apiRoute.action, mutatingByName[apiRoute.action]})
	}
	encoded, err := json.MarshalIndent(golden, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(encoded, '\n')
}

// TestRoutes_goldenMatchesTable garde la copie TypeScript (web/src/api/actions.ts) : le test Vitest compare cette
// sortie à sa table, dans les deux sens.
func TestRoutes_goldenMatchesTable(t *testing.T) {
	expectedValue := currentRoutesGolden(t)
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(routesGoldenPath), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(routesGoldenPath, expectedValue, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gotValue, err := os.ReadFile(routesGoldenPath)
	if err != nil {
		t.Fatalf("%v: regenerate with: go test ./internal/web -run TestRoutes_goldenMatchesTable -update", err)
	}
	if !bytes.Equal(gotValue, expectedValue) {
		t.Fatalf("%s is out of date with the route table: regenerate with: go test ./internal/web -run TestRoutes_goldenMatchesTable -update, then update web/src/api/actions.ts", routesGoldenPath)
	}
}
