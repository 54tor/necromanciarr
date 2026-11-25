package fsys

import "testing"

// SetLocalCaseInsensitiveForTest force, le temps d'un test, la sensibilité à la casse du volume local lue par
// NewRegistry (les tests Windows et macOS se jouent aussi sous Linux).
func SetLocalCaseInsensitiveForTest(t *testing.T, caseInsensitive bool) {
	previous := localCaseInsensitive
	localCaseInsensitive = caseInsensitive
	t.Cleanup(func() { localCaseInsensitive = previous })
}
