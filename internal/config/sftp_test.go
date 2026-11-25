package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// Secrets factices à faible entropie : le mot de passe change sous l'échappement JSON (« " », « & », « / ») et aucun
// de ses morceaux n'atteint 16 caractères, si bien que seuls les secrets connus et le masquage des champs le cachent.
const (
	sftpPasswordForTest   = `fake"pass&word/0000`
	sftpPassphraseForTest = "0000bbbb0000bbbb"
)

// sftpProvider rend une entrée sftp minimale valide ; changes remplace ou ajoute des clés, une valeur nil retire la clé.
func sftpProvider(changes map[string]any) map[string]any {
	provider := map[string]any{
		"type": "sftp", "name": "seedbox", "address": "seedbox.example.com", "user": "media",
		"identityFile": absPath("data", "ssh", "id_ed25519"), "knownHosts": absPath("data", "ssh", "known_hosts"),
	}
	for key, value := range changes {
		if value == nil {
			delete(provider, key)
			continue
		}
		provider[key] = value
	}
	return provider
}

// configWith rend un config.json qui déclare provider, plus les clés de premier niveau de topLevel.
func configWith(t *testing.T, provider map[string]any, topLevel map[string]any) string {
	t.Helper()
	document := map[string]any{"fsProviders": []any{provider}}
	for key, value := range topLevel {
		document[key] = value
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func sftpEnvironment() map[string]string {
	return map[string]string{"SFTP_PASSWORD": sftpPasswordForTest, "SFTP_KEY_PASSPHRASE": sftpPassphraseForTest}
}

func TestConfig_sftpDefaults(t *testing.T) {
	loadedConfig, err := loadFromJSON(t, configWith(t, sftpProvider(nil), nil), nil)
	if err != nil {
		t.Fatal(err)
	}

	gotValue := loadedConfig.FSProviders[0]
	expectedValue := FSProvider{
		Type: "sftp", Name: "seedbox", Address: "seedbox.example.com:22", User: "media",
		IdentityFile: absPath("data", "ssh", "id_ed25519"), KnownHosts: absPath("data", "ssh", "known_hosts"),
		MaxConcurrentRequests: 16, Timeout: 30 * time.Second, Keepalive: 15 * time.Second, Exec: "auto",
		ExecHashTimeout: 30 * time.Minute,
	}
	if fmt.Sprint(gotValue) != fmt.Sprint(expectedValue) {
		t.Fatalf("valeurObtenue %+v, valeurAttendue %+v", gotValue, expectedValue)
	}
}

func TestConfig_sftpReadsEveryKey(t *testing.T) {
	provider := sftpProvider(map[string]any{
		"agent": true, "password": "${SFTP_PASSWORD}", "identityPassphrase": "${SFTP_KEY_PASSPHRASE}",
		"readBytesPerSecond": 10485760, "maxConcurrentRequests": 4, "timeout": "5s", "keepalive": "7s", "exec": "required",
		"execHashTimeout": "2m",
	})

	loadedConfig, err := loadFromJSON(t, configWith(t, provider, nil), sftpEnvironment())
	if err != nil {
		t.Fatal(err)
	}

	gotValue := loadedConfig.FSProviders[0]
	isExpected := gotValue.Agent && gotValue.Password == sftpPasswordForTest && gotValue.IdentityPassphrase == sftpPassphraseForTest &&
		gotValue.ReadBytesPerSecond == 10485760 && gotValue.MaxConcurrentRequests == 4 && gotValue.Timeout == 5*time.Second &&
		gotValue.Keepalive == 7*time.Second && gotValue.Exec == "required" && gotValue.ExecHashTimeout == 2*time.Minute
	if !isExpected {
		t.Fatalf("valeurObtenue %+v, valeurAttendue every key read", gotValue)
	}
}

func TestConfig_sftpAddressKeepsAGivenPortAndBracketsIPv6(t *testing.T) {
	cases := map[string]string{
		"seedbox.example.com:2222": "seedbox.example.com:2222",
		"[::1]:22":                 "[::1]:22",
		"[::1]":                    "[::1]:22",
		"192.0.2.10":               "192.0.2.10:22",
	}
	for rawAddress, valeurAttendue := range cases {
		content := configWith(t, sftpProvider(map[string]any{"address": rawAddress}), nil)

		loadedConfig, err := loadFromJSON(t, content, nil)
		if err != nil || loadedConfig.FSProviders[0].Address != valeurAttendue {
			t.Fatalf("%q: valeurObtenue %+v (err %v), valeurAttendue %q", rawAddress, loadedConfig.FSProviders, err, valeurAttendue)
		}
	}
}

func TestConfig_sftpRequiresKnownHosts(t *testing.T) {
	cases := []struct {
		knownHosts     any
		valeurAttendue string
	}{{nil, "knownHosts is required"}, {"", "knownHosts is required"}, {"known_hosts", "knownHosts must be an absolute"}}
	for _, testCase := range cases {
		content := configWith(t, sftpProvider(map[string]any{"knownHosts": testCase.knownHosts}), nil)

		_, err := loadFromJSON(t, content, nil)

		if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), testCase.valeurAttendue) {
			t.Fatalf("knownHosts %v: valeurObtenue %v, valeurAttendue a usage error %q", testCase.knownHosts, err, testCase.valeurAttendue)
		}
	}
}

func TestConfig_sftpPasswordOnlyFromVariable(t *testing.T) {
	for _, key := range []string{"password", "identityPassphrase"} {
		for _, literal := range []string{sftpPasswordForTest, "prefix-${SFTP_PASSWORD}", "${SFTP_PASSWORD}${SFTP_KEY_PASSPHRASE}"} {
			content := configWith(t, sftpProvider(map[string]any{key: literal}), nil)

			_, err := loadFromJSON(t, content, sftpEnvironment())

			if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), key) {
				t.Fatalf("%s %q: valeurObtenue %v, valeurAttendue a usage error naming the key", key, literal, err)
			}
			for _, secret := range []string{sftpPasswordForTest, sftpPassphraseForTest, "prefix-"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("%s: the error echoes the value: %v", key, err)
				}
			}
		}
	}
}

func TestConfig_sftpRefusesInvalidValues(t *testing.T) {
	cases := []struct {
		changes        map[string]any
		valeurAttendue string
	}{
		{map[string]any{"exec": "always"}, "auto, off, required"},
		{map[string]any{"maxConcurrentRequests": 0}, "maxConcurrentRequests"},
		{map[string]any{"readBytesPerSecond": -1}, "readBytesPerSecond"},
		{map[string]any{"timeout": "0s"}, "timeout"},
		{map[string]any{"keepalive": "soon"}, "keepalive"},
		{map[string]any{"execHashTimeout": "-1m"}, "execHashTimeout"},
		{map[string]any{"mounts": []string{"/mnt/x=seedbox:"}}, "mounts"},
		{map[string]any{"rc": "http://rclone:5572"}, "rc"},
		{map[string]any{"pasword": "${SFTP_PASSWORD}"}, "pasword"},
		{map[string]any{"identityPassphrase": "${SFTP_KEY_PASSPHRASE}", "identityFile": nil, "agent": true}, "identityPassphrase"},
		{map[string]any{"identityFile": "id_ed25519"}, "identityFile"},
		{map[string]any{"name": "Bad_Name"}, "name"},
	}
	for _, testCase := range cases {
		_, err := loadFromJSON(t, configWith(t, sftpProvider(testCase.changes), nil), sftpEnvironment())

		if err == nil || !strings.Contains(err.Error(), testCase.valeurAttendue) {
			t.Fatalf("%v: valeurObtenue %v, valeurAttendue an error naming %q", testCase.changes, err, testCase.valeurAttendue)
		}
	}
}

func TestConfig_sftpRequiresUserAddressAndOneAuthenticationMethod(t *testing.T) {
	cases := []struct {
		changes        map[string]any
		valeurAttendue string
	}{
		{map[string]any{"user": nil}, "user"},
		{map[string]any{"address": nil}, "address"},
		{map[string]any{"address": "::1"}, "address"},
		{map[string]any{"address": "seedbox.example.com:0"}, "address"},
		{map[string]any{"identityFile": nil}, "identityFile, agent or password"},
	}
	for _, testCase := range cases {
		_, err := loadFromJSON(t, configWith(t, sftpProvider(testCase.changes), nil), nil)

		if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), testCase.valeurAttendue) {
			t.Fatalf("%v: valeurObtenue %v, valeurAttendue a usage error naming %q", testCase.changes, err, testCase.valeurAttendue)
		}
	}
	agentOnly := configWith(t, sftpProvider(map[string]any{"identityFile": nil, "agent": true}), nil)
	if _, err := loadFromJSON(t, agentOnly, map[string]string{}); err != nil {
		t.Fatalf("agent without SSH_AUTH_SOCK at load: valeurObtenue %v, valeurAttendue no error", err)
	}
}

func TestConfig_sftpProviderExposesAFilesystem(t *testing.T) {
	content := configWith(t, sftpProvider(nil), map[string]any{"libraryRoots": []string{"sftp://seedbox/srv/media"}})

	loadedConfig, err := loadFromJSON(t, content, nil)

	if err != nil || loadedConfig.LibraryRoots[0] != "sftp://seedbox/srv/media" {
		t.Fatalf("valeurObtenue %v, valeurAttendue an sftp:// root on a declared sftp provider", err)
	}
}

func TestConfig_refusesRemoteDBPath(t *testing.T) {
	content := configWith(t, sftpProvider(nil), map[string]any{"dbPath": "sftp://seedbox/srv/necromanciarr.db"})

	_, err := loadFromJSON(t, content, nil)

	if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), "dbPath") {
		t.Fatalf("valeurObtenue %v, valeurAttendue a usage error naming dbPath", err)
	}
	configDir := t.TempDir()
	writeFile(t, configDir, configFileName, "{}")
	if _, err := Load([]string{"--db-path", "sftp://seedbox/x.db"}, lookupFrom(nil), configDir); !errors.Is(err, ErrUsage) {
		t.Fatalf("--db-path: valeurObtenue %v, valeurAttendue a usage error", err)
	}
}

func TestConfigCheck_masksSFTPSecrets(t *testing.T) {
	provider := sftpProvider(map[string]any{"password": "${SFTP_PASSWORD}", "identityPassphrase": "${SFTP_KEY_PASSPHRASE}"})
	loadedConfig, err := loadFromJSON(t, configWith(t, provider, nil), sftpEnvironment())
	if err != nil {
		t.Fatal(err)
	}
	redactedJSON, err := loadedConfig.RedactedJSON()
	if err != nil {
		t.Fatal(err)
	}
	leakingText := "dial failed: password " + sftpPasswordForTest + ", passphrase " + sftpPassphraseForTest
	redactedText := RedactText(leakingText, loadedConfig.KnownSecrets()...)
	redactedTree, err := RedactJSON(map[string]string{"message": leakingText}, loadedConfig.KnownSecrets()...)
	if err != nil {
		t.Fatal(err)
	}
	renderings := []string{
		string(redactedJSON), loadedConfig.String(), loadedConfig.LogValue().String(), fmt.Sprintf("%#v", loadedConfig),
		fmt.Sprintf("%v", slog.Any("c", loadedConfig)), loadedConfig.FSProviders[0].String(), redactedText, string(redactedTree),
	}

	for _, rendered := range renderings {
		for _, secret := range []string{sftpPasswordForTest, `fake\"pass`, "pass&word", sftpPassphraseForTest} {
			if strings.Contains(rendered, secret) {
				t.Fatalf("valeurObtenue %q in %s, valeurAttendue no secret", secret, rendered)
			}
		}
	}
	visible := []string{`"address": "seedbox.example.com:22"`, `"user": "media"`, jsonText(absPath("data", "ssh", "known_hosts")), jsonText(absPath("data", "ssh", "id_ed25519"))}
	for _, valeurAttendue := range visible {
		if !strings.Contains(string(redactedJSON), valeurAttendue) {
			t.Fatalf("valeurAttendue %q visible in %s", valeurAttendue, redactedJSON)
		}
	}
	if loadedConfig.FSProviders[0].Password != sftpPasswordForTest {
		t.Fatal("redaction must not alter the loaded configuration")
	}
}
