package sftpfs_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"necromanciarr/internal/fsys/sftpfs"
	"necromanciarr/internal/fsys/sftpfs/sftptest"
	"necromanciarr/internal/fsys/sftpfs/shellquote"
)

const (
	guardScript      = "docs/reference/remote-command-guard.sh"
	guardBlockStart  = "# début des lignes autorisées"
	guardBlockEnd    = "# fin des lignes autorisées"
	guardRefusedExit = 126
)

// readGuardScript lit le script de garde en remontant depuis le dossier du test.
func readGuardScript(t *testing.T) (string, string) {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		scriptPath := filepath.Join(directory, guardScript)
		content, err := os.ReadFile(scriptPath) // #nosec G304 -- fichier du dépôt, lu par le test
		if err == nil {
			return scriptPath, string(content)
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Skipf("%s introuvable depuis le dossier du test", guardScript)
		}
		directory = parent
	}
}

// guardArm rend la branche du script pour une ligne : entre guillemets doubles (\, ", $ et ` échappés), tabulation
// écrite ${tab}.
func guardArm(line string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`", "\t", "${tab}").Replace(line)
	return `"` + escaped + `") return 0 ;;`
}

func TestGuardScript_allowsExactlyTheTableLines(t *testing.T) {
	_, script := readGuardScript(t)
	start, end := strings.Index(script, guardBlockStart), strings.Index(script, guardBlockEnd)
	if start < 0 || end < start {
		t.Fatalf("%s : bloc des lignes autorisées introuvable", guardScript)
	}
	block := strings.Split(strings.TrimSpace(script[start:end]), "\n")[1:]
	valeurObtenue := make([]string, 0, len(block))
	for _, arm := range block {
		valeurObtenue = append(valeurObtenue, strings.TrimSpace(arm))
	}
	var valeurAttendue []string
	for _, line := range sftpfs.GuardLines() {
		valeurAttendue = append(valeurAttendue, guardArm(line))
	}

	if !slices.Equal(valeurObtenue, valeurAttendue) {
		t.Fatalf("valeurObtenue\n%s\nvaleurAttendue\n%s", strings.Join(valeurObtenue, "\n"), strings.Join(valeurAttendue, "\n"))
	}
	if strings.Count(script, ") return 0 ;;") != len(valeurAttendue) {
		t.Fatalf("valeurAttendue aucune branche autorisée hors du bloc généré")
	}
}

// runGuard lance le script sous /bin/sh avec la ligne demandée ; rend sortie standard et code de sortie.
func runGuard(t *testing.T, scriptPath, requested string) ([]byte, int) {
	t.Helper()
	return runGuardWithPath(t, scriptPath, requested, "/usr/bin:/bin")
}

// runGuardWithPath est runGuard avec un PATH donné (programmes factices en tête).
func runGuardWithPath(t *testing.T, scriptPath, requested, searchPath string) ([]byte, int) {
	t.Helper()
	command := exec.Command("/bin/sh", scriptPath) // #nosec G204 -- script du dépôt, ligne constante du test
	command.Env = []string{"SSH_ORIGINAL_COMMAND=" + requested, "PATH=" + searchPath}
	var stdout bytes.Buffer
	command.Stdout, command.Stdin = &stdout, strings.NewReader("")
	err := command.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return stdout.Bytes(), exitErr.ExitCode()
	}
	if err != nil {
		t.Fatal(err)
	}
	return stdout.Bytes(), 0
}

func TestGuardScript_runsTheShellProbeAndRefusesAnyOtherLine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("script POSIX : pas de /bin/sh sous Windows")
	}
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh absent")
	}
	scriptPath, _ := readGuardScript(t)

	probeOutput, probeExit := runGuard(t, scriptPath, shellquote.ShellSemanticsProbe)

	if probeExit != 0 || string(probeOutput) != shellquote.ShellSemanticsExpected {
		t.Fatalf("valeurObtenue %q (code %d), valeurAttendue %q", probeOutput, probeExit, shellquote.ShellSemanticsExpected)
	}
	// Lignes hors table, inoffensives même si une garde fautive les laissait passer.
	for _, scratch := range []string{"'printf' 'leaked'", "printf leaked", shellquote.ShellSemanticsProbe + "; printf leaked", "", " " + sftpfs.GuardLines()[1]} {
		output, exitCode := runGuard(t, scriptPath, scratch)
		if exitCode != guardRefusedExit || len(output) != 0 {
			t.Errorf("%q : valeurObtenue %q (code %d), valeurAttendue refus %d sans sortie", scratch, output, exitCode, guardRefusedExit)
		}
	}
}

// guardStubPrograms : programmes des lignes de la table, remplacés par des scripts qui écrivent leur nom ; aucun vrai
// programme distant n'est lancé, et chaque ligne autorisée doit atteindre le sien.
var guardStubPrograms = []string{"stat", "xargs", "sha1sum", "md5sum", "nice", "ionice"}

func stubProgramsDir(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	for _, program := range guardStubPrograms {
		sftptest.WriteFile(t, filepath.Join(directory, program), []byte("#!/bin/sh\nprintf 'ran:%s' \"$(basename \"$0\")\"\n"), 0o755)
	}
	return directory
}

func TestGuardScript_dispatchesEveryTableLineAndRefusesItsMutations(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("script POSIX : pas de /bin/sh sous Windows")
	}
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh absent")
	}
	scriptPath, _ := readGuardScript(t)
	searchPath := stubProgramsDir(t) + ":/usr/bin:/bin"

	for _, line := range sftpfs.GuardLines() {
		output, exitCode := runGuardWithPath(t, scriptPath, line, searchPath)
		expectedOutput := shellquote.ShellSemanticsExpected
		if line != shellquote.ShellSemanticsProbe {
			firstProgram := strings.Trim(strings.Fields(line)[0], "'")
			expectedOutput = "ran:" + firstProgram
		}
		if exitCode != 0 || string(output) != expectedOutput {
			t.Errorf("%q : valeurObtenue %q (code %d), valeurAttendue %q (code 0)", line, output, exitCode, expectedOutput)
		}
		for _, mutated := range []string{line + " ", strings.Replace(line, "'", "\"", 1), strings.ReplaceAll(line, "\t", " ")} {
			if mutated == line {
				continue
			}
			mutatedOutput, mutatedExit := runGuardWithPath(t, scriptPath, mutated, searchPath)
			if mutatedExit != guardRefusedExit || len(mutatedOutput) != 0 {
				t.Errorf("%q : valeurObtenue %q (code %d), valeurAttendue refus %d", mutated, mutatedOutput, mutatedExit, guardRefusedExit)
			}
		}
	}
}
