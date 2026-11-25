package hashsource

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/sftpfs"
)

// TypeSFTPExec nomme la source qui fait calculer l'empreinte par l'hôte distant d'un volume sftp (ssh exec).
const TypeSFTPExec = "sftp-exec"

// ErrRemoteExecFailed : la commande d'empreinte a échoué après une sonde réussie (lot refusé, délai, sortie non
// conforme, fichier disparu) ; code remote_exec_failed. L'appelant prouve alors cet élément par une lecture SFTP.
var ErrRemoteExecFailed = errors.New("hashsource: remote hash command failed")

// execCommands : algorithme de la liste fermée -> commande distante et nom annoncé par Capabilities.RemoteHash.
var execCommands = map[Algorithm]struct {
	command  sftpfs.CommandID
	announce string
}{
	SHA1: {command: sftpfs.CommandSHA1, announce: "sha1"},
	MD5:  {command: sftpfs.CommandMD5, announce: "md5"},
}

// SFTPExec est la source sftp-exec : un chemin sftp://<volume>/… est haché par sha1sum ou md5sum sur l'hôte, une
// commande par fichier, seulement si la sonde de ce volume annonce l'algorithme (Capabilities.RemoteHash). Les
// octets restent sur l'hôte (ni readBytesPerSecond, ni lecture locale) : le coût est annoncé par remote_hash_bytes.
type SFTPExec struct {
	volumes map[string]*sftpfs.Volume
}

// NewSFTPExec rend la source des volumes donnés, par nom de fournisseur.
func NewSFTPExec(volumes map[string]*sftpfs.Volume) *SFTPExec {
	return &SFTPExec{volumes: volumes}
}

// Hash rend Available faux, sans erreur, pour un chemin local, un volume inconnu ou sans l'algorithme ; une panne
// du volume reste fsys.ErrRemoteUnavailable ; tout autre échec est ErrRemoteExecFailed (compté remote_exec_failed).
func (source *SFTPExec) Hash(ctx context.Context, stored string, algorithm Algorithm) (Result, error) {
	volume, location, offered := source.volumeFor(ctx, stored, algorithm)
	if !offered {
		return Result{}, nil
	}
	value, err := source.remoteHash(ctx, volume, location.Path, algorithm)
	if err != nil {
		if errors.Is(err, fsys.ErrRemoteUnavailable) || ctx.Err() != nil {
			return Result{}, err
		}
		volume.NoteExecFailures(1)
		return Result{}, fmt.Errorf("%w: %w", ErrRemoteExecFailed, err)
	}
	return Result{Value: value, Origin: Backend, Available: true}, nil
}

// volumeFor rend le volume de stored quand sa sonde annonce l'algorithme.
func (source *SFTPExec) volumeFor(ctx context.Context, stored string, algorithm Algorithm) (*sftpfs.Volume, fsys.Location, bool) {
	execCommand, known := execCommands[algorithm]
	if !known || !strings.HasPrefix(stored, "sftp://") {
		return nil, fsys.Location{}, false
	}
	location, err := fsys.ParseLocation(stored)
	if err != nil {
		return nil, fsys.Location{}, false
	}
	volume, registered := source.volumes[location.Volume]
	if !registered {
		return nil, fsys.Location{}, false
	}
	capabilities, err := volume.Capabilities(ctx)
	if err != nil {
		return nil, fsys.Location{}, false
	}
	for _, announced := range capabilities.RemoteHash {
		if announced == execCommand.announce {
			return volume, location, true
		}
	}
	return nil, fsys.Location{}, false
}

// remoteHash canonicalise le chemin (Exec n'admet que des chemins canoniques) et contrôle la réponse.
func (source *SFTPExec) remoteHash(ctx context.Context, volume *sftpfs.Volume, name string, algorithm Algorithm) (string, error) {
	canonical, err := volume.Canonical(name)
	if err != nil {
		return "", err
	}
	results, err := volume.Exec(ctx, execCommands[algorithm].command, []string{canonical})
	if err != nil {
		return "", err
	}
	if results[0].Err != nil {
		return "", results[0].Err
	}
	value := results[0].Hash
	if !isLowerHex(value, algorithm.hexLength()) {
		return "", fmt.Errorf("hashsource: %s answer is not a %s hash", TypeSFTPExec, algorithm)
	}
	return value, nil
}

func isLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	return strings.Trim(value, "0123456789abcdef") == ""
}
