package fsystest

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/local"
)

// RemoteVolume est un volume distant factice, réservé aux tests : ses chemins sont POSIX (« sftp://<nom>/… »), servis
// par un dossier local temporaire. FileIDKnown bascule l'identité des fichiers (inconnue par défaut, comme en SFTP
// sans exec) ; Unavailable fait échouer chaque appel avec fsys.ErrRemoteUnavailable, comme une connexion perdue.
// Une cible de lien symbolique en chemin complet est traduite vers ce dossier à l'écriture et retraduite par Readlink.
type RemoteVolume struct {
	name        string
	backing     string
	inner       fsys.Volume
	FileIDKnown atomic.Bool
	Unavailable atomic.Bool
}

// remoteVolumeIdentity : tout le volume factice est un seul système de fichiers distant.
const remoteVolumeIdentity = "remote-volume"

// NewRemoteVolume crée un volume factice nommé name (un nom de fournisseur valide, en minuscules), vide.
func NewRemoteVolume(t testing.TB, name string) *RemoteVolume {
	t.Helper()
	backing, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &RemoteVolume{name: name, backing: backing, inner: local.New()}
}

// LocalPath rend le chemin, sur le disque qui sert le volume, du chemin distant remotePath (pour préparer un test).
func (volume *RemoteVolume) LocalPath(remotePath string) string {
	return filepath.Join(volume.backing, filepath.FromSlash(path.Clean("/"+remotePath)))
}

// Location rend l'adresse fsys du chemin distant remotePath.
func (volume *RemoteVolume) Location(remotePath string) fsys.Location {
	return fsys.Location{Volume: volume.name, Path: path.Clean(remotePath)}
}

// MkdirAll crée, hors du volume (préparation de test), le dossier distant remotePath.
func (volume *RemoteVolume) MkdirAll(t testing.TB, remotePath string) {
	t.Helper()
	if err := os.MkdirAll(volume.LocalPath(remotePath), 0o750); err != nil {
		t.Fatal(err)
	}
}

func (volume *RemoteVolume) unavailable() error {
	if volume.Unavailable.Load() {
		return fmt.Errorf("%w: test volume %s", fsys.ErrRemoteUnavailable, volume.name)
	}
	return nil
}

// toLocal refuse tout chemin distant qui n'est pas POSIX absolu et propre.
func (volume *RemoteVolume) toLocal(remotePath string) (string, error) {
	if err := volume.unavailable(); err != nil {
		return "", err
	}
	isClean := path.IsAbs(remotePath) && path.Clean(remotePath) == remotePath
	if !isClean {
		return "", fmt.Errorf("%w: remote path is not absolute and clean", fs.ErrInvalid)
	}
	return volume.LocalPath(remotePath), nil
}

func (volume *RemoteVolume) toRemote(localPath string) (string, error) {
	relative, err := filepath.Rel(volume.backing, localPath)
	if err != nil || !filepath.IsLocal(relative) && relative != "." {
		return "", fmt.Errorf("%w: outside the test volume", fsys.ErrCannotCanonicalize)
	}
	if relative == "." {
		return "/", nil
	}
	return "/" + filepath.ToSlash(relative), nil
}

// Volume nomme le volume.
func (volume *RemoteVolume) Volume() string { return volume.name }

// Paths rend la syntaxe POSIX distante.
func (volume *RemoteVolume) Paths() fsys.PathSyntax { return fsys.RemotePaths() }

// Open ouvre en lecture seule.
func (volume *RemoteVolume) Open(name string) (fsys.File, error) {
	localPath, err := volume.toLocal(name)
	if err != nil {
		return nil, err
	}
	return volume.inner.Open(localPath)
}

// Stat suit les liens symboliques.
func (volume *RemoteVolume) Stat(name string) (fs.FileInfo, error) {
	localPath, err := volume.toLocal(name)
	if err != nil {
		return nil, err
	}
	return volume.inner.Stat(localPath)
}

// Lstat ne suit pas les liens symboliques.
func (volume *RemoteVolume) Lstat(name string) (fs.FileInfo, error) {
	localPath, err := volume.toLocal(name)
	if err != nil {
		return nil, err
	}
	return volume.inner.Lstat(localPath)
}

// ReadDir liste le dossier, trié par nom.
func (volume *RemoteVolume) ReadDir(name string) ([]fs.DirEntry, error) {
	localPath, err := volume.toLocal(name)
	if err != nil {
		return nil, err
	}
	return volume.inner.ReadDir(localPath)
}

// Readlink rend la cible écrite.
func (volume *RemoteVolume) Readlink(name string) (string, error) {
	localPath, err := volume.toLocal(name)
	if err != nil {
		return "", err
	}
	target, err := volume.inner.Readlink(localPath)
	if err != nil || !filepath.IsAbs(target) {
		return target, err
	}
	return volume.toRemote(target)
}

// Canonical résout les liens symboliques sous le dossier qui sert le volume.
func (volume *RemoteVolume) Canonical(name string) (string, error) {
	localPath, err := volume.toLocal(name)
	if err != nil {
		return "", fmt.Errorf("%w: %w", fsys.ErrCannotCanonicalize, err)
	}
	resolved, err := volume.inner.Canonical(localPath)
	if err != nil {
		return "", err
	}
	return volume.toRemote(resolved)
}

// VolumeID rend une identité unique pour tout le volume.
func (volume *RemoteVolume) VolumeID(name string) (string, bool, error) {
	if _, err := volume.Lstat(name); err != nil {
		return "", false, err
	}
	return remoteVolumeIdentity, true, nil
}

// FileID n'est connue que si FileIDKnown.
func (volume *RemoteVolume) FileID(info fs.FileInfo) (fsys.FileID, bool) {
	if !volume.FileIDKnown.Load() {
		return fsys.FileID{}, false
	}
	return volume.inner.FileID(info)
}

// FreeSpace est inconnue, comme sans statvfs.
func (volume *RemoteVolume) FreeSpace(string) (uint64, error) {
	if err := volume.unavailable(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("%w: free space unknown on the test volume", fs.ErrInvalid)
}

// Capabilities : liens durs et symboliques, remplacement atomique, aucune identité annoncée (contrôle textuel seul).
func (volume *RemoteVolume) Capabilities(context.Context) (fsys.Capabilities, error) {
	if err := volume.unavailable(); err != nil {
		return fsys.Capabilities{}, err
	}
	return fsys.Capabilities{VolumeIdentity: true, Symlink: true, Hardlink: true, AtomicReplace: true}, nil
}

// OpenWriteRoot ouvre la racine d'écriture du volume local qui sert le dossier, vue sous ses chemins distants.
func (volume *RemoteVolume) OpenWriteRoot(dir string) (fsys.WriteRoot, error) {
	localPath, err := volume.toLocal(dir)
	if err != nil {
		return nil, err
	}
	inner, err := volume.inner.OpenWriteRoot(localPath)
	if err != nil {
		return nil, err
	}
	return &remoteWriteRoot{volume: volume, root: volume.Location(dir), inner: inner}, nil
}

// remoteWriteRoot traduit les sources distantes vers le disque qui sert le volume ; les chemins relatifs sont en « / ».
type remoteWriteRoot struct {
	volume *RemoteVolume
	root   fsys.Location
	inner  fsys.WriteRoot
}

func (root *remoteWriteRoot) source(source fsys.Location) (fsys.Location, error) {
	if source.Volume != root.volume.name {
		return fsys.Location{}, fmt.Errorf("%w: source on another volume", fsys.ErrUnconfined)
	}
	if !strings.HasPrefix(source.Path, "/") {
		return fsys.Location{}, fmt.Errorf("%w: relative source", fsys.ErrUnconfined)
	}
	localPath, err := root.volume.toLocal(source.Path)
	if err != nil {
		return fsys.Location{}, err
	}
	return fsys.Location{Path: localPath}, nil
}

func (root *remoteWriteRoot) Root() fsys.Location { return root.root }

func (root *remoteWriteRoot) MkdirAll(relative string, perm fs.FileMode) error {
	if err := root.volume.unavailable(); err != nil {
		return err
	}
	return root.inner.MkdirAll(relative, perm)
}

func (root *remoteWriteRoot) Symlink(target, relative string) error {
	if err := root.volume.unavailable(); err != nil {
		return err
	}
	if path.IsAbs(target) {
		target = root.volume.LocalPath(target)
	}
	return root.inner.Symlink(target, relative)
}

func (root *remoteWriteRoot) Hardlink(source fsys.Location, relative string) error {
	localSource, err := root.source(source)
	if err != nil {
		return err
	}
	return root.inner.Hardlink(localSource, relative)
}

func (root *remoteWriteRoot) CreateExclusive(relative string, perm fs.FileMode) (io.WriteCloser, error) {
	if err := root.volume.unavailable(); err != nil {
		return nil, err
	}
	return root.inner.CreateExclusive(relative, perm)
}

func (root *remoteWriteRoot) CopyFrom(ctx context.Context, source fsys.Location, relative string) (int64, error) {
	localSource, err := root.source(source)
	if err != nil {
		return 0, err
	}
	return root.inner.CopyFrom(ctx, localSource, relative)
}

func (root *remoteWriteRoot) Replace(oldRelative, newRelative string) error {
	if err := root.volume.unavailable(); err != nil {
		return err
	}
	return root.inner.Replace(oldRelative, newRelative)
}

func (root *remoteWriteRoot) Remove(relative string) error {
	if err := root.volume.unavailable(); err != nil {
		return err
	}
	return root.inner.Remove(relative)
}

func (root *remoteWriteRoot) Lstat(relative string) (fs.FileInfo, error) {
	if err := root.volume.unavailable(); err != nil {
		return nil, err
	}
	return root.inner.Lstat(relative)
}

func (root *remoteWriteRoot) Close() error { return root.inner.Close() }
