package config

import (
	"strings"
	"testing"
)

func TestPublicURL_acceptsAnHttpsOriginAndStripsATrailingSlash(t *testing.T) {
	testCases := []struct {
		name                 string
		storedJSON           string
		valeurAttendueOrigin string
	}{
		{"trailing slash", `{"publicUrl": "https://Necro.Example.com/"}`, "https://necro.example.com"},
		{"default https port", `{"publicUrl": "https://necro.example.com:443"}`, "https://necro.example.com"},
		{"default http port", `{"publicUrl": "http://necro.example.com:80/"}`, "http://necro.example.com"},
		{"custom port kept", `{"publicUrl": "https://necro.example.com:8443"}`, "https://necro.example.com:8443"},
		{"upper-case scheme", `{"publicUrl": "HTTPS://necro.example.com"}`, "https://necro.example.com"},
		{"ipv6 host", `{"publicUrl": "http://[::1]:8787"}`, "http://[::1]:8787"},
		{"trailing-dot host", `{"publicUrl": "https://Necro.Example.com.:8443"}`, "https://necro.example.com:8443"},
		{"trailing-dot localhost", `{"publicUrl": "http://localhost.:8787"}`, "http://localhost:8787"},
		{"unset", `{}`, ""},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			loadedConfig := mustLoadStored(t, nil, nil, testCase.storedJSON)

			if valeurObtenue := loadedConfig.PublicURL; valeurObtenue != testCase.valeurAttendueOrigin {
				t.Fatalf("valeurObtenue %q, valeurAttendue %q", valeurObtenue, testCase.valeurAttendueOrigin)
			}
		})
	}
}

func TestPublicURL_originComesFromTheStoredSetting(t *testing.T) {
	loadedConfig := mustLoadStored(t, nil, nil, `{"publicUrl": "https://necro.example.com"}`)

	if valeurObtenue := originOf(loadedConfig, settingPublicURL, "").Origin; valeurObtenue != originOverride {
		t.Fatalf("valeurObtenue %q, valeurAttendue %q", valeurObtenue, originOverride)
	}
}

func TestPublicURL_refusesAPathQueryFragmentOrUserinfo(t *testing.T) {
	testCases := []struct{ name, storedJSON string }{
		{"path", `{"publicUrl": "https://necro.example.com/app"}`},
		{"query", `{"publicUrl": "https://necro.example.com?a=b"}`},
		{"empty query mark", `{"publicUrl": "https://necro.example.com/?"}`},
		{"fragment", `{"publicUrl": "https://necro.example.com#top"}`},
		{"userinfo", `{"publicUrl": "https://alice:secret@necro.example.com"}`},
		{"other scheme", `{"publicUrl": "ftp://necro.example.com"}`},
		{"empty host", `{"publicUrl": "https://"}`},
		{"not a url", `{"publicUrl": "necro.example.com"}`},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := loadStored(t, nil, nil, testCase.storedJSON)

			if err == nil {
				t.Fatal("valeurObtenue nil, valeurAttendue an error")
			}
			if !strings.Contains(err.Error(), settingPublicURL) || strings.Contains(err.Error(), "secret") {
				t.Fatalf("valeurObtenue %q, valeurAttendue a message naming publicUrl without the credentials", err)
			}
		})
	}
}

func TestWebAuthnOrigin_unavailableOverPlainHttpOffLocalhost(t *testing.T) {
	testCases := []Config{
		{ListenAddr: "0.0.0.0:8787", PublicURL: "http://necro.example.com"},
		{ListenAddr: "0.0.0.0:8787"},
		{ListenAddr: "127.0.0.1:8787", PublicURL: "http://necro.example.com"},
	}
	for _, testCase := range testCases {
		origin, rpID, available := testCase.WebAuthnOrigin()

		if available || origin != "" || rpID != "" {
			t.Fatalf("%+v: valeurObtenue (%q, %q, %v), valeurAttendue unavailable and empty", testCase, origin, rpID, available)
		}
	}
}

func TestWebAuthnOrigin_availableOverHttps(t *testing.T) {
	loadedConfig := Config{ListenAddr: "0.0.0.0:8787", PublicURL: "https://necro.example.com:8443"}

	origin, rpID, available := loadedConfig.WebAuthnOrigin()

	if !available || origin != "https://necro.example.com:8443" || rpID != "necro.example.com" {
		t.Fatalf("valeurObtenue (%q, %q, %v), valeurAttendue (https://necro.example.com:8443, necro.example.com, true)", origin, rpID, available)
	}
}

func TestWebAuthnOrigin_availableOnLocalhostOverHttp(t *testing.T) {
	testCases := []struct{ publicURL, valeurAttendueOrigin, valeurAttendueRPID string }{
		{"http://localhost:8787", "http://localhost:8787", "localhost"},
		{"https://localhost:8443", "https://localhost:8443", "localhost"},
	}
	for _, testCase := range testCases {
		loadedConfig := Config{ListenAddr: "0.0.0.0:8787", PublicURL: testCase.publicURL}

		origin, rpID, available := loadedConfig.WebAuthnOrigin()

		if !available || origin != testCase.valeurAttendueOrigin || rpID != testCase.valeurAttendueRPID {
			t.Fatalf("%s: valeurObtenue (%q, %q, %v)", testCase.publicURL, origin, rpID, available)
		}
	}
}

func TestWebAuthnOrigin_unavailableWhenTheHostIsAnIPAddress(t *testing.T) {
	for _, publicURL := range []string{"http://127.0.0.1:9000", "http://[::1]:9000", "https://192.0.2.1", "https://[2001:db8::1]:8443", "https://[fe80::1%25en0]:8443"} {
		loadedConfig := Config{ListenAddr: "127.0.0.1:8787", PublicURL: publicURL}

		origin, rpID, available := loadedConfig.WebAuthnOrigin()

		if available || origin != "" || rpID != "" {
			t.Fatalf("%s: valeurObtenue (%q, %q, %v), valeurAttendue unavailable, an IP address cannot be an RP ID", publicURL, origin, rpID, available)
		}
	}
}

func TestWebAuthnOrigin_unavailableForAnUnnormalisedTrailingDotHostWhateverTheScheme(t *testing.T) {
	for _, publicURL := range []string{"http://localhost.:8787", "https://localhost.", "https://necro.example.com."} {
		loadedConfig := Config{ListenAddr: "127.0.0.1:8787", PublicURL: publicURL}

		origin, rpID, available := loadedConfig.WebAuthnOrigin()

		if available || origin != "" || rpID != "" {
			t.Fatalf("%s: valeurObtenue (%q, %q, %v), valeurAttendue unavailable", publicURL, origin, rpID, available)
		}
	}
}

func TestWebAuthnOrigin_withoutPublicUrlOnALoopbackListenUsesLocalhostAndThePort(t *testing.T) {
	loadedConfig := Config{ListenAddr: "127.0.0.1:8787"}

	origin, rpID, available := loadedConfig.WebAuthnOrigin()

	if !available || origin != "http://localhost:8787" || rpID != "localhost" {
		t.Fatalf("valeurObtenue (%q, %q, %v), valeurAttendue (http://localhost:8787, localhost, true)", origin, rpID, available)
	}
}

func TestWebAuthnOrigin_zeroValueConfigIsUnavailable(t *testing.T) {
	origin, rpID, available := Config{}.WebAuthnOrigin()

	if available || origin != "" || rpID != "" {
		t.Fatalf("valeurObtenue (%q, %q, %v), valeurAttendue unavailable", origin, rpID, available)
	}
}
