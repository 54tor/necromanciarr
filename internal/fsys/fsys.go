// Package fsys abstrait les systèmes de fichiers (disque local, volumes SFTP) : une vue en lecture seule pour les
// racines de bibliothèque et des racines d'écriture confinées, obtenues uniquement par Registry.WriteRoot, qui refuse
// toute racine recouvrant une bibliothèque (invariant I1 par construction).
package fsys

import (
	"context"
	"errors"
	"io"
	"io/fs"
)

// Erreurs typées du paquet, à tester avec errors.Is ; leurs messages ne contiennent jamais de chemin.
var (
	// ErrWriteInLibrary refuse une racine d'écriture égale à une racine de bibliothèque, qui la contient ou y est contenue.
	ErrWriteInLibrary = errors.New("fsys: write root overlaps a library root")
	// ErrUnconfined refuse un chemin relatif qui sort de la racine d'écriture (absolu, "..", parent symbolique).
	ErrUnconfined = errors.New("fsys: path escapes the write root")
	// ErrNoAtomicReplace signale un volume incapable de remplacer une entrée existante de façon atomique.
	ErrNoAtomicReplace = errors.New("fsys: atomic replace unsupported by the volume")
	// ErrRemoteUnavailable signale un volume distant injoignable ou cassé.
	ErrRemoteUnavailable = errors.New("fsys: remote volume unavailable")
	// ErrUnknownVolume signale une Location dont le volume n'est pas déclaré.
	ErrUnknownVolume = errors.New("fsys: unknown volume")
	// ErrCannotCanonicalize signale un volume qui ne sait pas donner la forme canonique d'un chemin.
	ErrCannotCanonicalize = errors.New("fsys: volume cannot canonicalize the path")
	// ErrInvalidLocation refuse une Location mal formée (ParseLocation).
	ErrInvalidLocation = errors.New("fsys: invalid location")
)

// FileID identifie un fichier sur un volume (périphérique et inode).
type FileID struct{ Device, Inode uint64 }

// Capabilities dit ce qu'un volume sait faire, sondé réellement.
type Capabilities struct {
	FileIdentity                                          bool // inode + périphérique fiables (local non réseau)
	VolumeIdentity                                        bool // dev local ou fsid statvfs
	Symlink, Hardlink, AtomicReplace, ServerCopy, Reflink bool
	// RemoteStat : identité de fichier distante obtenue par ssh exec stat.
	RemoteStat bool
	// RemoteHash liste les empreintes calculables par l'hôte distant ("sha1", "md5").
	RemoteHash []string
	Reasons    map[string]string // fonction -> raison d'absence (code)
}

// ReadFS est la seule vue d'une racine de bibliothèque : aucune méthode n'écrit. Les noms sont des chemins absolus
// dans la syntaxe du volume (Paths).
type ReadFS interface {
	Volume() string
	Open(name string) (File, error) // toujours lecture seule (O_RDONLY local, SSH_FXF_READ seul en SFTP)
	Stat(name string) (fs.FileInfo, error)
	Lstat(name string) (fs.FileInfo, error)
	ReadDir(name string) ([]fs.DirEntry, error)
	Readlink(name string) (string, error)
	// Canonical rend la forme canonique d'un chemin, non relative et propre (Paths().Clean) : EvalSymlinks + Abs
	// (+ casse repliée sous Windows) en local, RealPath puis nettoyage POSIX sans ".." à distance. Un volume qui
	// ne sait pas canonicaliser rend ErrCannotCanonicalize.
	Canonical(name string) (string, error)
	VolumeID(name string) (id string, known bool, err error)
	FileID(info fs.FileInfo) (id FileID, known bool) // toujours known=false en SFTP sans exec
	FreeSpace(name string) (uint64, error)
	Paths() PathSyntax // filepath local, path (POSIX) distant
	Capabilities(ctx context.Context) (Capabilities, error)
}

// File est un fichier ouvert en lecture seule. Une implémentation ne rend jamais *os.File (Chmod, Chown,
// Truncate et Write y restent appelables par assertion de type, Chmod réussit même en O_RDONLY) : elle
// l'enveloppe dans un type non exporté qui n'expose que ReadAt, Read, Seek, Close et Stat.
type File interface {
	io.ReaderAt
	io.Closer
	Stat() (fs.FileInfo, error)
}

// WriteRoot est un dossier où l'on peut écrire, confiné : tout chemin est relatif, sans "..", sans segment symlink.
// Toute sortie de la racine rend ErrUnconfined.
type WriteRoot interface {
	Root() Location
	MkdirAll(relative string, perm fs.FileMode) error
	Symlink(target, relative string) error
	Hardlink(source Location, relative string) error                           // source sur le même volume, jamais ouverte en écriture
	CreateExclusive(relative string, perm fs.FileMode) (io.WriteCloser, error) // O_WRONLY|O_CREATE|O_EXCL
	CopyFrom(ctx context.Context, source Location, relative string) (int64, error)
	Replace(oldRelative, newRelative string) error // remplacement atomique, sinon ErrNoAtomicReplace
	Remove(relative string) error
	Lstat(relative string) (fs.FileInfo, error)
	Close() error
}

// Volume joint la lecture et l'ouverture d'une racine d'écriture. OpenWriteRoot n'est appelé que par
// Registry.WriteRoot, après le contrôle de recouvrement des bibliothèques : aucun autre paquet ne l'appelle.
// Go ne permet pas de sceller cet appel à la compilation : une règle forbidigo de golangci-lint l'interdira hors
// de internal/fsys, ajoutée par la tâche qui migre le premier paquet appelant. Une WriteRoot réelle n'expose ni
// Volume() ni volume imbriqué.
type Volume interface {
	ReadFS
	OpenWriteRoot(dir string) (WriteRoot, error)
}
