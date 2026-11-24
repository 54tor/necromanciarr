package i18n

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var placeholderPattern = regexp.MustCompile(`\{([a-z_]+)\}`)

func TestParseLanguage_rejectsUnknownValue(t *testing.T) {
	for _, rawValue := range []string{"de", "fr_FR", "fr-FR", "", "f r"} {
		if _, err := ParseLanguage(rawValue); !errors.Is(err, ErrUnknownLanguage) {
			t.Fatalf("gotValue %v for %q, expectedValue ErrUnknownLanguage", err, rawValue)
		}
	}
	for _, testCase := range []struct {
		rawValue      string
		expectedValue Language
	}{{"\tFR ", French}, {"en", English}, {"En\n", English}} {
		gotValue, err := ParseLanguage(testCase.rawValue)
		if err != nil || gotValue != testCase.expectedValue {
			t.Fatalf("gotValue %q (%v) for %q, expectedValue %q", gotValue, err, testCase.rawValue, testCase.expectedValue)
		}
	}
}

func TestCatalog_frenchAndEnglishHaveSameKeys(t *testing.T) {
	frenchKeys := slices.Sorted(maps.Keys(french))
	englishKeys := slices.Sorted(maps.Keys(english))

	if !slices.Equal(frenchKeys, englishKeys) {
		t.Fatalf("gotValue fr %v, expectedValue the same keys as en %v", frenchKeys, englishKeys)
	}
}

func placeholdersOf(template string) []string {
	var names []string
	for _, match := range placeholderPattern.FindAllStringSubmatch(template, -1) {
		names = append(names, match[1])
	}
	slices.Sort(names)
	return slices.Compact(names)
}

func TestCatalog_templatesUseOnlyDeclaredParams(t *testing.T) {
	for key, frenchTemplate := range french {
		expectedValue := placeholdersOf(frenchTemplate)
		gotValue := placeholdersOf(english[key])
		if !slices.Equal(gotValue, expectedValue) {
			t.Errorf("key %q: en params %v, expectedValue fr params %v", key, gotValue, expectedValue)
		}
		if strings.Contains(frenchTemplate, "%") || strings.Contains(english[key], "%") {
			t.Errorf("key %q: a fmt verb in a template", key)
		}
	}
}

func declaredKeyConstants(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]string{}
	fileSet := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fileSet, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		collectKeyConstants(parsed, declared)
	}
	return declared
}

func collectKeyConstants(parsed *ast.File, declared map[string]string) {
	for _, declaration := range parsed.Decls {
		generic, isGeneric := declaration.(*ast.GenDecl)
		if !isGeneric || generic.Tok != token.CONST {
			continue
		}
		for _, spec := range generic.Specs {
			valueSpec := spec.(*ast.ValueSpec)
			typeName, isNamed := valueSpec.Type.(*ast.Ident)
			if !isNamed || typeName.Name != "Key" {
				continue
			}
			for position, name := range valueSpec.Names {
				literal := valueSpec.Values[position].(*ast.BasicLit)
				value, _ := strconv.Unquote(literal.Value)
				declared[name.Name] = value
			}
		}
	}
}

func TestCatalog_everyKeyConstantHasBothTranslations(t *testing.T) {
	declared := declaredKeyConstants(t)
	if len(declared) < 4 {
		t.Fatalf("gotValue %d constants, expectedValue the seeded keys to be found", len(declared))
	}
	registered := map[Key]bool{}
	for _, key := range Keys() {
		registered[key] = true
	}
	for name, value := range declared {
		key := Key(value)
		if !registered[key] {
			t.Errorf("constant %s (%q) is missing from Keys()", name, value)
		}
		if _, found := french[key]; !found {
			t.Errorf("constant %s (%q) has no French text", name, value)
		}
		if _, found := english[key]; !found {
			t.Errorf("constant %s (%q) has no English text", name, value)
		}
	}
	if len(registered) != len(declared) {
		t.Errorf("Keys() has %d entries, expectedValue %d declared constants", len(registered), len(declared))
	}
}

func TestTranslator_redactsSecretParams(t *testing.T) {
	const secret = "s3cret-token-value"
	redact := func(text string) string { return strings.ReplaceAll(text, secret, "***") }
	translator := New(French, redact)

	gotValue := translator.Render(Message{Key: KeyLegacyText, Params: map[string]string{"text": "token " + secret}})

	if strings.Contains(gotValue, secret) || !strings.Contains(gotValue, "***") {
		t.Fatalf("gotValue %q, expectedValue the secret masked", gotValue)
	}
}

func TestTranslator_countUsesFrenchAndEnglishPluralRules(t *testing.T) {
	catalog := map[Key]string{"one": "{count} fichier", "many": "{count} fichiers"}
	for _, testCase := range []struct {
		language      Language
		count         int
		expectedValue string
	}{
		{French, 0, "0 fichier"},
		{French, 1, "1 fichier"},
		{French, 2, "2 fichiers"},
		{English, 0, "0 fichiers"},
		{English, 1, "1 fichier"},
		{English, 2, "2 fichiers"},
	} {
		translator := Translator{language: testCase.language, catalog: catalog, redact: identity}
		if gotValue := translator.Count("one", "many", testCase.count, nil); gotValue != testCase.expectedValue {
			t.Errorf("%s %d: gotValue %q, expectedValue %q", testCase.language, testCase.count, gotValue, testCase.expectedValue)
		}
	}
}

func TestTranslator_missingParamLeavesPlaceholder(t *testing.T) {
	translator := New(English, nil)

	gotValue := translator.Text(KeyConfigCheckLanguage, map[string]string{"language": "en"})

	if expectedValue := "Language: en (source: {source})"; gotValue != expectedValue {
		t.Fatalf("gotValue %q, expectedValue %q", gotValue, expectedValue)
	}
}

func TestTranslator_paramValuesAreNotRescanned(t *testing.T) {
	translator := New(English, nil)

	gotValue := translator.Text(KeyConfigCheckLanguage, map[string]string{"language": "{source}", "source": "env"})

	if expectedValue := "Language: {source} (source: env)"; gotValue != expectedValue {
		t.Fatalf("gotValue %q, expectedValue %q", gotValue, expectedValue)
	}
}

func TestTranslator_rendersInTheChosenLanguage(t *testing.T) {
	params := map[string]string{"value": "de"}

	frenchText := New(French, nil).Text(KeyErrorUsageUnknownLanguage, params)
	englishText := New(English, nil).Text(KeyErrorUsageUnknownLanguage, params)

	if expectedValue := "langue inconnue « de » : valeurs admises fr, en"; frenchText != expectedValue {
		t.Fatalf("gotValue %q, expectedValue %q", frenchText, expectedValue)
	}
	if expectedValue := `unknown language "de": allowed values are fr, en`; englishText != expectedValue {
		t.Fatalf("gotValue %q, expectedValue %q", englishText, expectedValue)
	}
}

func TestDecodeMessages_acceptsLegacyStrings(t *testing.T) {
	expectedValue := []Message{
		{Key: KeyLegacyText, Params: map[string]string{"text": "old warning"}},
		{Key: KeyErrorInternal},
		{Key: KeyConfigCheckLanguage, Params: map[string]string{"language": "fr", "source": "flag"}},
	}

	gotValue, err := DecodeMessages([]byte(`["old warning", {"key":"error.internal"},
		{"key":"config.check.language","params":{"language":"fr","source":"flag"}}]`))

	if err != nil || !reflect.DeepEqual(gotValue, expectedValue) {
		t.Fatalf("gotValue %+v (%v), expectedValue %+v", gotValue, err, expectedValue)
	}
}

func TestDecodeMessages_rejectsMalformedElements(t *testing.T) {
	for _, raw := range []string{`{"key":"x"}`, `[1]`, `[{"params":{}}]`, `[null]`, ``} {
		if _, err := DecodeMessages([]byte(raw)); err == nil {
			t.Errorf("expectedValue an error for %q", raw)
		}
	}
}
