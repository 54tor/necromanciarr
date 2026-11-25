package fsys_test

import (
	"context"
	"errors"
	"hash/fnv"
	"io"
	"io/fs"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"necromanciarr/internal/fsys"
)

// fakeVolume est un volume minimal pour le registre (pas pour la suite de conformité) : Canonical, Stat et
// FileID sont réglables par test (liens symboliques, alias d'identité, refus) et les appels sont comptés.
type fakeVolume struct {
	name         string
	canonical    func(name string) (string, error)
	capabilities fsys.Capabilities
	// sharedIDs donne à des graphies différentes une même identité (montage lié, NFD, nom 8.3).
	sharedIDs map[string]uint64
	statError map[string]error
	// unknownIDs : FileID rend known=false pour ces chemins.
	unknownIDs map[string]bool
	paths      fsys.PathSyntax
	// unavailable simule un volume injoignable, réparable en cours de test.
	unavailable      atomic.Bool
	capabilitiesFunc func(ctx context.Context) (fsys.Capabilities, error)

	callsMutex     sync.Mutex
	canonicalCalls map[string]int
}

// posixCanonical imite un volume distant : nettoyage POSIX et alias symboliques résolus par préfixe.
func posixCanonical(symlinks map[string]string) func(string) (string, error) {
	return func(name string) (string, error) {
		cleaned := fsys.RemotePaths().Clean(name)
		for alias, target := range symlinks {
			if cleaned == alias || strings.HasPrefix(cleaned, alias+"/") {
				cleaned = target + strings.TrimPrefix(cleaned, alias)
			}
		}
		return cleaned, nil
	}
}

func newFakeVolume(name string) *fakeVolume {
	return &fakeVolume{name: name, canonical: posixCanonical(nil), paths: fsys.RemotePaths()}
}

var errFake = errors.New("fake: not implemented")

func (volume *fakeVolume) Volume() string                        { return volume.name }
func (volume *fakeVolume) Open(string) (fsys.File, error)        { return nil, errFake }
func (volume *fakeVolume) Lstat(string) (fs.FileInfo, error)     { return nil, errFake }
func (volume *fakeVolume) ReadDir(string) ([]fs.DirEntry, error) { return nil, errFake }
func (volume *fakeVolume) Readlink(string) (string, error)       { return "", errFake }
func (volume *fakeVolume) VolumeID(string) (string, bool, error) { return "", false, errFake }
func (volume *fakeVolume) FreeSpace(string) (uint64, error)      { return 0, errFake }
func (volume *fakeVolume) Paths() fsys.PathSyntax                { return volume.paths }

func (volume *fakeVolume) Capabilities(ctx context.Context) (fsys.Capabilities, error) {
	if volume.capabilitiesFunc != nil {
		return volume.capabilitiesFunc(ctx)
	}
	return volume.capabilities, nil
}

func (volume *fakeVolume) Canonical(name string) (string, error) {
	volume.callsMutex.Lock()
	if volume.canonicalCalls == nil {
		volume.canonicalCalls = map[string]int{}
	}
	volume.canonicalCalls[name]++
	volume.callsMutex.Unlock()
	if volume.unavailable.Load() {
		return "", fsys.ErrRemoteUnavailable
	}
	return volume.canonical(name)
}

func (volume *fakeVolume) callsFor(name string) int {
	volume.callsMutex.Lock()
	defer volume.callsMutex.Unlock()
	return volume.canonicalCalls[name]
}

// Stat rend une fiche dont Sys porte le chemin : FileID en tire l'identité.
func (volume *fakeVolume) Stat(name string) (fs.FileInfo, error) {
	if err := volume.statError[name]; err != nil {
		return nil, err
	}
	return fakeInfo{path: name}, nil
}

func (volume *fakeVolume) FileID(info fs.FileInfo) (fsys.FileID, bool) {
	path, isFake := info.Sys().(string)
	if !isFake || volume.unknownIDs[path] {
		return fsys.FileID{}, false
	}
	if shared, isShared := volume.sharedIDs[path]; isShared {
		return fsys.FileID{Device: 1, Inode: shared}, true
	}
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(path))
	return fsys.FileID{Device: 1, Inode: hasher.Sum64()}, true
}

func (volume *fakeVolume) OpenWriteRoot(dir string) (fsys.WriteRoot, error) {
	return &fakeWriteRoot{root: fsys.Location{Volume: volume.name, Path: dir}}, nil
}

type fakeInfo struct{ path string }

func (info fakeInfo) Name() string       { return info.path }
func (info fakeInfo) Size() int64        { return 0 }
func (info fakeInfo) Mode() fs.FileMode  { return fs.ModeDir }
func (info fakeInfo) ModTime() time.Time { return time.Time{} }
func (info fakeInfo) IsDir() bool        { return true }
func (info fakeInfo) Sys() any           { return info.path }

type fakeWriteRoot struct{ root fsys.Location }

func (root *fakeWriteRoot) Root() fsys.Location                  { return root.root }
func (root *fakeWriteRoot) MkdirAll(string, fs.FileMode) error   { return errFake }
func (root *fakeWriteRoot) Symlink(string, string) error         { return errFake }
func (root *fakeWriteRoot) Hardlink(fsys.Location, string) error { return errFake }
func (root *fakeWriteRoot) Replace(string, string) error         { return errFake }
func (root *fakeWriteRoot) Remove(string) error                  { return errFake }
func (root *fakeWriteRoot) Lstat(string) (fs.FileInfo, error)    { return nil, errFake }
func (root *fakeWriteRoot) Close() error                         { return nil }

func (root *fakeWriteRoot) CopyFrom(context.Context, fsys.Location, string) (int64, error) {
	return 0, errFake
}

func (root *fakeWriteRoot) CreateExclusive(string, fs.FileMode) (io.WriteCloser, error) {
	return nil, errFake
}
