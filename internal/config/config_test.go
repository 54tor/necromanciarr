package config

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"necromanciarr/internal/i18n"
)

func writeFile(t *testing.T, directory, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustLoad(t *testing.T, args []string, environment map[string]string, configDir string) Config {
	t.Helper()
	loadedConfig, err := Load(args, lookupFrom(environment), configDir)
	if err != nil {
		t.Fatal(err)
	}
	return loadedConfig
}

func TestLoad_defaultsListenToLoopbackWithoutFile(t *testing.T) {
	expectedValue := "127.0.0.1:8787"

	loadedConfig := mustLoad(t, nil, nil, t.TempDir())

	if gotValue := loadedConfig.ListenAddr; gotValue != expectedValue {
		t.Fatalf("gotValue %q, expectedValue %q", gotValue, expectedValue)
	}
}

func TestLoad_acceptsNonLoopbackWithoutTokenForOneShotCommands(t *testing.T) {
	loadedConfig := mustLoad(t, []string{"--listen", "0.0.0.0:8787"}, nil, t.TempDir())

	if gotValue := loadedConfig.ListenAddr; gotValue != "0.0.0.0:8787" {
		t.Fatalf("gotValue %q, expectedValue the non-loopback listen loaded without token", gotValue)
	}
}

func TestValidateServe_rejectsNonLoopbackWithoutToken(t *testing.T) {
	if err := ValidateServe(Config{ListenAddr: "0.0.0.0:8787"}); err == nil {
		t.Fatal("expected an error for a non-loopback address without token")
	}
}

func TestValidateServe_acceptsLoopbackWithoutTokenAndNonLoopbackWithToken(t *testing.T) {
	for _, settings := range []Config{{ListenAddr: "127.0.0.1:8787"}, {ListenAddr: "0.0.0.0:8787", AuthToken: longTestToken}} {
		if err := ValidateServe(settings); err != nil {
			t.Fatalf("listen %q: gotValue %v, expectedValue no error", settings.ListenAddr, err)
		}
	}
}

func TestLoad_acceptsNonLoopbackWithTokenFromEnvironment(t *testing.T) {
	environment := map[string]string{envAuthToken: longTestToken}

	loadedConfig := mustLoad(t, []string{"--listen", "0.0.0.0:8787"}, environment, t.TempDir())

	if loadedConfig.AuthToken != longTestToken {
		t.Fatal("AuthToken not read from environment")
	}
}

func TestLoad_priorityCliOverEnvOverStoredOverDefault(t *testing.T) {
	const stored = `{"libraryApi": "http://file.example.com"}`
	environment := map[string]string{envLibraryAPI: "http://env.example.com"}
	cases := []struct {
		name        string
		args        []string
		environment map[string]string
		expectedAPI string
		expectedDB  string
	}{
		{"stored only", nil, nil, "http://file.example.com", defaultDBPath},
		{"env over stored", nil, environment, "http://env.example.com", defaultDBPath},
		{"cli over env", []string{"--library-api", "http://cli.example.com", "--db-path", "/home/u/cli.db"}, environment, "http://cli.example.com", "/home/u/cli.db"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			loadedConfig := mustLoadStored(t, testCase.args, testCase.environment, stored)
			if loadedConfig.LibraryAPI != testCase.expectedAPI || loadedConfig.DBPath != testCase.expectedDB {
				t.Fatalf("gotValue %q %q, expectedValue %q %q", loadedConfig.LibraryAPI, loadedConfig.DBPath, testCase.expectedAPI, testCase.expectedDB)
			}
		})
	}
	if gotValue := mustLoad(t, nil, nil, t.TempDir()).DBPath; gotValue != defaultDBPath {
		t.Fatalf("default DBPath gotValue %q", gotValue)
	}
}

func TestLoad_configDirFlagAndEnvironmentChooseTheDirectory(t *testing.T) {
	flagDir, environmentDir := t.TempDir(), t.TempDir()
	environment := map[string]string{envConfigDir: environmentDir}

	if gotValue := mustLoad(t, []string{"--config-dir", flagDir}, environment, t.TempDir()).ConfigDir; gotValue != flagDir {
		t.Fatalf("flag: gotValue %q", gotValue)
	}
	if gotValue := mustLoad(t, nil, environment, t.TempDir()).ConfigDir; gotValue != environmentDir {
		t.Fatalf("env: gotValue %q", gotValue)
	}
}

func TestLoad_readsDotEnvFromConfigDirWithoutOverridingEnvironment(t *testing.T) {
	configDir := t.TempDir()
	writeFile(t, configDir, ".env", "# commentaire\nLIBRARY_API=https://dotenv.example.com\nexport NECROMANCIARR_SEED_CATEGORY=\"dot-env-category\"\nNECROMANCIARR_LISTEN=127.0.0.1:1111\n")
	environment := map[string]string{envListen: "127.0.0.1:2222"}

	loadedConfig := mustLoad(t, nil, environment, configDir)

	if loadedConfig.LibraryAPI != "https://dotenv.example.com" {
		t.Errorf("library api gotValue %q", loadedConfig.LibraryAPI)
	}
	if loadedConfig.ListenAddr != "127.0.0.1:2222" {
		t.Errorf("listen gotValue %q: .env must not override the environment", loadedConfig.ListenAddr)
	}
	if loadedConfig.LinkCategory != "dot-env-category" {
		t.Errorf("seed category gotValue %q", loadedConfig.LinkCategory)
	}
}

func TestLoad_ignoresTheWorkingDirectoryDotEnvWhenTheConfigDirHasOne(t *testing.T) {
	configDir, workingDir := t.TempDir(), t.TempDir()
	writeFile(t, workingDir, ".env", "LIBRARY_API=https://cwd.example.com\n")
	writeFile(t, configDir, ".env", "NECROMANCIARR_SEED_CATEGORY=from-config-dir\n")
	t.Chdir(workingDir)

	loadedConfig := mustLoad(t, nil, nil, configDir)

	if loadedConfig.LibraryAPI != "" || loadedConfig.LinkCategory != "from-config-dir" || loadedConfig.WorkingDirDotEnv {
		t.Fatalf("gotValue %q %q (cwd .env used %v), expectedValue only the config dir .env read",
			loadedConfig.LibraryAPI, loadedConfig.LinkCategory, loadedConfig.WorkingDirDotEnv)
	}
}

func TestLoad_readsTheWorkingDirectoryDotEnvOnlyWithoutAConfigDirOneAndSaysSo(t *testing.T) {
	configDir, workingDir := t.TempDir(), t.TempDir()
	writeFile(t, workingDir, ".env", "LIBRARY_API=https://cwd.example.com\n")
	t.Chdir(workingDir)

	loadedConfig := mustLoad(t, nil, nil, configDir)

	if loadedConfig.LibraryAPI != "https://cwd.example.com" || !loadedConfig.WorkingDirDotEnv {
		t.Fatalf("gotValue %q (cwd .env used %v), expectedValue the working directory .env used and flagged", loadedConfig.LibraryAPI, loadedConfig.WorkingDirDotEnv)
	}
}

func TestLoad_undefinedVariableNamesItInTheError(t *testing.T) {
	_, err := loadStored(t, nil, nil, `{"fsProviders": [{"type": "rclone", "name": "remote", "rc": "http://rcuser:${NOPE_KEY}@rclone:5572"}]}`)

	if err == nil || !strings.Contains(err.Error(), "NOPE_KEY") || !strings.Contains(err.Error(), "fsProviders[0]") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoad_errorsNeverContainServiceSecrets(t *testing.T) {
	cases := map[string]string{
		"torrent client type": `{"torrentClients": [{"type": "transmission", "url": "http://example.com", "user": "alice", "password": "secret-pass"}]}`,
		"torrent client url":  `{"torrentClients": [{"type": "qbittorrent", "url": "ftp://example.com", "user": "alice", "password": "secret-pass"}]}`,
		"radarr scheme":       `{"radarr": [{"url": "ftp://example.com", "apiKey": "secret-key"}]}`,
		"radarr fragment":     `{"radarr": [{"url": "http://example.com#secret-other", "apiKey": "secret-key"}]}`,
		"library api":         `{"libraryApi": "ftp://alice:secret-pass@example.com"}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := loadStored(t, nil, nil, content)

			if err == nil || strings.Contains(err.Error(), "secret-") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestLoad_rejectsMissingExplicitConfigDirWithoutCreatingIt(t *testing.T) {
	missingDir := filepath.Join(t.TempDir(), "typo")

	_, flagErr := Load([]string{"--config-dir", missingDir}, lookupFrom(nil), t.TempDir())
	_, envErr := Load(nil, lookupFrom(map[string]string{envConfigDir: missingDir}), t.TempDir())

	for _, err := range []error{flagErr, envErr} {
		if err == nil || !strings.Contains(err.Error(), strconv.Quote(missingDir)) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if _, err := os.Stat(missingDir); err == nil {
		t.Fatal("explicit dir must not be created")
	}
}

func TestLoad_existingConfigDirWithoutDatabaseLoads(t *testing.T) {
	mustLoad(t, []string{"--config-dir", t.TempDir()}, nil, t.TempDir())
}

func TestLoad_rejectsZeroReadRateFlag(t *testing.T) {
	_, err := Load([]string{"--read-bytes-per-second", "0"}, lookupFrom(nil), t.TempDir())

	if err == nil || !strings.Contains(err.Error(), "must be positive") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoad_tieBreakReadBytesDefaultsTo50GiBAndRejectsNegative(t *testing.T) {
	const expectedValue = int64(50 << 30)
	if gotValue := mustLoad(t, nil, nil, t.TempDir()).TieBreakReadBytes; gotValue != expectedValue {
		t.Fatalf("gotValue %d, expectedValue %d", gotValue, expectedValue)
	}
	_, err := loadStored(t, nil, nil, `{"tieBreakReadBytes": -1}`)

	if err == nil || !strings.Contains(err.Error(), "tieBreakReadBytes must be positive") {
		t.Fatalf("gotValue %v, expectedValue a refusal naming tieBreakReadBytes", err)
	}
}

func TestLoad_extraPositionalArgumentIsUsageError(t *testing.T) {
	_, err := Load([]string{"extra"}, lookupFrom(nil), t.TempDir())

	if !errors.Is(err, ErrUsage) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConfig_redactsUserinfoInRadarrAndLibraryApi(t *testing.T) {
	loadedConfig := mustLoadStored(t, nil, nil, `{
  "libraryApi": "https://bob:secret-library@example.com/api?token=secret-token&page=2",
  "radarr": ["http://alice:secret-proxy@example.com:7878/radarr?apikey=secret-radarr"]
}`)

	if loadedConfig.Radarr[0].BaseURL != "http://example.com:7878/radarr" || loadedConfig.Radarr[0].User != "alice" {
		t.Fatalf("unexpected service %+v", loadedConfig.Radarr[0])
	}
	redactedJSON, err := loadedConfig.RedactedJSON()
	if err != nil {
		t.Fatal(err)
	}
	directJSON, err := json.Marshal(loadedConfig)
	if err != nil {
		t.Fatal(err)
	}
	var logBuffer bytes.Buffer
	slog.New(slog.NewJSONHandler(&logBuffer, nil)).Info("config", "config", loadedConfig, "service", loadedConfig.Radarr[0])
	renderings := []string{string(redactedJSON), string(directJSON), logBuffer.String(), loadedConfig.String(), loadedConfig.Radarr[0].String()}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		renderings = append(renderings, fmt.Sprintf(verb, loadedConfig), fmt.Sprintf(verb, loadedConfig.Radarr[0]))
	}
	for _, rendering := range renderings {
		assertNoSecret(t, rendering)
	}
}

func TestConfig_redactsUrlCredentials(t *testing.T) {
	environment := map[string]string{envAuthToken: "secret-auth-secret-auth-secret-auth", envLibraryProviderToken: "secret-provider"}
	loadedConfig := mustLoadStored(t, nil, environment, `{
  "radarr": ["http://example.com:7878?apikey=secret-radarr"],
  "torrentClients": ["qbittorrent:http://alice:secret-qbit@example.com:8080"]
}`)

	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		assertNoSecret(t, fmt.Sprintf(verb, loadedConfig))
	}
	var logBuffer bytes.Buffer
	slog.New(slog.NewTextHandler(&logBuffer, nil)).Info("config", "config", loadedConfig)
	assertNoSecret(t, logBuffer.String())
	redactedJSON, err := loadedConfig.RedactedJSON()
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecret(t, string(redactedJSON))
	if !strings.Contains(string(redactedJSON), "***") {
		t.Fatalf("expected redaction mark in %s", redactedJSON)
	}
}

func TestLoad_dedupeDefaultsWhenSectionAbsent(t *testing.T) {
	loadedConfig := mustLoad(t, nil, nil, t.TempDir())

	gotValue := loadedConfig.Dedupe
	if len(gotValue.TorrentRoots) != 0 || !slices.Equal(gotValue.Kinds, []string{"symlink"}) ||
		!gotValue.RelativeSymlinks || gotValue.MinFileAge != 10*time.Minute || !gotValue.RecheckAfter {
		t.Fatalf("unexpected dedupe defaults %+v", gotValue)
	}
}

func TestLoad_dedupeReadsEveryKey(t *testing.T) {
	gotValue := mustLoadStored(t, nil, nil, `{"dedupe": {"torrentRoots": ["/home/u/torrents"], "kinds": ["hardlink", "symlink"],
		"relativeSymlinks": false, "minFileAge": "1h30m", "recheckAfter": false}}`).Dedupe

	expectedValue := Dedupe{
		TorrentRoots: []string{"/home/u/torrents"}, Kinds: []string{"hardlink", "symlink"},
		RelativeSymlinks: false, MinFileAge: 90 * time.Minute, RecheckAfter: false, Verify: "auto",
	}
	if !reflect.DeepEqual(gotValue, expectedValue) {
		t.Fatalf("gotValue %+v, expectedValue %+v", gotValue, expectedValue)
	}
}

func TestLoad_dedupeRejectsUnknownKindNamingTheKey(t *testing.T) {
	_, err := loadStored(t, nil, nil, `{"dedupe": {"kinds": ["symlink", "junction"]}}`)

	if err == nil || !strings.Contains(err.Error(), "dedupe.kinds[1]") {
		t.Fatalf("expected an error naming dedupe.kinds[1], got %v", err)
	}
}

func TestLoad_dedupeRejectsInvalidMinFileAge(t *testing.T) {
	for _, rawAge := range []string{"soon", "-1m"} {
		_, err := loadStored(t, nil, nil, `{"dedupe": {"minFileAge": "`+rawAge+`"}}`)

		if err == nil || !strings.Contains(err.Error(), "dedupe.minFileAge") {
			t.Fatalf("expected an error naming dedupe.minFileAge for %q, got %v", rawAge, err)
		}
	}
}

func TestConfig_rejectsDedupeRootsOverlappingLibraryRoots(t *testing.T) {
	cases := []struct {
		name            string
		libraryRoot     string
		torrentRoot     string
		expectedOverlap bool
	}{
		{"equal", "/mnt/remote/1080p", "/mnt/remote/1080p/", true},
		{"torrent root is an ancestor", "/mnt/remote/1080p", "/mnt/remote", true},
		{"torrent root is a descendant", "/mnt/remote", "/mnt/remote/qBittorrent", true},
		{"siblings", "/mnt/remote/1080p", "/mnt/remote/qBittorrent", false},
		{"shared prefix is not a parent", "/data/tv", "/data/tvshows", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := loadStored(t, nil, nil, fmt.Sprintf(`{"libraryRoots": [%q], "dedupe": {"torrentRoots": [%q]}}`,
				onThisSystem(testCase.libraryRoot), onThisSystem(testCase.torrentRoot)))

			gotOverlap := err != nil
			if gotOverlap != testCase.expectedOverlap {
				t.Fatalf("overlap error %v, expected overlap %v", err, testCase.expectedOverlap)
			}
			if gotOverlap && !strings.Contains(err.Error(), "dedupe.torrentRoots[0]") {
				t.Fatalf("error does not name the key: %v", err)
			}
		})
	}
}

func TestConfig_rejectsLinkDirsOverlappingLibraryRoots(t *testing.T) {
	cases := map[string]struct {
		linkDir         string
		expectedOverlap bool
	}{
		"equal":       {"/mnt/remote/1080p", true},
		"ancestor":    {"/mnt/remote", true},
		"descendant":  {"/mnt/remote/1080p/links", true},
		"sibling":     {"/mnt/remote/links", false},
		"same prefix": {"/mnt/remote/1080p-links", false},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := loadStored(t, nil, nil, fmt.Sprintf(`{"libraryRoots": [%q], "linkDirs": [%q]}`,
				onThisSystem("/mnt/remote/1080p"), onThisSystem(testCase.linkDir)))

			gotOverlap := err != nil && strings.Contains(err.Error(), "linkDirs[0]")
			if gotOverlap != testCase.expectedOverlap || (err != nil && !gotOverlap) {
				t.Fatalf("gotValue %v, expectedValue overlap %v", err, testCase.expectedOverlap)
			}
		})
	}
}

func TestConfig_readsLibraryApiFromEnvironment(t *testing.T) {
	environment := map[string]string{envLibraryAPI: "https://example.com/api/necromanciarr", envLibraryProviderToken: "provider-secret"}
	loadedConfig := mustLoad(t, nil, environment, t.TempDir())
	if loadedConfig.LibraryAPI != "https://example.com/api/necromanciarr" || loadedConfig.LibraryProviderToken != "provider-secret" {
		t.Fatalf("gotValue %q %q", loadedConfig.LibraryAPI, loadedConfig.LibraryProviderToken)
	}
}

// longTestToken a la longueur minimale admise, à faible entropie (gitleaks).
const longTestToken = "token-for-test-token-for-test-00"

func TestLoad_rejectsAShortAuthTokenWithoutPrintingIt(t *testing.T) {
	const shortToken = "short-token-for-test"

	_, err := Load(nil, lookupFrom(map[string]string{envAuthToken: shortToken}), t.TempDir())

	if err == nil || !strings.Contains(err.Error(), envAuthToken) || strings.Contains(err.Error(), shortToken) {
		t.Fatalf("gotValue %v, expectedValue an error naming the variable without its value", err)
	}
}

func TestValidateServe_nonLoopbackWithoutTokenNamesTheVariable(t *testing.T) {
	loadedConfig := mustLoad(t, nil, map[string]string{envListen: "0.0.0.0:8787"}, t.TempDir())

	err := ValidateServe(loadedConfig)

	expectedLength := strconv.Itoa(MinAuthTokenLength)
	if err == nil || !strings.Contains(err.Error(), envAuthToken) || !strings.Contains(err.Error(), "api-key") ||
		!strings.Contains(err.Error(), expectedLength) {
		t.Fatalf("gotValue %v, expectedValue a refusal naming the token variable, its minimum length and the generator", err)
	}
}

func TestLoad_listenEnvOverridesTheStoredValueAndTheFlagOverridesTheEnv(t *testing.T) {
	const stored = `{"listen": "127.0.0.1:1111"}`
	environment := map[string]string{envListen: "127.0.0.1:2222"}

	fromEnvironment := mustLoadStored(t, nil, environment, stored)
	fromFlag := mustLoadStored(t, []string{"--listen", "127.0.0.1:3333"}, environment, stored)
	fromFile := mustLoadStored(t, nil, map[string]string{envListen: ""}, stored)

	gotValues := []string{fromEnvironment.ListenAddr, fromFlag.ListenAddr, fromFile.ListenAddr}
	expectedValues := []string{"127.0.0.1:2222", "127.0.0.1:3333", "127.0.0.1:1111"}
	if !slices.Equal(gotValues, expectedValues) {
		t.Fatalf("gotValue %v, expectedValue %v", gotValues, expectedValues)
	}
}

func TestGenerateAuthToken_isLongRandomAndURLSafe(t *testing.T) {
	first, firstErr := GenerateAuthToken()
	second, secondErr := GenerateAuthToken()

	if firstErr != nil || secondErr != nil || first == second || len(first) < 43 || len(first) < MinAuthTokenLength {
		t.Fatalf("gotValue %q %q (%v %v), expectedValue two distinct tokens of at least 43 characters", first, second, firstErr, secondErr)
	}
	if strings.ContainsAny(first, "+/=") {
		t.Fatalf("gotValue %q, expectedValue base64url without padding", first)
	}
}

func TestLanguage_defaultsToFrenchAndFollowsPriorityChain(t *testing.T) {
	const stored = `{"language": "en"}`
	loadedConfig := mustLoad(t, nil, nil, t.TempDir())
	if loadedConfig.Language != i18n.French || loadedConfig.LanguageSource != "default" {
		t.Fatalf("gotValue %q/%q, expectedValue fr/default", loadedConfig.Language, loadedConfig.LanguageSource)
	}
	loadedConfig = mustLoadStored(t, nil, nil, stored)
	if loadedConfig.Language != i18n.English || loadedConfig.LanguageSource != "config.json" {
		t.Fatalf("gotValue %q/%q, expectedValue en/config.json", loadedConfig.Language, loadedConfig.LanguageSource)
	}
	loadedConfig = mustLoadStored(t, nil, map[string]string{"NECROMANCIARR_LANGUAGE": " FR "}, stored)
	if loadedConfig.Language != i18n.French || loadedConfig.LanguageSource != "NECROMANCIARR_LANGUAGE" {
		t.Fatalf("gotValue %q/%q, expectedValue fr/env", loadedConfig.Language, loadedConfig.LanguageSource)
	}
	loadedConfig = mustLoadStored(t, []string{"--language", "en"}, map[string]string{"NECROMANCIARR_LANGUAGE": "fr"}, stored)
	if loadedConfig.Language != i18n.English || loadedConfig.LanguageSource != "--language" {
		t.Fatalf("gotValue %q/%q, expectedValue en/--language", loadedConfig.Language, loadedConfig.LanguageSource)
	}
}

func TestLanguage_unknownValueIsUsageError(t *testing.T) {
	for name, load := range map[string]func() error{
		"flag": func() error { _, err := Load([]string{"--language", "de"}, lookupFrom(nil), t.TempDir()); return err },
		"env": func() error {
			_, err := Load(nil, lookupFrom(map[string]string{"NECROMANCIARR_LANGUAGE": "de"}), t.TempDir())
			return err
		},
		"stored": func() error { _, err := loadStored(t, nil, nil, `{"language": "xx"}`); return err },
	} {
		err := load()
		if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), "fr, en") {
			t.Fatalf("%s: gotValue %v, expectedValue a usage error naming the allowed values", name, err)
		}
	}
	_, err := Load([]string{"--language", "de"}, lookupFrom(nil), t.TempDir())
	if !strings.Contains(err.Error(), `"de"`) {
		t.Fatalf("gotValue %v, expectedValue the value named", err)
	}
}

func TestLanguage_unknownValueEchoesAtMostTwentyCharacters(t *testing.T) {
	longValue := strings.Repeat("x", 60)

	_, err := Load([]string{"--language", longValue}, lookupFrom(nil), t.TempDir())

	if !errors.Is(err, ErrUsage) || strings.Contains(err.Error(), longValue) || !strings.Contains(err.Error(), `"`+strings.Repeat("x", 20)+`"`) {
		t.Fatalf("gotValue %v, expectedValue the value cut to 20 characters", err)
	}
}

func TestLanguage_sourceIsNotPartOfTheConfigurationJSON(t *testing.T) {
	loadedConfig := mustLoad(t, []string{"--language", "en"}, nil, t.TempDir())

	encoded, err := loadedConfig.RedactedJSON()

	if err != nil || strings.Contains(string(encoded), "languageSource") || !strings.Contains(string(encoded), `"language": "en"`) {
		t.Fatalf("gotValue %q (%v), expectedValue the language without its source", encoded, err)
	}
}

// testSecretKeyBytes est une clé de test à faible entropie, jamais une vraie clé.
var testSecretKeyBytes = bytes.Repeat([]byte{0x01}, SecretKeySize)

var testSecretKey = base64.RawURLEncoding.EncodeToString(testSecretKeyBytes)

const torznabOnly = `{"torznab": ["https://prowlarr.example.com/12/api?apikey=secret-indexer-key"]}`

func TestConfig_torznabRequiresSecretKey(t *testing.T) {
	_, err := loadTorznabConfig(t, torznabOnly, map[string]string{envSecretKey: ""})

	if !errors.Is(err, ErrSecretKeyRequired) || err.Error() != "torznab requires NECROMANCIARR_SECRET_KEY" {
		t.Fatalf("gotValue %v, expectedValue ErrSecretKeyRequired", err)
	}
	loadedConfig, err := loadTorznabConfig(t, torznabOnly, map[string]string{envSecretKey: testSecretKey})
	if err != nil || !bytes.Equal(loadedConfig.SecretKey, testSecretKeyBytes) {
		t.Fatalf("gotValue %v, %v, expectedValue the decoded 32-byte key", loadedConfig.SecretKey, err)
	}
	withoutTorznab, err := loadTorznabConfig(t, `{}`, map[string]string{envSecretKey: ""})
	if err != nil || withoutTorznab.SecretKey != nil {
		t.Fatalf("gotValue %v, %v, expectedValue no key needed without torznab", withoutTorznab.SecretKey, err)
	}
}

func TestConfig_secretKeyWrongLengthNamesVariableOnly(t *testing.T) {
	invalidValues := map[string]string{
		"16 bytes":            base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x02}, 16)),
		"33 bytes":            base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x02}, 33)),
		"padded":              base64.URLEncoding.EncodeToString(bytes.Repeat([]byte{0x02}, 32)),
		"standard alphabet":   base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{0xfb}, 32)),
		"hex":                 strings.Repeat("0f", 32),
		"non canonical bits":  testSecretKey[:42] + "F",
		"surrounding space":   " " + testSecretKey + " ",
		"not base64":          strings.Repeat("$", 43),
		"newline inside":      testSecretKey[:20] + "\n" + testSecretKey[20:],
		"twice the valid key": testSecretKey + testSecretKey,
	}
	for name, value := range invalidValues {
		t.Run(name, func(t *testing.T) {
			_, err := loadTorznabConfig(t, `{}`, map[string]string{envSecretKey: value})

			if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), envSecretKey) {
				t.Fatalf("gotValue %v, expectedValue a usage error naming %s", err, envSecretKey)
			}
			for start := 0; start+6 <= len(value); start++ {
				if fragment := strings.TrimSpace(value[start : start+6]); len(fragment) == 6 && strings.Contains(err.Error(), fragment) {
					t.Fatalf("gotValue %q, expectedValue no fragment of the value (%q)", err, fragment)
				}
			}
		})
	}
}

func TestConfig_secretKeyIsAKnownSecretAndNeverRendered(t *testing.T) {
	loadedConfig, err := loadTorznabConfig(t, torznabOnly, map[string]string{envSecretKey: testSecretKey})
	if err != nil {
		t.Fatal(err)
	}
	forms := []string{
		testSecretKey, base64.URLEncoding.EncodeToString(testSecretKeyBytes), base64.StdEncoding.EncodeToString(testSecretKeyBytes),
		base64.RawStdEncoding.EncodeToString(testSecretKeyBytes), hex.EncodeToString(testSecretKeyBytes), string(testSecretKeyBytes),
	}
	for _, form := range forms {
		if !slices.Contains(loadedConfig.KnownSecrets(), form) {
			t.Fatalf("gotValue KnownSecrets without the form %q", form)
		}
		if redacted := RedactText("key="+form+" end", loadedConfig.KnownSecrets()...); strings.Contains(redacted, form) {
			t.Fatalf("gotValue %q, expectedValue the form masked", redacted)
		}
	}
	var logged bytes.Buffer
	slog.New(slog.NewTextHandler(&logged, nil)).Info("config", "value", loadedConfig)
	encoded, err := json.Marshal(loadedConfig)
	if err != nil {
		t.Fatal(err)
	}
	renderings := []string{fmt.Sprint(loadedConfig), fmt.Sprintf("%+v", loadedConfig), fmt.Sprintf("%#v", loadedConfig), string(encoded), logged.String()}
	for _, rendering := range renderings {
		for _, leaked := range []string{testSecretKey[:12], "[1 1 1 1", "0x01, 0x01", "AQEBAQEB", "SecretKey:[1"} {
			if strings.Contains(rendering, leaked) {
				t.Fatalf("gotValue %q in %q", leaked, rendering)
			}
		}
	}
}
