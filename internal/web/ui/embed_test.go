package ui

import "testing"

func TestAssetsEmbedIndexHTML(t *testing.T) {
	content, err := Assets.ReadFile("dist/index.html")
	if err != nil {
		t.Fatalf("dist/index.html absent des ressources embarquées : %v", err)
	}
	if len(content) == 0 {
		t.Fatal("dist/index.html est vide")
	}
}
