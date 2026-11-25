package fsys_test

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Les règles forbidigo qui portent I1 et leurs exclusions sont épinglées ici : en retirer une, élargir une exclusion
// ou ajouter une clé de configuration (exclusions globales, dossiers ignorés) doit passer par ce test et sa revue.
// Le décodage est strict : toute clé inconnue de .golangci.yml le fait échouer. La preuve que les règles tirent est
// faite par des violations témoins (rapport de la tâche C7).

// forbidRule et exclusionRule reprennent les entrées de .golangci.yml, champ pour champ.
type forbidRule struct {
	Pattern string `yaml:"pattern"`
	Pkg     string `yaml:"pkg"`
	Msg     string `yaml:"msg"`
}

type exclusionRule struct {
	Path       string   `yaml:"path"`
	PathExcept string   `yaml:"path-except"`
	Linters    []string `yaml:"linters"`
	Text       string   `yaml:"text"`
	Source     string   `yaml:"source"`
}

type lintConfiguration struct {
	Version string `yaml:"version"`
	Linters struct {
		Default  string   `yaml:"default"`
		Enable   []string `yaml:"enable"`
		Disable  []string `yaml:"disable"`
		Settings struct {
			Forbidigo struct {
				AnalyzeTypes bool         `yaml:"analyze-types"`
				Forbid       []forbidRule `yaml:"forbid"`
			} `yaml:"forbidigo"`
		} `yaml:"settings"`
		Exclusions struct {
			Paths []string        `yaml:"paths"`
			Rules []exclusionRule `yaml:"rules"`
		} `yaml:"exclusions"`
	} `yaml:"linters"`
}

const (
	messageWrites   = "write to disk only through fsys.WriteRoot (fsys.Registry.WriteRoot)"
	messageSyscalls = "mutate the disk by system call only inside internal/fsys/local"
	messageExec     = "run external programs only from internal/hashsource/rclone.go"
	messageSession  = "remote commands run only from internal/fsys/sftpfs/exec.go"
)

var expectedForbidRules = []forbidRule{
	{Pattern: `^fsys\.Volume\.OpenWriteRoot$`, Msg: "only fsys.Registry.WriteRoot opens a write root"},
	{Pattern: `^os\.(Link|Symlink|WriteFile|Rename|Remove|RemoveAll|Mkdir|MkdirAll|MkdirTemp|Create|CreateTemp|OpenFile|OpenRoot|Chmod|Chown|Lchown|Truncate|Chtimes|CopyFS)$`, Pkg: `^os$`, Msg: messageWrites},
	{Pattern: `^os\.File\.(Chmod|Chown|Truncate|Write|WriteAt|WriteString|ReadFrom)$`, Pkg: `^os$`, Msg: messageWrites},
	{Pattern: `^ioutil\.(WriteFile|TempFile|TempDir)$`, Pkg: `^io/ioutil$`, Msg: messageWrites},
	{Pattern: `^(syscall|unix)\.(Unlink|Unlinkat|Rename|Renameat|Renameat2|Link|Linkat|Symlink|Symlinkat|Mkdir|Mkdirat|Rmdir|Chmod|Fchmod|Fchmodat|Chown|Fchown|Truncate|Ftruncate|Utimes|UtimesNano|UtimesNanoAt|Creat)$`, Pkg: `^(syscall|golang\.org/x/sys/unix)$`, Msg: messageSyscalls},
	{Pattern: `^exec\.(Command|CommandContext)$`, Pkg: `^os/exec$`, Msg: messageExec},
	{Pattern: `^ssh\.InsecureIgnoreHostKey$`, Pkg: `^golang\.org/x/crypto/ssh$`, Msg: "host keys are always checked against knownHosts"},
	{Pattern: `^ssh\.Session\.(Run|Start|Output|CombinedOutput)$`, Pkg: `^golang\.org/x/crypto/ssh$`, Msg: messageSession},
}

var forbidigoOnly = []string{"forbidigo"}

var expectedExclusionRules = []exclusionRule{
	{Path: `^internal/(fsys/(local|fsystest)|store)/`, Linters: forbidigoOnly, Text: "write to disk only through"},
	{Path: `^(cmd/necromanciarr|internal/(app|config|crossseed|dedupe|doctor|hashsource|library|linker|pipeline|repair|web))/[^/]+_test\.go$`, Linters: forbidigoOnly, Text: "write to disk only through"},
	{Path: `^internal/fsys/`, Linters: forbidigoOnly, Text: "only fsys.Registry.WriteRoot"},
	{Path: `^internal/pipeline/registry_test\.go$`, Linters: forbidigoOnly, Text: "only fsys.Registry.WriteRoot"},
	{Path: `^internal/fsys/local/hardlink_unix\.go$`, Linters: forbidigoOnly, Text: "mutate the disk by system call only"},
	{Path: `^internal/hashsource/rclone\.go$`, Linters: forbidigoOnly, Text: "run external programs only from"},
	{Path: `^internal/fsys/sftpfs/exec\.go$`, Linters: forbidigoOnly, Text: "remote commands run only from"},
}

// repositoryRoot : ce test tourne dans internal/fsys.
var repositoryRoot = filepath.Join("..", "..")

func decodeLintConfiguration(t *testing.T) lintConfiguration {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(repositoryRoot, ".golangci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)
	var configuration lintConfiguration
	if err := decoder.Decode(&configuration); err != nil {
		t.Fatalf("expectedValue .golangci.yml made only of the pinned keys, gotValue %v", err)
	}
	return configuration
}

func TestLintRules_pinTheI1BansAndTheirExclusions(t *testing.T) {
	configuration := decodeLintConfiguration(t)
	linters := configuration.Linters

	if !slices.Contains(linters.Enable, "forbidigo") || slices.Contains(linters.Disable, "forbidigo") {
		t.Fatalf("expectedValue forbidigo enabled and not disabled, gotValue enable %v disable %v", linters.Enable, linters.Disable)
	}
	if !linters.Settings.Forbidigo.AnalyzeTypes {
		t.Fatal("expectedValue forbidigo analyze-types true, gotValue false")
	}
	if len(linters.Exclusions.Paths) != 0 {
		t.Fatalf("expectedValue no global exclusion path, gotValue %v", linters.Exclusions.Paths)
	}
	if gotValue := linters.Settings.Forbidigo.Forbid; !slices.EqualFunc(gotValue, expectedForbidRules, func(got, expected forbidRule) bool { return got == expected }) {
		t.Fatalf("forbidigo rules changed:\ngotValue      %q\nexpectedValue %q", gotValue, expectedForbidRules)
	}
	if gotValue := linters.Exclusions.Rules; !slices.EqualFunc(gotValue, expectedExclusionRules, sameExclusion) {
		t.Fatalf("exclusion rules changed:\ngotValue      %q\nexpectedValue %q", gotValue, expectedExclusionRules)
	}
}

func sameExclusion(got, expected exclusionRule) bool {
	return got.Path == expected.Path && got.PathExcept == expected.PathExcept && got.Text == expected.Text &&
		got.Source == expected.Source && slices.Equal(got.Linters, expected.Linters)
}

// nolintDirective repère une directive nolint qui couvre forbidigo : nommée, ou sans liste (tous les linters).
var nolintDirective = regexp.MustCompile(`//\s*nolint(\s|$|:[^\n]*\bforbidigo\b)`)

func TestRepository_hasNoNolintDirectiveCoveringForbidigo(t *testing.T) {
	var offenders []string
	walkErr := filepath.WalkDir(repositoryRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		isSkippedDirectory := entry.IsDir() && (entry.Name() == ".git" || entry.Name() == "node_modules")
		if isSkippedDirectory {
			return filepath.SkipDir
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		content, err := os.ReadFile(path) // #nosec G304 -- fichiers du dépôt
		if err != nil {
			return err
		}
		if nolintDirective.Match(content) {
			offenders = append(offenders, filepath.ToSlash(path))
		}
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}

	if len(offenders) != 0 {
		t.Fatalf("expectedValue no nolint directive covering forbidigo, gotValue %v", offenders)
	}
}

func TestNolintDirective_flagsNamedAndBlanketDirectives(t *testing.T) {
	// Directives construites par concaténation : ce fichier ne doit pas lui-même en contenir une.
	const directive = "//" + "nolint"
	flagged := []string{"x() " + directive + ":forbidigo // reason", "x() // " + "nolint:gosec,forbidigo", "x() " + directive + "\n", "x() " + directive}
	allowed := []string{"x() " + directive + ":nilerr // reason", "x() " + directive + ":gosec"}
	for _, line := range flagged {
		if !nolintDirective.MatchString(line) {
			t.Errorf("expectedValue %q flagged, gotValue allowed", line)
		}
	}
	for _, line := range allowed {
		if nolintDirective.MatchString(line) {
			t.Errorf("expectedValue %q allowed, gotValue flagged", line)
		}
	}
}
