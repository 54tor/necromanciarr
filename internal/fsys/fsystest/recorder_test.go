package fsystest

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/local"
)

// stubVolume rend des valeurs nulles partout : il ne sert qu'à vérifier que l'enregistreur note chaque appel.
type stubVolume struct{}

func (stubVolume) Volume() string                         { return "" }
func (stubVolume) Open(string) (fsys.File, error)         { return nil, fs.ErrNotExist }
func (stubVolume) Stat(string) (fs.FileInfo, error)       { return nil, fs.ErrNotExist }
func (stubVolume) Lstat(string) (fs.FileInfo, error)      { return nil, fs.ErrNotExist }
func (stubVolume) ReadDir(string) ([]fs.DirEntry, error)  { return nil, nil }
func (stubVolume) Readlink(string) (string, error)        { return "", nil }
func (stubVolume) Canonical(name string) (string, error)  { return name, nil }
func (stubVolume) VolumeID(string) (string, bool, error)  { return "", false, nil }
func (stubVolume) FileID(fs.FileInfo) (fsys.FileID, bool) { return fsys.FileID{}, false }
func (stubVolume) FreeSpace(string) (uint64, error)       { return 0, nil }
func (stubVolume) Paths() fsys.PathSyntax                 { return fsys.RemotePaths() }
func (stubVolume) Capabilities(context.Context) (fsys.Capabilities, error) {
	return fsys.Capabilities{}, nil
}

func (stubVolume) OpenWriteRoot(dir string) (fsys.WriteRoot, error) {
	return stubWriteRoot{dir: dir}, nil
}

type stubWriteRoot struct{ dir string }

func (root stubWriteRoot) Root() fsys.Location                                    { return fsys.Location{Path: root.dir} }
func (stubWriteRoot) MkdirAll(string, fs.FileMode) error                          { return nil }
func (stubWriteRoot) Symlink(string, string) error                                { return nil }
func (stubWriteRoot) Hardlink(fsys.Location, string) error                        { return nil }
func (stubWriteRoot) CreateExclusive(string, fs.FileMode) (io.WriteCloser, error) { return nil, nil }

func (stubWriteRoot) CopyFrom(context.Context, fsys.Location, string) (int64, error) { return 0, nil }
func (stubWriteRoot) Replace(string, string) error                                   { return nil }
func (stubWriteRoot) Remove(string) error                                            { return nil }
func (stubWriteRoot) Lstat(string) (fs.FileInfo, error)                              { return nil, fs.ErrNotExist }
func (stubWriteRoot) Close() error                                                   { return nil }
func (stubWriteRoot) Reflink(fsys.Location, string) error                            { return nil }
func (stubWriteRoot) Chtimes(string, time.Time, time.Time) error                     { return nil }

// readOnlyWriteRootMethods sont les seules méthodes d'une WriteRoot qui n'écrivent pas.
var readOnlyWriteRootMethods = []string{"Root", "Lstat", "Close"}

// optionalWriteRootInterfaces sont les options qu'une WriteRoot peut porter : l'enregistreur doit les noter aussi.
var optionalWriteRootInterfaces = []reflect.Type{reflect.TypeFor[fsys.Reflinker](), reflect.TypeFor[fsys.TimesSetter]()}

func zeroArgument(argumentType reflect.Type) reflect.Value {
	if argumentType == reflect.TypeFor[context.Context]() {
		return reflect.ValueOf(context.Background())
	}
	if argumentType == reflect.TypeFor[string]() {
		return reflect.ValueOf("entry")
	}
	return reflect.Zero(argumentType)
}

// methodsNotRecorded rend les méthodes de interfaceType dont l'appel sur subject n'a laissé aucune trace (ou aucune
// trace d'écriture, pour celles qui écrivent).
func methodsNotRecorded(t *testing.T, interfaceType reflect.Type, subject any, calls func() []Call, readOnly []string) []string {
	t.Helper()
	var missing []string
	for methodIndex := range interfaceType.NumMethod() {
		name := interfaceType.Method(methodIndex).Name
		before := len(calls())
		callEveryMethodNamed(t, subject, name)
		recorded := calls()[before:]
		expectWrite := !slices.Contains(readOnly, name)
		if !hasCall(recorded, name, expectWrite) {
			missing = append(missing, name)
		}
	}
	return missing
}

func callEveryMethodNamed(t *testing.T, subject any, name string) {
	t.Helper()
	bound := reflect.ValueOf(subject).MethodByName(name)
	if !bound.IsValid() {
		t.Fatalf("%T has no method %s", subject, name)
	}
	arguments := make([]reflect.Value, bound.Type().NumIn())
	for argumentIndex := range arguments {
		arguments[argumentIndex] = zeroArgument(bound.Type().In(argumentIndex))
	}
	bound.Call(arguments)
}

func hasCall(calls []Call, method string, expectWrite bool) bool {
	return slices.ContainsFunc(calls, func(call Call) bool {
		return call.Method == method && (!expectWrite || len(call.Written) > 0)
	})
}

func TestRecorder_recordsEveryVolumeMethod(t *testing.T) {
	recorder := NewRecorder(stubVolume{})

	missing := methodsNotRecorded(t, reflect.TypeFor[fsys.Volume](), recorder, recorder.Calls, readOnlyVolumeMethods(t))

	if len(missing) != 0 {
		t.Fatalf("expectedValue every fsys.Volume method recorded, gotValue unrecorded %v", missing)
	}
}

// readOnlyVolumeMethods : toutes les méthodes de Volume sauf OpenWriteRoot, qui ouvre une racine d'écriture.
func readOnlyVolumeMethods(t *testing.T) []string {
	t.Helper()
	volumeType := reflect.TypeFor[fsys.Volume]()
	var names []string
	for methodIndex := range volumeType.NumMethod() {
		if name := volumeType.Method(methodIndex).Name; name != "OpenWriteRoot" {
			names = append(names, name)
		}
	}
	return names
}

func TestRecorder_recordsEveryWriteRootMethodAndOption(t *testing.T) {
	recorder := NewRecorder(stubVolume{})
	root, err := recorder.OpenWriteRoot("/links")
	if err != nil {
		t.Fatal(err)
	}
	interfaces := append([]reflect.Type{reflect.TypeFor[fsys.WriteRoot]()}, optionalWriteRootInterfaces...)

	for _, interfaceType := range interfaces {
		missing := methodsNotRecorded(t, interfaceType, root, recorder.Calls, readOnlyWriteRootMethods)

		if len(missing) != 0 {
			t.Fatalf("expectedValue every %s method recorded, gotValue unrecorded %v", interfaceType, missing)
		}
	}
}

// widerVolume simule une méthode ajoutée à l'interface sans que l'enregistreur la note.
type widerVolume interface {
	fsys.Volume
	Chmod(name string, mode fs.FileMode) error
}

type widerRecorder struct{ *Recorder }

func (widerRecorder) Chmod(string, fs.FileMode) error { return nil }

func TestMethodsNotRecorded_flagsAMethodAddedToTheInterface(t *testing.T) {
	recorder := NewRecorder(stubVolume{})
	subject := widerRecorder{Recorder: recorder}

	gotValue := methodsNotRecorded(t, reflect.TypeFor[widerVolume](), subject, recorder.Calls, readOnlyVolumeMethods(t))

	expectedValue := []string{"Chmod"}
	if !slices.Equal(gotValue, expectedValue) {
		t.Fatalf("expectedValue %v, gotValue %v", expectedValue, gotValue)
	}
}

func TestRecorder_recordsBothPathsOfReplaceAndTheTargetOfLinks(t *testing.T) {
	recorder := NewRecorder(stubVolume{})
	root, err := recorder.OpenWriteRoot("/links")
	if err != nil {
		t.Fatal(err)
	}
	source := fsys.Location{Path: "/library/movie.mkv"}
	reflinker, _ := root.(fsys.Reflinker)

	_ = root.Replace("tmp/a", "b")
	_ = root.Hardlink(source, "hard.mkv")
	_, _ = root.CopyFrom(context.Background(), source, "copy.mkv")
	_ = reflinker.Reflink(source, "clone.mkv")
	_ = root.Symlink("/library/movie.mkv", "soft.mkv")

	expectedValue := map[string][]string{
		"Replace":  {"/links/tmp/a", "/links/b"},
		"Hardlink": {"/links/hard.mkv"},
		"CopyFrom": {"/links/copy.mkv"},
		"Reflink":  {"/links/clone.mkv"},
		"Symlink":  {"/links/soft.mkv"},
	}
	for _, call := range recorder.WriteCalls() {
		expectedWritten, checked := expectedValue[call.Method]
		if !checked {
			continue
		}
		if !slices.Equal(call.Written, expectedWritten) {
			t.Fatalf("%s: expectedValue written %v, gotValue %v", call.Method, expectedWritten, call.Written)
		}
		isLink := call.Method != "Replace" && call.Method != "Symlink"
		if isLink && !slices.Contains(call.Paths, source.String()) {
			t.Fatalf("%s: expectedValue the source %s in %v", call.Method, source, call.Paths)
		}
		delete(expectedValue, call.Method)
	}
	if len(expectedValue) != 0 {
		t.Fatalf("expectedValue every write recorded, gotValue missing %v", expectedValue)
	}
}

// failureCatcher retient l'échec d'une assertion au lieu d'arrêter le test qui la vérifie.
type failureCatcher struct {
	testing.TB
	failures []string
}

func (catcher *failureCatcher) Errorf(format string, arguments ...any) {
	catcher.failures = append(catcher.failures, format)
}

func TestAssertNoWriteUnder_catchesAWriteUnderARootOnly(t *testing.T) {
	skipOnWindows(t)
	libraryRoot, linkDir := t.TempDir(), t.TempDir()
	recorder := NewRecorder(local.New())
	root, err := recorder.OpenWriteRoot(linkDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := root.MkdirAll("outside", 0o750); err != nil {
		t.Fatal(err)
	}
	clean := &failureCatcher{TB: t}
	recorder.AssertNoWriteUnder(clean, libraryRoot)
	if len(clean.failures) != 0 {
		t.Fatalf("expectedValue no failure for writes outside the library, gotValue %v", clean.failures)
	}

	libraryWriteRoot, err := recorder.OpenWriteRoot(libraryRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = libraryWriteRoot.Close() }()
	dirty := &failureCatcher{TB: t}
	recorder.AssertNoWriteUnder(dirty, libraryRoot)

	if len(dirty.failures) == 0 {
		t.Fatal("expectedValue a failure for a write root opened on the library, gotValue none")
	}
}

func TestRecorder_isSafeForConcurrentCalls(t *testing.T) {
	recorder := NewRecorder(stubVolume{})
	const callers = 8
	done := make(chan struct{})
	for callerIndex := range callers {
		go func() {
			_, _ = recorder.Stat("/x/" + strconv.Itoa(callerIndex))
			done <- struct{}{}
		}()
	}
	for range callers {
		<-done
	}

	if gotValue := len(recorder.Calls()); gotValue != callers {
		t.Fatalf("expectedValue %d calls, gotValue %d", callers, gotValue)
	}
}

// fsystestImportPath et sftptestImportPath sont les paquets d'outillage réservés aux tests.
const (
	fsystestImportPath = "necromanciarr/internal/fsys/fsystest"
	sftptestImportPath = "necromanciarr/internal/fsys/sftpfs/sftptest"
)

// testOnlyPackages nomme les dossiers de ces paquets, seuls autorisés à s'importer entre eux hors des tests.
var testOnlyPackages = []string{"fsystest", "sftptest"}

func TestFsystest_isImportedOnlyByTests(t *testing.T) {
	repositoryRoot := filepath.Join("..", "..", "..")
	var offenders []string
	for _, top := range []string{"internal", "cmd"} {
		offenders = append(offenders, productionImporters(t, filepath.Join(repositoryRoot, top))...)
	}

	if len(offenders) != 0 {
		t.Fatalf("expectedValue fsystest and sftptest imported by test files only, gotValue %v", offenders)
	}
}

// productionImporters liste les fichiers non test qui importent fsystest ou sftptest, hors de ces paquets.
func productionImporters(t *testing.T, directory string) []string {
	t.Helper()
	var offenders []string
	walkErr := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		isProductionGo := !entry.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go")
		if !isProductionGo || slices.Contains(testOnlyPackages, filepath.Base(filepath.Dir(path))) {
			return nil
		}
		if importsFsystest(t, path) {
			offenders = append(offenders, filepath.ToSlash(path))
		}
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
	return offenders
}

func importsFsystest(t *testing.T, path string) bool {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	return slices.ContainsFunc(parsed.Imports, func(spec *ast.ImportSpec) bool {
		importPath, _ := strconv.Unquote(spec.Path.Value)
		return importPath == fsystestImportPath || importPath == sftptestImportPath
	})
}

func TestImportsFsystest_flagsAProductionImport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leak.go")
	source := "package leak\n\nimport _ \"" + fsystestImportPath + "\"\n"
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	if !importsFsystest(t, path) {
		t.Fatal("expectedValue a production import of fsystest flagged, gotValue none")
	}
}

func TestImportsFsystest_flagsAProductionImportOfSftptest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leak.go")
	source := "package leak\n\nimport _ \"" + sftptestImportPath + "\"\n"
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	if !importsFsystest(t, path) {
		t.Fatal("expectedValue a production import of sftptest flagged, gotValue none")
	}
}
