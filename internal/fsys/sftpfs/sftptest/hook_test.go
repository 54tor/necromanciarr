package sftptest_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"necromanciarr/internal/fsys/sftpfs/sftptest"
)

func TestServer_onRequestRunsBeforeTheRequestIsServed(t *testing.T) {
	skipSymlinksOnWindows(t)
	var server *sftptest.Server
	var once sync.Once
	server = sftptest.Start(t, sftptest.OnRequest("Mkdir", func(sftptest.Request) {
		once.Do(func() {
			sftptest.ReplaceWithSymlink(t, filepath.Join(server.Root, "links", "a"), "/outside")
		})
	}))
	sftptest.Mkdir(t, filepath.Join(server.Root, "links", "a"))
	sftptest.Mkdir(t, filepath.Join(server.Root, "outside"))
	client := connect(t, server)

	if err := client.Mkdir("/links/a/b"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	if _, err := os.Lstat(filepath.Join(server.Root, "outside", "b")); err != nil {
		t.Errorf("valeurObtenue %v, valeurAttendue the directory created through the swapped parent", err)
	}
}

func TestServer_exclusiveOpenNeverFollowsAFinalSymlink(t *testing.T) {
	skipSymlinksOnWindows(t)
	server := sftptest.Start(t)
	symlinkInRoot(t, server, "/missing-target", "dangling")
	client := connect(t, server)

	file, err := client.OpenFile("/dangling", os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err == nil {
		_ = file.Close()
		t.Fatal("valeurObtenue a created file, valeurAttendue an exclusive open refused on a dangling symlink")
	}

	if _, err := os.Lstat(filepath.Join(server.Root, "missing-target")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("valeurObtenue %v, valeurAttendue nothing created at the symlink target", err)
	}
}

func TestServer_withDirectoryModeGivesCreatedDirectoriesThatMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	server := sftptest.Start(t, sftptest.WithDirectoryMode(0o777))
	client := connect(t, server)

	if err := client.Mkdir("/open"); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(filepath.Join(server.Root, "open"))
	if err != nil {
		t.Fatal(err)
	}
	if valeurObtenue := info.Mode().Perm(); valeurObtenue != 0o777 {
		t.Errorf("valeurObtenue %v, valeurAttendue 0777 whatever the process umask", valeurObtenue)
	}
}
