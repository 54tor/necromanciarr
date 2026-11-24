package ui

import "testing"

// Une interface construite fournit index.html ; sinon seul le marqueur .placeholder est embarqué.
func TestAssetsEmbedBuiltIndexOrPlaceholder(t *testing.T) {
	for _, name := range []string{"dist/index.html", "dist/.placeholder"} {
		content, err := Assets.ReadFile(name)
		if err == nil && len(content) > 0 {
			return
		}
	}
	t.Fatal("ni dist/index.html ni dist/.placeholder ne sont embarqués")
}
