package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

const rcPasswordForTest = "RC-PASS-FOR-TEST"

func loadFromJSON(t *testing.T, content string, environment map[string]string) (Config, error) {
	t.Helper()
	configDir := t.TempDir()
	writeFile(t, configDir, configFileName, content)
	return Load(nil, lookupFrom(environment), configDir)
}

func TestFsProviders_parsesRcloneEntryWithMounts(t *testing.T) {
	content := `{"fsProviders": [{"type": "rclone", "name": "mega", "rc": "http://rcuser:${RCLONE_RC_PASS}@rclone:5572",
		"binary": "/usr/bin/rclone", "timeout": "30s", "mounts": ["/volumes/mega=megas4:bucket/prefix", "/volumes/other=remote:"]}]}`

	loadedConfig, err := loadFromJSON(t, content, map[string]string{"RCLONE_RC_PASS": rcPasswordForTest})
	if err != nil {
		t.Fatal(err)
	}
	expectedValue := FSProvider{
		Type: "rclone", Name: "mega", RC: "http://rcuser:" + rcPasswordForTest + "@rclone:5572", Binary: "/usr/bin/rclone",
		Timeout: 30 * time.Second, Mounts: []FSMount{{LocalPath: "/volumes/mega", Target: "megas4:bucket/prefix"}, {LocalPath: "/volumes/other", Target: "remote:"}},
	}
	if len(loadedConfig.FSProviders) != 1 || fmt.Sprint(loadedConfig.FSProviders[0]) != fmt.Sprint(expectedValue) {
		t.Fatalf("gotValue %+v, expectedValue %+v", loadedConfig.FSProviders, expectedValue)
	}
}

func TestFsProviders_defaultsWhenAbsentOrMinimal(t *testing.T) {
	absent := mustLoad(t, nil, nil, t.TempDir())
	minimal, err := loadFromJSON(t, `{"fsProviders": [{"type": "rclone", "name": "a"}]}`, nil)

	if err != nil || len(absent.FSProviders) != 0 || minimal.FSProviders[0].Timeout != 60*time.Second {
		t.Fatalf("absent %v, minimal %+v, err %v", absent.FSProviders, minimal.FSProviders, err)
	}
}

func TestFsProviders_rejectsUnknownTypeNamingKnownOnes(t *testing.T) {
	_, err := loadFromJSON(t, `{"fsProviders": [{"type": "ftp", "name": "a"}]}`, nil)

	if err == nil || !strings.Contains(err.Error(), `"ftp"`) || !strings.Contains(err.Error(), "known types: rclone") {
		t.Fatalf("got %v", err)
	}
	_, err = loadFromJSON(t, `{"fsProviders": [{"name": "a"}]}`, nil)
	if err == nil || !strings.Contains(err.Error(), "type is required") {
		t.Fatalf("missing type: got %v", err)
	}
}

func TestFsProviders_rejectsUnknownKeyForType(t *testing.T) {
	_, err := loadFromJSON(t, `{"fsProviders": [{"type": "rclone", "name": "a", "host": "x"}]}`, nil)

	if err == nil || !strings.Contains(err.Error(), `"host"`) {
		t.Fatalf("got %v", err)
	}
}

func TestFsProviders_rejectsDuplicateNameAndSharedMountPath(t *testing.T) {
	cases := []struct{ caseName, providers, expectedSubstring string }{
		{"duplicate name", `[{"type":"rclone","name":"dup","mounts":["/x=r:"]},{"type":"rclone","name":"dup","mounts":["/y=r:"]}]`, `duplicate name "dup"`},
		{"shared path", `[{"type":"rclone","name":"a","mounts":["/x=r:"]},{"type":"rclone","name":"b","mounts":["/x=s:"]}]`, `local path "/x" is mounted by both "a" and "b"`},
		{"duplicate in one", `[{"type":"rclone","name":"a","mounts":["/x=r:","/x=s:"]}]`, `local path "/x" is mounted by both "a" and "a"`},
		{"missing name", `[{"type":"rclone"}]`, `name "" is required`},
		{"uppercase name", `[{"type":"rclone","name":"Mega"}]`, `name "Mega"`},
		{"two separators", `[{"type":"rclone","name":"a","mounts":["/a=b=c:x"]}]`, `exactly one "="`},
		{"relative local", `[{"type":"rclone","name":"a","mounts":["a/b=r:x"]}]`, `local path "a/b" must be absolute`},
		{"parent segment", `[{"type":"rclone","name":"a","mounts":["/a/../b=r:x"]}]`, `local path "/a/../b" must be absolute`},
		{"remote no colon", `[{"type":"rclone","name":"a","mounts":["/a=remote"]}]`, `remote "remote" must contain`},
		{"remote is option", `[{"type":"rclone","name":"a","mounts":["/a=-x:y"]}]`, `remote "-x:y" must contain`},
		{"bad timeout", `[{"type":"rclone","name":"a","timeout":"soon"}]`, `timeout: "soon"`},
		{"rc not http", `[{"type":"rclone","name":"a","rc":"ftp://u:` + rcPasswordForTest + `@h:1"}]`, "rc must be an http(s) url"},
		{"rc without host", `[{"type":"rclone","name":"a","rc":"http://u:` + rcPasswordForTest + `@"}]`, "rc must be an http(s) url"},
		{"rc fragment", `[{"type":"rclone","name":"a","rc":"http://u:` + rcPasswordForTest + `@h:1#x"}]`, "rc must be an http(s) url"},
		{"undefined variable", `[{"type":"rclone","name":"a","rc":"http://u:${NOPE}@h:1"}]`, "undefined variable NOPE"},
	}
	for _, testCase := range cases {
		_, err := loadFromJSON(t, `{"fsProviders": `+testCase.providers+`}`, nil)

		if err == nil || !strings.Contains(err.Error(), "fsProviders") || !strings.Contains(err.Error(), testCase.expectedSubstring) ||
			strings.Contains(err.Error(), rcPasswordForTest) {
			t.Fatalf("%s: gotValue %v, expected substring %q", testCase.caseName, err, testCase.expectedSubstring)
		}
	}
}

func TestFsProviders_duplicateJSONKeysKeepTheLastValue(t *testing.T) {
	// Limite d'encoding/json, documentée ici : aucune détection de clé dupliquée.
	loadedConfig, err := loadFromJSON(t, `{"fsProviders": [{"type": "rclone", "name": "first", "name": "second"}]}`, nil)

	if err != nil || loadedConfig.FSProviders[0].Name != "second" {
		t.Fatalf("gotValue %+v %v, expectedValue name second", loadedConfig.FSProviders, err)
	}
}

func TestFSProvider_redactsRcCredentialsWhenPrintedAlone(t *testing.T) {
	provider := FSProvider{Type: "rclone", Name: "mega", RC: "http://rcuser:" + rcPasswordForTest + "@rclone:5572"}
	renderings := []string{
		provider.String(), fmt.Sprintf("%v", provider), fmt.Sprintf("%+v", provider), fmt.Sprintf("%#v", provider),
		fmt.Sprintf("%v", slog.Any("p", provider)), fmt.Sprintf("%v", &provider),
	}

	for _, rendered := range renderings {
		if strings.Contains(rendered, rcPasswordForTest) || strings.Contains(rendered, "rcuser") || !strings.Contains(rendered, "rclone:5572") {
			t.Fatalf("bad rendering: %s", rendered)
		}
	}
	marshaled, err := json.Marshal(provider)
	if err != nil || strings.Contains(string(marshaled), rcPasswordForTest) {
		t.Fatalf("marshal: %s %v", marshaled, err)
	}
}

func TestFsProviders_nestedMountPathsAcrossProvidersAreAllowed(t *testing.T) {
	_, err := loadFromJSON(t, `{"fsProviders": [{"type":"rclone","name":"a","mounts":["/x=r:"]},{"type":"rclone","name":"b","mounts":["/x/y=s:"]}]}`, nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestFsProviders_oldRcloneKeyIsRefusedWithRenameHint(t *testing.T) {
	for _, content := range []string{`{"rclone": {"rc": "http://rclone:5572"}}`, `{"rclone": null}`} {
		_, err := loadFromJSON(t, content, nil)

		if err == nil || !strings.Contains(err.Error(), "renamed to") || !strings.Contains(err.Error(), "fsProviders") {
			t.Fatalf("%s: got %v", content, err)
		}
	}
}

func TestFsProviders_redactsRcCredentials(t *testing.T) {
	loadedConfig, err := loadFromJSON(t, `{"fsProviders": [{"type": "rclone", "name": "mega", "rc": "http://rcuser:${RCLONE_RC_PASS}@rclone:5572",
		"mounts": ["/volumes/mega=megas4:bucket"]}]}`, map[string]string{"RCLONE_RC_PASS": rcPasswordForTest})
	if err != nil {
		t.Fatal(err)
	}
	redactedJSON, err := loadedConfig.RedactedJSON()
	if err != nil {
		t.Fatal(err)
	}
	renderings := []string{string(redactedJSON), loadedConfig.String(), loadedConfig.LogValue().String(), fmt.Sprintf("%#v", loadedConfig), fmt.Sprintf("%v", slog.Any("c", loadedConfig))}

	for _, rendered := range renderings {
		if strings.Contains(rendered, rcPasswordForTest) || strings.Contains(rendered, "rcuser") {
			t.Fatalf("credentials leaked: %s", rendered)
		}
	}
	for _, visible := range []string{"megas4:bucket", "rclone:5572", `"name": "mega"`} {
		if !strings.Contains(string(redactedJSON), visible) {
			t.Fatalf("%q must stay visible: %s", visible, redactedJSON)
		}
	}
	if !strings.Contains(loadedConfig.FSProviders[0].RC, rcPasswordForTest) {
		t.Fatal("redaction must not alter the loaded configuration")
	}
}

func TestLoad_dedupeVerifyDefaultsToAutoAndRejectsUnknownMode(t *testing.T) {
	if gotValue := mustLoad(t, nil, nil, t.TempDir()).Dedupe.Verify; gotValue != "auto" {
		t.Fatalf("gotValue %q, expectedValue auto", gotValue)
	}
	for _, mode := range []string{"auto", "full", "sampled", "hash"} {
		configDir := t.TempDir()
		writeFile(t, configDir, configFileName, `{"dedupe": {"verify": "`+mode+`"}}`)
		if gotValue := mustLoad(t, nil, nil, configDir).Dedupe.Verify; gotValue != mode {
			t.Fatalf("gotValue %q, expectedValue %q", gotValue, mode)
		}
	}
	_, err := loadFromJSON(t, `{"dedupe": {"verify": "quick"}}`, nil)

	if err == nil || !strings.Contains(err.Error(), "dedupe.verify") {
		t.Fatalf("got %v", err)
	}
}
