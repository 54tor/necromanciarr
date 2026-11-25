package fsystest

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/local"
)

// Call est un appel noté par Recorder : Paths liste tous les chemins en jeu (sources comprises), Written ceux que
// l'appel crée, modifie ou retire (vide pour une lecture). Les chemins sont absolus dans la syntaxe du volume.
type Call struct {
	Method  string
	Volume  string
	Paths   []string
	Written []string
}

// Recorder habille un fsys.Volume et note chaque appel, lecture comme écriture, y compris ceux des racines
// d'écriture qu'il ouvre et leurs options (Reflink, Chtimes). Sûr entre goroutines. Infrastructure de test
// seulement : il ne doit jamais être branché en production (un test refuse tout import de fsystest hors des
// fichiers de test). Il n'élargit rien : la racine rendue n'expose ni Volume ni l'enregistreur ; une option que le
// volume enveloppé n'a pas est refusée (local.ErrUnsupported) après avoir été notée.
type Recorder struct {
	inner fsys.Volume
	mutex sync.Mutex
	calls []Call
}

// NewRecorder habille inner.
func NewRecorder(inner fsys.Volume) *Recorder { return &Recorder{inner: inner} }

// Calls rend une copie de tous les appels notés, dans l'ordre.
func (recorder *Recorder) Calls() []Call {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	return slices.Clone(recorder.calls)
}

// WriteCalls rend les seuls appels qui écrivent.
func (recorder *Recorder) WriteCalls() []Call {
	var writes []Call
	for _, call := range recorder.Calls() {
		if len(call.Written) > 0 {
			writes = append(writes, call)
		}
	}
	return writes
}

// AssertNoWriteUnder échoue (Errorf) pour chaque chemin écrit égal à l'une des racines ou situé dessous ; les racines
// sont d'abord rendues canoniques par le volume enveloppé, comme le fait le registre.
func (recorder *Recorder) AssertNoWriteUnder(t testing.TB, roots ...string) {
	t.Helper()
	canonicalRoots := make([]string, 0, len(roots))
	for _, root := range roots {
		canonical, err := recorder.inner.Canonical(root)
		if err != nil {
			t.Errorf("fsystest: root %s cannot be canonicalized: %v", root, err)
			continue
		}
		canonicalRoots = append(canonicalRoots, canonical)
	}
	for _, call := range recorder.WriteCalls() {
		for _, written := range call.Written {
			if root, under := recorder.under(written, canonicalRoots); under {
				t.Errorf("fsystest: %s wrote %s under the library root %s", call.Method, written, root)
			}
		}
	}
}

func (recorder *Recorder) under(candidate string, roots []string) (string, bool) {
	syntax := recorder.inner.Paths()
	for _, root := range roots {
		relative, err := syntax.Rel(recorder.comparable(root), recorder.comparable(candidate))
		isOutside := err != nil || relative == ".." || strings.HasPrefix(relative, "../") || strings.HasPrefix(relative, `..\`)
		if !isOutside {
			return root, true
		}
	}
	return "", false
}

// comparable replie la casse sur un disque local qui l'ignore (NTFS, APFS), comme le registre.
func (recorder *Recorder) comparable(name string) string {
	foldsCase := recorder.inner.Volume() == "" && (runtime.GOOS == "windows" || runtime.GOOS == "darwin")
	if foldsCase {
		return strings.ToLower(name)
	}
	return name
}

func (recorder *Recorder) record(method string, paths, written []string) {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	recorder.calls = append(recorder.calls, Call{Method: method, Volume: recorder.inner.Volume(), Paths: paths, Written: written})
}

func (recorder *Recorder) read(method string, paths ...string) { recorder.record(method, paths, nil) }

// Volume rend le nom du volume enveloppé.
func (recorder *Recorder) Volume() string {
	recorder.read("Volume")
	return recorder.inner.Volume()
}

// Open note puis délègue.
func (recorder *Recorder) Open(name string) (fsys.File, error) {
	recorder.read("Open", name)
	return recorder.inner.Open(name)
}

// Stat note puis délègue.
func (recorder *Recorder) Stat(name string) (fs.FileInfo, error) {
	recorder.read("Stat", name)
	return recorder.inner.Stat(name)
}

// Lstat note puis délègue.
func (recorder *Recorder) Lstat(name string) (fs.FileInfo, error) {
	recorder.read("Lstat", name)
	return recorder.inner.Lstat(name)
}

// ReadDir note puis délègue.
func (recorder *Recorder) ReadDir(name string) ([]fs.DirEntry, error) {
	recorder.read("ReadDir", name)
	return recorder.inner.ReadDir(name)
}

// Readlink note puis délègue.
func (recorder *Recorder) Readlink(name string) (string, error) {
	recorder.read("Readlink", name)
	return recorder.inner.Readlink(name)
}

// Canonical note puis délègue.
func (recorder *Recorder) Canonical(name string) (string, error) {
	recorder.read("Canonical", name)
	return recorder.inner.Canonical(name)
}

// VolumeID note puis délègue.
func (recorder *Recorder) VolumeID(name string) (string, bool, error) {
	recorder.read("VolumeID", name)
	return recorder.inner.VolumeID(name)
}

// FileID note puis délègue.
func (recorder *Recorder) FileID(info fs.FileInfo) (fsys.FileID, bool) {
	recorder.read("FileID")
	return recorder.inner.FileID(info)
}

// FreeSpace note puis délègue.
func (recorder *Recorder) FreeSpace(name string) (uint64, error) {
	recorder.read("FreeSpace", name)
	return recorder.inner.FreeSpace(name)
}

// Paths note puis délègue.
func (recorder *Recorder) Paths() fsys.PathSyntax {
	recorder.read("Paths")
	return recorder.inner.Paths()
}

// Capabilities note puis délègue.
func (recorder *Recorder) Capabilities(ctx context.Context) (fsys.Capabilities, error) {
	recorder.read("Capabilities")
	return recorder.inner.Capabilities(ctx)
}

// OpenWriteRoot note l'ouverture comme une écriture visant dir, puis habille la racine rendue.
func (recorder *Recorder) OpenWriteRoot(dir string) (fsys.WriteRoot, error) {
	recorder.record("OpenWriteRoot", []string{dir}, []string{dir})
	inner, err := recorder.inner.OpenWriteRoot(dir)
	if err != nil {
		return nil, err
	}
	return &recordingWriteRoot{recorder: recorder, inner: inner, dir: dir}, nil
}

// recordingWriteRoot note chaque appel de la racine habillée, chemins rendus absolus sous dir.
type recordingWriteRoot struct {
	recorder *Recorder
	inner    fsys.WriteRoot
	dir      string
}

func (root *recordingWriteRoot) full(relative string) string {
	return root.recorder.inner.Paths().Join(root.dir, relative)
}

// write note une écriture des chemins relatifs written ; extra ajoute des chemins seulement lus (source, cible).
func (root *recordingWriteRoot) write(method string, extra []string, written ...string) {
	fullWritten := make([]string, 0, len(written))
	for _, relative := range written {
		fullWritten = append(fullWritten, root.full(relative))
	}
	root.recorder.record(method, append(slices.Clone(fullWritten), extra...), fullWritten)
}

func (root *recordingWriteRoot) Root() fsys.Location {
	root.recorder.read("Root", root.dir)
	return root.inner.Root()
}

func (root *recordingWriteRoot) MkdirAll(relative string, perm fs.FileMode) error {
	root.write("MkdirAll", nil, relative)
	return root.inner.MkdirAll(relative, perm)
}

func (root *recordingWriteRoot) Symlink(target, relative string) error {
	root.write("Symlink", []string{target}, relative)
	return root.inner.Symlink(target, relative)
}

func (root *recordingWriteRoot) Hardlink(source fsys.Location, relative string) error {
	root.write("Hardlink", []string{source.String()}, relative)
	return root.inner.Hardlink(source, relative)
}

func (root *recordingWriteRoot) CreateExclusive(relative string, perm fs.FileMode) (io.WriteCloser, error) {
	root.write("CreateExclusive", nil, relative)
	return root.inner.CreateExclusive(relative, perm)
}

func (root *recordingWriteRoot) CopyFrom(ctx context.Context, source fsys.Location, relative string) (int64, error) {
	root.write("CopyFrom", []string{source.String()}, relative)
	return root.inner.CopyFrom(ctx, source, relative)
}

func (root *recordingWriteRoot) Replace(oldRelative, newRelative string) error {
	root.write("Replace", nil, oldRelative, newRelative)
	return root.inner.Replace(oldRelative, newRelative)
}

func (root *recordingWriteRoot) Remove(relative string) error {
	root.write("Remove", nil, relative)
	return root.inner.Remove(relative)
}

func (root *recordingWriteRoot) Lstat(relative string) (fs.FileInfo, error) {
	root.recorder.read("Lstat", root.full(relative))
	return root.inner.Lstat(relative)
}

func (root *recordingWriteRoot) Close() error {
	root.recorder.read("Close", root.dir)
	return root.inner.Close()
}

// Reflink note puis délègue ; sans l'option sur la racine enveloppée, refuse comme le linker le ferait.
func (root *recordingWriteRoot) Reflink(source fsys.Location, relative string) error {
	root.write("Reflink", []string{source.String()}, relative)
	reflinker, supported := root.inner.(fsys.Reflinker)
	if !supported {
		return fmt.Errorf("%w: reflink on the recorded volume", local.ErrUnsupported)
	}
	return reflinker.Reflink(source, relative)
}

// Chtimes note puis délègue ; sans l'option sur la racine enveloppée, refuse (date laissée inconnue).
func (root *recordingWriteRoot) Chtimes(relative string, accessTime, modTime time.Time) error {
	root.write("Chtimes", nil, relative)
	setter, supported := root.inner.(fsys.TimesSetter)
	if !supported {
		return fmt.Errorf("%w: chtimes on the recorded volume", local.ErrUnsupported)
	}
	return setter.Chtimes(relative, accessTime, modTime)
}
