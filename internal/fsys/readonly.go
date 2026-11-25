package fsys

import (
	"context"
	"io/fs"
)

// readOnlyView scelle un volume derrière ReadFS : champ non exporté et méthodes recopiées une à une (pas
// d'imbrication), donc aucune assertion de type ne rend Volume ni OpenWriteRoot.
type readOnlyView struct{ inner ReadFS }

func (view readOnlyView) Volume() string                         { return view.inner.Volume() }
func (view readOnlyView) Open(name string) (File, error)         { return view.inner.Open(name) }
func (view readOnlyView) Stat(name string) (fs.FileInfo, error)  { return view.inner.Stat(name) }
func (view readOnlyView) Lstat(name string) (fs.FileInfo, error) { return view.inner.Lstat(name) }
func (view readOnlyView) ReadDir(name string) ([]fs.DirEntry, error) {
	return view.inner.ReadDir(name)
}
func (view readOnlyView) Readlink(name string) (string, error) { return view.inner.Readlink(name) }
func (view readOnlyView) Canonical(name string) (string, error) { return view.inner.Canonical(name) }

func (view readOnlyView) VolumeID(name string) (string, bool, error) {
	return view.inner.VolumeID(name)
}

func (view readOnlyView) FileID(info fs.FileInfo) (FileID, bool) { return view.inner.FileID(info) }
func (view readOnlyView) FreeSpace(name string) (uint64, error)  { return view.inner.FreeSpace(name) }
func (view readOnlyView) Paths() PathSyntax                      { return view.inner.Paths() }
func (view readOnlyView) Capabilities(ctx context.Context) (Capabilities, error) {
	return view.inner.Capabilities(ctx)
}
