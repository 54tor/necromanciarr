package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"necromanciarr/internal/config"
	"necromanciarr/internal/repair"
	"necromanciarr/internal/store"
)

// treeEntry est l'instantané d'une entrée : texte du lien pour un symlink, contenu pour un fichier, et ses métadonnées
// sans suivre les liens (date, inode).
type treeEntry struct {
	content []byte
	info    fs.FileInfo
}

func snapshotTree(t *testing.T, root string) map[string]treeEntry {
	t.Helper()
	snapshot := map[string]treeEntry{}
	err := filepath.WalkDir(root, func(entryPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		info, err := os.Lstat(entryPath)
		if err != nil {
			return err
		}
		var content []byte
		if info.Mode()&fs.ModeSymlink != 0 {
			target, err := os.Readlink(entryPath)
			content = []byte(target)
			if err != nil {
				return err
			}
		} else if content, err = os.ReadFile(entryPath); err != nil { // #nosec G304 -- chemin de test sous t.TempDir()
			return err
		}
		snapshot[entryPath] = treeEntry{content: content, info: info}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func assertTreeUnchanged(t *testing.T, before, after map[string]treeEntry) {
	t.Helper()
	if len(after) != len(before) {
		t.Fatalf("gotValue %d entries, expectedValue %d: ListLinks changed the tree", len(after), len(before))
	}
	for entryPath, expectedValue := range before {
		gotValue, present := after[entryPath]
		unchanged := present && bytes.Equal(gotValue.content, expectedValue.content) &&
			gotValue.info.ModTime().Equal(expectedValue.info.ModTime()) && os.SameFile(gotValue.info, expectedValue.info)
		if !unchanged {
			t.Fatalf("gotValue a change on %s, expectedValue no write", entryPath)
		}
	}
}

type linksFixture struct {
	t       *testing.T
	store   *store.Store
	root    string
	library string
	links   string
}

func newLinksFixture(t *testing.T) *linksFixture {
	t.Helper()
	root := t.TempDir()
	fixture := &linksFixture{t: t, store: openTestStore(t), root: root, library: filepath.Join(root, "library"), links: filepath.Join(root, "links")}
	for _, directory := range []string{fixture.library, fixture.links} {
		if err := os.MkdirAll(directory, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	return fixture
}

func (fixture *linksFixture) libraryFile(name string) string {
	fixture.t.Helper()
	path := filepath.Join(fixture.library, name)
	writeAgedFile(fixture.t, path, []byte("content of "+name), agedModTime)
	return path
}

func (fixture *linksFixture) record(kind, linkPath, targetPath string) {
	fixture.t.Helper()
	fixture.recordSized(kind, linkPath, targetPath, 42)
}

func (fixture *linksFixture) recordSized(kind, linkPath, targetPath string, size int64) {
	fixture.t.Helper()
	link := store.Link{Kind: kind, LinkPath: linkPath, TargetPath: targetPath, Size: size, State: store.LinkOK, CreatedAt: fixedNow}
	if err := fixture.store.RecordLink(context.Background(), link); err != nil {
		fixture.t.Fatal(err)
	}
}

func (fixture *linksFixture) symlink(name, targetPath string) string {
	fixture.t.Helper()
	linkPath := filepath.Join(fixture.links, name)
	if err := os.Symlink(targetPath, linkPath); err != nil {
		fixture.t.Fatal(err)
	}
	fixture.record("symlink", linkPath, targetPath)
	return linkPath
}

func (fixture *linksFixture) hardlink(name, targetPath string) string {
	fixture.t.Helper()
	linkPath := filepath.Join(fixture.links, name)
	if err := os.Link(targetPath, linkPath); err != nil {
		fixture.t.Fatal(err)
	}
	fixture.record("hardlink", linkPath, targetPath)
	return linkPath
}

func (fixture *linksFixture) service() *service {
	return New(Deps{Store: fixture.store}).(*service)
}

func (fixture *linksFixture) storedStates() map[string]store.LinkState {
	fixture.t.Helper()
	links, err := fixture.store.ListLinks(context.Background(), "")
	if err != nil {
		fixture.t.Fatal(err)
	}
	states := map[string]store.LinkState{}
	for _, link := range links {
		states[link.LinkPath] = link.State
	}
	return states
}

func statesByPath(page LinkPage) map[string]string {
	states := map[string]string{}
	for _, item := range page.Items {
		states[item.LinkPath] = item.State
	}
	return states
}

func TestListLinks_reportsOkDivergedLostUnreadableWithoutWriting(t *testing.T) {
	fixture := newLinksFixture(t)
	okSymlink := fixture.symlink("ok.mkv", fixture.libraryFile("ok.mkv"))
	okHardlink := fixture.hardlink("hard.mkv", fixture.libraryFile("hard.mkv"))
	divergedTarget := fixture.libraryFile("replaced.mkv")
	diverged := fixture.hardlink("replaced.mkv", divergedTarget)
	writeAgedFile(t, divergedTarget+".new", []byte("other content"), agedModTime)
	if err := os.Rename(divergedTarget+".new", divergedTarget); err != nil {
		t.Fatal(err)
	}
	brokenTarget := fixture.libraryFile("gone.mkv")
	broken := fixture.symlink("gone.mkv", brokenTarget)
	if err := os.Remove(brokenTarget); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(fixture.links, "never-there.mkv")
	fixture.record("symlink", missing, fixture.libraryFile("orphan.mkv"))
	unreadable := fixture.symlink("not-a-dir.mkv", filepath.Join(fixture.libraryFile("plain.mkv"), "child"))
	replacedSymlink := fixture.symlink("replaced-symlink.mkv", fixture.libraryFile("kept.mkv"))
	if err := os.Remove(replacedSymlink); err != nil {
		t.Fatal(err)
	}
	writeAgedFile(t, replacedSymlink, []byte("a regular file now"), agedModTime)
	copyContent := []byte("copy content")
	resizedCopy := filepath.Join(fixture.links, "resized.mkv")
	writeAgedFile(t, resizedCopy, copyContent, agedModTime)
	fixture.recordSized("reflink", resizedCopy, fixture.libraryFile("resized.mkv"), int64(len(copyContent))+1)
	sameSizeCopy := filepath.Join(fixture.links, "same-size.mkv")
	writeAgedFile(t, sameSizeCopy, copyContent, agedModTime)
	fixture.recordSized("reflink", sameSizeCopy, fixture.libraryFile("same-size.mkv"), int64(len(copyContent)))
	unsizedCopy := filepath.Join(fixture.links, "unsized.mkv")
	writeAgedFile(t, unsizedCopy, copyContent, agedModTime)
	fixture.recordSized("reflink", unsizedCopy, fixture.libraryFile("unsized.mkv"), 0)
	statesBefore := fixture.storedStates()
	treeBefore := snapshotTree(t, fixture.root)

	page, err := fixture.service().ListLinks(context.Background(), LinkFilter{})

	expectedValue := map[string]string{
		okSymlink: LinkStateOK, okHardlink: LinkStateOK, diverged: LinkStateDiverged, broken: LinkStateLost,
		missing: LinkStateLost, unreadable: LinkStateUnreadable, replacedSymlink: LinkStateDiverged,
		resizedCopy: LinkStateDiverged, sameSizeCopy: LinkStateOK, unsizedCopy: LinkStateOK,
	}
	gotValue := statesByPath(page)
	if err != nil || len(gotValue) != len(expectedValue) || page.Total != len(expectedValue) || page.Truncated || page.Next != 0 {
		t.Fatalf("gotValue %+v %v, expectedValue every link once, complete", page, err)
	}
	for linkPath, expectedState := range expectedValue {
		if gotValue[linkPath] != expectedState {
			t.Fatalf("%s: gotValue %q, expectedValue %q", linkPath, gotValue[linkPath], expectedState)
		}
	}
	expectedCounts := map[string]int{LinkStateOK: 4, LinkStateDiverged: 3, LinkStateLost: 2, LinkStateUnreadable: 1}
	for state, expectedCount := range expectedCounts {
		if page.Counts[state] != expectedCount {
			t.Fatalf("counts: gotValue %v, expectedValue %v", page.Counts, expectedCounts)
		}
	}
	for _, item := range page.Items {
		hasReason := item.Reason != ""
		comparedOK := item.State == LinkStateOK && item.Compared
		if item.RecordedAt != fixedNow || item.Kind == "" || hasReason == comparedOK || item.Compared == (item.LinkPath == unsizedCopy) {
			t.Fatalf("gotValue %+v, expectedValue the recorded fields, a reason unless compared ok, compared false only when unsized", item)
		}
	}
	assertTreeUnchanged(t, treeBefore, snapshotTree(t, fixture.root))
	statesAfter := fixture.storedStates()
	for linkPath, stateBefore := range statesBefore {
		if statesAfter[linkPath] != stateBefore {
			t.Fatalf("%s: gotValue stored state %q, expectedValue %q unchanged", linkPath, statesAfter[linkPath], stateBefore)
		}
	}

	filtered, err := fixture.service().ListLinks(context.Background(), LinkFilter{State: LinkStateLost})
	if err != nil || filtered.Total != 2 || len(filtered.Items) != 2 || filtered.Counts[LinkStateOK] != 4 {
		t.Fatalf("gotValue %+v %v, expectedValue the two lost links and the full counts", filtered, err)
	}
	_, err = fixture.service().ListLinks(context.Background(), LinkFilter{State: "broken"})
	if !errors.Is(err, config.ErrUsage) {
		t.Fatalf("gotValue %v, expectedValue a usage error for an unknown state", err)
	}
}

// stalledAfter rend ok pour les count premiers liens, puis bloque comme un Stat sur un montage figé (jusqu'à release).
func stalledAfter(count int, release <-chan struct{}) func(store.Link) repair.Inspection {
	inspected := 0
	return func(store.Link) repair.Inspection {
		inspected++
		if inspected > count {
			<-release
		}
		return repair.Inspection{Condition: repair.ConditionOK}
	}
}

func (fixture *linksFixture) recordMany(count int) {
	fixture.t.Helper()
	for index := range count {
		fixture.record("symlink", filepath.Join(fixture.links, fmt.Sprintf("link-%04d", index)), "/home/u/library/x")
	}
}

func TestListLinks_boundsResultsAndReportsTruncation(t *testing.T) {
	fixture := newLinksFixture(t)
	recorded := MaxLinkLimit + 1
	fixture.recordMany(recorded)
	listing := fixture.service()
	listing.inspect = func(store.Link) repair.Inspection { return repair.Inspection{Condition: repair.ConditionOK} }

	first, err := listing.ListLinks(context.Background(), LinkFilter{Limit: 12})
	last, lastErr := listing.ListLinks(context.Background(), LinkFilter{Limit: 12, Offset: recorded - 5})
	byDefault, defaultErr := listing.ListLinks(context.Background(), LinkFilter{})
	clamped, clampErr := listing.ListLinks(context.Background(), LinkFilter{Limit: MaxLinkLimit + 500})

	if err != nil || len(first.Items) != 12 || first.Total != recorded || first.Next != 12 || first.Truncated {
		t.Fatalf("first page: gotValue %d items, total %d, next %d, %v, expectedValue 12 of %d, next 12", len(first.Items), first.Total, first.Next, err, recorded)
	}
	if lastErr != nil || len(last.Items) != 5 || last.Next != 0 {
		t.Fatalf("last page: gotValue %d items, next %d, %v, expectedValue the 5 remaining links and no next", len(last.Items), last.Next, lastErr)
	}
	if defaultErr != nil || len(byDefault.Items) != DefaultLinkLimit || byDefault.Next != DefaultLinkLimit {
		t.Fatalf("default: gotValue %d items, next %d, %v, expectedValue %d", len(byDefault.Items), byDefault.Next, defaultErr, DefaultLinkLimit)
	}
	if clampErr != nil || len(clamped.Items) != MaxLinkLimit || clamped.Next != MaxLinkLimit {
		t.Fatalf("clamped: gotValue %d items, next %d, %v, expectedValue at most %d", len(clamped.Items), clamped.Next, clampErr, MaxLinkLimit)
	}
	if _, err := listing.ListLinks(context.Background(), LinkFilter{Offset: -1}); !errors.Is(err, config.ErrUsage) {
		t.Fatalf("gotValue %v, expectedValue a usage error for a negative offset", err)
	}

	release := make(chan struct{})
	defer close(release)
	listing.inspect = stalledAfter(5, release)
	listing.inspectionBudget = 50 * time.Millisecond
	started := time.Now()
	partial, err := listing.ListLinks(context.Background(), LinkFilter{})

	if err != nil || !partial.Truncated || len(partial.Items) != 5 || partial.Total != 5 || time.Since(started) > 5*time.Second {
		t.Fatalf("gotValue %+v %v after %v, expectedValue the 5 links inspected in time, flagged truncated", partial, err, time.Since(started))
	}
}

func TestListLinks_honoursContextCancellation(t *testing.T) {
	fixture := newLinksFixture(t)
	fixture.recordMany(3)
	listing := fixture.service()
	release := make(chan struct{})
	defer close(release)
	listing.inspect = stalledAfter(0, release)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)

	_, err := listing.ListLinks(ctx, LinkFilter{})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("gotValue %v, expectedValue context.Canceled while a link stalls", err)
	}
}

// eventuallyAvailable relance ListLinks jusqu'à ce que la garde soit rendue (la goroutine bloquée a vraiment fini).
func eventuallyAvailable(t *testing.T, listing *service) LinkPage {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		page, err := listing.ListLinks(context.Background(), LinkFilter{})
		if !errors.Is(err, ErrInspectionBusy) {
			if err != nil {
				t.Fatal(err)
			}
			return page
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("gotValue ErrInspectionBusy for 5 s, expectedValue the guard released once the inspection returned")
	return LinkPage{}
}

func TestListLinks_allowsOneInspectionAtATimeUntilTheBlockedOneReturns(t *testing.T) {
	fixture := newLinksFixture(t)
	fixture.recordMany(3)
	listing := fixture.service()
	release := make(chan struct{})
	listing.inspect = stalledAfter(1, release)
	listing.inspectionBudget = 30 * time.Millisecond

	first, firstErr := listing.ListLinks(context.Background(), LinkFilter{})
	_, secondErr := listing.ListLinks(context.Background(), LinkFilter{})
	code, _ := Classify(secondErr)

	if firstErr != nil || !first.Truncated || len(first.Items) != 1 {
		t.Fatalf("first: gotValue %+v %v, expectedValue one link and truncated", first, firstErr)
	}
	if !errors.Is(secondErr, ErrInspectionBusy) || code != ErrorCodeInspectionBusy {
		t.Fatalf("second: gotValue %v (%s), expectedValue ErrInspectionBusy while the first inspection is blocked", secondErr, code)
	}
	close(release)
	if page := eventuallyAvailable(t, listing); page.Truncated || len(page.Items) != 3 {
		t.Fatalf("after release: gotValue %+v, expectedValue the full list", page)
	}
}

func TestListLinks_keepsTheGuardAfterACancelledCallUntilTheInspectionReturns(t *testing.T) {
	fixture := newLinksFixture(t)
	fixture.recordMany(2)
	listing := fixture.service()
	release := make(chan struct{})
	listing.inspect = stalledAfter(0, release)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)

	_, cancelledErr := listing.ListLinks(ctx, LinkFilter{})
	_, busyErr := listing.ListLinks(context.Background(), LinkFilter{})

	if !errors.Is(cancelledErr, context.Canceled) || !errors.Is(busyErr, ErrInspectionBusy) {
		t.Fatalf("gotValue %v then %v, expectedValue context.Canceled then ErrInspectionBusy", cancelledErr, busyErr)
	}
	close(release)
	listing.inspect = func(store.Link) repair.Inspection { return repair.Inspection{Condition: repair.ConditionOK} }
	eventuallyAvailable(t, listing)
}
