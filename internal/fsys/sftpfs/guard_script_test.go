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
	command := exec.Command("/bin/sh", scriptPath) // #nosec G204 -- script du dépôt, ligne constante du test
	command.Env = []string{"SSH_ORIGINAL_COMMAND=" + requested, "PATH=/usr/bin:/bin"}
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
