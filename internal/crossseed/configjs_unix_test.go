//go:build unix

package crossseed

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Le moteur lit par défaut le fichier nommé par sourceMappingURL ; une FIFO sans écrivain bloquerait cette lecture.
func TestLoadConfigJS_sourceMapCommentIsNeverRead(t *testing.T) {
	fifoPath := filepath.Join(t.TempDir(), "map.json")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	path := writeConfigJS(t, "module.exports = { port: 2468 };\n//# sourceMappingURL=file://"+fifoPath+"\n")
	finished := make(chan error, 1)

	go func() {
		_, err := LoadConfigJS(context.Background(), path, lookupFrom(nil))
		finished <- err
	}()

	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(evaluationTimeout + time.Second):
		t.Fatal("the source map file was opened")
	}
}

// Une FIFO sans écrivain bloquerait os.Open : elle doit être refusée sur son type, avant l'ouverture.
func TestLoadConfigJS_fifoIsRejectedWithoutBlocking(t *testing.T) {
	fifoPath := filepath.Join(t.TempDir(), "config.js")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	finished := make(chan error, 1)

	go func() {
		_, err := LoadConfigJS(context.Background(), fifoPath, lookupFrom(nil))
		finished <- err
	}()

	select {
	case err := <-finished:
		if !errors.Is(err, ErrNotRegularFile) || !strings.Contains(err.Error(), "named pipe") {
			t.Fatalf("gotValue %v, expectedValue %v naming a named pipe", err, ErrNotRegularFile)
		}
	case <-time.After(time.Second):
		t.Fatal("reading the FIFO blocked")
	}
}
