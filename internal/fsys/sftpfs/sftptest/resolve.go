package sftptest

import (
	"errors"
	"os"
	"path"
	"strings"
)

// maxSymlinkDepth borne les liens suivis pour un chemin, comme SYMLOOP_MAX sous Linux.
const maxSymlinkDepth = 40

var errSymlinkLoop = errors.New("sftptest: too many levels of symbolic links")

// resolve traduit un chemin du client en chemin relatif au dossier servi, sans aucun lien symbolique : chaque lien
// rencontré est suivi (le dernier segment seulement si followLast), une cible qui commence par « / » part de la racine virtuelle
// (le dossier servi), une cible relative du dossier du lien, et « .. » s'arrête à la racine virtuelle comme dans un
// chroot. os.Root reste la seconde barrière.
func (handler *fileHandler) resolve(clientPath string, followLast bool) (string, error) {
	pending := handler.segments(clientPath)
	var resolved []string
	followed := 0
	for len(pending) > 0 {
		segment := pending[0]
		pending = pending[1:]
		switch segment {
		case "", ".":
			continue
		case "..":
			if len(resolved) > 0 {
				resolved = resolved[:len(resolved)-1]
			}
			continue
		}
		candidate := append(append([]string(nil), resolved...), segment)
		isLast := len(pending) == 0
		target, isLink := handler.linkTarget(candidate)
		if !isLink || (isLast && !followLast) {
			resolved = candidate
			continue
		}
		if followed++; followed > maxSymlinkDepth {
			return "", errSymlinkLoop
		}
		if strings.HasPrefix(target, "/") {
			resolved = nil
		}
		pending = append(handler.segments(target), pending...)
	}
	return joinRelative(resolved), nil
}

// segments découpe un chemin du client ou une cible de lien ; un préfixe « /C: » d'un serveur Windows simulé tombe.
func (handler *fileHandler) segments(clientPath string) []string {
	if handler.server.settings.windowsRoot {
		clientPath = strings.TrimPrefix(clientPath, windowsDrivePrefix)
	}
	return strings.Split(clientPath, "/")
}

// linkTarget rend la cible de candidate quand c'est un lien symbolique ; tout autre cas (absent, fichier) dit non.
func (handler *fileHandler) linkTarget(candidate []string) (string, bool) {
	relativePath := joinRelative(candidate)
	info, err := handler.server.root.Lstat(relativePath)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return "", false
	}
	target, err := handler.server.root.Readlink(relativePath)
	if err != nil {
		return "", false
	}
	return target, true
}

func joinRelative(segments []string) string {
	if len(segments) == 0 {
		return "."
	}
	return path.Join(segments...)
}
