package main

import (
	"fmt"
	"os"
)

const usageExitCode = 2

func main() {
	fmt.Fprintln(os.Stderr, "usage: necromanciarr <serve|scan|match|verify|link|seed>")
	os.Exit(usageExitCode)
}
