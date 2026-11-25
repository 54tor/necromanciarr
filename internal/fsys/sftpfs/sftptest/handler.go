package sftptest

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"strings"

	"github.com/pkg/sftp"
)

const (
	// windowsDrivePrefix est le début des chemins d'un serveur Windows (OpenSSH pour Windows).
	windowsDrivePrefix = "/C:"
	directoryMode      = 0o755
	fileMode           = 0o644
	// statVFSBlockSize et statVFSBlocks décrivent un volume factice de 4 Gio ; fsid fixe, nom long de 255 octets.
	statVFSBlockSize = 4096
	statVFSBlocks    = 1 << 20
	statVFSFsid      = 1
	statVFSNameMax   = 255
)

// extensionOfMethod lie une méthode pkg/sftp à l'extension qui la porte.
var extensionOfMethod = map[string]string{
	"PosixRename": extensionPosixRename,
	"Link":        extensionHardlink,
	"StatVFS":     extensionStatVFS,
}

var errSizeUnsupported = errors.New("sftptest: setstat of the size is not supported")

// fileHandler sert les requêtes SFTP dans server.root (os.Root : aucun chemin ne sort du dossier servi).
type fileHandler struct{ server *Server }

// admit enregistre la requête, la fige si sa méthode l'est, puis la refuse si son extension est masquée.
func (handler *fileHandler) admit(request Request, requestContext context.Context) error {
	server := handler.server
	server.record(request)
	if server.settings.stalledMethods[request.Method] {
		select {
		case <-requestContext.Done():
		case <-server.stopped:
		}
		return sftp.ErrSSHFxConnectionLost
	}
	if extension, found := extensionOfMethod[request.Method]; found && server.settings.hiddenExtensions[extension] {
		return sftp.ErrSSHFxOpUnsupported
	}
	return nil
}

// relative traduit un chemin du client (absolu, nettoyé par pkg/sftp) en chemin relatif au dossier servi.
func (handler *fileHandler) relative(clientPath string) string {
	if handler.server.settings.windowsRoot {
		clientPath = strings.TrimPrefix(clientPath, windowsDrivePrefix)
	}
	relativePath := strings.TrimPrefix(path.Clean("/"+clientPath), "/")
	if relativePath == "" {
		return "."
	}
	return relativePath
}

// Fileread ouvre un fichier en lecture (méthode Get).
func (handler *fileHandler) Fileread(request *sftp.Request) (io.ReaderAt, error) {
	if err := handler.admit(Request{Method: request.Method, Path: request.Filepath}, request.Context()); err != nil {
		return nil, err
	}
	file, err := handler.server.root.Open(handler.relative(request.Filepath))
	if err != nil {
		return nil, err
	}
	return file, nil
}

// Filewrite ouvre un fichier en écriture seule (méthode Put).
func (handler *fileHandler) Filewrite(request *sftp.Request) (io.WriterAt, error) {
	file, err := handler.openForWrite(request, os.O_WRONLY)
	if err != nil {
		return nil, err
	}
	return file, nil
}

// OpenFile ouvre un fichier en lecture et écriture (méthode Open).
func (handler *fileHandler) OpenFile(request *sftp.Request) (sftp.WriterAtReaderAt, error) {
	file, err := handler.openForWrite(request, os.O_RDWR)
	if err != nil {
		return nil, err
	}
	return file, nil
}

func (handler *fileHandler) openForWrite(request *sftp.Request, accessFlag int) (*os.File, error) {
	if err := handler.admit(Request{Method: request.Method, Path: request.Filepath}, request.Context()); err != nil {
		return nil, err
	}
	openFlags := request.Pflags()
	flags := accessFlag
	for _, option := range []struct {
		isSet bool
		flag  int
	}{{openFlags.Creat, os.O_CREATE}, {openFlags.Trunc, os.O_TRUNC}, {openFlags.Excl, os.O_EXCL}, {openFlags.Append, os.O_APPEND}} {
		if option.isSet {
			flags |= option.flag
		}
	}
	return handler.server.root.OpenFile(handler.relative(request.Filepath), flags, fileMode)
}

// Filecmd sert les requêtes sans réponse de données : Setstat, Rename, Rmdir, Mkdir, Link, Symlink, Remove.
func (handler *fileHandler) Filecmd(request *sftp.Request) error {
	recorded := Request{Method: request.Method, Path: request.Filepath, Target: request.Target}
	if request.Method == "Symlink" {
		recorded = handler.symlinkRequest(request)
	}
	if err := handler.admit(recorded, request.Context()); err != nil {
		return err
	}
	root := handler.server.root
	source, target := handler.relative(recorded.Path), handler.relative(recorded.Target)
	switch request.Method {
	case "Setstat":
		return handler.setstat(request, source)
	case "Rename":
		return renameWithoutReplacing(root, source, target)
	case "Rmdir", "Remove":
		return root.Remove(source)
	case "Mkdir":
		return root.Mkdir(source, directoryMode)
	case "Link":
		return root.Link(source, target)
	case "Symlink":
		return root.Symlink(recorded.Target, source)
	default:
		return sftp.ErrSSHFxOpUnsupported
	}
}

// symlinkRequest : pkg/sftp donne la cible brute dans Filepath et le lien, nettoyé, dans Target (ordre OpenSSH) ;
// l'option ReversedSymlinkArgs les échange, la cible reste alors le second argument tel que pkg/sftp l'a nettoyé
// (chemin absolu). Le journal porte le lien en Path et le texte de la cible en Target.
func (handler *fileHandler) symlinkRequest(request *sftp.Request) Request {
	linkPath, targetText := request.Target, request.Filepath
	if handler.server.settings.reversedSymlinkArgs {
		linkPath, targetText = path.Clean("/"+targetText), linkPath
	}
	return Request{Method: "Symlink", Path: linkPath, Target: targetText}
}

// renameWithoutReplacing suit SSH_FXP_RENAME : une cible existante n'est jamais remplacée.
func renameWithoutReplacing(root *os.Root, source, target string) error {
	if _, err := root.Lstat(target); err == nil {
		return os.ErrExist
	}
	return root.Rename(source, target)
}

func (handler *fileHandler) setstat(request *sftp.Request, relativePath string) error {
	flags, attributes := request.AttrFlags(), request.Attributes()
	if flags.Size {
		return errSizeUnsupported
	}
	if flags.Permissions {
		if err := handler.server.root.Chmod(relativePath, attributes.FileMode().Perm()); err != nil {
			return err
		}
	}
	if flags.Acmodtime {
		return handler.server.root.Chtimes(relativePath, attributes.AccessTime(), attributes.ModTime())
	}
	return nil
}

// PosixRename sert posix-rename@openssh.com : remplacement atomique d'une cible existante.
func (handler *fileHandler) PosixRename(request *sftp.Request) error {
	if err := handler.admit(Request{Method: "PosixRename", Path: request.Filepath, Target: request.Target}, request.Context()); err != nil {
		return err
	}
	return handler.server.root.Rename(handler.relative(request.Filepath), handler.relative(request.Target))
}

// StatVFS sert statvfs@openssh.com avec un volume factice.
func (handler *fileHandler) StatVFS(request *sftp.Request) (*sftp.StatVFS, error) {
	if err := handler.admit(Request{Method: "StatVFS", Path: request.Filepath}, request.Context()); err != nil {
		return nil, err
	}
	if _, err := handler.server.root.Stat(handler.relative(request.Filepath)); err != nil {
		return nil, err
	}
	freeBlocks := uint64(statVFSBlocks / 2)
	return &sftp.StatVFS{
		Bsize: statVFSBlockSize, Frsize: statVFSBlockSize, Blocks: statVFSBlocks, Bfree: freeBlocks, Bavail: freeBlocks,
		Files: statVFSBlocks, Ffree: freeBlocks, Favail: freeBlocks, Fsid: statVFSFsid, Namemax: statVFSNameMax,
	}, nil
}

// Filelist sert List (contenu d'un dossier) et Stat.
func (handler *fileHandler) Filelist(request *sftp.Request) (sftp.ListerAt, error) {
	if err := handler.admit(Request{Method: request.Method, Path: request.Filepath}, request.Context()); err != nil {
		return nil, err
	}
	relativePath := handler.relative(request.Filepath)
	if request.Method != "List" {
		return listOne(handler.server.root.Stat(relativePath))
	}
	directory, err := handler.server.root.Open(relativePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = directory.Close() }()
	entries, err := directory.Readdir(-1)
	if err != nil {
		return nil, err
	}
	return fileInfos(entries), nil
}

// Lstat sert SSH_FXP_LSTAT sans suivre un lien symbolique.
func (handler *fileHandler) Lstat(request *sftp.Request) (sftp.ListerAt, error) {
	if err := handler.admit(Request{Method: "Lstat", Path: request.Filepath}, request.Context()); err != nil {
		return nil, err
	}
	return listOne(handler.server.root.Lstat(handler.relative(request.Filepath)))
}

func listOne(info os.FileInfo, err error) (sftp.ListerAt, error) {
	if err != nil {
		return nil, err
	}
	return fileInfos{info}, nil
}

// Readlink rend le texte de la cible d'un lien symbolique.
func (handler *fileHandler) Readlink(clientPath string) (string, error) {
	if err := handler.admit(Request{Method: "Readlink", Path: clientPath}, context.Background()); err != nil {
		return "", err
	}
	return handler.server.root.Readlink(handler.relative(clientPath))
}

// RealPath rend le chemin absolu nettoyé, préfixé par « /C: » pour un serveur Windows simulé.
func (handler *fileHandler) RealPath(clientPath string) (string, error) {
	if err := handler.admit(Request{Method: "RealPath", Path: clientPath}, context.Background()); err != nil {
		return "", err
	}
	cleaned := path.Clean("/" + clientPath)
	if handler.server.settings.windowsRoot && !strings.HasPrefix(cleaned, windowsDrivePrefix) {
		cleaned = path.Clean(windowsDrivePrefix + cleaned)
	}
	if cleaned == windowsDrivePrefix {
		cleaned += "/"
	}
	return cleaned, nil
}

// fileInfos sert une liste déjà lue par tranches, comme l'attend pkg/sftp.
type fileInfos []os.FileInfo

// ListAt copie les entrées à partir de offset ; io.EOF signale la fin de la liste.
func (infos fileInfos) ListAt(destination []os.FileInfo, offset int64) (int, error) {
	if offset >= int64(len(infos)) {
		return 0, io.EOF
	}
	copied := copy(destination, infos[offset:])
	if offset+int64(copied) >= int64(len(infos)) {
		return copied, io.EOF
	}
	return copied, nil
}
