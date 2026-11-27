package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func noEnvironment(string) (string, bool) { return "", false }

func bootstrapArgs(configDir string) []string { return []string{"--config-dir", configDir} }

func readEnvFile(t *testing.T, configDir string) string {
	t.Helper()
	contenu, err := os.ReadFile(filepath.Join(configDir, ".env"))
	if err != nil {
		t.Fatalf("read .env: %v", err)
	}
	return string(contenu)
}

// skipWithoutUnixPermissions : sous Windows, chmod ne pose ni le mode 0600 ni un dossier non inscriptible.
func skipWithoutUnixPermissions(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("les modes Unix n'existent pas sous Windows")
	}
}

func TestEnsureBootstrap_createsEnvFileWithBothSecretsMode0600(t *testing.T) {
	skipWithoutUnixPermissions(t)
	configDir := t.TempDir()

	result, err := EnsureBootstrap(bootstrapArgs(configDir), noEnvironment, configDir)
	if err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}

	info, err := os.Stat(filepath.Join(configDir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if modeObtenu := info.Mode().Perm(); modeObtenu != 0o600 {
		t.Errorf("modeAttendu 0600, modeObtenu %o", modeObtenu)
	}
	variables, err := readDotEnv(filepath.Join(configDir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if longueurObtenue := len(variables["NECROMANCIARR_SECRET_KEY"]); longueurObtenue != 43 {
		t.Errorf("longueurAttendue 43, longueurObtenue %d", longueurObtenue)
	}
	if len(variables["NECROMANCIARR_AUTH_TOKEN"]) < MinAuthTokenLength {
		t.Errorf("auth token too short: %d", len(variables["NECROMANCIARR_AUTH_TOKEN"]))
	}
	if valeurAttendue := variables["NECROMANCIARR_AUTH_TOKEN"]; result.AuthToken != valeurAttendue {
		t.Errorf("valeurAttendue %q, valeurObtenue %q", valeurAttendue, result.AuthToken)
	}
	if len(result.Created) != 2 {
		t.Errorf("Created attendu 2 noms, obtenu %v", result.Created)
	}
}

func TestEnsureBootstrap_keepsExistingContentAndAppends(t *testing.T) {
	configDir := t.TempDir()
	contenuInitial := "# note\nLIBRARY_PROVIDER_TOKEN=x\n"
	if err := os.WriteFile(filepath.Join(configDir, ".env"), []byte(contenuInitial), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := EnsureBootstrap(bootstrapArgs(configDir), noEnvironment, configDir); err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}

	contenuObtenu := readEnvFile(t, configDir)
	if !strings.HasPrefix(contenuObtenu, contenuInitial) {
		t.Errorf("contenu initial modifié : %q", contenuObtenu)
	}
	if !strings.Contains(contenuObtenu, "NECROMANCIARR_SECRET_KEY=") || !strings.Contains(contenuObtenu, "NECROMANCIARR_AUTH_TOKEN=") {
		t.Errorf("secrets non ajoutés : %q", contenuObtenu)
	}
}

func TestEnsureBootstrap_neverOverwritesAPresentValue(t *testing.T) {
	t.Run("key in .env only the token is added", func(t *testing.T) {
		configDir := t.TempDir()
		contenuInitial := "NECROMANCIARR_SECRET_KEY=existing\n"
		if err := os.WriteFile(filepath.Join(configDir, ".env"), []byte(contenuInitial), 0o600); err != nil {
			t.Fatal(err)
		}
		result, err := EnsureBootstrap(bootstrapArgs(configDir), noEnvironment, configDir)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Created) != 1 || result.Created[0] != "NECROMANCIARR_AUTH_TOKEN" {
			t.Errorf("Created obtenu %v", result.Created)
		}
		if !strings.HasPrefix(readEnvFile(t, configDir), contenuInitial) {
			t.Error("clé existante modifiée")
		}
	})
	t.Run("key in environment writes nothing for it", func(t *testing.T) {
		configDir := t.TempDir()
		environment := func(name string) (string, bool) {
			return "fromenv", name == "NECROMANCIARR_SECRET_KEY"
		}
		if _, err := EnsureBootstrap(bootstrapArgs(configDir), environment, configDir); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(readEnvFile(t, configDir), "NECROMANCIARR_SECRET_KEY=") {
			t.Error("clé de l'environnement réécrite")
		}
	})
}

func TestEnsureBootstrap_doesNothingWhenBothPresent(t *testing.T) {
	configDir := t.TempDir()
	contenuInitial := "NECROMANCIARR_SECRET_KEY=a\nNECROMANCIARR_AUTH_TOKEN=b\n"
	if err := os.WriteFile(filepath.Join(configDir, ".env"), []byte(contenuInitial), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := EnsureBootstrap(bootstrapArgs(configDir), noEnvironment, configDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Created) != 0 || result.AuthToken != "" {
		t.Errorf("résultat non vide : %+v", result)
	}
	if contenuObtenu := readEnvFile(t, configDir); contenuObtenu != contenuInitial {
		t.Errorf("valeurAttendue %q, valeurObtenue %q", contenuInitial, contenuObtenu)
	}
}

func TestEnsureBootstrap_refusesWhenDatabaseExistsWithoutKey(t *testing.T) {
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "necromanciarr.db"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := EnsureBootstrap(bootstrapArgs(configDir), noEnvironment, configDir)
	if !errors.Is(err, ErrBootstrapWouldOrphanSecrets) {
		t.Fatalf("erreur obtenue %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(configDir, ".env")); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf(".env écrit malgré le refus : %v", statErr)
	}
}

func TestEnsureBootstrap_reportsUnwritableDirectory(t *testing.T) {
	skipWithoutUnixPermissions(t)
	configDir := t.TempDir()
	if err := os.Chmod(configDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(configDir, 0o700) })

	_, err := EnsureBootstrap(bootstrapArgs(configDir), noEnvironment, configDir)
	if !errors.Is(err, ErrBootstrapNotWritable) {
		t.Fatalf("erreur obtenue %v", err)
	}
	for _, nomVariable := range []string{"NECROMANCIARR_SECRET_KEY", "NECROMANCIARR_AUTH_TOKEN"} {
		if !strings.Contains(err.Error(), nomVariable) {
			t.Errorf("message sans %s : %v", nomVariable, err)
		}
	}
}

func TestEnsureBootstrap_concurrentCallsLeaveOneKey(t *testing.T) {
	configDir := t.TempDir()
	var attente sync.WaitGroup
	for range 8 {
		attente.Add(1)
		go func() {
			defer attente.Done()
			if _, err := EnsureBootstrap(bootstrapArgs(configDir), noEnvironment, configDir); err != nil {
				t.Error(err)
			}
		}()
	}
	attente.Wait()

	if nombreObtenu := strings.Count(readEnvFile(t, configDir), "NECROMANCIARR_SECRET_KEY="); nombreObtenu != 1 {
		t.Errorf("nombreAttendu 1, nombreObtenu %d", nombreObtenu)
	}
}

func TestEnsureBootstrap_generatesBothWhenEnvExampleLinesAreBlank(t *testing.T) {
	configDir := t.TempDir()
	contenuInitial := "# copied from .env.example\nLIBRARY_PROVIDER_TOKEN=x\nNECROMANCIARR_SECRET_KEY=\nNECROMANCIARR_AUTH_TOKEN=\n"
	if err := os.WriteFile(filepath.Join(configDir, ".env"), []byte(contenuInitial), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := EnsureBootstrap(bootstrapArgs(configDir), noEnvironment, configDir)
	if err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}

	if len(result.Created) != 2 {
		t.Errorf("Created attendu 2 noms, obtenu %v", result.Created)
	}
	if !strings.HasPrefix(readEnvFile(t, configDir), contenuInitial) {
		t.Error("contenu initial modifié")
	}
	variables, err := readDotEnv(filepath.Join(configDir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if variables["NECROMANCIARR_SECRET_KEY"] == "" || variables["NECROMANCIARR_AUTH_TOKEN"] == "" {
		t.Errorf("secrets encore vides : %v", variables)
	}
}

func TestEnsureBootstrap_createsMissingDefaultConfigDirMode0700(t *testing.T) {
	skipWithoutUnixPermissions(t)
	defaultDir := filepath.Join(t.TempDir(), "absent", "necromanciarr")

	if _, err := EnsureBootstrap(nil, noEnvironment, defaultDir); err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}

	info, err := os.Stat(defaultDir)
	if err != nil {
		t.Fatal(err)
	}
	if modeObtenu := info.Mode().Perm(); modeObtenu != 0o700 {
		t.Errorf("modeAttendu 0700, modeObtenu %o", modeObtenu)
	}
	if _, err := os.Stat(filepath.Join(defaultDir, ".env")); err != nil {
		t.Errorf(".env absent : %v", err)
	}
}

func TestEnsureBootstrap_explicitMissingConfigDirStillFails(t *testing.T) {
	missingDir := filepath.Join(t.TempDir(), "absent")
	environment := func(name string) (string, bool) { return missingDir, name == "CONFIG_DIR" }
	cases := map[string]func() error{
		"flag": func() error {
			_, err := EnsureBootstrap(bootstrapArgs(missingDir), noEnvironment, missingDir)
			return err
		},
		"environment": func() error {
			_, err := EnsureBootstrap(nil, environment, missingDir)
			return err
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			if err := run(); err == nil || errors.Is(err, ErrBootstrapNotWritable) {
				t.Errorf("erreur de dossier inexistant attendue, obtenue %v", err)
			}
			if _, statErr := os.Stat(missingDir); !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("dossier explicite créé : %v", statErr)
			}
		})
	}
}

func TestEnsureBootstrap_tightensLooseEnvFileMode(t *testing.T) {
	skipWithoutUnixPermissions(t)
	configDir := t.TempDir()
	envPath := filepath.Join(configDir, ".env")
	if err := os.WriteFile(envPath, []byte("OTHER=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(envPath, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := EnsureBootstrap(bootstrapArgs(configDir), noEnvironment, configDir); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if modeObtenu := info.Mode().Perm(); modeObtenu != 0o600 {
		t.Errorf("modeAttendu 0600, modeObtenu %o", modeObtenu)
	}
}

func TestEnsureBootstrap_appendsToWorkingDirEnvWhenConfigDirHasNone(t *testing.T) {
	configDir := t.TempDir()
	workingDir := t.TempDir()
	contenuInitial := "NECROMANCIARR_SECRET_KEY=existing\nOTHER=1\n"
	if err := os.WriteFile(filepath.Join(workingDir, ".env"), []byte(contenuInitial), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workingDir)

	result, err := EnsureBootstrap(bootstrapArgs(configDir), noEnvironment, configDir)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Created) != 1 || result.Created[0] != "NECROMANCIARR_AUTH_TOKEN" {
		t.Errorf("Created obtenu %v", result.Created)
	}
	if _, statErr := os.Stat(filepath.Join(configDir, ".env")); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf(".env créé dans le dossier de configuration : %v", statErr)
	}
	if contenuObtenu := readEnvFile(t, workingDir); !strings.HasPrefix(contenuObtenu, contenuInitial) || !strings.Contains(contenuObtenu, "NECROMANCIARR_AUTH_TOKEN=") {
		t.Errorf("contenuObtenu %q", contenuObtenu)
	}
}

func TestEnsureBootstrap_doesNotGlueLinesWhenEnvLacksTrailingNewline(t *testing.T) {
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, ".env"), []byte("OTHER=1"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := EnsureBootstrap(bootstrapArgs(configDir), noEnvironment, configDir); err != nil {
		t.Fatal(err)
	}

	variables, err := readDotEnv(filepath.Join(configDir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if valeurObtenue := variables["OTHER"]; valeurObtenue != "1" {
		t.Errorf("valeurAttendue \"1\", valeurObtenue %q", valeurObtenue)
	}
	if variables["NECROMANCIARR_SECRET_KEY"] == "" || variables["NECROMANCIARR_AUTH_TOKEN"] == "" {
		t.Errorf("secrets collés à la ligne précédente : %v", variables)
	}
}
