package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestMountCapabilities_roundTrip(t *testing.T) {
	testStore := openTestStore(t)
	probedAt := time.Unix(1700000000, 0).UTC()
	expectedValue := MountCapabilities{MountPath: "/home/u/mnt/mega", Hashes: []string{"MD5"}, ModTime: true, Symlinks: false, ProbedAt: probedAt}

	if err := testStore.SaveMountCapabilities(context.Background(), expectedValue); err != nil {
		t.Fatal(err)
	}
	gotValue, found, err := testStore.LoadMountCapabilities(context.Background(), "/home/u/mnt/mega")

	if err != nil || !found {
		t.Fatalf("found %v err %v", found, err)
	}
	if gotValue.MountPath != expectedValue.MountPath || len(gotValue.Hashes) != 1 || gotValue.Hashes[0] != "MD5" ||
		!gotValue.ModTime || gotValue.Symlinks || !gotValue.ProbedAt.Equal(probedAt) {
		t.Fatalf("got %+v, expected %+v", gotValue, expectedValue)
	}
}

func TestMountCapabilities_replacesAndReportsUnknownMount(t *testing.T) {
	testStore := openTestStore(t)
	ctx := context.Background()
	if _, found, err := testStore.LoadMountCapabilities(ctx, "/home/u/mnt/none"); err != nil || found {
		t.Fatalf("unknown mount: found %v err %v", found, err)
	}
	first := MountCapabilities{MountPath: "/home/u/mnt/a", Hashes: []string{"MD5", "SHA1"}, ProbedAt: time.Unix(1, 0)}
	second := MountCapabilities{MountPath: "/home/u/mnt/a", Hashes: nil, Symlinks: true, ProbedAt: time.Unix(2, 0)}
	for _, capabilities := range []MountCapabilities{first, second} {
		if err := testStore.SaveMountCapabilities(ctx, capabilities); err != nil {
			t.Fatal(err)
		}
	}

	gotValue, _, err := testStore.LoadMountCapabilities(ctx, "/home/u/mnt/a")

	if err != nil || len(gotValue.Hashes) != 0 || !gotValue.Symlinks {
		t.Fatalf("second save must replace the first: %v %+v", err, gotValue)
	}
}

func TestRecordLinkAndFingerprint_keepBackendHashes(t *testing.T) {
	testStore := openTestStore(t)
	ctx := context.Background()
	link := sampleLink()
	link.HashAlgo, link.Hash = "MD5", "d41d8cd98f00b204e9800998ecf8427e"
	if err := testStore.RecordLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	modTime := time.Unix(1700000000, 0).UTC()
	entry := CachedFingerprint{Path: "/home/u/a.mkv", Size: 3, ModTime: modTime, HeadTailHash: "ht", VerifiedAt: modTime, HashAlgo: "MD5", Hash: "ab"}
	for range 2 {
		if err := testStore.RecordFingerprint(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}

	gotLinks, err := testStore.ListLinks(ctx, "")
	if err != nil || len(gotLinks) != 1 || gotLinks[0].HashAlgo != "MD5" || gotLinks[0].Hash != link.Hash {
		t.Fatalf("link hash lost: %v %+v", err, gotLinks)
	}
	var gotAlgo, gotHash sql.NullString
	err = testStore.database.QueryRow(`SELECT hash_algo, hash FROM fingerprints WHERE path = ?`, entry.Path).Scan(&gotAlgo, &gotHash)
	if err != nil || gotAlgo.String != "MD5" || gotHash.String != "ab" {
		t.Fatalf("fingerprint hash lost: %v %v %v", err, gotAlgo, gotHash)
	}
}

func TestStore_upgradesOldLinksAndFingerprintsTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	oldDatabase, err := sql.Open("sqlite", dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	_, err = oldDatabase.Exec(`CREATE TABLE links (id INTEGER PRIMARY KEY, kind TEXT NOT NULL, link_path TEXT NOT NULL UNIQUE,
	target_path TEXT NOT NULL, relative INTEGER NOT NULL, target_dev INTEGER, target_ino INTEGER, inode_reliable INTEGER NOT NULL,
	size INTEGER NOT NULL, fingerprint TEXT NOT NULL, state TEXT NOT NULL, created_at INTEGER NOT NULL, verified_at INTEGER NOT NULL,
	mtime INTEGER);
	CREATE TABLE fingerprints (id INTEGER PRIMARY KEY, path TEXT NOT NULL UNIQUE, size INTEGER NOT NULL, mtime INTEGER NOT NULL,
	head_tail_hash TEXT NOT NULL, verified_at INTEGER NOT NULL);
	INSERT INTO links (kind, link_path, target_path, relative, inode_reliable, size, fingerprint, state, created_at, verified_at)
	VALUES ('symlink', '/home/u/a.mkv', '/home/u/b.mkv', 0, 0, 5, 'f', 'ok', 1, 1);
	INSERT INTO fingerprints (path, size, mtime, head_tail_hash, verified_at) VALUES ('/home/u/b.mkv', 5, 7, 'ht', 1)`)
	if err != nil || oldDatabase.Close() != nil {
		t.Fatal(err)
	}

	for range 2 {
		reopened, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		gotLinks, err := reopened.ListLinks(context.Background(), "")
		if err != nil || len(gotLinks) != 1 || gotLinks[0].Hash != "" || gotLinks[0].HashAlgo != "" {
			t.Fatalf("old link lost or unreadable: %v %+v", err, gotLinks)
		}
		gotFingerprint, found, err := reopened.LookupFingerprint(context.Background(), "/home/u/b.mkv", 5, time.Unix(0, 7))
		if err != nil || !found || gotFingerprint != "ht" {
			t.Fatalf("old fingerprint lost: %v %v %q", err, found, gotFingerprint)
		}
		if err := reopened.RecordFingerprint(context.Background(), CachedFingerprint{Path: "/home/u/c.mkv", HeadTailHash: "x", HashAlgo: "MD5", Hash: "h"}); err != nil {
			t.Fatal(err)
		}
		_ = reopened.Close()
	}
}
