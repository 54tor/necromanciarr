package pipeline

import (
	"fmt"
	"testing"

	"necromanciarr/internal/fsys"
)

func TestWriteRootRefusalReason_remoteExecRequiredKeepsItsOwnReason(t *testing.T) {
	refusal := fmt.Errorf("app: probe sftp volume %q: %w", "seedbox", fsys.ErrRemoteExecRequired)

	if valeurObtenue := writeRootRefusalReason(refusal); valeurObtenue != ReasonRemoteExecRequired {
		t.Fatalf("valeurObtenue %q, valeurAttendue %q", valeurObtenue, ReasonRemoteExecRequired)
	}
}
