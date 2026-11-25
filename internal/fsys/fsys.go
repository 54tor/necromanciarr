// Package fsys abstrait les systèmes de fichiers (disque local, volumes SFTP) : une vue en lecture seule pour les
// racines de bibliothèque et des racines d'écriture confinées, obtenues uniquement par Registry.WriteRoot, qui refuse
// toute racine recouvrant une bibliothèque (invariant I1 par construction).
package fsys

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"time"
)

// Erreurs typées du paquet, à tester avec errors.Is ; leurs messages ne contiennent jamais de chemin.
var (
	// ErrWriteInLibrary refuse une racine d'écriture égale à une racine de bibliothèque, qui la contient ou y est contenue.
	ErrWriteInLibrary = errors.New("fsys: write root overlaps a library root")
	// ErrNoLibraryRoots refuse toute racine d'écriture quand le registre ne connaît aucune racine de bibliothèque :
	// sans elles, rien ne prouve l'absence de recouvrement. Un tel registre ne sert qu'à lire.
	ErrNoLibraryRoots = errors.New("fsys: no library root registered, write roots are refused")
	// ErrUnconfined refuse un chemin relatif qui sort de la racine d'écriture (absolu, "..", parent symbolique).
	ErrUnconfined = errors.New("fsys: path escapes the write root")
	// ErrNoAtomicReplace signale un volume incapable de remplacer une entrée existante de façon atomique.
	ErrNoAtomicReplace = errors.New("fsys: atomic replace unsupported by the volume")
	// ErrServerCopyUnavailable refuse une copie qu'aucune copie côté serveur ne peut faire (SFTP sans copy-data) :
	// jamais de repli sur une copie par flux à travers le réseau, qui relirait puis réécrirait tout le média (Force).
	ErrServerCopyUnavailable = errors.New("fsys: server-side copy unavailable on the volume")
	// ErrSymlinkOrderUnverified refuse un lien symbolique tant que la sonde de la session distante n'a pas prouvé que
	// le serveur lit les arguments dans l'ordre d'OpenSSH : un serveur à l'ordre inverse poserait le lien à la place
	// de la cible, bibliothèque comprise.
	ErrSymlinkOrderUnverified = errors.New("fsys: symlink argument order not verified on this session")
	// ErrRemoteExecRequired refuse une racine d'un volume distant réglé exec required dont l'exécution distante n'est
	// pas utilisable (sonde en échec) : raison remote_exec_required.
	ErrRemoteExecRequired = errors.New("fsys: remote commands required but unavailable")
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
	// FileIdentity : FileID rend des identités pour les dossiers, assez stables pour le contrôle de recouvrement du
	// registre (une collision fausse n'y cause qu'un refus). Ce n'est PAS une preuve « même fichier » pour une
	// opération destructive (dedupe) : celle-ci passe par l'accesseur strict du volume (local.ReliableFileID).
	FileIdentity                                          bool
	VolumeIdentity                                        bool // dev local ou fsid statvfs
	Symlink, Hardlink, AtomicReplace, ServerCopy, Reflink bool
	// RemoteStat : identité de fichier distante obtenue par ssh exec stat.
	RemoteStat bool
	// RemoteHash liste les empreintes calculables par l'hôte distant ("sha1", "md5").
	RemoteHash []string
	Reasons    map[string]string // fonction -> raison d'absence (code)
}

// ReasonNotProbed : raison d'une fonction qu'aucune sonde n'a encore essayée sur la session courante du volume.
const ReasonNotProbed = "not_probed"

// Probed dit qu'aucune fonction n'attend sa sonde : sinon ses booléens ne disent pas encore ce que le volume sait faire,
// et l'appelant ouvre d'abord une racine d'écriture (Registry.WriteRoot, qui sonde) avant de conclure.
func (capabilities Capabilities) Probed() bool {
	for _, reason := range capabilities.Reasons {
		if reason == ReasonNotProbed {
			return false
		}
	}
	return true
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
	// en local, RealPath puis nettoyage POSIX sans ".." à distance. C'est la forme SUR DISQUE (casse d'origine) :
	// elle nourrit Root et pathMappings ; le registre replie lui-même la casse pour comparer. Un volume qui
	// ne sait pas canonicaliser rend ErrCannotCanonicalize.
	Canonical(name string) (string, error)
	VolumeID(name string) (id string, known bool, err error)
	// FileID rend l'identité au sens de Capabilities.FileIdentity ; known=false (inode nul, plateforme sans inode,
	// SFTP sans exec) fait retomber le registre sur le seul contrôle textuel pour ce chemin.
	FileID(info fs.FileInfo) (id FileID, known bool)
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

// Reflinker est l'option d'une WriteRoot qui sait cloner un fichier du même volume (copie à l'écriture) sans
// repli sur une copie complète : CopyFrom copie toujours, Reflink refuse (ErrCrossDevice, ErrUnsupported du
// volume) quand le clonage est impossible. Une destination existante n'est jamais remplacée.
type Reflinker interface {
	Reflink(source Location, relative string) error
}

// TimesSetter est l'option d'une WriteRoot qui sait fixer les dates d'un fichier qu'elle a elle-même créé par
// CreateExclusive pendant cette ouverture, et qui est toujours ce fichier (sonde de la date de modification d'un
// montage) : tout autre nom, un lien dur vers un fichier d'ailleurs ou une entrée d'une ouverture précédente est
// refusé (ErrUnconfined). Un volume sans elle laisse cette date inconnue.
type TimesSetter interface {
	Chtimes(relative string, accessTime, modTime time.Time) error
}

// Limiter plafonne un débit de lecture, en octets ; même forme que torrentcheck.Limiter et dedupe.Limiter,
// compatible avec golang.org/x/time/rate.Limiter.
type Limiter interface {
	WaitN(ctx context.Context, n int) error
}

// Volume joint la lecture et l'ouverture d'une racine d'écriture. OpenWriteRoot n'est appelé que par
// Registry.WriteRoot, après le contrôle de recouvrement des bibliothèques : aucun autre paquet ne l'appelle.
// Go ne permet pas de sceller cet appel à la compilation : une règle forbidigo de golangci-lint l'interdira hors
// de internal/fsys, ajoutée par la tâche qui migre le premier paquet appelant. Une WriteRoot réelle n'expose ni
// Volume() ni volume imbriqué.
type Volume interface {
	ReadFS
	// OpenWriteRoot reçoit le dossier canonique ; il compare l'identité du dossier ouvert à un Stat fait juste
	// avant l'ouverture et refuse s'ils diffèrent (fenêtre TOCTOU entre Canonical et l'ouverture).
	OpenWriteRoot(dir string) (WriteRoot, error)
}
