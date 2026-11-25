package sftptest

import (
	"context"
	"errors"
	"io"
	"os"
	"path"

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

// chmodBits : droits et bits setuid, setgid et sticky qu'un Setstat de droits applique.
const chmodBits = os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky

var errSizeUnsupported = errors.New("sftptest: setstat of the size is not supported")

// fileHandler sert les requêtes SFTP dans server.root (os.Root : aucun chemin ne sort du dossier servi) ; ended se
// ferme quand la connexion du client tombe.
type fileHandler struct {
	server *Server
	ended  <-chan struct{}
}

// admit enregistre la requête, lance les crochets OnRequest, la fige si sa méthode l'est, puis la refuse si son
// extension est masquée.
func (handler *fileHandler) admit(request Request, requestContext context.Context) error {
	server := handler.server
	server.record(request)
	server.runHooks(request)
	if server.settings.stalledMethods[request.Method] {
		select {
		case <-requestContext.Done():
		case <-handler.ended:
		case <-server.stopped:
		}
		return sftp.ErrSSHFxConnectionLost
	}
	if extension, found := extensionOfMethod[request.Method]; found && server.settings.hiddenExtensions[extension] {
		return sftp.ErrSSHFxOpUnsupported
	}
	if server.takeFailure(request.Method) {
		return errTransientFailure
	}
	return nil
}

// followed résout tous les liens, dernier segment compris (Stat, ouverture, liste) ; unfollowed garde le dernier
// segment tel quel (Lstat, Readlink, création, suppression, renommage).
func (handler *fileHandler) followed(clientPath string) (string, error) {
	return handler.resolve(clientPath, true)
}

func (handler *fileHandler) unfollowed(clientPath string) (string, error) {
	return handler.resolve(clientPath, false)
}

// Fileread ouvre un fichier en lecture (méthode Get).
func (handler *fileHandler) Fileread(request *sftp.Request) (io.ReaderAt, error) {
	if err := handler.admit(Request{Method: request.Method, Path: request.Filepath, Flags: request.Flags}, request.Context()); err != nil {
		return nil, err
	}
	relativePath, err := handler.followed(request.Filepath)
	if err != nil {
		return nil, err
	}
	file, err := handler.server.root.Open(relativePath)
	if err != nil {
		return nil, err
	}
	if limit, found := handler.server.settings.truncatedReads[path.Clean(request.Filepath)]; found {
		return truncatedReader{file: file, limit: limit}, nil
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
	if err := handler.admit(Request{Method: request.Method, Path: request.Filepath, Flags: request.Flags}, request.Context()); err != nil {
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
	// O_EXCL ne suit jamais un lien final, comme open(2) : un lien pendant n'est pas une place libre.
	relativePath, err := handler.resolve(request.Filepath, !openFlags.Excl)
	if err != nil {
		return nil, err
	}
	return handler.server.root.OpenFile(relativePath, flags, fileMode)
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
	if request.Method == "Setstat" {
		relativePath, err := handler.followed(recorded.Path)
		if err != nil {
			return err
		}
		return handler.setstat(request, relativePath)
	}
	source, err := handler.unfollowed(recorded.Path)
	if err != nil {
		return err
	}
	if request.Method == "Symlink" {
		return handler.server.root.Symlink(recorded.Target, source)
	}
	return handler.applyOnTwoPaths(request.Method, source, recorded.Target)
}

// applyOnTwoPaths sert Rename, Rmdir, Remove, Mkdir et Link ; target n'est lu que par Rename et Link.
func (handler *fileHandler) applyOnTwoPaths(method, source, clientTarget string) error {
	root := handler.server.root
	target, err := handler.unfollowed(clientTarget)
	if err != nil {
		return err
	}
	switch method {
	case "Rename":
		return renameWithoutReplacing(root, source, target)
	case "Rmdir", "Remove":
		return root.Remove(source)
	case "Mkdir":
		return handler.mkdir(source)
	case "Link":
		return root.Link(source, target)
	default:
		return sftp.ErrSSHFxOpUnsupported
	}
}

// mkdir crée un dossier ; avec WithDirectoryMode, son mode exact est posé, umask du processus ignoré.
func (handler *fileHandler) mkdir(relativePath string) error {
	root, mode := handler.server.root, handler.server.settings.directoryMode
	if err := root.Mkdir(relativePath, directoryMode); err != nil {
		return err
	}
	if mode == 0 {
		return nil
	}
	return root.Chmod(relativePath, mode)
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
		// Bits spéciaux compris, comme chmod(2) d'OpenSSH : un client qui ne les masque pas se voit.
		if err := handler.server.root.Chmod(relativePath, attributes.FileMode()&chmodBits); err != nil {
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
	source, err := handler.unfollowed(request.Filepath)
	if err != nil {
		return err
	}
	target, err := handler.unfollowed(request.Target)
	if err != nil {
		return err
	}
	return handler.server.root.Rename(source, target)
}

// StatVFS sert statvfs@openssh.com avec un volume factice.
func (handler *fileHandler) StatVFS(request *sftp.Request) (*sftp.StatVFS, error) {
	if err := handler.admit(Request{Method: "StatVFS", Path: request.Filepath}, request.Context()); err != nil {
		return nil, err
	}
	relativePath, err := handler.followed(request.Filepath)
	if err != nil {
		return nil, err
	}
	if _, err := handler.server.root.Stat(relativePath); err != nil {
		return nil, err
	}
	freeBlocks := uint64(statVFSBlocks / 2)
	response := &sftp.StatVFS{
		Bsize: statVFSBlockSize, Frsize: statVFSBlockSize, Blocks: statVFSBlocks, Bfree: freeBlocks, Bavail: freeBlocks,
		Files: statVFSBlocks, Ffree: freeBlocks, Favail: freeBlocks, Fsid: statVFSFsid, Namemax: statVFSNameMax,
	}
	if mutate := handler.server.settings.statVFSMutator; mutate != nil {
		mutate(response)
	}
	return response, nil
}

// Filelist sert List (contenu d'un dossier) et Stat.
func (handler *fileHandler) Filelist(request *sftp.Request) (sftp.ListerAt, error) {
	if err := handler.admit(Request{Method: request.Method, Path: request.Filepath}, request.Context()); err != nil {
		return nil, err
	}
	if fakes, found := handler.fakeListing(request.Filepath); found && request.Method == "List" {
		return fakes, nil
	}
	if fake, found := handler.fakeEntry(request.Filepath); found && request.Method != "List" {
		return fileInfos{fake}, nil
	}
	relativePath, err := handler.followed(request.Filepath)
	if err != nil {
		return nil, err
	}
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
	if fake, found := handler.fakeEntry(request.Filepath); found {
		return fileInfos{fake}, nil
	}
	relativePath, err := handler.unfollowed(request.Filepath)
	if err != nil {
		return nil, err
	}
	return listOne(handler.server.root.Lstat(relativePath))
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
	if target, found := handler.server.settings.readlinkTargets[path.Clean(clientPath)]; found {
		return target, nil
	}
	relativePath, err := handler.unfollowed(clientPath)
	if err != nil {
		return "", err
	}
	return handler.server.root.Readlink(relativePath)
}

// RealPath rend le chemin absolu canonique, liens résolus comme le fait OpenSSH, préfixé par « /C: » pour un
// serveur Windows simulé.
func (handler *fileHandler) RealPath(clientPath string) (string, error) {
	if err := handler.admit(Request{Method: "RealPath", Path: clientPath}, context.Background()); err != nil {
		return "", err
	}
	relativePath, err := handler.followed(clientPath)
	if err != nil {
		return "", err
	}
	canonical := path.Clean("/" + relativePath)
	if handler.server.settings.windowsRoot {
		canonical = windowsDrivePrefix + canonical
	}
	return canonical, nil
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
