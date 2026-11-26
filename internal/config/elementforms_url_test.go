package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const (
	dummyProxyKey  = "dummy-proxy-key-0000"
	dummyAPIKey    = "dummy-api-key-0000"
	dummyPassword  = "dummy-pass-0000"
	placeholderURL = "/proxy/{proxyKey}/"
)

func TestElementFromRaw_splitsTheProxyKeyAndTheCredentialsOutOfTheURL(t *testing.T) {
	scenarios := []struct {
		list, raw      string
		valeurAttendue Element
	}{
		{
			"radarr", "http://alice:" + dummyPassword + "@radarr.example.com:7476/proxy/" + dummyProxyKey + "/radarr?apikey=" + dummyAPIKey,
			Element{
				"url": "http://radarr.example.com:7476/proxy/{proxyKey}/radarr", "apiKey": dummyAPIKey,
				"user": "alice", "password": dummyPassword, "proxyKey": dummyProxyKey,
			},
		},
		{
			"torrentClients", "qbittorrent:http://alice:" + dummyPassword + "@qbit.example.com:8080/proxy/" + dummyProxyKey + "/qbit",
			Element{
				"type": "qbittorrent", "readOnly": false, "url": "http://qbit.example.com:8080/proxy/{proxyKey}/qbit",
				"user": "alice", "password": dummyPassword, "proxyKey": dummyProxyKey,
			},
		},
	}
	for _, scenario := range scenarios {
		valeurObtenue, err := ElementFromRaw(scenario.list, scenario.raw)

		if err != nil || !reflect.DeepEqual(valeurObtenue, scenario.valeurAttendue) {
			t.Fatalf("%s: valeurObtenue %v (%v), valeurAttendue %v", scenario.list, valeurObtenue, err, scenario.valeurAttendue)
		}
		joined, err := RawFromElement(scenario.list, valeurObtenue)
		if err != nil || joined != scenario.raw {
			t.Fatalf("%s: valeurObtenue %v (%v), valeurAttendue la forme brute d'origine", scenario.list, joined, err)
		}
	}
}

func TestRawFromElement_escapesAProxyKeyInThePath(t *testing.T) {
	element := Element{"url": "http://radarr.example.com" + placeholderURL + "radarr", "apiKey": dummyAPIKey, "proxyKey": "a/b c"}
	valeurAttendue := "http://radarr.example.com/proxy/a%2Fb%20c/radarr?apikey=" + dummyAPIKey

	valeurObtenue, err := RawFromElement("radarr", element)

	if err != nil || valeurObtenue != valeurAttendue {
		t.Fatalf("valeurObtenue %v (%v), valeurAttendue %q", valeurObtenue, err, valeurAttendue)
	}
	splitBack, err := ElementFromRaw("radarr", valeurAttendue)
	if err != nil || splitBack["proxyKey"] != "a/b c" {
		t.Fatalf("valeurObtenue %v (%v), valeurAttendue la clé décodée", splitBack, err)
	}
}

func TestRawFromElement_refusesAPlaceholderWithoutProxyKey(t *testing.T) {
	element := Element{"url": "http://radarr.example.com" + placeholderURL + "radarr", "apiKey": dummyAPIKey}

	_, err := RawFromElement("radarr", element)

	if !errors.Is(err, ErrInvalidElement) || strings.Contains(err.Error(), "radarr.example.com") {
		t.Fatalf("valeurObtenue %v, valeurAttendue un élément invalide sans l'url", err)
	}
}

func TestRawFromElement_joinsTheTorznabAPIKeyIntoTheURL(t *testing.T) {
	element := Element{"name": "idx", "url": "https://indexer.example.com/api/v1/12", "apiKey": dummyAPIKey}
	valeurAttendue := "https://indexer.example.com/api/v1/12?apikey=" + dummyAPIKey

	raw, err := RawFromElement("torznab", element)
	if err != nil {
		t.Fatal(err)
	}
	indexers, err := parseTorznab([]json.RawMessage{raw.(json.RawMessage)})

	if err != nil || len(indexers) != 1 || indexers[0].URL != valeurAttendue {
		t.Fatalf("valeurObtenue %+v (%v), valeurAttendue l'url %q", indexers, err, valeurAttendue)
	}
}

func TestRejectSecretInURL_refusesASecretWrittenInTheURL(t *testing.T) {
	refusedURLs := map[string]string{
		"radarr":         "http://alice:" + dummyPassword + "@radarr.example.com:7878",
		"sonarr":         "http://sonarr.example.com:8989?apikey=" + dummyAPIKey,
		"torrentClients": "http://qbit.example.com:8080/proxy/" + dummyProxyKey + "/qbit",
		"torznab":        "https://indexer.example.com/api?APIKEY=" + dummyAPIKey,
	}
	for list, refusedURL := range refusedURLs {
		err := RejectSecretInURL(list, map[string]any{"url": refusedURL})

		if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), list+".url") {
			t.Errorf("%s: valeurObtenue %v, valeurAttendue un refus d'usage nommant %s.url", list, err, list)
			continue
		}
		if strings.Contains(err.Error(), "example.com") || strings.Contains(err.Error(), "dummy") {
			t.Errorf("%s: valeurObtenue %q, valeurAttendue un message sans l'url", list, err)
		}
	}
}

func TestRejectSecretInURL_acceptsAPlainURLAndThePlaceholder(t *testing.T) {
	acceptedURLs := []string{"http://radarr.example.com:7878", "http://radarr.example.com" + placeholderURL + "radarr"}
	for _, acceptedURL := range acceptedURLs {
		if err := RejectSecretInURL("radarr", map[string]any{"url": acceptedURL}); err != nil {
			t.Errorf("valeurObtenue %v, valeurAttendue %q accepté", err, acceptedURL)
		}
	}
}

func TestSecretFields_keepTheURLInClear(t *testing.T) {
	valeurAttendue := map[string][]string{
		"radarr": {"apiKey", "password", "proxyKey"}, "sonarr": {"apiKey", "password", "proxyKey"},
		"torrentClients": {"password", "proxyKey"}, "torznab": {"apiKey"},
	}
	for list, expected := range valeurAttendue {
		if valeurObtenue := SecretFields(list); !reflect.DeepEqual(valeurObtenue, expected) {
			t.Errorf("%s: valeurObtenue %v, valeurAttendue %v", list, valeurObtenue, expected)
		}
	}
}

func TestElementFromRaw_splitsTheTorznabAPIKeyOutOfTheURL(t *testing.T) {
	valeurAttendue := Element{"url": "https://indexer.example.com/api/v1/12", "apiKey": dummyAPIKey}

	valeurObtenue, err := ElementFromRaw("torznab", "https://indexer.example.com/api/v1/12?apikey="+dummyAPIKey)

	if err != nil || !reflect.DeepEqual(valeurObtenue, valeurAttendue) {
		t.Fatalf("valeurObtenue %v (%v), valeurAttendue %v", valeurObtenue, err, valeurAttendue)
	}
}

func TestConfig_neverRendersAProxyKeyNorATorznabKeyStoredApart(t *testing.T) {
	const proxyKey, indexerKey = "dummy-proxy-key-1111", "dummy-indexer-key-1111"
	content := `{"radarr": [{"url": "http://radarr.example.com:7476/proxy/{proxyKey}/radarr", "apiKey": "` + dummyAPIKey +
		`", "proxyKey": "` + proxyKey + `"}], "torznab": [{"name": "idx", "url": "https://indexer.example.com/api/v1/12", "apiKey": "` +
		indexerKey + `"}]}`
	loadedConfig, err := loadStored(t, nil, map[string]string{envSecretKey: testSecretKey}, content)
	if err != nil {
		t.Fatal(err)
	}
	redactedJSON, err := loadedConfig.RedactedJSON()
	if err != nil {
		t.Fatal(err)
	}
	leakingText := "radarr at " + loadedConfig.Radarr[0].BaseURL + " and indexer " + loadedConfig.Torznab[0].URL
	renderings := []string{
		fmt.Sprintf("%v %+v %#v %s", loadedConfig, loadedConfig, loadedConfig, loadedConfig), string(redactedJSON),
		RedactText(leakingText, loadedConfig.KnownSecrets()...),
	}

	for _, valeurObtenue := range renderings {
		if strings.Contains(valeurObtenue, proxyKey) || strings.Contains(valeurObtenue, indexerKey) {
			t.Fatalf("valeurObtenue %q, valeurAttendue ni clé de proxy ni clé d'indexeur", valeurObtenue)
		}
	}
	if !strings.Contains(leakingText, proxyKey) || !strings.Contains(leakingText, indexerKey) {
		t.Fatalf("valeurObtenue %q, valeurAttendue les clés rejointes dans la configuration chargée", leakingText)
	}
}
