package library

import (
	"context"
	"io/fs"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"necromanciarr/internal/fsys"
)

// memoryNode est une entrée de l'arbre factice : dossier, lien symbolique ou fichier, sur un périphérique donné.
type memoryNode struct {
	isDir, isSymlink bool
	size             int64
	device           string
}

// memoryVolume est un volume distant factice en mémoire ; il note chaque méthode appelée. Les méthodes que Scan
// n'a pas le droit d'utiliser (Open, Readlink, Stat d'un lien) font échouer le test.
type memoryVolume struct {
	fsys.ReadFS
	t     *testing.T
	name  string
	nodes map[string]memoryNode
	mutex sync.Mutex
	calls []string
}

func (volume *memoryVolume) record(call string) {
	volume.mutex.Lock()
	defer volume.mutex.Unlock()
	volume.calls = append(volume.calls, call)
}

func (volume *memoryVolume) OpenWriteRoot(string) (fsys.WriteRoot, error) {
	volume.t.Error("scan must never open a write root")
	return nil, fsys.ErrUnconfined
}

func (volume *memoryVolume) Volume() string         { return volume.name }
func (volume *memoryVolume) Paths() fsys.PathSyntax { return fsys.RemotePaths() }

func (volume *memoryVolume) Canonical(name string) (string, error) {
	volume.record("Canonical")
	return name, nil
}

func (volume *memoryVolume) VolumeID(name string) (string, bool, error) {
	volume.record("VolumeID")
	node, present := volume.nodes[name]
	if !present {
		return "", false, fs.ErrNotExist
	}
	return node.device, node.device != "", nil
}

func (volume *memoryVolume) Stat(name string) (fs.FileInfo, error) {
	volume.record("Stat")
	node, present := volume.nodes[name]
	if !present {
		return nil, fs.ErrNotExist
	}
	if node.isSymlink {
		volume.t.Errorf("Stat through the symlink %s", name)
	}
	return memoryInfo{name: path.Base(name), node: node}, nil
}

func (volume *memoryVolume) ReadDir(name string) ([]fs.DirEntry, error) {
	volume.record("ReadDir")
	var entries []fs.DirEntry
	for nodePath, node := range volume.nodes {
		if nodePath != name && path.Dir(nodePath) == name {
			entries = append(entries, fs.FileInfoToDirEntry(memoryInfo{name: path.Base(nodePath), node: node}))
		}
	}
	return entries, nil
}

type memoryInfo struct {
	name string
	node memoryNode
}

func (info memoryInfo) Name() string       { return info.name }
func (info memoryInfo) Size() int64        { return info.node.size }
func (info memoryInfo) ModTime() time.Time { return time.Unix(1700000000, 0) }
func (info memoryInfo) IsDir() bool        { return info.node.isDir }
func (info memoryInfo) Sys() any           { return nil }
func (info memoryInfo) Mode() fs.FileMode {
	switch {
	case info.node.isSymlink:
		return fs.ModeSymlink | 0o777
	case info.node.isDir:
		return fs.ModeDir | 0o755
	}
	return 0o644
}

func newMemoryRegistry(t *testing.T, volume *memoryVolume) *fsys.Registry {
	t.Helper()
	registry, err := fsys.NewRegistry(map[string]fsys.Volume{volume.name: volume}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func TestScan_walksThroughReadFS(t *testing.T) {
	volume := &memoryVolume{t: t, name: "seedbox", nodes: map[string]memoryNode{
		"/srv/media":                       {isDir: true, device: "1"},
		"/srv/media/Movies":                {isDir: true, device: "1"},
		"/srv/media/Movies/Film.mkv":       {size: 7},
		"/srv/media/Movies/Film.nfo":       {size: 1},
		"/srv/media/Movies/link.mkv":       {isSymlink: true, size: 9},
		"/srv/media/Movies/dir-link":       {isSymlink: true, isDir: true},
		"/srv/media/Mounted":               {isDir: true, device: "2"},
		"/srv/media/Mounted/Hidden.mkv":    {size: 3},
		"/srv/media/Shows/S01E01.mkv":      {size: 5},
		"/srv/media/Shows":                 {isDir: true, device: "1"},
		"/srv/media/Movies/dir-link/x.mkv": {size: 4},
	}}
	testStore := openTestStore(t)

	err := Scan(context.Background(), fsys.Location{Volume: "seedbox", Path: "/srv/media"}, newMemoryRegistry(t, volume), testStore)
	if err != nil {
		t.Fatal(err)
	}

	gotFiles, _ := testStore.ListMediaFiles(context.Background())
	var valeurObtenue []string
	for _, file := range gotFiles {
		valeurObtenue = append(valeurObtenue, file.Path)
	}
	slices.Sort(valeurObtenue)
	valeurAttendue := []string{"sftp://seedbox/srv/media/Movies/Film.mkv", "sftp://seedbox/srv/media/Shows/S01E01.mkv"}
	if !slices.Equal(valeurObtenue, valeurAttendue) {
		t.Fatalf("valeurAttendue %v, valeurObtenue %v", valeurAttendue, valeurObtenue)
	}
	for _, call := range volume.calls {
		if strings.Contains(call, "Open") || strings.Contains(call, "Readlink") {
			t.Fatalf("scan called %s", call)
		}
	}
	if !slices.Contains(volume.calls, "ReadDir") {
		t.Fatalf("scan never listed a directory: %v", volume.calls)
	}
}
