package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	testStore, err := Open(filepath.Join(t.TempDir(), "necromanciarr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = testStore.Close() })
	return testStore
}

func sampleLink() Link {
	moment := time.Unix(1700000000, 0).UTC()
	return Link{
		Kind:          "symlink",
		LinkPath:      "/home/u/torrents/yuyu.mkv",
		TargetPath:    "/home/u/library/yuyu (1944)/mon film.mkv",
		Relative:      true,
		TargetDevice:  42,
		TargetInode:   4242,
		InodeReliable: true,
		Size:          1000,
		Fingerprint:   "abc123",
		State:         LinkOK,
		CreatedAt:     moment,
		VerifiedAt:    moment,
		ModTime:       moment.Add(-time.Hour),
	}
}

func TestRecordLink_upsertsOnLinkPath(t *testing.T) {
	ctx := context.Background()
	testStore := openTestStore(t)
	link := sampleLink()
	if err := testStore.RecordLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	link.TargetPath = "/home/u/library/other.mkv"
	link.Size = 2000
	if err := testStore.RecordLink(ctx, link); err != nil {
		t.Fatal(err)
	}

	gotLinks, err := testStore.ListLinks(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(gotLinks) != 1 {
		t.Fatalf("got %d links, want 1", len(gotLinks))
	}
	gotValue := gotLinks[0]
	expectedValue := link
	expectedValue.ID = gotValue.ID
	if gotValue.ID == 0 || gotValue != expectedValue {
		t.Fatalf("gotValue %+v, expectedValue %+v", gotValue, expectedValue)
	}
}

func TestRecordLink_doesNotStoreInodeOnNonLocalVolume(t *testing.T) {
	ctx := context.Background()
	testStore := openTestStore(t)
	link := sampleLink()
	link.InodeReliable = false

	if err := testStore.RecordLink(ctx, link); err != nil {
		t.Fatal(err)
	}

	var storedDevice, storedInode sql.NullInt64
	row := testStore.database.QueryRowContext(ctx, `SELECT target_dev, target_ino FROM links WHERE link_path = ?`, link.LinkPath)
	if err := row.Scan(&storedDevice, &storedInode); err != nil {
		t.Fatal(err)
	}
	if storedDevice.Valid || storedInode.Valid {
		t.Fatalf("expected NULL device and inode, got %v %v", storedDevice, storedInode)
	}
	gotLinks, err := testStore.ListLinks(ctx, LinkOK)
	if err != nil {
		t.Fatal(err)
	}
	gotValue := gotLinks[0]
	if gotValue.InodeReliable || gotValue.TargetDevice != 0 || gotValue.TargetInode != 0 {
		t.Fatalf("expected no inode, got %+v", gotValue)
	}
}

func TestRecordLink_rejectsUnknownState(t *testing.T) {
	link := sampleLink()
	link.State = "fine"

	err := openTestStore(t).RecordLink(context.Background(), link)

	if !errors.Is(err, ErrUnknownLinkState) {
		t.Fatalf("expected ErrUnknownLinkState, got %v", err)
	}
}

func TestListLinks_filtersByState(t *testing.T) {
	ctx := context.Background()
	testStore := openTestStore(t)
	brokenLink := sampleLink()
	brokenLink.LinkPath = "/home/u/torrents/broken.mkv"
	brokenLink.State = LinkBroken
	for _, link := range []Link{sampleLink(), brokenLink} {
		if err := testStore.RecordLink(ctx, link); err != nil {
			t.Fatal(err)
		}
	}

	gotLinks, err := testStore.ListLinks(ctx, LinkBroken)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotLinks) != 1 || gotLinks[0].LinkPath != brokenLink.LinkPath {
		t.Fatalf("gotValue %+v, expected only %s", gotLinks, brokenLink.LinkPath)
	}
}

func TestSetLinkState_updatesStateAndVerification(t *testing.T) {
	ctx := context.Background()
	testStore := openTestStore(t)
	link := sampleLink()
	if err := testStore.RecordLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	verifiedAt := link.VerifiedAt.Add(time.Hour)

	if err := testStore.SetLinkState(ctx, link.LinkPath, LinkDiverged, verifiedAt); err != nil {
		t.Fatal(err)
	}

	gotLinks, err := testStore.ListLinks(ctx, LinkDiverged)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotLinks) != 1 || !gotLinks[0].VerifiedAt.Equal(verifiedAt) {
		t.Fatalf("gotValue %+v, expected one diverged link verified at %v", gotLinks, verifiedAt)
	}
	if err := testStore.SetLinkState(ctx, link.LinkPath, "fine", verifiedAt); !errors.Is(err, ErrUnknownLinkState) {
		t.Fatalf("expected ErrUnknownLinkState, got %v", err)
	}
}

func TestUpdateLinkTarget_changesTargetAndReportsMissingLink(t *testing.T) {
	ctx := context.Background()
	testStore := openTestStore(t)
	link := sampleLink()
	if err := testStore.RecordLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	expectedValue := "/home/u/library/renamed.mkv"

	if err := testStore.UpdateLinkTarget(ctx, link.LinkPath, expectedValue); err != nil {
		t.Fatal(err)
	}

	gotLinks, err := testStore.ListLinks(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if gotValue := gotLinks[0].TargetPath; gotValue != expectedValue {
		t.Fatalf("gotValue %q, expectedValue %q", gotValue, expectedValue)
	}
	if err := testStore.UpdateLinkTarget(ctx, "/home/u/none.mkv", expectedValue); !errors.Is(err, ErrLinkNotFound) {
		t.Fatalf("expected ErrLinkNotFound, got %v", err)
	}
}

func TestFingerprintCache_hitsOnlyWhenSizeAndModTimeMatch(t *testing.T) {
	ctx := context.Background()
	testStore := openTestStore(t)
	modTime := time.Unix(1700000000, 123).UTC()
	entry := CachedFingerprint{Path: "/home/u/library/film.mkv", Size: 10, ModTime: modTime, HeadTailHash: "feed", VerifiedAt: modTime}
	if err := testStore.RecordFingerprint(ctx, entry); err != nil {
		t.Fatal(err)
	}
	entry.HeadTailHash = "beef"
	if err := testStore.RecordFingerprint(ctx, entry); err != nil {
		t.Fatal(err)
	}

	gotValue, found, err := testStore.LookupFingerprint(ctx, entry.Path, entry.Size, modTime)
	if err != nil || !found || gotValue != "beef" {
		t.Fatalf("gotValue %q found %v err %v, expectedValue beef", gotValue, found, err)
	}
	for _, staleCase := range []struct {
		size    int64
		modTime time.Time
	}{{11, modTime}, {10, modTime.Add(time.Nanosecond)}} {
		_, found, err := testStore.LookupFingerprint(ctx, entry.Path, staleCase.size, staleCase.modTime)
		if err != nil || found {
			t.Fatalf("expected a miss for %+v, got found %v err %v", staleCase, found, err)
		}
	}
}

func TestRecordLink_keepsModTimeAndZeroWhenUnknown(t *testing.T) {
	ctx := context.Background()
	testStore := openTestStore(t)
	unknown := sampleLink()
	unknown.LinkPath, unknown.ModTime = "/home/u/torrents/unknown.mkv", time.Time{}
	for _, link := range []Link{sampleLink(), unknown} {
		if err := testStore.RecordLink(ctx, link); err != nil {
			t.Fatal(err)
		}
	}

	gotLinks, err := testStore.ListLinks(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(gotLinks) != 2 || !gotLinks[1].ModTime.Equal(sampleLink().ModTime) || !gotLinks[0].ModTime.IsZero() {
		t.Fatalf("unexpected mod times %+v", gotLinks)
	}
}

func TestOpen_addsMtimeColumnToAnOlderLinksTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	oldDatabase, err := sql.Open("sqlite", dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	_, err = oldDatabase.Exec(`CREATE TABLE links (id INTEGER PRIMARY KEY, kind TEXT NOT NULL, link_path TEXT NOT NULL UNIQUE,
	target_path TEXT NOT NULL, relative INTEGER NOT NULL, target_dev INTEGER, target_ino INTEGER, inode_reliable INTEGER NOT NULL,
	size INTEGER NOT NULL, fingerprint TEXT NOT NULL, state TEXT NOT NULL, created_at INTEGER NOT NULL, verified_at INTEGER NOT NULL);
	INSERT INTO links (kind, link_path, target_path, relative, inode_reliable, size, fingerprint, state, created_at, verified_at)
	VALUES ('symlink', '/home/u/a.mkv', '/home/u/b.mkv', 0, 0, 5, 'f', 'ok', 1, 1)`)
	if err != nil || oldDatabase.Close() != nil {
		t.Fatal(err)
	}

	for range 2 {
		reopened, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		gotLinks, err := reopened.ListLinks(context.Background(), "")
		if err != nil || len(gotLinks) != 1 || gotLinks[0].Size != 5 || !gotLinks[0].ModTime.IsZero() {
			t.Fatalf("old row lost or unreadable: %v %+v", err, gotLinks)
		}
		_ = reopened.Close()
	}
}
