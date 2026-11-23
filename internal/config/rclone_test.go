package config

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

const rcPasswordForTest = "RC-PASS-FOR-TEST"

func TestLoad_rcloneDefaultsWhenSectionAbsent(t *testing.T) {
	gotValue := mustLoad(t, nil, nil, t.TempDir()).Rclone

	if gotValue.RC != "" || gotValue.Binary != "" || gotValue.Timeout != 60*time.Second || len(gotValue.Mounts) != 0 {
		t.Fatalf("unexpected rclone defaults %+v", gotValue)
	}
}

func TestLoad_rcloneReadsEveryKeyAndInterpolatesPassword(t *testing.T) {
	configDir := t.TempDir()
	writeFile(t, configDir, configFileName, `{"rclone": {"rc": "http://rcuser:${RCLONE_RC_PASS}@rclone:5572", "binary": "/usr/bin/rclone",
		"timeout": "30s", "mounts": ["/volumes/mega=megas4:bucket/prefix", "/volumes/other=remote:"]}}`)

	gotValue := mustLoad(t, nil, map[string]string{"RCLONE_RC_PASS": rcPasswordForTest}, configDir).Rclone

	expectedMounts := []RcloneMount{{LocalPath: "/volumes/mega", Remote: "megas4:bucket/prefix"}, {LocalPath: "/volumes/other", Remote: "remote:"}}
	if gotValue.RC != "http://rcuser:"+rcPasswordForTest+"@rclone:5572" || gotValue.Binary != "/usr/bin/rclone" ||
		gotValue.Timeout != 30*time.Second || fmt.Sprint(gotValue.Mounts) != fmt.Sprint(expectedMounts) {
		t.Fatalf("unexpected rclone %+v", gotValue)
	}
}

func TestLoad_rcloneRejectsInvalidValuesWithoutEchoingTheRcURL(t *testing.T) {
	cases := map[string]string{
		"two separators":   `{"rclone": {"mounts": ["/a=b=c:x"]}}`,
		"no separator":     `{"rclone": {"mounts": ["/a"]}}`,
		"relative local":   `{"rclone": {"mounts": ["a/b=r:x"]}}`,
		"parent segment":   `{"rclone": {"mounts": ["/a/../b=r:x"]}}`,
		"remote no colon":  `{"rclone": {"mounts": ["/a=remote"]}}`,
		"remote is option": `{"rclone": {"mounts": ["/a=-x:y"]}}`,
		"duplicate mount":  `{"rclone": {"mounts": ["/a=r:x", "/a=s:y"]}}`,
		"bad timeout":      `{"rclone": {"timeout": "soon"}}`,
		"negative timeout": `{"rclone": {"timeout": "-1s"}}`,
		"rc not http":      `{"rclone": {"rc": "ftp://u:` + rcPasswordForTest + `@h:1"}}`,
		"rc without host":  `{"rclone": {"rc": "http://u:` + rcPasswordForTest + `@"}}`,
		"rc fragment":      `{"rclone": {"rc": "http://u:` + rcPasswordForTest + `@h:1#x"}}`,
	}
	for name, content := range cases {
		configDir := t.TempDir()
		writeFile(t, configDir, configFileName, content)

		_, err := Load(nil, lookupFrom(nil), configDir)

		if err == nil || !strings.Contains(err.Error(), "rclone") || strings.Contains(err.Error(), rcPasswordForTest) {
			t.Fatalf("%s: got %v", name, err)
		}
	}
}

func TestConfig_rcloneCredentialsAreRedactedEverywhere(t *testing.T) {
	configDir := t.TempDir()
	writeFile(t, configDir, configFileName, `{"rclone": {"rc": "http://rcuser:${RCLONE_RC_PASS}@rclone:5572", "mounts": ["/volumes/mega=megas4:bucket"]}}`)
	loadedConfig := mustLoad(t, nil, map[string]string{"RCLONE_RC_PASS": rcPasswordForTest}, configDir)
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
	if !strings.Contains(string(redactedJSON), "megas4:bucket") || !strings.Contains(string(redactedJSON), "rclone:5572") {
		t.Fatalf("remote names and host must stay visible: %s", redactedJSON)
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
	configDir := t.TempDir()
	writeFile(t, configDir, configFileName, `{"dedupe": {"verify": "quick"}}`)

	_, err := Load(nil, lookupFrom(nil), configDir)

	if err == nil || !strings.Contains(err.Error(), "dedupe.verify") {
		t.Fatalf("got %v", err)
	}
}
