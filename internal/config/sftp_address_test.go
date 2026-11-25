package config

import "testing"

func TestSFTPAddressKey_givesOneKeyToEverySpellingOfOneAddress(t *testing.T) {
	cases := []struct{ raw, valeurAttendue string }{
		{"Seedbox.Example.COM", "seedbox.example.com:22"},
		{"seedbox.example.com:22", "seedbox.example.com:22"},
		{"seedbox.example.com.", "seedbox.example.com:22"},
		{"SEEDBOX.example.com.:2222", "seedbox.example.com:2222"},
		{"[::1]", "[::1]:22"},
		{"[0:0:0:0:0:0:0:1]:22", "[::1]:22"},
		{"[2001:DB8::1]:2022", "[2001:db8::1]:2022"},
		{"127.0.0.1", "127.0.0.1:22"},
	}
	for _, testCase := range cases {
		valeurObtenue, err := SFTPAddressKey(testCase.raw)
		if err != nil || valeurObtenue != testCase.valeurAttendue {
			t.Errorf("SFTPAddressKey(%q) = %q, %v; want %q", testCase.raw, valeurObtenue, err, testCase.valeurAttendue)
		}
	}
}

func TestSFTPAddressKey_refusesWhitespaceAndEmptyHosts(t *testing.T) {
	for _, raw := range []string{"", " seedbox.example.com", "seedbox .example.com", "seedbox.example.com\t", ":22", "."} {
		if key, err := SFTPAddressKey(raw); err == nil {
			t.Errorf("SFTPAddressKey(%q) = %q, want an error", raw, key)
		}
	}
}

func TestSFTPAddressKey_keepsDifferentAddressesApart(t *testing.T) {
	first, firstErr := SFTPAddressKey("seedbox.example.com")
	second, secondErr := SFTPAddressKey("seedbox.example.com:2222")
	third, thirdErr := SFTPAddressKey("mirror.example.com")
	if firstErr != nil || secondErr != nil || thirdErr != nil || first == second || first == third {
		t.Fatalf("keys %q %q %q (errors %v %v %v) must differ", first, second, third, firstErr, secondErr, thirdErr)
	}
}

func TestIsLoopbackSFTPAddress_recognisesLocalhostAndLoopbackIPs(t *testing.T) {
	cases := map[string]bool{
		"localhost": true, "LocalHost.:2222": true, "127.0.0.1": true, "127.10.0.3:22": true, "[::1]": true,
		"seedbox.example.com": false, "10.0.0.1": false, "[2001:db8::1]": false, "localhost.example.com": false,
	}
	for raw, valeurAttendue := range cases {
		if valeurObtenue := IsLoopbackSFTPAddress(raw); valeurObtenue != valeurAttendue {
			t.Errorf("IsLoopbackSFTPAddress(%q) = %v, want %v", raw, valeurObtenue, valeurAttendue)
		}
	}
}
