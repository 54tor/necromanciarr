package pipeline

import (
	"context"
	"sync/atomic"
	"testing"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/local"
)

// innerVolume nomme le champ embarqué autrement que la méthode Volume qu'il apporte.
type innerVolume = fsys.Volume

// resettingVolume compte les ouvertures d'étape d'un volume distant (sftpfs.Volume.ResetForStage).
type resettingVolume struct {
	innerVolume
	resets atomic.Int32
}

func (volume *resettingVolume) ResetForStage() { volume.resets.Add(1) }

func newResettingRegistry(t *testing.T) (*fsys.Registry, *resettingVolume) {
	t.Helper()
	volume := &resettingVolume{innerVolume: local.New()}
	registry, err := fsys.NewRegistry(map[string]fsys.Volume{"": volume}, []fsys.Location{{Path: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	return registry, volume
}

func TestRun_opensANewStageOnEveryVolume(t *testing.T) {
	registry, volume := newResettingRegistry(t)

	if _, err := Run(context.Background(), StageScan, Deps{Store: openTestStore(t), Registry: registry}); err != nil {
		t.Fatal(err)
	}

	if valeurObtenue := volume.resets.Load(); valeurObtenue != 1 {
		t.Fatalf("valeurObtenue %d resets, valeurAttendue 1", valeurObtenue)
	}
}

func TestWithLock_opensANewStageOnEveryVolume(t *testing.T) {
	registry, volume := newResettingRegistry(t)

	err := WithLock(context.Background(), Deps{Store: openTestStore(t), Registry: registry}, "dedupe", func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}

	if valeurObtenue := volume.resets.Load(); valeurObtenue != 1 {
		t.Fatalf("valeurObtenue %d resets, valeurAttendue 1", valeurObtenue)
	}
}
