package sftptest_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/pkg/sftp"

	"necromanciarr/internal/fsys/sftpfs/sftptest"
)

func TestServer_withListingServesRawNames(t *testing.T) {
	server := sftptest.Start(t, sftptest.WithListing("/hostile",
		sftptest.Entry{Name: "plain", Size: 3}, sftptest.Entry{Name: "x\x00y", Size: 1}, sftptest.Entry{Name: "../"}))
	client := connect(t, server)

	infos, err := client.ReadDir("/hostile")
	if err != nil {
		t.Fatal(err)
	}
	valeurObtenue := []string{}
	for _, info := range infos {
		valeurObtenue = append(valeurObtenue, info.Name())
	}
	// pkg/sftp réduit chaque nom à path.Base : « ../ » arrive « .. », le NUL reste.
	valeurAttendue := []string{"plain", "x\x00y", ".."}
	if !slices.Equal(valeurObtenue, valeurAttendue) {
		t.Fatalf("valeurObtenue %q, valeurAttendue %q", valeurObtenue, valeurAttendue)
	}
	info, err := client.Lstat("/hostile/plain")
	if err != nil || info.Size() != 3 {
		t.Fatalf("valeurObtenue %v (err %v), valeurAttendue the fake entry of size 3", info, err)
	}
}

func TestServer_withReadlinkTargetAnswersTheGivenTarget(t *testing.T) {
	server := sftptest.Start(t, sftptest.WithReadlinkTarget("/link", "/far/away"))
	client := connect(t, server)

	valeurObtenue, err := client.ReadLink("/link")
	if err != nil || valeurObtenue != "/far/away" {
		t.Fatalf("valeurObtenue %q (err %v), valeurAttendue /far/away", valeurObtenue, err)
	}
}

func TestServer_withStatVFSChangesTheReply(t *testing.T) {
	server := sftptest.Start(t, sftptest.WithStatVFS(func(reply *sftp.StatVFS) { reply.Fsid = 42 }))
	client := connect(t, server)

	reply, err := client.StatVFS("/")
	if err != nil || reply.Fsid != 42 {
		t.Fatalf("valeurObtenue %+v (err %v), valeurAttendue fsid 42", reply, err)
	}
}

func TestServer_recordsTheOpenFlags(t *testing.T) {
	server := sftptest.Start(t)
	client := connect(t, server)
	writeRemoteFile(t, client, "/file", "content")

	file, err := client.OpenFile("/file", os.O_RDONLY|os.O_EXCL)
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()

	valeurAttendue := sftptest.Request{Method: "Get", Path: "/file", Flags: sftptest.FlagRead | sftptest.FlagExclusive}
	if !slices.Contains(server.Requests(), valeurAttendue) {
		t.Fatalf("valeurObtenue %+v, valeurAttendue %+v recorded", server.Requests(), valeurAttendue)
	}
}

func TestServer_withTruncatedReadsEndsReadsEarlyButKeepsTheSize(t *testing.T) {
	server := sftptest.Start(t, sftptest.WithTruncatedReads("/movie.mkv", 4))
	sftptest.WriteFile(t, filepath.Join(server.Root, "movie.mkv"), []byte("0123456789"), 0o644)
	client := connect(t, server)
	file, err := client.Open("/movie.mkv")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()

	buffer := make([]byte, 10)
	readCount, err := file.ReadAt(buffer, 0)

	if readCount != 4 || !errors.Is(err, io.EOF) {
		t.Errorf("valeurObtenue %d bytes, %v, valeurAttendue 4 bytes then io.EOF", readCount, err)
	}
	info, err := file.Stat()
	if err != nil || info.Size() != 10 {
		t.Errorf("valeurObtenue %v (err %v), valeurAttendue the real size 10", info, err)
	}
}
