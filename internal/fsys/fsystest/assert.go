package fsystest

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"testing"

	"necromanciarr/internal/fsys"
)

// fixtureEntries : ReadDir doit les rendre dans cet ordre (tri par nom, '-' avant '.').
func fixtureEntries(subject Subject) []string {
	if subject.LibraryHasSymlink {
		return []string{FixtureNoteName, FixtureLinkName, FixtureMovieName, FixtureSubdirName}
	}
	return []string{FixtureNoteName, FixtureMovieName, FixtureSubdirName}
}

func openWriteRoot(t *testing.T, subject Subject) fsys.WriteRoot {
	t.Helper()
	canonical, err := subject.Volume.Canonical(subject.LinkDir)
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	root, err := subject.Volume.OpenWriteRoot(canonical)
	if err != nil {
		t.Fatalf("OpenWriteRoot: %v", err)
	}
	t.Cleanup(func() { closeQuietly(root) })
	return root
}

func capabilitiesOf(t *testing.T, volume fsys.Volume) fsys.Capabilities {
	t.Helper()
	capabilities, err := volume.Capabilities(t.Context())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	return capabilities
}

func writeThroughRoot(t *testing.T, root fsys.WriteRoot, relative, content string) {
	t.Helper()
	writer, err := root.CreateExclusive(relative, fileMode)
	if err != nil {
		t.Fatalf("CreateExclusive(%q): %v", relative, err)
	}
	if _, err := io.WriteString(writer, content); err != nil {
		closeQuietly(writer)
		t.Fatalf("write %q: %v", relative, err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close %q: %v", relative, err)
	}
}

func assertUnconfined(t *testing.T, method, relative string, err error) {
	t.Helper()
	if !errors.Is(err, fsys.ErrUnconfined) {
		t.Errorf("%s(%q) error = %v, want ErrUnconfined", method, relative, err)
	}
}

func assertContent(t *testing.T, volume fsys.Volume, name, expectedValue string) {
	t.Helper()
	file, err := volume.Open(name)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer closeQuietly(file)
	info, err := file.Stat()
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if gotValue := readAllAt(t, file, info.Size()); gotValue != expectedValue {
		t.Errorf("content = %q, want %q", gotValue, expectedValue)
	}
}

// assertLibraryIntact vérifie que la bibliothèque a gardé exactement ses entrées et ses contenus.
func assertLibraryIntact(t *testing.T, subject Subject) {
	t.Helper()
	volume := subject.Volume
	expectedEntries := fixtureEntries(subject)
	if gotValue := entryNames(t, volume, subject.LibraryRoot); !slices.Equal(gotValue, expectedEntries) {
		t.Errorf("library entries = %q, want %q", gotValue, expectedEntries)
	}
	subdirEntries := entryNames(t, volume, volume.Paths().Join(subject.LibraryRoot, FixtureSubdirName))
	if len(subdirEntries) != 0 {
		t.Errorf("library subdir entries = %q, want none", subdirEntries)
	}
	assertContent(t, volume, volume.Paths().Join(subject.LibraryRoot, FixtureMovieName), FixtureMovieContent)
	assertContent(t, volume, volume.Paths().Join(subject.LibraryRoot, FixtureNoteName), FixtureNoteContent)
}

func entryNames(t *testing.T, volume fsys.Volume, dir string) []string {
	t.Helper()
	entries, err := volume.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func readAllAt(t *testing.T, reader io.ReaderAt, size int64) string {
	t.Helper()
	var buffer bytes.Buffer
	if _, err := io.Copy(&buffer, io.NewSectionReader(reader, 0, size+1)); err != nil {
		t.Fatalf("read: %v", err)
	}
	return buffer.String()
}

func closeQuietly(closer io.Closer) {
	_ = closer.Close()
}
