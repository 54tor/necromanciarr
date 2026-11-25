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
