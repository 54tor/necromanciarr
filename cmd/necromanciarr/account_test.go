package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"necromanciarr/internal/app"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/store"
)

const (
	accountTestUsername = "alice"
	accountTestPassword = "correct horse battery staple"
	sealedSecretMarker  = "sealed:"
)

var longSecretPattern = regexp.MustCompile(`[A-Za-z0-9+/=_-]{20,}`)

type accountFixture struct {
	configDir    string
	sessionToken string
}

// createAccountWithFactors ouvre le vrai Service sur une base temporaire, crée le compte par SetupAccount, puis pose
// un TOTP scellé et une clé de second facteur directement dans le store.
func createAccountWithFactors(t *testing.T) accountFixture {
	t.Helper()
	useEnvironment(t, nil)
	configDir := t.TempDir()
	fixture := createAccountWithoutFactors(t, configDir)
	openedStore, err := store.Open(filepath.Join(configDir, testDatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = openedStore.Close() }()
	ctx := context.Background()
	factor := store.TOTPFactor{SecretSealed: []byte(sealedSecretMarker + "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"), CreatedAt: time.Now()}
	if err := openedStore.SetTOTP(ctx, factor); err != nil {
		t.Fatal(err)
	}
	credential := store.WebAuthnCredential{ID: []byte("key-1"), Name: "yubikey", Kind: "second_factor", Record: []byte(`{}`), CreatedAt: time.Now()}
	if err := openedStore.AddCredential(ctx, credential); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func createAccountWithoutFactors(t *testing.T, configDir string) accountFixture {
	t.Helper()
	arguments := []string{"--config-dir", configDir}
	settings, err := loadConfig(arguments)
	if err != nil {
		t.Fatal(err)
	}
	service, closer, err := openService(context.Background(), settings, storedConfigLoader(arguments))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closer.Close() }()
	grant, err := service.SetupAccount(context.Background(), accountTestUsername, accountTestPassword, app.ClientInfo{})
	if err != nil {
		t.Fatal(err)
	}
	return accountFixture{configDir: configDir, sessionToken: grant.Token}
}

func sessionResumes(t *testing.T, fixture accountFixture) bool {
	t.Helper()
	arguments := []string{"--config-dir", fixture.configDir}
	settings, err := loadConfig(arguments)
	if err != nil {
		t.Fatal(err)
	}
	service, closer, err := openService(context.Background(), settings, storedConfigLoader(arguments))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closer.Close() }()
	_, resumeErr := service.ResumeSession(context.Background(), fixture.sessionToken)
	return resumeErr == nil
}

func TestAccountStatus_reportsNoAccountThenTheAccountWithItsFactors(t *testing.T) {
	useEnvironment(t, nil)
	emptyConfigDir := t.TempDir()

	withoutAccount := runCli(t, emptyConfigDir, "account", "status")
	withoutAccountJSON := runCli(t, emptyConfigDir, "account", "status", "--json")
	fixture := createAccountWithFactors(t)
	withAccount := runCli(t, fixture.configDir, "account", "status")
	withAccountJSON := runCli(t, fixture.configDir, "account", "status", "--json")

	if withoutAccount.exitCode != successExitCode || !strings.Contains(withoutAccount.stdout, "first-launch token") {
		t.Fatalf("valeurObtenue exit %d stdout %q, valeurAttendue la phrase du jeton de premier lancement", withoutAccount.exitCode, withoutAccount.stdout)
	}
	var summaryObtained accountStatusView
	if err := json.Unmarshal([]byte(withoutAccountJSON.stdout), &summaryObtained); err != nil || summaryObtained.Exists {
		t.Fatalf("valeurObtenue %q (%v), valeurAttendue un résumé sans compte", withoutAccountJSON.stdout, err)
	}
	for _, expectedLine := range []string{"Account: alice", "Second-factor keys: 1", "Passkeys: 0", "TOTP: set"} {
		if !strings.Contains(withAccount.stdout, expectedLine) {
			t.Fatalf("valeurObtenue %q, valeurAttendue la ligne %q", withAccount.stdout, expectedLine)
		}
	}
	summaryExpected := accountStatusView{Exists: true, Username: accountTestUsername, SecondFactorKeys: 1, TOTP: true}
	if err := json.Unmarshal([]byte(withAccountJSON.stdout), &summaryObtained); err != nil || summaryObtained != summaryExpected {
		t.Fatalf("valeurObtenue %+v (%v), valeurAttendue %+v", summaryObtained, err, summaryExpected)
	}
}

func TestAccountStatus_neverPrintsAHashOrASecret(t *testing.T) {
	fixture := createAccountWithFactors(t)

	for _, arguments := range [][]string{{"account", "status"}, {"account", "status", "--json"}} {
		result := runCli(t, fixture.configDir, arguments...)
		combined := result.stdout + result.stderr

		forbidden := []string{accountTestPassword, "$argon2id$", sealedSecretMarker, fixture.sessionToken}
		for _, text := range forbidden {
			if strings.Contains(combined, text) {
				t.Fatalf("%v: valeurObtenue %q contient %q, valeurAttendue aucun secret", arguments, combined, text)
			}
		}
		if long := longSecretPattern.FindString(combined); long != "" {
			t.Fatalf("%v: valeurObtenue le motif long %q, valeurAttendue aucun secret de 20 caractères", arguments, long)
		}
	}
}

func TestAccountReset_requiresYes(t *testing.T) {
	fixture := createAccountWithFactors(t)

	result := runCli(t, fixture.configDir, "account", "reset")
	status := runCli(t, fixture.configDir, "account", "status")

	if result.exitCode != usageExitCode || !strings.Contains(result.stderr+result.stdout, "--yes") {
		t.Fatalf("valeurObtenue exit %d (%q %q), valeurAttendue %d et l'explication de --yes", result.exitCode, result.stdout, result.stderr, usageExitCode)
	}
	if !strings.Contains(status.stdout, "Account: alice") || !sessionResumes(t, fixture) {
		t.Fatalf("valeurObtenue %q, valeurAttendue le compte et la session intacts", status.stdout)
	}
}

func TestAccountReset_removesAccountKeysTotpAndSessionsAndReopensSetup(t *testing.T) {
	fixture := createAccountWithFactors(t)

	result := runCli(t, fixture.configDir, "account", "reset", "--yes")
	status := runCli(t, fixture.configDir, "account", "status")

	if result.exitCode != successExitCode || !strings.Contains(result.stdout, "first-launch token") {
		t.Fatalf("valeurObtenue exit %d stdout %q stderr %q, valeurAttendue la suite à donner", result.exitCode, result.stdout, result.stderr)
	}
	if !strings.Contains(status.stdout, "No account") {
		t.Fatalf("valeurObtenue %q, valeurAttendue aucun compte", status.stdout)
	}
	if sessionResumes(t, fixture) {
		t.Fatal("valeurObtenue une session reprise, valeurAttendue la session effacée")
	}
	reopened := createAccountWithoutFactors(t, fixture.configDir)
	if reopened.sessionToken == "" {
		t.Fatal("valeurObtenue aucune session, valeurAttendue un compte recréable")
	}
}

func TestAccountReset_worksOnAnInvalidStoredConfiguration(t *testing.T) {
	fixture := createAccountWithFactors(t)
	writeStoredRows(t, fixture.configDir, "123", nil)

	status := runCli(t, fixture.configDir, "account", "status")
	result := runCli(t, fixture.configDir, "account", "reset", "--yes")
	after := runCli(t, fixture.configDir, "account", "status")

	if status.exitCode != successExitCode || result.exitCode != successExitCode {
		t.Fatalf("valeurObtenue status %d (%s) reset %d (%s), valeurAttendue deux succès", status.exitCode, status.stderr, result.exitCode, result.stderr)
	}
	if !strings.Contains(after.stdout, "No account") {
		t.Fatalf("valeurObtenue %q, valeurAttendue aucun compte", after.stdout)
	}
}

func TestAccountCommands_doNotRunTheSecretBootstrap(t *testing.T) {
	useEnvironment(t, nil)
	emptyConfigDir := t.TempDir()

	runCli(t, emptyConfigDir, "account", "status")
	runCli(t, emptyConfigDir, "account", "reset", "--yes")
	entries, err := os.ReadDir(emptyConfigDir)
	if err != nil {
		t.Fatal(err)
	}

	for _, entry := range entries {
		if entry.Name() == ".env" {
			t.Fatal("valeurObtenue un .env créé, valeurAttendue aucun amorçage des secrets")
		}
	}
}

func TestAccountCommands_areCLIOnlyWithNoActionNorWebRouteAndListedInTheUsage(t *testing.T) {
	registeredActions := app.Actions(i18n.New(i18n.English, nil))
	routes := goldenRoutes(t)
	usage := usageText(i18n.New(i18n.English, nil))

	for _, spec := range accountCommands {
		name := spec.name()
		hasAction := slices.ContainsFunc(registeredActions, func(action app.Action) bool { return action.Name == name })
		hasRoute := slices.ContainsFunc(routes, func(route routeEntry) bool {
			return route.Action == name || slices.Contains(strings.Split(route.Path, "/"), spec.path[0])
		})
		if hasAction || hasRoute || !strings.Contains(usage, name) {
			t.Fatalf("%q: valeurObtenue action %v, route %v, usage %v, valeurAttendue une commande de la CLI seule listée", name, hasAction, hasRoute, strings.Contains(usage, name))
		}
	}
}

func TestAccountReset_onlyTheExactYesFlagConfirms(t *testing.T) {
	fixture := createAccountWithFactors(t)
	configDirArguments := []string{"--config-dir", fixture.configDir}
	refusedInvocations := [][]string{
		{"--json", "yes"},
		{"--config-dir", fixture.configDir, "yes"},
		{"-yes"},
		{"--yes=false"},
		{"--yes=0"},
		{"--yes=true"},
		{"-y"},
		{"--ye"},
		{"yes"},
	}

	for _, invocationArguments := range refusedInvocations {
		var stdout, stderr bytes.Buffer
		arguments := append([]string{"account", "reset"}, invocationArguments...)
		exitCodeObtained := run(append(arguments, append(configDirArguments, "--language", "en")...), &stdout, &stderr)

		if exitCodeObtained != usageExitCode {
			t.Fatalf("%v: valeurObtenue exit %d, valeurAttendue %d", invocationArguments, exitCodeObtained, usageExitCode)
		}
		statusObtained := runCli(t, fixture.configDir, "account", "status")
		if !strings.Contains(statusObtained.stdout, "Account: alice") {
			t.Fatalf("%v: valeurObtenue %q, valeurAttendue le compte intact", invocationArguments, statusObtained.stdout)
		}
	}
	accepted := runCli(t, fixture.configDir, "account", "reset", "--yes")
	if accepted.exitCode != successExitCode {
		t.Fatalf("valeurObtenue exit %d, valeurAttendue 0 pour --yes", accepted.exitCode)
	}
}
