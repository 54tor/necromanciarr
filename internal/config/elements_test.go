package config

import (
	"encoding/json"
	"testing"

	"necromanciarr/internal/store"
)

func radarrElement(adresse string) Element { return Element{"url": adresse, "apiKey": "dummy"} }

func overrideValue(t *testing.T, element Element) []byte {
	t.Helper()
	encoded, err := json.Marshal(element)
	if err != nil {
		t.Fatalf("encode element: %v", err)
	}
	return encoded
}

func TestElementID_dropsCredentialsPathAndQuery(t *testing.T) {
	valeurAttendue := "http://example.com:7878"
	valeurObtenue := ElementID("radarr", radarrElement("http://alice:pw@example.com:7878/x?apikey=k"))
	if valeurObtenue != valeurAttendue {
		t.Fatalf("valeurAttendue %q, valeurObtenue %q", valeurAttendue, valeurObtenue)
	}
}

func TestAssignIDs_suffixesCollisionsInUpstreamOrder(t *testing.T) {
	elements := []Element{radarrElement("http://example.com:1/a"), radarrElement("http://example.com:1/b"), radarrElement("http://example.com:1/c")}
	valeurAttendue := []string{"http://example.com:1", "http://example.com:1#2", "http://example.com:1#3"}
	valeurObtenue := AssignIDs("radarr", elements)
	for position := range valeurAttendue {
		if valeurObtenue[position] != valeurAttendue[position] {
			t.Fatalf("valeurAttendue %v, valeurObtenue %v", valeurAttendue, valeurObtenue)
		}
	}
}

func TestResolveList_keepsUpstreamOrderThenAdditions(t *testing.T) {
	upstream := []Element{{"path": "/b"}, {"path": "/a"}}
	overrides := []store.ConfigOverride{
		{List: "linkDirs", ItemID: "/z", Value: overrideValue(t, Element{"path": "/z"})},
		{List: "linkDirs", ItemID: "/c", Value: overrideValue(t, Element{"path": "/c"})},
		{List: "other", ItemID: "/x", Value: overrideValue(t, Element{"path": "/x"})},
	}
	valeurAttendue := []string{"/b", "/a", "/c", "/z"}
	resolved, orphans := ResolveList("linkDirs", upstream, overrides)
	if len(resolved) != len(valeurAttendue) || len(orphans) != 0 {
		t.Fatalf("valeurAttendue %v, valeurObtenue %v (orphelins %v)", valeurAttendue, resolved, orphans)
	}
	for position, identifiantAttendu := range valeurAttendue {
		if resolved[position].ItemID != identifiantAttendu {
			t.Fatalf("valeurAttendue %v, valeurObtenue %v", valeurAttendue, resolved)
		}
	}
	if resolved[2].Origin != OriginAdded || resolved[0].Origin != OriginCrossSeed {
		t.Fatalf("origines inattendues: %v", resolved)
	}
}

func TestResolveList_overrideReplacesTheWholeElementAndKeepsItsID(t *testing.T) {
	upstream := []Element{radarrElement("http://example.com:7878")}
	remplacement := radarrElement("http://other.example.com:9")
	overrides := []store.ConfigOverride{{List: "radarr", ItemID: "http://example.com:7878", Value: overrideValue(t, remplacement)}}
	resolved, _ := ResolveList("radarr", upstream, overrides)
	valeurObtenue := resolved[0]
	if valeurObtenue.ItemID != "http://example.com:7878" || valeurObtenue.Element["url"] != "http://other.example.com:9" || valeurObtenue.Origin != OriginOverride {
		t.Fatalf("valeurObtenue %+v", valeurObtenue)
	}
}

func TestResolveList_deletedMarkerHidesAnUpstreamElement(t *testing.T) {
	upstream := []Element{{"path": "/a"}, {"path": "/b"}}
	overrides := []store.ConfigOverride{{List: "linkDirs", ItemID: "/a", Deleted: true}}
	resolved, orphans := ResolveList("linkDirs", upstream, overrides)
	if len(resolved) != 1 || resolved[0].ItemID != "/b" || len(orphans) != 0 {
		t.Fatalf("valeurObtenue %+v orphelins %v", resolved, orphans)
	}
}

func TestResolveList_reportsOrphanWhenUpstreamElementDisappeared(t *testing.T) {
	overrides := []store.ConfigOverride{
		{List: "linkDirs", ItemID: "/gone", Value: overrideValue(t, Element{"path": "/gone"}), UpstreamFingerprint: "abc"},
		{List: "linkDirs", ItemID: "/gone2", Deleted: true, UpstreamFingerprint: "abc"},
	}
	resolved, orphans := ResolveList("linkDirs", nil, overrides)
	valeurAttendue := []OrphanOverride{{"linkDirs", "/gone"}, {"linkDirs", "/gone2"}}
	if len(resolved) != 0 || len(orphans) != 2 || orphans[0] != valeurAttendue[0] || orphans[1] != valeurAttendue[1] {
		t.Fatalf("valeurAttendue %v, valeurObtenue %v / %v", valeurAttendue, orphans, resolved)
	}
}

func TestResolveList_reportsDriftWhenUpstreamValueChanged(t *testing.T) {
	ancien := Element{"path": "/a", "mode": "old"}
	courant := Element{"path": "/a", "mode": "new"}
	overrides := []store.ConfigOverride{{List: "linkDirs", ItemID: "/a", Value: overrideValue(t, ancien), UpstreamFingerprint: Fingerprint(ancien)}}
	resolved, _ := ResolveList("linkDirs", []Element{courant}, overrides)
	if !resolved[0].Drift {
		t.Fatalf("valeurAttendue drift vrai, valeurObtenue %+v", resolved[0])
	}
	resolvedSansDerive, _ := ResolveList("linkDirs", []Element{ancien}, overrides)
	if resolvedSansDerive[0].Drift {
		t.Fatalf("valeurAttendue drift faux, valeurObtenue %+v", resolvedSansDerive[0])
	}
}

func TestResolveList_followsItemIDWhenUpstreamReorders(t *testing.T) {
	overrides := []store.ConfigOverride{{List: "linkDirs", ItemID: "/b", Deleted: true}}
	resolved, _ := ResolveList("linkDirs", []Element{{"path": "/b"}, {"path": "/a"}}, overrides)
	resolvedReordered, _ := ResolveList("linkDirs", []Element{{"path": "/a"}, {"path": "/b"}}, overrides)
	if len(resolved) != 1 || resolved[0].ItemID != "/a" || len(resolvedReordered) != 1 || resolvedReordered[0].ItemID != "/a" {
		t.Fatalf("valeurObtenue %+v / %+v", resolved, resolvedReordered)
	}
}

func TestResolveList_reportsUnreadableOverrideInsteadOfDroppingUpstream(t *testing.T) {
	overrides := []store.ConfigOverride{{List: "linkDirs", ItemID: "/a", Value: []byte("not json")}}
	resolved, orphans := ResolveList("linkDirs", []Element{{"path": "/a"}}, overrides)
	orphelinAttendu := OrphanOverride{List: "linkDirs", ItemID: "/a"}
	if len(resolved) != 1 || resolved[0].ItemID != "/a" || resolved[0].Origin != OriginCrossSeed {
		t.Fatalf("valeurAttendue élément amont conservé, valeurObtenue %+v", resolved)
	}
	if len(orphans) != 1 || orphans[0] != orphelinAttendu {
		t.Fatalf("valeurAttendue %v, valeurObtenue %v", orphelinAttendu, orphans)
	}
}
