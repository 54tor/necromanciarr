package sftpfs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/pkg/sftp"

	"necromanciarr/internal/fsys"
)

const (
	extensionStatVFS = "statvfs@openssh.com"
	// Clés et codes de Capabilities.Reasons (contrat machine, jamais traduits).
	reasonKeyFileIdentity   = "fileIdentity"
	reasonKeyVolumeIdentity = "volumeIdentity"
	reasonKeyServerCopy     = "serverCopy"
	reasonKeySymlink        = "symlink"
	reasonKeyHardlink       = "hardlink"
	reasonKeyAtomicReplace  = "atomicReplace"
	reasonRemoteStatMissing = "remote_stat_unavailable"
	// reasonExtensionMissing : le serveur n'annonce pas l'extension ; aucune requête n'est envoyée pour elle.
	reasonExtensionMissing = "extension_missing"
	// reasonProbeFailed : l'extension est annoncée (ou la requête fait partie du protocole) mais l'essai réel a échoué.
	reasonProbeFailed = "probe_failed"
	// reasonServerCopyByClient : pkg/sftp n'envoie pas copy-data, quelle que soit l'annonce du serveur.
	reasonServerCopyByClient = "server_copy_unsupported_by_client"
	// reasonNotProbed : aucune sonde n'a encore abouti sur cette session.
	reasonNotProbed = fsys.ReasonNotProbed
)

// errStatVFSMissing : le serveur n'annonce pas statvfs@openssh.com.
var errStatVFSMissing = fmt.Errorf("sftpfs: %s not offered by the server: %w", extensionStatVFS, errors.ErrUnsupported)

// Le volume est la vue en lecture seule d'un fournisseur sftp : aucune méthode de ce fichier n'écrit (les écritures
// passent par la racine confinée de write.go).
var _ fsys.ReadFS = (*Volume)(nil)

// background : l'interface ReadFS ne porte pas de contexte ; timeout borne chaque opération.
var background = context.Background()

// checkName refuse un nom relatif (résolu par le serveur depuis le dossier de session) ou coupé par un NUL.
func checkName(operation, name string) error {
	if !path.IsAbs(name) || strings.ContainsRune(name, 0) {
		return fmt.Errorf("sftpfs: %s %q: %w: absolute POSIX path without NUL required", operation, name, fs.ErrInvalid)
	}
	return nil
}

// Volume rend le nom du fournisseur, celui des adresses sftp://<name>/….
func (volume *Volume) Volume() string { return volume.provider.Name }

// Paths rend la syntaxe POSIX : un chemin distant ne passe jamais par filepath.
func (volume *Volume) Paths() fsys.PathSyntax { return fsys.RemotePaths() }

// Open ouvre name avec SSH_FXF_READ seul ; le fichier rendu n'expose que la lecture. Ce qui n'est pas un fichier
// ordinaire (dossier, FIFO, périphérique) est refusé avant l'ouverture (fs.ErrInvalid).
func (volume *Volume) Open(name string) (fsys.File, error) {
	info, err := volume.Stat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("sftpfs: open %q: %w: not a regular file", name, fs.ErrInvalid)
	}
	var opened readOnlyFile
	err = volume.callSession(background, "open", func(current *session) error {
		file, openErr := current.client.OpenFile(name, os.O_RDONLY)
		opened = readOnlyFile{volume: volume, current: current, file: file, name: name}
		return openErr
	})
	if err != nil {
		return nil, mapError("open", name, err)
	}
	return opened, nil
}

// Stat suit les liens symboliques.
func (volume *Volume) Stat(name string) (fs.FileInfo, error) {
	return volume.statWith("stat", name, (*sftp.Client).Stat)
}

// Lstat ne suit pas un lien symbolique final.
func (volume *Volume) Lstat(name string) (fs.FileInfo, error) {
	return volume.statWith("lstat", name, (*sftp.Client).Lstat)
}

func (volume *Volume) statWith(operation, name string, statFn func(*sftp.Client, string) (os.FileInfo, error)) (fs.FileInfo, error) {
	if err := checkName(operation, name); err != nil {
		return nil, err
	}
	var info fs.FileInfo
	err := volume.call(background, operation, func(client *sftp.Client) error {
		var statErr error
		info, statErr = statFn(client, name)
		return statErr
	})
	if err != nil {
		return nil, mapError(operation, name, err)
	}
	checked, err := checkInfo(operation, name, info)
	if err != nil {
		return nil, err
	}
	return namedInfo{FileInfo: checked, name: name, followed: operation == "stat"}, nil
}

// ReadDir rend les entrées de name triées par nom ; les noms dangereux (vide, « . », « .. », « / », NUL, plus de
// 4096 octets), les tailles impossibles et les doublons sont écartés (keepListingName). Au-delà de
// maxListingEntries entrées : ErrHostileResponse.
func (volume *Volume) ReadDir(name string) ([]fs.DirEntry, error) {
	if err := checkName("readdir", name); err != nil {
		return nil, err
	}
	var infos []fs.FileInfo
	err := volume.call(background, "readdir", func(client *sftp.Client) error {
		var listErr error
		infos, listErr = client.ReadDir(name)
		return listErr
	})
	if err != nil {
		return nil, mapError("readdir", name, err)
	}
	return listingEntries(name, infos)
}

// Readlink rend la cible brute d'un lien symbolique ; plus de 4096 octets ou un NUL : ErrHostileResponse.
func (volume *Volume) Readlink(name string) (string, error) {
	if err := checkName("readlink", name); err != nil {
		return "", err
	}
	var target string
	err := volume.call(background, "readlink", func(client *sftp.Client) error {
		var linkErr error
		target, linkErr = client.ReadLink(name)
		return linkErr
	})
	if err != nil {
		return "", mapError("readlink", name, err)
	}
	return checkLinkTarget(name, target)
}

// Canonical rend le RealPath du serveur nettoyé (path.Clean) : absolu, sans « .. », égal à son propre Clean. Tout
// échec, volume cassé compris (ErrRemoteUnavailable), rend fsys.ErrCannotCanonicalize.
func (volume *Volume) Canonical(name string) (string, error) {
	if err := checkName("realpath", name); err != nil {
		return "", fmt.Errorf("%w: %w", fsys.ErrCannotCanonicalize, err)
	}
	var resolved string
	err := volume.call(background, "realpath", func(client *sftp.Client) error {
		var realErr error
		resolved, realErr = client.RealPath(name)
		return realErr
	})
	if err != nil {
		return "", fmt.Errorf("%w: %w", fsys.ErrCannotCanonicalize, mapError("realpath", name, err))
	}
	isUsable := path.IsAbs(resolved) && len(resolved) <= maxNameBytes && !strings.ContainsRune(resolved, 0)
	if !isUsable {
		return "", fmt.Errorf("%w: %w: sftpfs: realpath %q: unusable answer", fsys.ErrCannotCanonicalize, ErrHostileResponse, name)
	}
	return path.Clean(resolved), nil
}

// VolumeID rend le fsid de statvfs@openssh.com (« fsid:<hexadécimal> ») ; sans l'extension, ou avec un fsid nul,
// l'identité est inconnue (known=false). Aucune identité n'est déduite de l'adresse.
func (volume *Volume) VolumeID(name string) (string, bool, error) {
	reply, err := volume.statVFS(name)
	if errors.Is(err, errors.ErrUnsupported) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if reply.Fsid == 0 {
		return "", false, nil
	}
	return fmt.Sprintf("fsid:%016x", reply.Fsid), true, nil
}

// FreeSpace rend f_bavail × f_frsize de statvfs@openssh.com. Sans l'extension : errors.ErrUnsupported, que linker
// range en ErrFreeSpaceUnknown (free_space_unknown) ; valeurs impossibles : ErrHostileResponse.
func (volume *Volume) FreeSpace(name string) (uint64, error) {
	reply, err := volume.statVFS(name)
	if err != nil {
		return 0, err
	}
	return availableBytes(name, reply)
}

func (volume *Volume) statVFS(name string) (*sftp.StatVFS, error) {
	if err := checkName("statvfs", name); err != nil {
		return nil, err
	}
	var reply *sftp.StatVFS
	err := volume.call(background, "statvfs", func(client *sftp.Client) error {
		if _, offered := client.HasExtension(extensionStatVFS); !offered {
			return errStatVFSMissing
		}
		var statErr error
		reply, statErr = client.StatVFS(name)
		return statErr
	})
	if err != nil {
		return nil, mapError("statvfs", name, err)
	}
	return reply, nil
}

// Capabilities rend, sans rien écrire ni sonder, les capacités prouvées par ProbeCapabilities sur la session
// courante ; avant toute sonde (ou après une reconnexion), aucune fonction dépendant d'une sonde n'est annoncée :
// raison extension_missing si le serveur ne l'annonce pas, not_probed sinon. Le registre l'appelle avant la première
// racine d'écriture (identités de fichier) : elle ne peut donc ni écrire ni échouer faute de dossier de liens.
func (volume *Volume) Capabilities(ctx context.Context) (fsys.Capabilities, error) {
	var capabilities fsys.Capabilities
	err := volume.callSession(ctx, "capabilities", func(current *session) error {
		if probed := current.capabilities.Load(); probed != nil {
			capabilities = volume.withExecReason(current, cloneCapabilities(*probed))
			return nil
		}
		capabilities = volume.withExecReason(current, unprobedCapabilities(current.client))
		return nil
	})
	if err != nil {
		return fsys.Capabilities{}, err
	}
	return capabilities, nil
}
