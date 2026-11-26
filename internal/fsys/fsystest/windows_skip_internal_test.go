package fsystest

import (
	"runtime"
	"testing"
)

// skipOnWindows écarte un test qui suppose une sémantique POSIX (serveur distant simulé sur le disque local).
func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("le volume distant simulé mappe des chemins POSIX sur le disque local, jamais un hôte Windows")
	}
}
