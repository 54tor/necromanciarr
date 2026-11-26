package config

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func expectRefusedWithoutURL(t *testing.T, label string, err error) {
	t.Helper()
	if !errors.Is(err, ErrUsage) {
		t.Errorf("%s: valeurObtenue %v, valeurAttendue un refus d'usage", label, err)
		return
	}
	if strings.Contains(err.Error(), "example.com") || strings.Contains(err.Error(), "dummy") {
		t.Errorf("%s: valeurObtenue %q, valeurAttendue un message sans l'url", label, err)
	}
}

func TestRejectSecretInURL_refusesAnyQueryAndAnUnparsableURL(t *testing.T) {
	refusedURLs := []string{
		"http://radarr.example.com:7878?a=1;apikey=dummy-key",
		"http://radarr.example.com:7878?token=dummy-token",
		"http://radarr.example.com:7878?apikey=dummy-key",
		"http://radarr.example.com:7878?ApiKey=dummy-key",
		"http://radarr.example.com:7878?apikey=dummy-a&apikey=dummy-b",
		"http://radarr.example.com:7878/%zz?dummy",
	}
	for _, refusedURL := range refusedURLs {
		expectRefusedWithoutURL(t, refusedURL, RejectSecretInURL("radarr", map[string]any{"url": refusedURL}))
	}
}

func TestRejectSecretInURL_acceptsThePlaceholderOnlyRightAfterTheProxySegment(t *testing.T) {
	refusedURLs := []string{
		"http://radarr.example.com:8080/k/{proxyKey}",
		"http://radarr.example.com:8080/{proxyKey}/proxy/",
		"http://radarr.example.com:8080/k/%7BproxyKey%7D",
		"http://radarr.example.com:8080/proxy/{proxyKey}/x/proxy/{proxyKey}",
		"http://radarr.example.com:8080/PROXY/dummy-key",
		"http://radarr.example.com:8080/proxy/{proxyKey}/x/proxy/dummy-key",
		"http://radarr.example.com:8080/proxy",
	}
	for _, refusedURL := range refusedURLs {
		expectRefusedWithoutURL(t, refusedURL, RejectSecretInURL("radarr", map[string]any{"url": refusedURL}))
	}
	acceptedURLs := []string{
		"http://radarr.example.com:8080/proxy/{proxyKey}/radarr",
		"http://radarr.example.com:8080/Proxy/%7BproxyKey%7D",
	}
	for _, acceptedURL := range acceptedURLs {
		if err := RejectSecretInURL("radarr", map[string]any{"url": acceptedURL}); err != nil {
			t.Errorf("%s: valeurObtenue %v, valeurAttendue accepté", acceptedURL, err)
		}
	}
}

func TestRawFromElement_refusesAProxyKeyWithoutPlaceholder(t *testing.T) {
	element := Element{"url": "http://radarr.example.com:7878", "apiKey": dummyAPIKey, "proxyKey": dummyProxyKey}
	valeurAttendue := "radarr.proxyKey is set but url has no {proxyKey} segment"

	_, err := RawFromElement("radarr", element)

	if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), valeurAttendue) || strings.Contains(err.Error(), dummyProxyKey) {
		t.Fatalf("valeurObtenue %v, valeurAttendue %q sans valeur", err, valeurAttendue)
	}
	element["proxyKey"] = ""
	if _, err := RawFromElement("radarr", element); err != nil {
		t.Fatalf("valeurObtenue %v, valeurAttendue une proxyKey vide tolérée", err)
	}
}

func TestElementFromRaw_splitsAnUpperCaseProxySegment(t *testing.T) {
	valeurAttendue := "http://qbit.example.com:8080/PROXY/{proxyKey}/qbit"

	element, err := ElementFromRaw("torrentClients", "qbittorrent:http://qbit.example.com:8080/PROXY/"+dummyProxyKey+"/qbit")

	if err != nil || element["url"] != valeurAttendue || element["proxyKey"] != dummyProxyKey {
		t.Fatalf("valeurObtenue %v (%v), valeurAttendue l'url %q et la clé à part", element, err, valeurAttendue)
	}
}

func TestRawFromElement_refusesASecondProxySegmentLeftByTheUpstreamSplit(t *testing.T) {
	element, err := ElementFromRaw("torrentClients", "qbittorrent:http://qbit.example.com:8080/proxy/dummy-a/x/proxy/dummy-b")
	if err != nil {
		t.Fatal(err)
	}

	_, err = RawFromElement("torrentClients", element)

	expectRefusedWithoutURL(t, "second proxy segment", err)
}

func TestRawFromElement_roundTripsAProxyKeyWithSpecialCharacters(t *testing.T) {
	valeurAttendue := "a/b c@d;e%f"
	element := Element{"url": "http://radarr.example.com/proxy/{proxyKey}/radarr", "apiKey": dummyAPIKey, "proxyKey": valeurAttendue}

	raw, err := RawFromElement("radarr", element)
	if err != nil {
		t.Fatal(err)
	}
	splitBack, err := ElementFromRaw("radarr", raw.(string))

	if valeurObtenue := splitBack["proxyKey"]; err != nil || valeurObtenue != valeurAttendue {
		t.Fatalf("valeurObtenue %v (%v), valeurAttendue %q", valeurObtenue, err, valeurAttendue)
	}
}

func TestLoadWith_namesAnOldFormatRowWithASealedValueInAClearField(t *testing.T) {
	content := `{"radarr": [{"url": "sealed:v1:dummy-old-format", "apiKey": "` + dummyAPIKey + `"}]}`
	valeurAttendue := "sealed value in a clear field: item from an earlier format, rewrite or remove it"

	_, err := loadStored(t, nil, map[string]string{envSecretKey: testSecretKey}, content)

	if !errors.Is(err, ErrInvalidStoredConfig) || !strings.Contains(err.Error(), valeurAttendue) {
		t.Fatalf("valeurObtenue %v, valeurAttendue %q", err, valeurAttendue)
	}
}

func TestConfig_masksAProxyKeyAfterAMixedCaseOrEncodedProxySegment(t *testing.T) {
	const proxyKey = "dummy-proxy-key-2222"
	for _, proxySegment := range []string{"Proxy", "%70roxy"} {
		content := `{"radarr": [{"url": "http://radarr.example.com:7476/` + proxySegment + `/{proxyKey}/radarr", "apiKey": "` +
			dummyAPIKey + `", "proxyKey": "` + proxyKey + `"}]}`
		loadedConfig, err := loadStored(t, nil, map[string]string{envSecretKey: testSecretKey}, content)
		if err != nil {
			t.Fatalf("%s: %v", proxySegment, err)
		}

		valeurObtenue := fmt.Sprintf("%v", loadedConfig) + RedactText("see "+loadedConfig.Radarr[0].BaseURL, loadedConfig.KnownSecrets()...)

		if strings.Contains(valeurObtenue, proxyKey) {
			t.Errorf("%s: valeurObtenue %q, valeurAttendue la clé de proxy masquée", proxySegment, valeurObtenue)
		}
	}
}
