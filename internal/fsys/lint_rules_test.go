package fsys_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
)

// Les règles forbidigo qui portent I1 et leurs exclusions sont épinglées ici : en retirer une, ou élargir une
// exclusion (internal/**, un paquet de plus), doit passer par ce test et sa revue, pas par une ligne glissée dans
// .golangci.yml. La preuve qu'elles tirent est faite par des violations témoins (rapport de la tâche C7).

// expectedForbiddenPatterns sont les motifs interdits, dans l'ordre du fichier.
var expectedForbiddenPatterns = []string{
	`^fsys\.Volume\.OpenWriteRoot$`,
	`^os\.(Link|Symlink|WriteFile|Rename|Remove|RemoveAll|Mkdir|MkdirAll|MkdirTemp|Create|CreateTemp|OpenFile|OpenRoot|Chmod|Chown|Lchown|Truncate|Chtimes)$`,
	`^ioutil\.(WriteFile|TempFile|TempDir)$`,
	`^ssh\.InsecureIgnoreHostKey$`,
	`^ssh\.Session\.(Run|Start|Output|CombinedOutput)$`,
}

// expectedExclusionPaths sont les seuls chemins exclus de forbidigo, dans l'ordre du fichier.
var expectedExclusionPaths = []string{
	`^internal/(fsys|store)/`,
	`^(cmd/necromanciarr|internal/(app|config|crossseed|dedupe|doctor|hashsource|library|linker|pipeline|repair|web))/[^/]+_test\.go$`,
	`^internal/fsys/`,
	`^internal/pipeline/registry_test\.go$`,
	`^internal/fsys/sftpfs/exec\.go$`,
}

var (
	patternLine   = regexp.MustCompile(`(?m)^\s*- pattern: '([^']+)'`)
	exclusionLine = regexp.MustCompile(`(?m)^\s*- path(?:-except)?: '([^']+)'`)
	// forbidigoExclusion compte les règles d'exclusion qui visent forbidigo, avec ou sans chemin.
	forbidigoExclusion = regexp.MustCompile(`(?m)^\s*-? ?linters: \[[^\]]*forbidigo[^\]]*\]`)
)

func lintConfiguration(t *testing.T) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "..", ".golangci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func captured(expression *regexp.Regexp, text string) []string {
	var values []string
	for _, match := range expression.FindAllStringSubmatch(text, -1) {
		values = append(values, match[1])
	}
	return values
}

func TestLintRules_pinTheI1BansAndTheirExclusions(t *testing.T) {
	configuration := lintConfiguration(t)

	gotPatterns, gotExclusions := captured(patternLine, configuration), captured(exclusionLine, configuration)

	if !slices.Equal(gotPatterns, expectedForbiddenPatterns) {
		t.Fatalf("forbidigo patterns changed:\ngotValue      %q\nexpectedValue %q", gotPatterns, expectedForbiddenPatterns)
	}
	if !slices.Equal(gotExclusions, expectedExclusionPaths) {
		t.Fatalf("forbidigo exclusions changed:\ngotValue      %q\nexpectedValue %q", gotExclusions, expectedExclusionPaths)
	}
	// Une exclusion sans chemin couvrirait tout le dépôt : chaque exclusion de forbidigo doit en avoir un.
	if gotRules := len(forbidigoExclusion.FindAllString(configuration, -1)); gotRules != len(expectedExclusionPaths) {
		t.Fatalf("expectedValue %d forbidigo exclusions, gotValue %d", len(expectedExclusionPaths), gotRules)
	}
}
