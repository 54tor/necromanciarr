package local

import (
	"runtime"
	"testing"
)

// skipOnWindows écarte un test qui suppose une sémantique POSIX (poignée de dossier ouverte).
func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows refuse de renommer un dossier tenu ouvert")
	}
}
