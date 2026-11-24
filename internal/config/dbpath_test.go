package config

import (
	"path/filepath"
	"testing"
)

func TestResolveDBPath_relativePathReadsFromConfigDir(t *testing.T) {
	configDir, absoluteDB := absPath("home", "u", "cfg"), absPath("var", "a.db")

	if gotValue := ResolveDBPath(Config{ConfigDir: configDir, DBPath: "a.db"}); gotValue != filepath.Join(configDir, "a.db") {
		t.Fatalf("gotValue %q", gotValue)
	}
	if gotValue := ResolveDBPath(Config{ConfigDir: configDir, DBPath: absoluteDB}); gotValue != absoluteDB {
		t.Fatalf("gotValue %q", gotValue)
	}
}
