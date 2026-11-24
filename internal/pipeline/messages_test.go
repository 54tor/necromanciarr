package pipeline

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestPipeline_usesOnlyTheMessageTypesOfTheCatalogue : la couche métier produit des messages, jamais du texte rendu.
func TestPipeline_usesOnlyTheMessageTypesOfTheCatalogue(t *testing.T) {
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			selector, isSelector := node.(*ast.SelectorExpr)
			if !isSelector {
				return true
			}
			qualifier, isIdentifier := selector.X.(*ast.Ident)
			isCatalogueUse := isIdentifier && qualifier.Name == "i18n" && selector.Sel.Name != "Message" && selector.Sel.Name != "Rendered" && !strings.HasPrefix(selector.Sel.Name, "Key")
			if isCatalogueUse {
				t.Errorf("%s: i18n.%s used, expectedValue only Message, Rendered and Key constants", path, selector.Sel.Name)
			}
			return true
		})
	}
}
