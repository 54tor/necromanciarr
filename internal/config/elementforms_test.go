package config

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func noVariables(string) (string, bool) { return "", false }

func TestElementFromRaw_splitsTheAPIKeyOutOfTheURL(t *testing.T) {
	valeurAttendue := Element{"url": "http://h:7878", "apiKey": "dummy-key"}

	valeurObtenue, err := ElementFromRaw("radarr", "http://h:7878?apikey=dummy-key")

	if err != nil || !reflect.DeepEqual(valeurObtenue, valeurAttendue) {
		t.Fatalf("attendu %v, obtenu %v (%v)", valeurAttendue, valeurObtenue, err)
	}
}

func TestElementFromRaw_splitsClientAndLinkDir(t *testing.T) {
	clientAttendu := Element{"type": "qbittorrent", "readOnly": true, "url": "http://h:8080", "user": "alice", "password": "pw"}
	clientObtenu, err := ElementFromRaw("torrentClients", "qbittorrent:readonly:http://alice:pw@h:8080")
	if err != nil || !reflect.DeepEqual(clientObtenu, clientAttendu) {
		t.Fatalf("attendu %v, obtenu %v (%v)", clientAttendu, clientObtenu, err)
	}
	dossierObtenu, _ := ElementFromRaw("linkDirs", "/data/links")
	if !reflect.DeepEqual(dossierObtenu, Element{"path": "/data/links"}) {
		t.Fatalf("obtenu %v", dossierObtenu)
	}
}

func TestRawFromElement_roundTripsRadarrSonarrAndClients(t *testing.T) {
	cas := []struct{ liste, brut string }{
		{"radarr", "http://h:7878?apikey=dummy-key"},
		{"sonarr", "https://alice:p%40ss%3Aw@h:8989/proxy?apikey=k%26x"},
		{"torrentClients", "qbittorrent:http://alice:p%40ss%3Aw@h:8080"},
		{"torrentClients", "qbittorrent:readonly:http://h:8080"},
		{"linkDirs", "/data/links"},
	}
	for _, cas := range cas {
		element, err := ElementFromRaw(cas.liste, cas.brut)
		if err != nil {
			t.Fatalf("%s: %v", cas.liste, err)
		}
		valeurObtenue, err := RawFromElement(cas.liste, element)
		if err != nil || valeurObtenue != cas.brut {
			t.Fatalf("%s: attendu %q, obtenu %v (%v)", cas.liste, cas.brut, valeurObtenue, err)
		}
	}
}

// Forme trackers : {"key", "announce": [...], "aliases": [...]} ; RawFromElement rend l'objet à une clé {key: {announce, aliases}}.
func TestRawFromElement_feedsTheExistingParsers(t *testing.T) {
	brutTracker, err := RawFromElement("trackers", Element{"key": "tracker-a", "announce": []any{"https://t.example.com/announce/abc"}, "aliases": []any{"ta"}})
	if err != nil {
		t.Fatal(err)
	}
	brutAutre, _ := RawFromElement("trackers", Element{"key": "tracker-b", "announce": []any{"https://u.example.com/announce/def"}})
	section, err := TrackersSection([]json.RawMessage{brutTracker.(json.RawMessage), brutAutre.(json.RawMessage)})
	if err != nil {
		t.Fatal(err)
	}
	trackers, err := parseTrackers(section, noVariables)
	if err != nil || len(trackers) != 2 || trackers[0].Key != "tracker-a" || trackers[0].Aliases[0] != "ta" {
		t.Fatalf("trackers obtenus %+v (%v)", trackers, err)
	}

	brutTorznab, _ := RawFromElement("torznab", Element{"url": "https://idx.example.com/api?apikey=dummy", "name": "idx"})
	indexeurs, err := parseTorznab([]json.RawMessage{brutTorznab.(json.RawMessage)}, noVariables)
	if err != nil || len(indexeurs) != 1 || indexeurs[0].Name != "idx" {
		t.Fatalf("torznab obtenu %+v (%v)", indexeurs, err)
	}

	brutFournisseur, _ := RawFromElement("fsProviders", Element{"type": "rclone", "name": "remote", "rc": "http://h:5572", "mounts": []any{"/mnt/a=remote:a"}})
	fournisseurs, err := parseFSProviders([]json.RawMessage{brutFournisseur.(json.RawMessage)}, noVariables)
	if err != nil || len(fournisseurs) != 1 || fournisseurs[0].Name != "remote" {
		t.Fatalf("fsProviders obtenu %+v (%v)", fournisseurs, err)
	}

	brutArr, _ := RawFromElement("radarr", Element{"url": "http://h:7878", "apiKey": "dummy-key"})
	services, err := parseArrServices("radarr", []string{brutArr.(string)})
	if err != nil || services[0].APIKey != "dummy-key" || services[0].BaseURL != "http://h:7878" {
		t.Fatalf("radarr obtenu %+v (%v)", services, err)
	}

	brutClient, _ := RawFromElement("torrentClients", Element{"type": "qbittorrent", "url": "http://h:8080", "user": "alice", "password": "pw"})
	clients, err := parseTorrentClients([]string{brutClient.(string)})
	if err != nil || clients[0].User != "alice" || clients[0].Password != "pw" {
		t.Fatalf("clients obtenus %+v (%v)", clients, err)
	}
}

func TestElementFromRaw_errorNeverContainsTheRawValue(t *testing.T) {
	const secret = "dummy-secret-value"
	cas := []struct{ liste, brut string }{
		{"radarr", "http://h:7878?apikey=" + secret + "&other=1"},
		{"sonarr", "ftp://h?apikey=" + secret},
		{"torrentClients", "qbittorrent:http://alice:" + secret + "@h:8080?x=1"},
		{"torrentClients", "qbittorrent:http://alice:" + secret + "/x@h:8080"},
	}
	for _, cas := range cas {
		_, err := ElementFromRaw(cas.liste, cas.brut)
		if !errors.Is(err, ErrInvalidElement) || strings.Contains(err.Error(), secret) {
			t.Fatalf("%s: erreur obtenue %v", cas.liste, err)
		}
	}
}
