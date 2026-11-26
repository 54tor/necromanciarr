package hashsource

import (
	"testing"

	"necromanciarr/internal/config"
)

func TestNewFromConfig_ignoresALocalVolume(t *testing.T) {
	local := config.FSProvider{Type: config.FSProviderTypeLocal, Name: "nas", Paths: []string{"/srv/media"}}
	valeurAttendue := 0

	registry, err := NewFromConfig([]config.FSProvider{local}, Deps{})

	if valeurObtenue := len(registry.providers); err != nil || valeurObtenue != valeurAttendue {
		t.Fatalf("valeurObtenue %d sources (%v), valeurAttendue %d", valeurObtenue, err, valeurAttendue)
	}
}
