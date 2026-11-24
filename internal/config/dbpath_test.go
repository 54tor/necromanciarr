package config

import "testing"

func TestResolveDBPath_relativePathReadsFromConfigDir(t *testing.T) {
	if gotValue := ResolveDBPath(Config{ConfigDir: "/home/u/cfg", DBPath: "a.db"}); gotValue != "/home/u/cfg/a.db" {
		t.Fatalf("gotValue %q", gotValue)
	}
	if gotValue := ResolveDBPath(Config{ConfigDir: "/home/u/cfg", DBPath: "/var/a.db"}); gotValue != "/var/a.db" {
		t.Fatalf("gotValue %q", gotValue)
	}
}
