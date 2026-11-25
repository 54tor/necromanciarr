package shellquote_test

import (
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"math/rand/v2"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"mvdan.cc/sh/v3/syntax"

	"necromanciarr/internal/fsys/sftpfs/shellquote"
)

// escapedSingleQuote est la seule partie hors guillemets simples admise par l'oracle : la barre oblique inverse suivie d'un guillemet simple.
const escapedSingleQuote = `\'`

const (
	placeholderBlockSize = 256
	privateUseFirst      = 0xF0000
	privateUseLast       = 0x10FFFD
)

// invalidBytePlaceholders remplace chaque octet d'UTF-8 invalide et chaque CR par une rune d'usage privé, car
// mvdan.cc/sh refuse l'UTF-8 invalide et retire le CR d'un CRLF, alors qu'un shell POSIX lit ces octets comme des
// caractères ordinaires (ni CR ni aucun octet ≥ 0x80 n'est de la syntaxe). Le bloc de 256 runes est choisi absent du
// script : la substitution est bijective et restore la défait.
func placeholdersForBytesTheParserMisreads(script string) (substituted string, restore func(string) string, err error) {
	base, err := freePlaceholderBase(script)
	if err != nil {
		return "", nil, err
	}
	var builder strings.Builder
	for offset := 0; offset < len(script); {
		decoded, size := utf8.DecodeRuneInString(script[offset:])
		isInvalidByte := decoded == utf8.RuneError && size == 1
		if isInvalidByte || decoded == '\r' {
			builder.WriteRune(base + rune(script[offset]))
		} else {
			builder.WriteString(script[offset : offset+size])
		}
		offset += size
	}
	restore = func(literal string) string {
		var restored strings.Builder
		for _, decoded := range literal {
			isPlaceholder := decoded >= base && decoded < base+placeholderBlockSize
			if isPlaceholder {
				restored.WriteByte(byte(decoded - base))
			} else {
				restored.WriteRune(decoded)
			}
		}
		return restored.String()
	}
	return builder.String(), restore, nil
}

func freePlaceholderBase(script string) (rune, error) {
	for base := rune(privateUseFirst); base+placeholderBlockSize-1 <= privateUseLast; base += placeholderBlockSize {
		isBlockFree := !strings.ContainsFunc(script, func(decoded rune) bool {
			return decoded >= base && decoded < base+placeholderBlockSize
		})
		if isBlockFree {
			return base, nil
		}
	}
	return 0, errors.New("no free private-use block for invalid bytes")
}

// literalWords analyse script avec l'analyseur POSIX et rend la valeur littérale de chaque mot de l'unique
// commande simple. Toute autre forme (plusieurs instructions, redirection, affectation, commentaire, expansion,
// guillemets doubles, $'…', partie littérale autre que \') est une erreur : l'oracle n'accepte que la forme
// produite par Quote.
func literalWords(script string) ([]string, error) {
	substituted, restore, err := placeholdersForBytesTheParserMisreads(script)
	if err != nil {
		return nil, err
	}
	words, err := parsedLiteralWords(substituted)
	for indexWord := range words {
		words[indexWord] = restore(words[indexWord])
	}
	return words, err
}

func parsedLiteralWords(script string) ([]string, error) {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangPOSIX), syntax.KeepComments(true)).Parse(strings.NewReader(script), "")
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if len(file.Last) != 0 {
		return nil, errors.New("trailing comment")
	}
	if len(file.Stmts) == 0 {
		return nil, nil
	}
	if len(file.Stmts) != 1 {
		return nil, fmt.Errorf("%d statements", len(file.Stmts))
	}
	statement := file.Stmts[0]
	hasStatementDecoration := statement.Negated || statement.Background || statement.Coprocess ||
		len(statement.Redirs) != 0 || len(statement.Comments) != 0
	if hasStatementDecoration {
		return nil, errors.New("statement carries an operator, a redirection or a comment")
	}
	call, isCall := statement.Cmd.(*syntax.CallExpr)
	if !isCall {
		return nil, fmt.Errorf("command is %T, not a simple command", statement.Cmd)
	}
	if len(call.Assigns) != 0 {
		return nil, errors.New("command carries an assignment")
	}
	words := make([]string, 0, len(call.Args))
	for _, word := range call.Args {
		literal, err := literalOfWord(word)
		if err != nil {
			return nil, err
		}
		words = append(words, literal)
	}
	return words, nil
}

func literalOfWord(word *syntax.Word) (string, error) {
	var literal strings.Builder
	for _, part := range word.Parts {
		switch typedPart := part.(type) {
		case *syntax.SglQuoted:
			if typedPart.Dollar {
				return "", errors.New("$'…' quoting")
			}
			literal.WriteString(typedPart.Value)
		case *syntax.Lit:
			if typedPart.Value != escapedSingleQuote {
				return "", fmt.Errorf("unquoted literal %q", typedPart.Value)
			}
			literal.WriteByte('\'')
		default:
			return "", fmt.Errorf("word part %T", part)
		}
	}
	return literal.String(), nil
}

// assertSingleLiteralWord exige que quoted soit exactement un mot égal à argument, octet pour octet.
func assertSingleLiteralWord(t *testing.T, argument, quoted string) {
	t.Helper()
	words, err := literalWords(quoted)
	if err != nil {
		t.Fatalf("Quote(%q) = %q: %v", argument, quoted, err)
	}
	if len(words) != 1 {
		t.Fatalf("Quote(%q) = %q: expectedValue 1 word, gotValue %d", argument, quoted, len(words))
	}
	if valeurObtenue := words[0]; valeurObtenue != argument {
		t.Fatalf("Quote(%q) = %q: expectedValue literal %q, gotValue %q", argument, quoted, argument, valeurObtenue)
	}
}

func FuzzQuote_roundTripsThroughPOSIXParser(f *testing.F) {
	seeds := []string{
		"a'b", "$(id)", "`id`", "-rf", "--", "\n", `'\''`, ";", "&&", "|", "*", "~", "$HOME", `\`,
		" ", "  a  b  ", "''", "'", "''''", "\xff\xfe", strings.Repeat("'", 4096), "${IFS}", "#comment",
		"\n; rm -rf /", "\r", "\t", "a\x00b", "", "\r\n", "\U000F0000\xff",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, argument string) {
		quoted, err := shellquote.Quote(argument)
		if strings.Contains(argument, "\x00") {
			if !errors.Is(err, shellquote.ErrUnquotable) {
				t.Fatalf("Quote(%q): expectedValue ErrUnquotable, gotValue %q, %v", argument, quoted, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("Quote(%q): %v", argument, err)
		}
		assertSingleLiteralWord(t, argument, quoted)
	})
}

func TestQuote_hostileNames(t *testing.T) {
	cases := []struct {
		argument       string
		valeurAttendue string
	}{
		{"a\nb", "'a\nb'"},
		{"'", `''\'''`},
		{"a'b", `'a'\''b'`},
		{`"`, `'"'`},
		{"$(id)", "'$(id)'"},
		{"`id`", "'`id`'"},
		{"-rf", "'-rf'"},
		{";", "';'"},
		{"*", "'*'"},
		{"", "''"},
	}
	for _, testCase := range cases {
		valeurObtenue, err := shellquote.Quote(testCase.argument)
		if err != nil {
			t.Fatalf("Quote(%q): %v", testCase.argument, err)
		}
		if valeurObtenue != testCase.valeurAttendue {
			t.Errorf("Quote(%q): expectedValue %q, gotValue %q", testCase.argument, testCase.valeurAttendue, valeurObtenue)
		}
		assertSingleLiteralWord(t, testCase.argument, valeurObtenue)
	}
}

func TestQuote_refusesNUL(t *testing.T) {
	for _, argument := range []string{"\x00", "a\x00b", "a\x00", "'\x00'"} {
		valeurObtenue, err := shellquote.Quote(argument)
		if !errors.Is(err, shellquote.ErrUnquotable) {
			t.Errorf("Quote(%q): expectedValue ErrUnquotable, gotValue %q, %v", argument, valeurObtenue, err)
		}
		if valeurObtenue != "" {
			t.Errorf("Quote(%q): expectedValue empty string with the error, gotValue %q", argument, valeurObtenue)
		}
	}
}

func TestQuote_keepsInvalidUTF8AndControlBytesUnaltered(t *testing.T) {
	for _, argument := range []string{"\xff\xfe", "a\xc3", "\x80", "\x1b[31m", "\r\n\t", "\u202eevil", "\u00a0", "\U000F0000\xff"} {
		valeurObtenue, err := shellquote.Quote(argument)
		if err != nil {
			t.Fatalf("Quote(%q): %v", argument, err)
		}
		if valeurAttendue := "'" + argument + "'"; valeurObtenue != valeurAttendue {
			t.Errorf("Quote(%q): expectedValue %q, gotValue %q", argument, valeurAttendue, valeurObtenue)
		}
		assertSingleLiteralWord(t, argument, valeurObtenue)
	}
}

// Un - initial n'est pas un problème de guillemets mais d'injection d'option : Quote ne le masque pas, l'appelant
// place -- avant les chemins ou n'envoie que des chemins absolus.
func TestQuote_leadingDashStaysLiteralButCallersMustUseDoubleDash(t *testing.T) {
	valeurObtenue, err := shellquote.Quote("-rf")
	if err != nil {
		t.Fatal(err)
	}
	if valeurAttendue := "'-rf'"; valeurObtenue != valeurAttendue {
		t.Fatalf("expectedValue %q, gotValue %q", valeurAttendue, valeurObtenue)
	}
	assertSingleLiteralWord(t, "-rf", valeurObtenue)
}

func TestJoin_quotesEachArgumentAndSeparatesWithOneSpace(t *testing.T) {
	valeurObtenue, err := shellquote.Join([]string{"a b", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if valeurAttendue := "'a b' 'c'"; valeurObtenue != valeurAttendue {
		t.Fatalf("expectedValue %q, gotValue %q", valeurAttendue, valeurObtenue)
	}
}

func TestJoin_emptySliceGivesEmptyString(t *testing.T) {
	valeurObtenue, err := shellquote.Join(nil)
	if err != nil || valeurObtenue != "" {
		t.Fatalf("expectedValue \"\", nil, gotValue %q, %v", valeurObtenue, err)
	}
}

func TestJoin_refusesTheFirstUnquotableArgument(t *testing.T) {
	valeurObtenue, err := shellquote.Join([]string{"ok", "a\x00b", "c"})
	if !errors.Is(err, shellquote.ErrUnquotable) || valeurObtenue != "" {
		t.Fatalf("expectedValue \"\", ErrUnquotable, gotValue %q, %v", valeurObtenue, err)
	}
}

// hostileAlphabet réunit les octets qui ont un sens pour un shell, NUL et un octet UTF-8 invalide.
const hostileAlphabet = "'\"\\$`;|&*~ -\n\t\x00\xff(){}<>#=?![]abcXYZ"

const (
	propertyIterations = 20000
	maxRandomLength    = 32
	maxRandomArguments = 6
)

func randomHostileString(generator *rand.Rand) string {
	var builder strings.Builder
	for range generator.IntN(maxRandomLength + 1) {
		builder.WriteByte(hostileAlphabet[generator.IntN(len(hostileAlphabet))])
	}
	return builder.String()
}

func TestQuote_randomHostileStringsRoundTripThroughPOSIXParser(t *testing.T) {
	generator := rand.New(rand.NewPCG(1, 2))
	for range propertyIterations {
		argument := randomHostileString(generator)
		quoted, err := shellquote.Quote(argument)
		if strings.Contains(argument, "\x00") {
			if !errors.Is(err, shellquote.ErrUnquotable) {
				t.Fatalf("Quote(%q): expectedValue ErrUnquotable, gotValue %q, %v", argument, quoted, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("Quote(%q): %v", argument, err)
		}
		assertSingleLiteralWord(t, argument, quoted)
	}
}

func TestJoin_randomHostileSlicesParseAsOneWordPerArgument(t *testing.T) {
	generator := rand.New(rand.NewPCG(3, 4))
	for range propertyIterations {
		arguments := make([]string, 1+generator.IntN(maxRandomArguments))
		for indexArgument := range arguments {
			arguments[indexArgument] = strings.ReplaceAll(randomHostileString(generator), "\x00", "")
		}
		joined, err := shellquote.Join(arguments)
		if err != nil {
			t.Fatalf("Join(%q): %v", arguments, err)
		}
		words, err := literalWords(joined)
		if err != nil {
			t.Fatalf("Join(%q) = %q: %v", arguments, joined, err)
		}
		if strings.Join(words, "\x00") != strings.Join(arguments, "\x00") || len(words) != len(arguments) {
			t.Fatalf("Join(%q) = %q: expectedValue the same %d words, gotValue %q", arguments, joined, len(arguments), words)
		}
	}
}

const localShellPath = "/bin/sh"

func requireLocalPOSIXShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX shell on Windows")
	}
	if _, err := os.Stat(localShellPath); err != nil {
		t.Skipf("%s absent: %v", localShellPath, err)
	}
}

func runInLocalShell(t *testing.T, script string) string {
	t.Helper()
	command := exec.Command(localShellPath, "-c", script) // #nosec G204 -- script de test, construit par la fonction sous test
	output, err := command.Output()
	if err != nil {
		t.Fatalf("sh -c %q: %v", script, err)
	}
	return string(output)
}

func TestQuote_roundTripsThroughShell(t *testing.T) {
	requireLocalPOSIXShell(t)
	arguments := []string{
		"a'b", "$(id)", "`id`", "-rf", "--", "a\nb", "\n; rm -rf /", `'\''`, ";", "&&", "|", "*", "~", "$HOME",
		`\`, "  a  b  ", "''", "'", "\xff\xfe", "${IFS}", "#comment", "\r", "\t", "\x1b[31m", "\u202eevil", "%s%d",
		"", "\r\n", "\x80", "a\xc3", "\ufeff", "\u2028", `a'\''b`, `x\`,
	}
	for _, argument := range arguments {
		quoted, err := shellquote.Quote(argument)
		if err != nil {
			t.Fatalf("Quote(%q): %v", argument, err)
		}
		// Le texte quoté est l'argument de printf dans la ligne de script : %s ne réinterprète pas son contenu.
		if valeurObtenue := runInLocalShell(t, "printf %s "+quoted); valeurObtenue != argument {
			t.Errorf("sh printed %s, expectedValue %s", strconv.Quote(valeurObtenue), strconv.Quote(argument))
		}
	}
}

func TestQuote_refusesNULBeforeAnyShellRuns(t *testing.T) {
	valeurObtenue, err := shellquote.Quote("a\x00")
	if !errors.Is(err, shellquote.ErrUnquotable) || valeurObtenue != "" {
		t.Fatalf("expectedValue \"\", ErrUnquotable, gotValue %q, %v", valeurObtenue, err)
	}
}

func TestShellSemanticsProbe_isBuiltByJoin(t *testing.T) {
	quotedArguments, err := shellquote.Join([]string{"a'b\\", "!x"})
	if err != nil {
		t.Fatal(err)
	}
	if valeurAttendue := "printf %s " + quotedArguments; shellquote.ShellSemanticsProbe != valeurAttendue {
		t.Fatalf("expectedValue %q, gotValue %q", valeurAttendue, shellquote.ShellSemanticsProbe)
	}
}

func TestShellSemanticsProbe_printsExpectedThroughLocalShell(t *testing.T) {
	requireLocalPOSIXShell(t)
	if valeurObtenue := runInLocalShell(t, shellquote.ShellSemanticsProbe); valeurObtenue != shellquote.ShellSemanticsExpected {
		t.Fatalf("expectedValue %q, gotValue %q", shellquote.ShellSemanticsExpected, valeurObtenue)
	}
}

func TestFreePlaceholderBase_skipsABlockUsedByTheScript(t *testing.T) {
	const valeurAttendue = privateUseFirst + placeholderBlockSize
	valeurObtenue, err := freePlaceholderBase("'\U000F0000\xff'")
	if err != nil || valeurObtenue != valeurAttendue {
		t.Fatalf("expectedValue %#x, gotValue %#x, %v", valeurAttendue, valeurObtenue, err)
	}
}

// mvdan.cc/sh sert d'oracle aux tests seulement : le binaire ne doit jamais l'embarquer.
func TestPackage_doesNotImportTheShellParserOutsideTests(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fileSet := token.NewFileSet()
	for _, entry := range entries {
		isProductionGoFile := strings.HasSuffix(entry.Name(), ".go") && !strings.HasSuffix(entry.Name(), "_test.go")
		if !isProductionGoFile {
			continue
		}
		file, err := parser.ParseFile(fileSet, entry.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, importSpec := range file.Imports {
			importPath, err := strconv.Unquote(importSpec.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(importPath, "mvdan.cc/sh") {
				t.Errorf("expectedValue no mvdan.cc/sh import in %s, gotValue %q", entry.Name(), importPath)
			}
		}
	}
}
