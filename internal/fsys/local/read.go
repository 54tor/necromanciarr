package local

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"necromanciarr/internal/fsys"
)

// errNotRegularFile refuse une FIFO, un périphérique, un dossier ou un fichier remplacé pendant l'ouverture :
// errors.Is(err, fs.ErrInvalid) le reconnaît.
var errNotRegularFile = fmt.Errorf("%w: local: not a regular file, or replaced while opening", fs.ErrInvalid)

// localInfo marque une information de fichier venue de ce volume, avec la fiabilité de son inode : seul FileID
// la lit. os.SameFile ne la reconnaît pas ; comparer par FileID.
type localInfo struct {
	fs.FileInfo
	identityReliable bool
}

// tagInfo classe le système de fichiers de classifiedPath : inode fiable seulement sur un volume Local.
func tagInfo(info fs.FileInfo, classifiedPath string) fs.FileInfo {
	kind, err := FilesystemKindOf(classifiedPath)
	return localInfo{FileInfo: info, identityReliable: err == nil && kind == Local}
}

// readOnlyFile cache *os.File : ni Write, ni Chmod, ni Truncate, ni Fd ne sont atteignables par assertion.
type readOnlyFile struct {
	file *os.File
	name string
}

func (readOnly readOnlyFile) ReadAt(buffer []byte, offset int64) (int, error) {
	return readOnly.file.ReadAt(buffer, offset)
}

func (readOnly readOnlyFile) Read(buffer []byte) (int, error) { return readOnly.file.Read(buffer) }

func (readOnly readOnlyFile) Seek(offset int64, whence int) (int64, error) {
	return readOnly.file.Seek(offset, whence)
}

func (readOnly readOnlyFile) Close() error { return readOnly.file.Close() }

func (readOnly readOnlyFile) Stat() (fs.FileInfo, error) {
	info, err := readOnly.file.Stat()
	if err != nil {
		return nil, err
	}
	return tagInfo(info, readOnly.name), nil
}

// Open ouvre name en lecture seule (O_RDONLY) ; le fichier rendu n'expose que la lecture. Tout ce qui n'est pas
// un fichier ordinaire (FIFO, périphérique, dossier) est refusé sans bloquer (fs.ErrInvalid).
func (volume) Open(name string) (fsys.File, error) {
	file, _, err := openRegularReadOnly(name)
	if err != nil {
		return nil, err
	}
	return readOnlyFile{file: file, name: name}, nil
}

// Stat suit les liens symboliques.
func (volume) Stat(name string) (fs.FileInfo, error) {
	info, err := os.Stat(name)
	if err != nil {
		return nil, err
	}
	return tagInfo(info, name), nil
}

// Lstat ne suit pas un lien symbolique final ; l'inode d'un lien se classe par le système de fichiers de son
// dossier, pas par celui de sa cible.
func (volume) Lstat(name string) (fs.FileInfo, error) {
	info, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	classifiedPath := name
	if info.Mode()&fs.ModeSymlink != 0 {
		classifiedPath = filepath.Dir(name)
	}
	return tagInfo(info, classifiedPath), nil
}

// ReadDir rend les entrées de name triées par nom.
func (volume) ReadDir(name string) ([]fs.DirEntry, error) { return os.ReadDir(name) }

// Readlink rend la cible brute d'un lien symbolique.
func (volume) Readlink(name string) (string, error) { return os.Readlink(name) }
