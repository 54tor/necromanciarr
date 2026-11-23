// Commande necromanciarr : point d'entrée de l'exécutable (serve, scan, match, verify, link, seed).
package main

import (
	"fmt"
	"os"
)

// usageExitCode est le code de sortie d'une ligne de commande invalide.
const usageExitCode = 2

func main() {
	fmt.Fprintln(os.Stderr, "usage: necromanciarr <serve|scan|match|verify|link|seed>")
	os.Exit(usageExitCode)
}
