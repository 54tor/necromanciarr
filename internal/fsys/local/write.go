package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"necromanciarr/internal/fsys"
)

// copyFileMode : une copie posée dans le dossier de liens doit rester lisible par le client torrent.
const copyFileMode fs.FileMode = 0o644

// copyBufferBytes borne la mémoire d'une copie par flux et l'intervalle entre deux contrôles d'annulation.
const copyBufferBytes = 1 << 20

// exclusiveCreateFlags : jamais d'entrée existante tronquée ni suivie (O_EXCL refuse aussi un lien symbolique).
const exclusiveCreateFlags = os.O_WRONLY | os.O_CREATE | os.O_EXCL

// errSourceChanged : la source a grandi pendant la copie ; la copie partielle est retirée.
var errSourceChanged = errors.New("local: copy source changed size during the copy")

// writeRoot est un dossier ouvert par handle (os.Root) ; il n'expose ni Volume() ni volume imbriqué. created garde
// l'identité des fichiers que cette racine a créés par CreateExclusive, seuls autorisés à Chtimes.
type writeRoot struct {
	dir              string
	root             *os.Root
	identity         fs.FileInfo
	identityReliable bool
	atomicReplace    bool
	createdMutex     sync.Mutex
	created          map[string]fs.FileInfo
}

// Root rend le dossier canonique reçu à l'ouverture.
func (root *writeRoot) Root() fsys.Location { return fsys.Location{Path: root.dir} }

// Close ferme le handle de la racine.
func (root *writeRoot) Close() error { return root.root.Close() }

// MkdirAll crée relative et ses parents manquants, segment par segment depuis les handles.
func (root *writeRoot) MkdirAll(relative string, perm fs.FileMode) error {
	segments, err := root.prepare(relative)
	if err != nil {
		return err
	}
	directory, owned, err := root.walkDirectories(segments, perm, true)
	if owned {
		_ = directory.Close()
	}
	return err
}

// Symlink pose relative -> target ; target n'est pas validée (lien vers la bibliothèque, son usage normal).
func (root *writeRoot) Symlink(target, relative string) error {
	destination, err := root.resolve(relative)
	if err != nil {
		return err
	}
	defer destination.close()
	return ClassifyLinkError(destination.parent.Symlink(target, destination.name))
}

// Hardlink lie source (chemin complet du même volume) à relative ; la source n'est jamais ouverte.
func (root *writeRoot) Hardlink(source fsys.Location, relative string) error {
	if err := checkSource(source); err != nil {
		return err
	}
	destination, err := root.resolve(relative)
	if err != nil {
		return err
	}
	defer destination.close()
	return ClassifyLinkError(linkInto(destination, source.Path))
}

// CreateExclusive crée relative en O_WRONLY|O_CREATE|O_EXCL ; une entrée existante rend fs.ErrExist. Seuls les
// droits 0o777 de perm sont gardés : jamais de setuid, setgid ni sticky dans un dossier de liens.
func (root *writeRoot) CreateExclusive(relative string, perm fs.FileMode) (io.WriteCloser, error) {
	segments, err := root.prepare(relative)
	if err != nil {
		return nil, err
	}
	destination, err := root.openParent(segments)
	if err != nil {
		return nil, err
	}
	defer destination.close()
	file, err := destination.parent.OpenFile(destination.name, exclusiveCreateFlags, perm&fs.ModePerm)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, errors.Join(err, file.Close(), destination.parent.Remove(destination.name))
	}
	root.remember(segments, info)
	return file, nil
}

func createdKey(segments []string) string { return strings.Join(segments, "/") }

func (root *writeRoot) remember(segments []string, info fs.FileInfo) {
	root.createdMutex.Lock()
	defer root.createdMutex.Unlock()
	if root.created == nil {
		root.created = map[string]fs.FileInfo{}
	}
	root.created[createdKey(segments)] = info
}

func (root *writeRoot) forget(segments []string) {
	root.createdMutex.Lock()
	defer root.createdMutex.Unlock()
	delete(root.created, createdKey(segments))
}

func (root *writeRoot) createdIdentity(segments []string) (fs.FileInfo, bool) {
	root.createdMutex.Lock()
	defer root.createdMutex.Unlock()
	info, known := root.created[createdKey(segments)]
	return info, known
}

// CopyFrom copie source vers relative : reflink si le système le permet, sinon flux borné à la taille de la
// source et interrompu par ctx. Une copie incomplète est retirée.
func (root *writeRoot) CopyFrom(ctx context.Context, source fsys.Location, relative string) (int64, error) {
	if err := checkSource(source); err != nil {
		return 0, err
	}
	destination, err := root.resolve(relative)
	if err != nil {
		return 0, err
	}
	defer destination.close()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	sourceFile, size, err := openCopySource(source.Path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = sourceFile.Close() }()
	destinationFile, err := destination.parent.OpenFile(destination.name, exclusiveCreateFlags, copyFileMode)
	if err != nil {
		return 0, err
	}
	copied, copyErr := fillCopy(ctx, destinationFile, sourceFile, size)
	closeErr := destinationFile.Close()
	if copyErr != nil || closeErr != nil {
		return 0, errors.Join(copyErr, closeErr, destination.parent.Remove(destination.name))
	}
	return copied, nil
}

// Replace renomme oldRelative sur newRelative ; sans remplacement atomique annoncé, rien n'est touché.
func (root *writeRoot) Replace(oldRelative, newRelative string) error {
	oldSegments, err := root.prepare(oldRelative)
	if err != nil {
		return err
	}
	newSegments, err := root.prepare(newRelative)
	if err != nil {
		return err
	}
	if !root.atomicReplace {
		return fsys.ErrNoAtomicReplace
	}
	defer root.forget(oldSegments)
	defer root.forget(newSegments)
	oldTarget, err := root.openParent(oldSegments)
	if err != nil {
		return err
	}
	defer oldTarget.close()
	if slices.Equal(oldSegments[:len(oldSegments)-1], newSegments[:len(newSegments)-1]) {
		return oldTarget.parent.Rename(oldTarget.name, newSegments[len(newSegments)-1])
	}
	newTarget, err := root.openParent(newSegments)
	if err != nil {
		return err
	}
	newTarget.close()
	// ponytail: entre deux dossiers, os.Root résout encore les parents par nom (confiné à la racine, mais un
	// parent remplacé entre le contrôle et le renommage n'est pas revu) ; même dossier = handle seul.
	return root.root.Rename(filepath.Join(oldSegments...), filepath.Join(newSegments...))
}

// Remove supprime un fichier, un lien ou un dossier vide de la racine.
func (root *writeRoot) Remove(relative string) error {
	segments, err := root.prepare(relative)
	if err != nil {
		return err
	}
	target, err := root.openParent(segments)
	if err != nil {
		return err
	}
	defer target.close()
	root.forget(segments)
	return target.parent.Remove(target.name)
}

// Lstat décrit relative sans suivre un lien final ; l'inode est fiable si la racine est sur un volume Local
// (aucun point de montage n'est traversé sous la racine).
func (root *writeRoot) Lstat(relative string) (fs.FileInfo, error) {
	target, err := root.resolve(relative)
	if err != nil {
		return nil, err
	}
	defer target.close()
	info, err := target.parent.Lstat(target.name)
	if err != nil {
		return nil, err
	}
	return localInfo{FileInfo: info, identityReliable: root.identityReliable}, nil
}

// checkSource exige une source du volume local à chemin absolu.
func checkSource(source fsys.Location) error {
	if source.Volume != "" || !filepath.IsAbs(source.Path) {
		return unconfined("source is not an absolute path of the local volume")
	}
	return nil
}

func openCopySource(sourcePath string) (*os.File, int64, error) {
	sourceFile, info, err := openRegularReadOnly(sourcePath)
	if err != nil {
		return nil, 0, err
	}
	return sourceFile, info.Size(), nil
}

// fillCopy tente le reflink ; un refus du système (autre volume, non pris en charge) passe à la copie par flux.
func fillCopy(ctx context.Context, destination, source *os.File, size int64) (int64, error) {
	cloneErr := cloneFile(destination, source)
	if cloneErr == nil {
		return size, nil
	}
	isFallback := errors.Is(cloneErr, ErrCrossDevice) || errors.Is(cloneErr, ErrUnsupported)
	if !isFallback {
		return 0, cloneErr
	}
	copied, err := streamCopy(ctx, destination, source, size)
	if err != nil {
		return 0, err
	}
	return copied, destination.Sync()
}

// streamCopy lit au plus size+1 octets : un octet de plus prouve que la source a grandi pendant la copie.
func streamCopy(ctx context.Context, destination io.Writer, source io.Reader, size int64) (int64, error) {
	limited := io.LimitReader(source, size+1)
	copied, err := io.CopyBuffer(destination, contextReader{ctx: ctx, reader: limited}, make([]byte, copyBufferBytes))
	if err != nil {
		return 0, err
	}
	if copied != size {
		return 0, fmt.Errorf("%w: %d bytes read, %d expected", errSourceChanged, copied, size)
	}
	return copied, nil
}

// contextReader arrête la lecture dès que ctx est annulé.
type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}

// Chtimes fixe les dates d'un fichier que cette racine a créé par CreateExclusive et qui est toujours le même
// (même identité, fichier ordinaire) : jamais un autre nom, jamais un lien dur vers un fichier d'ailleurs (la
// bibliothèque partage alors l'inode), jamais une entrée d'une ouverture précédente. Sinon ErrUnconfined.
func (root *writeRoot) Chtimes(relative string, accessTime, modTime time.Time) error {
	segments, err := root.prepare(relative)
	if err != nil {
		return err
	}
	createdInfo, known := root.createdIdentity(segments)
	if !known {
		return unconfined("times are set only on entries this write root created")
	}
	target, err := root.openParent(segments)
	if err != nil {
		return err
	}
	defer target.close()
	current, err := target.parent.Lstat(target.name)
	if err != nil {
		return err
	}
	if !current.Mode().IsRegular() || !os.SameFile(current, createdInfo) {
		return unconfined("entry is no longer the file this write root created")
	}
	return target.parent.Chtimes(target.name, accessTime, modTime)
}
