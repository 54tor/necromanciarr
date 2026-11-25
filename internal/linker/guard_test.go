package linker

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// bannedImports : le linker ne touche le disque qu'à travers fsys ; os, syscall, x/sys et exec ouvriraient une
// écriture hors de toute WriteRoot (os.OpenRoot, syscall.Link, une commande externe…).
var bannedImports = []string{"os", "os/exec", "syscall", "io/ioutil"}

const bannedImportPrefix = "golang.org/x/sys/"

// forbiddenOsWrites : défense en profondeur si l'interdiction d'import était levée par erreur.
var forbiddenOsWrites = []string{
	"Link", "Symlink", "WriteFile", "Rename", "Remove", "RemoveAll", "Mkdir", "MkdirAll", "MkdirTemp", "Create",
	"CreateTemp", "OpenFile", "OpenRoot", "Chmod", "Chown", "Lchown", "Truncate", "Chtimes",
}

// writeRootConstructor n'est appelé que par fsys.Registry.WriteRoot.
const writeRootConstructor = "OpenWriteRoot"

func TestLinker_nonTestFilesUseNoDiskAccessOutsideFsys(t *testing.T) {
	var gotValue []string
	for _, sourceFile := range nonTestSourceFiles(t, ".") {
		gotValue = append(gotValue, forbiddenUsesIn(parseFile(t, sourceFile, nil))...)
	}
	if len(gotValue) != 0 {
		t.Fatalf("expectedValue no disk access outside fsys in linker, gotValue %v", gotValue)
	}
}

func TestForbiddenUsesIn_flagsEveryBypass(t *testing.T) {
	cases := map[string]string{
		"dot import of os":   "import . \"os\"\nfunc f() { _ = Link(\"a\", \"b\") }",
		"blank import of os": "import _ \"os\"",
		"aliased os.Link":    "import system \"os\"\nfunc f() { _ = system.Link(\"a\", \"b\") }",
		"os.OpenRoot":        "import \"os\"\nfunc f() { _, _ = os.OpenRoot(\"/\") }",
		"syscall.Link":       "import \"syscall\"\nfunc f() { _ = syscall.Link(\"a\", \"b\") }",
		"x/sys/unix":         "import \"golang.org/x/sys/unix\"\nfunc f() { _ = unix.Linkat(0, \"a\", 0, \"b\", 0) }",
		"os/exec":            "import \"os/exec\"\nfunc f() { _ = exec.Command(\"ln\") }",
		"io/ioutil":          "import \"io/ioutil\"\nfunc f() { _ = ioutil.WriteFile }",
		"OpenWriteRoot":      "func f(volume interface{ OpenWriteRoot(string) (any, error) }) { _, _ = volume.OpenWriteRoot(\"/\") }",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			gotValue := forbiddenUsesIn(parseFile(t, "fixture.go", "package linker\n"+body+"\n"))
			if len(gotValue) == 0 {
				t.Fatal("expectedValue the bypass flagged, gotValue nothing")
			}
		})
	}
	clean := "package linker\nimport \"path/filepath\"\nfunc f() { _ = filepath.Join(\"a\") }\n"
	if gotValue := forbiddenUsesIn(parseFile(t, "fixture.go", clean)); len(gotValue) != 0 {
		t.Fatalf("expectedValue nothing flagged in clean code, gotValue %v", gotValue)
	}
}

// writeRootCallAllowList : hors de internal/fsys, seuls ces fichiers appellent OpenWriteRoot, avec leur raison.
var writeRootCallAllowList = map[string]string{
	"internal/pipeline/registry_test.go": "volume de test qui relaie OpenWriteRoot au volume local enveloppé ; seul le registre l'appelle",
}

func TestTree_callsOpenWriteRootOnlyInsideFsys(t *testing.T) {
	repositoryRoot := filepath.Join("..", "..")
	var gotValue []string
	for _, tree := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(repositoryRoot, tree), func(entryPath string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			relative, err := filepath.Rel(repositoryRoot, entryPath)
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			if entry.IsDir() && relative == "internal/fsys" {
				return filepath.SkipDir
			}
			_, allowed := writeRootCallAllowList[relative]
			if entry.IsDir() || !strings.HasSuffix(relative, ".go") || allowed {
				return nil
			}
			gotValue = append(gotValue, writeRootCallsIn(parseFile(t, entryPath, nil))...)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(gotValue) != 0 {
		t.Fatalf("expectedValue OpenWriteRoot called only by internal/fsys, gotValue %v", gotValue)
	}
}

type parsedFile struct {
	fileSet *token.FileSet
	file    *ast.File
}

func parseFile(t *testing.T, name string, source any) parsedFile {
	t.Helper()
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, name, source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	return parsedFile{fileSet: fileSet, file: file}
}

func nonTestSourceFiles(t *testing.T, directory string) []string {
	t.Helper()
	sourceFiles, err := filepath.Glob(filepath.Join(directory, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	isTest := func(sourceFile string) bool { return strings.HasSuffix(sourceFile, "_test.go") }
	return slices.DeleteFunc(sourceFiles, isTest)
}

// forbiddenUsesIn relève les imports interdits (quel que soit leur nom, point ou blanc compris), les appels
// os.<écriture> et tout appel à OpenWriteRoot.
func forbiddenUsesIn(parsed parsedFile) []string {
	var found []string
	osNames := map[string]bool{}
	for _, imported := range parsed.file.Imports {
		importPath := strings.Trim(imported.Path.Value, `"`)
		if isBannedImport(importPath) {
			found = append(found, fmt.Sprintf("%s: import %s as %s", parsed.position(imported), importPath, importName(imported)))
		}
		if importPath == "os" && imported.Name == nil {
			osNames["os"] = true
		} else if importPath == "os" {
			osNames[imported.Name.Name] = true
		}
	}
	ast.Inspect(parsed.file, func(node ast.Node) bool {
		selector, isSelector := node.(*ast.SelectorExpr)
		if !isSelector {
			return true
		}
		packageName, isIdent := selector.X.(*ast.Ident)
		isOsWrite := isIdent && osNames[packageName.Name] && slices.Contains(forbiddenOsWrites, selector.Sel.Name)
		if isOsWrite {
			found = append(found, fmt.Sprintf("%s: os.%s", parsed.position(selector), selector.Sel.Name))
		}
		return true
	})
	return append(found, writeRootCallsIn(parsed)...)
}

func writeRootCallsIn(parsed parsedFile) []string {
	var found []string
	ast.Inspect(parsed.file, func(node ast.Node) bool {
		call, isCall := node.(*ast.CallExpr)
		if !isCall {
			return true
		}
		selector, isSelector := call.Fun.(*ast.SelectorExpr)
		if isSelector && selector.Sel.Name == writeRootConstructor {
			found = append(found, fmt.Sprintf("%s: call to %s", parsed.position(selector), writeRootConstructor))
		}
		return true
	})
	return found
}

func isBannedImport(importPath string) bool {
	return slices.Contains(bannedImports, importPath) || strings.HasPrefix(importPath, bannedImportPrefix)
}

func importName(imported *ast.ImportSpec) string {
	if imported.Name != nil {
		return imported.Name.Name
	}
	return "default"
}

func (parsed parsedFile) position(node ast.Node) token.Position {
	return parsed.fileSet.Position(node.Pos())
}
