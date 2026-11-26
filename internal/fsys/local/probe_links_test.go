package local

import (
	"os"
	"runtime"
	"testing"
)

func TestProbeLinks_measuresTheLinksForRealAndLeavesNoFile(t *testing.T) {
	linkDir := t.TempDir()
	root, err := New().OpenWriteRoot(linkDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()

	capabilities, err := ProbeLinks(root)
	if err != nil {
		t.Fatal(err)
	}

	if runtime.GOOS != "windows" && (!capabilities.Hardlink || !capabilities.Symlink) {
		t.Errorf("valeurObtenue %+v, valeurAttendue liens dur et symbolique mesurés vrais", capabilities)
	}
	if _, isMeasured := capabilities.Reasons["reflink"]; !capabilities.Reflink && !isMeasured {
		t.Errorf("valeurObtenue %+v, valeurAttendue un reflink mesuré ou sa raison d'absence", capabilities)
	}
	entries, err := os.ReadDir(linkDir)
	if valeurObtenue := len(entries); err != nil || valeurObtenue != 0 {
		t.Fatalf("valeurObtenue %d entrées (%v), valeurAttendue aucun fichier laissé", valeurObtenue, err)
	}
}
