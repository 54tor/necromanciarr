package qbit_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"necromanciarr/internal/qbit"
)

// addSmallForeignTorrents ajoute count torrents étrangers légers, hors du dossier du film.
func addSmallForeignTorrents(fake *fakeQbit, count int) {
	for torrentIndex := range count {
		hash := fmt.Sprintf("%040x", torrentIndex+1)
		fake.torrents[hash] = &fakeTorrent{Name: "other", SavePath: "/home/u/other", State: "uploading", Progress: 1}
	}
}

// countListings ne compte que les pages de la liste complète (les relectures par hash de la garde sont exclues).
func countListings(queries []string) int {
	listings := 0
	for _, query := range queries {
		if !strings.Contains(query, "hashes=") {
			listings++
		}
	}
	return listings
}

func TestTorrentsCovering_readsSixtyThousandTorrentsPageByPage(t *testing.T) {
	const foreignCount = 60_000
	fake := newFakeQbit(webAPIVersion4)
	addSmallForeignTorrents(fake, foreignCount)
	fake.torrents[hashMovie] = movieTorrent(1)
	server := fake.start(t)
	client := newTestClient(t, server.URL, "", "")

	covering, err := client.TorrentsCovering(context.Background(), moviePath)

	if err != nil || !slices.Equal(covering, []string{hashMovie}) {
		t.Fatalf("gotValue %v %v, expectedValue the movie torrent found", covering, err)
	}
	// Les pages se chevauchent d'un torrent : chacune après la première avance de TorrentPageSize-1.
	expectedPages := foreignCount/(qbit.TorrentPageSize-1) + 1
	if fake.maxServed > qbit.TorrentPageSize || len(fake.infoQueries) != expectedPages {
		t.Fatalf("gotValue %d entries per response at most over %d pages, expectedValue at most %d per page over %d pages",
			fake.maxServed, len(fake.infoQueries), qbit.TorrentPageSize, expectedPages)
	}
	if !strings.Contains(fake.infoQueries[1], "sort=hash") || !strings.Contains(fake.infoQueries[1], "offset=499") {
		t.Fatalf("gotValue %q, expectedValue a stable sort by hash and an offset", fake.infoQueries[1])
	}
}

func TestRecheckPath_stopsListingOnAShortPage(t *testing.T) {
	fake := newFakeQbit(webAPIVersion4)
	addSmallForeignTorrents(fake, 1199)
	fake.torrents[hashMovie] = movieTorrent(1)
	server := fake.start(t)
	client := newTestClient(t, server.URL, "", "")

	if err := client.RecheckPath(context.Background(), moviePath); err != nil {
		t.Fatalf("RecheckPath: %v", err)
	}
	if listingCalls := countListings(fake.infoQueries); listingCalls != 3 {
		t.Fatalf("gotValue %d listing pages, expectedValue 3 (offsets 0, 499, 998)", listingCalls)
	}
	if gotState, _ := fake.torrentState(hashMovie); gotState != "uploading" {
		t.Fatalf("gotValue %s, expectedValue the movie rechecked and resumed", gotState)
	}
}

func TestTorrentsCovering_refusesAnOversizedPageWithATypedError(t *testing.T) {
	fake := newFakeQbit(webAPIVersion4)
	addSmallForeignTorrents(fake, 600)
	bulkyName := strings.Repeat("n", 20<<10)
	for _, torrent := range fake.torrents {
		torrent.Name = bulkyName
	}
	server := fake.start(t)
	client := newTestClient(t, server.URL, "", "")

	_, err := client.TorrentsCovering(context.Background(), moviePath)

	if !errors.Is(err, qbit.ErrResponseTooLarge) {
		t.Fatalf("gotValue %v, expectedValue ErrResponseTooLarge for a page over 8 MiB", err)
	}
}

func TestTorrentsCovering_stopsPagingWhenTheContextIsCancelled(t *testing.T) {
	fake := newFakeQbit(webAPIVersion4)
	addSmallForeignTorrents(fake, 5000)
	ctx, cancel := context.WithCancel(context.Background())
	fake.onInfo = func(call int) {
		if call == 2 {
			cancel()
		}
	}
	server := fake.start(t)
	client := newTestClient(t, server.URL, "", "")

	_, err := client.TorrentsCovering(ctx, moviePath)

	if !errors.Is(err, context.Canceled) || len(fake.infoQueries) > 3 {
		t.Fatalf("gotValue %v after %d pages, expectedValue a cancelled read that stops paging", err, len(fake.infoQueries))
	}
}

func TestRecheckPath_pausesNothingWhenOnePageFails(t *testing.T) {
	fake := newFakeQbit(webAPIVersion4)
	addSmallForeignTorrents(fake, 1500)
	fake.torrents[hashMovie] = movieTorrent(1)
	fake.failInfoAt = 2
	server := fake.start(t)
	client := newTestClient(t, server.URL, "", "")

	covering, coveringErr := client.TorrentsCovering(context.Background(), moviePath)
	fake.update(func(locked *fakeQbit) { locked.infoQueries, locked.failInfoAt = nil, 2 })
	recheckErr := client.RecheckPath(context.Background(), moviePath)

	if coveringErr == nil || covering != nil || recheckErr == nil {
		t.Fatalf("gotValue %v %v / %v, expectedValue the whole listing refused", covering, coveringErr, recheckErr)
	}
	if paused := slices.ContainsFunc(fake.snapshotActions(), func(action string) bool { return strings.HasPrefix(action, "pause") }); paused {
		t.Fatalf("gotValue %v, expectedValue nothing paused", fake.snapshotActions())
	}
}

func TestTorrentsCovering_rereadsAListThatShiftsBetweenPages(t *testing.T) {
	fake := newFakeQbit(webAPIVersion4)
	addSmallForeignTorrents(fake, 1500)
	fake.torrents[hashMovie] = movieTorrent(1)
	fake.onInfo = func(call int) {
		if call == 2 {
			delete(fake.torrents, fmt.Sprintf("%040x", 1))
		}
	}
	server := fake.start(t)
	client := newTestClient(t, server.URL, "", "")

	covering, err := client.TorrentsCovering(context.Background(), moviePath)

	if err != nil || !slices.Equal(covering, []string{hashMovie}) || len(fake.infoQueries) <= 4 {
		t.Fatalf("gotValue %v %v after %d pages, expectedValue the shift detected and the list read again", covering, err, len(fake.infoQueries))
	}
}

func TestTorrentsCovering_givesUpOnAListThatNeverSettles(t *testing.T) {
	fake := newFakeQbit(webAPIVersion4)
	addSmallForeignTorrents(fake, 1500)
	removed := 0
	fake.onInfo = func(call int) {
		if call%2 == 0 {
			removed++
			delete(fake.torrents, fmt.Sprintf("%040x", removed))
		}
	}
	server := fake.start(t)
	client := newTestClient(t, server.URL, "", "")

	if _, err := client.TorrentsCovering(context.Background(), moviePath); !errors.Is(err, qbit.ErrListingUnstable) {
		t.Fatalf("gotValue %v, expectedValue ErrListingUnstable", err)
	}
}
