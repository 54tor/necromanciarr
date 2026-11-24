// Package ui embarque l'interface web construite par Vite (dossier web/).
package ui

import "embed"

// Assets contient le dossier dist. Sans « make ui », seul le marqueur commité .placeholder
// est présent (pas d'index.html), ce qui permet de compiler et tester sans Node.
//
//go:embed all:dist
var Assets embed.FS
