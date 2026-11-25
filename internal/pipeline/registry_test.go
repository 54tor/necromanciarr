package pipeline

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/local"
)

// recordingVolume est le volume local dont Open note chaque fichier ouvert et compte les octets lus par ReadAt.
type recordingVolume struct {
	fsys.ReadFS
	inner     fsys.Volume
	mutex     sync.Mutex
	opened    []string
	bytesRead atomic.Int64
}

func newRecordingRegistry(t *testing.T) (*fsys.Registry, *recordingVolume) {
	t.Helper()
	inner := local.New()
	volume := &recordingVolume{ReadFS: inner, inner: inner}
	registry, err := fsys.NewRegistry(map[string]fsys.Volume{"": volume}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return registry, volume
}

func (volume *recordingVolume) OpenWriteRoot(dir string) (fsys.WriteRoot, error) {
	return volume.inner.OpenWriteRoot(dir)
}

func (volume *recordingVolume) Open(name string) (fsys.File, error) {
	file, err := volume.ReadFS.Open(name)
	if err != nil {
		return nil, err
	}
	volume.mutex.Lock()
	volume.opened = append(volume.opened, name)
	volume.mutex.Unlock()
	return &recordingFile{File: file, volume: volume}, nil
}

type recordingFile struct {
	fsys.File
	volume *recordingVolume
}

func (file *recordingFile) ReadAt(buffer []byte, offset int64) (int, error) {
	readCount, err := file.File.ReadAt(buffer, offset)
	file.volume.bytesRead.Add(int64(readCount))
	return readCount, err
}

// byteCountingLimiter note chaque octet demandé au limiteur de lecture.
type byteCountingLimiter struct{ requested atomic.Int64 }

func (limiter *byteCountingLimiter) WaitN(_ context.Context, count int) error {
	limiter.requested.Add(int64(count))
	return nil
}

func TestVerify_readsThroughRegistry(t *testing.T) {
	fixture := newVerifyFixture(t)
	fixture.productionOpener = true
	registry, volume := newRecordingRegistry(t)
	fixture.registry = registry
	fixture.addCandidate("Movie.2020", "", movieOnly())

	result := fixture.mustRun()

	if result.Verified == 0 {
		t.Fatalf("verify did nothing: %+v", result)
	}
	valeurAttendue := filepath.Join(fixture.root, "movie.mkv")
	if len(volume.opened) == 0 || volume.opened[0] != valeurAttendue {
		t.Fatalf("valeurAttendue the file opened through the registry (%s), valeurObtenue %v", valeurAttendue, volume.opened)
	}
	if volume.bytesRead.Load() < movieSize {
		t.Fatalf("valeurAttendue at least %d bytes read through the registry reader, valeurObtenue %d", movieSize, volume.bytesRead.Load())
	}
}

func TestVerify_registryReaderStaysUnderTheReadLimiter(t *testing.T) {
	fixture := newVerifyFixture(t)
	fixture.productionOpener = true
	registry, volume := newRecordingRegistry(t)
	fixture.registry = registry
	limiter := &byteCountingLimiter{}
	fixture.limiter = limiter
	fixture.addCandidate("Movie.2020", "", movieOnly())

	fixture.mustRun()

	valeurObtenue := limiter.requested.Load()
	if volume.bytesRead.Load() == 0 || valeurObtenue < volume.bytesRead.Load() {
		t.Fatalf("valeurAttendue the limiter to be charged for the %d bytes read, valeurObtenue %d", volume.bytesRead.Load(), valeurObtenue)
	}
}
