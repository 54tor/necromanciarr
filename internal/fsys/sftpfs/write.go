package sftpfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync/atomic"

	"github.com/pkg/sftp"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/local"
)

const (
	extensionHardlink    = "hardlink@openssh.com"
	extensionPosixRename = "posix-rename@openssh.com"
	// exclusiveCreateFlags : SSH_FXF_WRITE|CREAT|EXCL ; jamais TRUNC, jamais READ, jamais un lien final suivi.
	exclusiveCreateFlags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
)

var _ fsys.Volume = (*Volume)(nil)

// writeRoot est un dossier du serveur où l'on écrit, lié à la session qui l'a vérifié : une session perdue rend
// fsys.ErrRemoteUnavailable, jamais une reprise sur une nouvelle session sans nouvel OpenWriteRoot. Il n'expose ni
// Volume() ni le volume. Les extensions sont lues sur cette session.
type writeRoot struct {
	volume        *Volume
	current       *session
	dir           string
	hardlink      bool
	atomicReplace bool
	closed        atomic.Bool
}

// OpenWriteRoot ouvre dir, chemin canonique reçu du seul Registry.WriteRoot : il doit être absolu, propre, un vrai
// dossier (Lstat : ni lien, ni fichier) que le serveur résout en lui-même (RealPath).
//
// Confinement : SFTP n'a ni openat ni descripteur de dossier, il est donc fondé sur les chemins. Chaque opération
// refuse un chemin mal formé sans requête, vérifie par Lstat chaque parent depuis la racine (un lien ou une entrée
// spéciale : fsys.ErrUnconfined), exige que le RealPath du parent soit exactement son chemin, puis écrit, puis revoit
// ce RealPath (une entrée créée sous un parent devenu lien est refusée, jamais retirée par chemin). Fenêtre TOCTOU
// acceptée : entre le dernier contrôle et l'écriture, un acteur qui écrit déjà dans le dossier de liens peut
// remplacer un parent par un lien ; l'écriture atterrit alors ailleurs avant d'être vue. Défense recommandée : un
// compte SFTP sans droit d'écriture sur la bibliothèque.
func (volume *Volume) OpenWriteRoot(dir string) (fsys.WriteRoot, error) {
	isCanonical := path.IsAbs(dir) && path.Clean(dir) == dir && len(dir) <= maxNameBytes && !strings.ContainsRune(dir, 0)
	if !isCanonical {
		return nil, unconfined(fmt.Sprintf("write root %q is not a canonical absolute path", dir))
	}
	var opened *writeRoot
	err := volume.callSession(background, "openwriteroot", func(current *session) error {
		if err := checkPlainDirectory(current.client, dir); err != nil {
			return err
		}
		if err := checkResolvesTo(current.client, dir); err != nil {
			return err
		}
		_, hasHardlink := current.client.HasExtension(extensionHardlink)
		_, hasPosixRename := current.client.HasExtension(extensionPosixRename)
		opened = &writeRoot{volume: volume, current: current, dir: dir, hardlink: hasHardlink, atomicReplace: hasPosixRename}
		return nil
	})
	if err != nil {
		return nil, mapError("openwriteroot", dir, err)
	}
	return opened, nil
}

// Root rend le volume et le dossier canonique de la racine.
func (root *writeRoot) Root() fsys.Location {
	return fsys.Location{Volume: root.volume.Volume(), Path: root.dir}
}

// Close rend la racine inutilisable ; la session, partagée par le volume, reste ouverte.
func (root *writeRoot) Close() error {
	root.closed.Store(true)
	return nil
}

// run exécute fn sur la session de la racine, sans jamais rouvrir.
func (root *writeRoot) run(ctx context.Context, operation, relative string, fn func(client *sftp.Client) error) error {
	if root.closed.Load() {
		return fmt.Errorf("sftpfs: %s %q: %w", operation, relative, fs.ErrClosed)
	}
	err := root.volume.callOn(ctx, root.current, operation, func(current *session) error { return fn(current.client) })
	return mapError(operation, relative, err)
}

// MkdirAll crée relative et ses parents manquants, segment par segment ; un segment déjà présent doit être un vrai
// dossier. perm n'est pas transmis : SSH_FXP_MKDIR de pkg/sftp ne porte pas de droits, le umask du serveur
// s'applique, et un chmod par chemin suivrait un lien posé entre-temps.
func (root *writeRoot) MkdirAll(relative string, _ fs.FileMode) error {
	segments, err := splitRelative(relative)
	if err != nil {
		return err
	}
	return root.run(background, "mkdir", relative, func(client *sftp.Client) error {
		current := root.dir
		for _, segment := range segments {
			parent := current
			current = path.Join(current, segment)
			if err := ensureDirectory(client, parent, current); err != nil {
				return err
			}
		}
		return checkResolvesTo(client, current)
	})
}

// ensureDirectory garde un dossier déjà présent ou le crée, après le RealPath de son parent ; un dossier créé par
// ailleurs entre-temps est accepté s'il est un vrai dossier.
func ensureDirectory(client *sftp.Client, parent, directory string) error {
	err := checkPlainDirectory(client, directory)
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := checkResolvesTo(client, parent); err != nil {
		return err
	}
	mkdirErr := client.Mkdir(directory)
	if err := checkPlainDirectory(client, directory); err != nil {
		return errors.Join(mkdirErr, err)
	}
	return nil
}

// Symlink pose relative -> target, dans l'ordre d'arguments d'OpenSSH que suit pkg/sftp. target est envoyé tel quel
// et le serveur le lit depuis le dossier du lien : une cible relative est calculée par l'appelant depuis
// l'emplacement du lien, jamais réécrite ici. Après coup, Readlink doit rendre target au chemin demandé : un serveur
// qui lit les arguments dans l'ordre RFC (lien posé ailleurs) est refusé (fsys.ErrUnconfined).
func (root *writeRoot) Symlink(target, relative string) error {
	if target == "" || len(target) > maxNameBytes || strings.ContainsRune(target, 0) {
		return fmt.Errorf("sftpfs: symlink %q: %w: target empty, too long or containing NUL", relative, fs.ErrInvalid)
	}
	destination, err := root.target(relative)
	if err != nil {
		return err
	}
	return root.run(background, "symlink", relative, func(client *sftp.Client) error {
		if err := root.checkParents(client, destination); err != nil {
			return err
		}
		if err := client.Symlink(target, destination.fullPath); err != nil {
			return classifyLinkError(client, destination.fullPath, err)
		}
		if err := checkAfterWrite(client, destination); err != nil {
			return err
		}
		return checkSymlinkApplied(client, destination.fullPath, target)
	})
}

func checkSymlinkApplied(client *sftp.Client, linkPath, target string) error {
	applied, err := client.ReadLink(linkPath)
	if err != nil || applied != target {
		return unconfined(fmt.Sprintf("the server did not create the symlink %q as asked (reversed arguments?)", linkPath))
	}
	return nil
}

// Hardlink lie source (chemin absolu de ce volume, lu par le serveur seul) à relative par hardlink@openssh.com ;
// sans l'extension : local.ErrUnsupported, sans requête.
func (root *writeRoot) Hardlink(source fsys.Location, relative string) error {
	if err := root.checkSource(source); err != nil {
		return err
	}
	destination, err := root.target(relative)
	if err != nil {
		return err
	}
	if !root.hardlink {
		return fmt.Errorf("%w: %w: sftpfs: %s not offered by the server", local.ErrUnsupported, errors.ErrUnsupported, extensionHardlink)
	}
	return root.run(background, "hardlink", relative, func(client *sftp.Client) error {
		if err := root.checkParents(client, destination); err != nil {
			return err
		}
		if err := client.Link(source.Path, destination.fullPath); err != nil {
			return classifyLinkError(client, destination.fullPath, err)
		}
		return checkAfterWrite(client, destination)
	})
}

// classifyLinkError range un refus de lien comme le volume local : destination présente (le serveur répond
// SSH_FX_FAILURE, un Lstat tranche), absence de la source telle quelle, tout autre refus local.ErrLinkRefused.
func classifyLinkError(client *sftp.Client, destination string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if _, lstatErr := client.Lstat(destination); lstatErr == nil {
		return fmt.Errorf("%w: %w: %w", local.ErrDestinationExists, fs.ErrExist, err)
	}
	return fmt.Errorf("%w: %w", local.ErrLinkRefused, err)
}

// CreateExclusive crée relative en SSH_FXF_WRITE|CREAT|EXCL ; une entrée existante (lien pendant compris) rend
// fs.ErrExist. Les droits perm & 0o777 sont posés par le handle (fsetstat), jamais par chemin. Le fichier rendu
// n'expose que Write et Close.
func (root *writeRoot) CreateExclusive(relative string, perm fs.FileMode) (io.WriteCloser, error) {
	destination, err := root.target(relative)
	if err != nil {
		return nil, err
	}
	var created exclusiveWriter
	err = root.run(background, "create", relative, func(client *sftp.Client) error {
		if err := root.checkParents(client, destination); err != nil {
			return err
		}
		file, err := client.OpenFile(destination.fullPath, exclusiveCreateFlags)
		if err != nil {
			return existingOr(client, destination.fullPath, err)
		}
		if err := checkAfterWrite(client, destination); err != nil {
			return errors.Join(err, file.Close())
		}
		if err := file.Chmod(perm & fs.ModePerm); err != nil {
			return errors.Join(err, file.Close())
		}
		created = exclusiveWriter{root: root, file: file, name: relative}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// existingOr : le serveur répond SSH_FX_FAILURE à une collision O_EXCL ; un Lstat qui trouve l'entrée en fait
// fs.ErrExist.
func existingOr(client *sftp.Client, name string, err error) error {
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
		return err
	}
	if _, lstatErr := client.Lstat(name); lstatErr == nil {
		return fmt.Errorf("%w: %w", fs.ErrExist, err)
	}
	return err
}

// CopyFrom rend toujours fsys.ErrServerCopyUnavailable : pkg/sftp n'a pas copy-data, et une copie par flux ferait
// transiter tout le média par le réseau. Source et destination sont vérifiées d'abord (fsys.ErrUnconfined).
func (root *writeRoot) CopyFrom(ctx context.Context, source fsys.Location, relative string) (int64, error) {
	if err := root.checkSource(source); err != nil {
		return 0, err
	}
	destination, err := root.target(relative)
	if err != nil {
		return 0, err
	}
	return 0, root.run(ctx, "copy", relative, func(client *sftp.Client) error {
		if err := root.checkParents(client, destination); err != nil {
			return err
		}
		return fmt.Errorf("%w: sftpfs: copy-data is not available", fsys.ErrServerCopyUnavailable)
	})
}

// Replace renomme oldRelative sur newRelative par posix-rename@openssh.com seulement : sans l'extension,
// fsys.ErrNoAtomicReplace et aucune requête, jamais un renommage simple ni une suppression puis création.
func (root *writeRoot) Replace(oldRelative, newRelative string) error {
	oldTarget, err := root.target(oldRelative)
	if err != nil {
		return err
	}
	newTarget, err := root.target(newRelative)
	if err != nil {
		return err
	}
	if !root.atomicReplace {
		return fmt.Errorf("%w: sftpfs: %s not offered by the server", fsys.ErrNoAtomicReplace, extensionPosixRename)
	}
	return root.run(background, "replace", oldRelative, func(client *sftp.Client) error {
		for _, target := range []confinedTarget{oldTarget, newTarget} {
			if err := root.checkParents(client, target); err != nil {
				return err
			}
		}
		if err := client.PosixRename(oldTarget.fullPath, newTarget.fullPath); err != nil {
			return err
		}
		// Un remplacement n'est pas annulable : un parent devenu lien est signalé, rien n'est retiré.
		for _, target := range []confinedTarget{oldTarget, newTarget} {
			if err := checkResolvesTo(client, target.parentPath); err != nil {
				return err
			}
		}
		return nil
	})
}

// Remove supprime un fichier, un lien (jamais sa cible) ou un dossier vide ; jamais récursif.
func (root *writeRoot) Remove(relative string) error {
	target, err := root.target(relative)
	if err != nil {
		return err
	}
	return root.run(background, "remove", relative, func(client *sftp.Client) error {
		if err := root.checkParents(client, target); err != nil {
			return err
		}
		return client.Remove(target.fullPath)
	})
}

// Lstat décrit relative sans suivre un lien final.
func (root *writeRoot) Lstat(relative string) (fs.FileInfo, error) {
	target, err := root.target(relative)
	if err != nil {
		return nil, err
	}
	var info fs.FileInfo
	err = root.run(background, "lstat", relative, func(client *sftp.Client) error {
		if err := root.checkParents(client, target); err != nil {
			return err
		}
		var lstatErr error
		info, lstatErr = client.Lstat(target.fullPath)
		return lstatErr
	})
	if err != nil {
		return nil, err
	}
	return checkInfo("lstat", relative, info)
}

// exclusiveWriter cache *sftp.File : seules Write et Close restent atteignables (ni ReadFrom, ni Truncate, ni
// Chmod, ni Stat), toujours sur la session de la racine.
type exclusiveWriter struct {
	root *writeRoot
	file *sftp.File
	name string
}

// Write écrit à la position courante.
func (writer exclusiveWriter) Write(buffer []byte) (int, error) {
	written := 0
	err := writer.root.volume.callOn(background, writer.root.current, "write", func(*session) error {
		var writeErr error
		written, writeErr = writer.file.Write(buffer)
		return writeErr
	})
	return written, mapError("write", writer.name, err)
}

// Close libère le handle distant.
func (writer exclusiveWriter) Close() error {
	err := writer.root.volume.callOn(background, writer.root.current, "close", func(*session) error {
		return writer.file.Close()
	})
	return mapError("close", writer.name, err)
}
