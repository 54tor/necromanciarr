package qbit_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// bulkyNameLength rend chaque entrée de torrents/info assez lourde pour dépasser l'ancien plafond de 8 Mio.
const bulkyNameLength = 2048

// addForeignTorrents ajoute count torrents étrangers, hors du dossier du film, au nom volumineux.
func addForeignTorrents(fake *fakeQbit, count int) {
	bulkyName := strings.Repeat("n", bulkyNameLength)
	for torrentIndex := range count {
		hash := fmt.Sprintf("%040x", torrentIndex+1)
		fake.torrents[hash] = &fakeTorrent{
			Name: bulkyName, SavePath: "/home/u/other", ContentPath: "/home/u/other/" + bulkyName, State: "uploading", Progress: 1,
		}
	}
}

func TestFindTorrent_asksOnlyForTheWantedHashInALargeClient(t *testing.T) {
	fake := newFakeQbit(webAPIVersion4)
	addForeignTorrents(fake, 6000)
	fake.torrents[hashMovie] = movieTorrent(1)
	server := fake.start(t)
	client := newTestClient(t, server.URL, "", "")

	found, present, err := client.FindTorrent(context.Background(), hashMovie)

	if err != nil || !present || found.Hash != hashMovie {
		t.Fatalf("gotValue %+v %v %v, expectedValue the movie torrent", found, present, err)
	}
	if _, present, err := client.FindTorrent(context.Background(), hashPack); err != nil || present {
		t.Fatalf("absent: gotValue %v %v, expectedValue not present without error", present, err)
	}
}

func TestRemoveKeepingData_neverAsksToDeleteFiles(t *testing.T) {
	fake := newFakeQbit(webAPIVersion5)
	fake.torrents[hashMovie] = movieTorrent(1)
	server := fake.start(t)
	client := newTestClient(t, server.URL, "", "")

	if err := client.RemoveKeepingData(context.Background(), hashMovie); err != nil {
		t.Fatal(err)
	}

	if expectedAction := "delete:" + hashMovie + ":deleteFiles=false"; !slices.Contains(fake.snapshotActions(), expectedAction) {
		t.Fatalf("gotValue %v, expectedValue %s", fake.snapshotActions(), expectedAction)
	}
}
