package fsys

import (
	"testing"
	"time"
)

// SetLocalCaseInsensitiveForTest force, le temps d'un test, la sensibilité à la casse du volume local lue par
// NewRegistry (les tests Windows et macOS se jouent aussi sous Linux).
func SetLocalCaseInsensitiveForTest(t *testing.T, caseInsensitive bool) {
	previous := localCaseInsensitive
	localCaseInsensitive = caseInsensitive
	t.Cleanup(func() { localCaseInsensitive = previous })
}

// SetCapabilitiesTimeoutForTest raccourcit le délai de la sonde des capacités le temps d'un test.
func SetCapabilitiesTimeoutForTest(t *testing.T, timeout time.Duration) {
	previous := capabilitiesTimeout
	capabilitiesTimeout = timeout
	t.Cleanup(func() { capabilitiesTimeout = previous })
}

// IdentityUnknownReasonForTest est le code noté pour une racine ou un ancêtre sans identité connue.
const IdentityUnknownReasonForTest = identityUnknownReason

// IdentityGapsForTest charge le volume et rend les chemins de bibliothèque comparés par le texte seul.
func IdentityGapsForTest(t *testing.T, registry *Registry, volumeName string) map[string]string {
	t.Helper()
	state, err := registry.volumes[volumeName].load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return state.identityGaps
}
