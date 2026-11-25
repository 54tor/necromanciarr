package sftpfs

import (
	"context"
	"crypto/md5"  // #nosec G501 -- empreinte attendue de md5sum sur l'octet de sonde, jamais une protection
	"crypto/sha1" // #nosec G505 -- empreinte attendue de sha1sum sur l'octet de sonde, jamais une protection
	"encoding/hex"
	"errors"
	"fmt"
	"slices"

	"necromanciarr/internal/config"
	"necromanciarr/internal/fsys"
)

// ExecReasonInconsistent : l'exécution distante répond, mais stat ou une empreinte contredit SFTP (taille, date,
// inode nul, lien dur vu comme deux fichiers, empreinte fausse), ou aucun fichier de sonde ne permet de le vérifier :
// identité et empreintes distantes restent coupées.
const ExecReasonInconsistent = "exec_inconsistent"

// maxModTimeSkewSeconds : stat rend des secondes, SFTP aussi ; un arrondi d'une seconde est toléré.
const maxModTimeSkewSeconds = 1

// errExecInconsistent : un contrôle croisé entre stat distant et SFTP a échoué.
var errExecInconsistent = errors.New("sftpfs: remote stat inconsistent with sftp")

// expectedProbeHashes : empreintes de probeContent, que sha1sum et md5sum doivent rendre sur le fichier de sonde.
var expectedProbeHashes = map[CommandID]string{
	CommandSHA1: probeSHA1(),
	CommandMD5:  probeMD5(),
}

func probeSHA1() string {
	sum := sha1.Sum([]byte(probeContent)) // #nosec G401 -- voir l'import
	return hex.EncodeToString(sum[:])
}

func probeMD5() string {
	sum := md5.Sum([]byte(probeContent)) // #nosec G401 -- voir l'import
	return hex.EncodeToString(sum[:])
}

// remoteExec sonde l'exécution distante (ProbeExec) puis la vérifie contre SFTP sur les fichiers que la sonde vient
// de poser dans la racine (contrôle croisé de stat, empreintes de probeContent). Seul un succès complet donne
// RemoteStat et RemoteHash ; sinon tout reste faux, Reasons["remoteExec"] dit pourquoi et, en auto, un seul
// avertissement remote_exec_unavailable est posé pour la session. exec required : ErrExecRequiredUnavailable.
func (probe *capabilityProbe) remoteExec() error {
	volume := probe.root.volume
	status, err := volume.ProbeExec(probe.ctx)
	if errors.Is(err, ErrExecRequiredUnavailable) || probe.isConclusive(err) {
		return err
	}
	reason := status.Reason
	if err == nil && status.Enabled {
		reason, err = probe.verifyRemoteExec(status)
		if probe.isConclusive(err) {
			return err
		}
	}
	if reason == "" {
		probe.capabilities.RemoteStat, probe.capabilities.RemoteHash = true, slices.Clone(status.Hashes)
		delete(probe.capabilities.Reasons, reasonKeyFileIdentity)
		return nil
	}
	return probe.refuseRemoteExec(reason)
}

// refuseRemoteExec garde la raison ; exec off est un choix de l'opérateur (aucun avertissement), required un refus.
func (probe *capabilityProbe) refuseRemoteExec(reason string) error {
	volume := probe.root.volume
	probe.capabilities.Reasons[reasonKeyRemoteExec] = reason
	switch volume.provider.Exec {
	case config.SFTPExecOff:
		return nil
	case config.SFTPExecRequired:
		return volume.disabledError(reason)
	}
	volume.warnOnce(CodeRemoteExecUnavailable, volume.provider.Name)
	return nil
}

// verifyRemoteExec rend une raison vide quand stat et chaque empreinte annoncée concordent avec SFTP.
func (probe *capabilityProbe) verifyRemoteExec(status ExecStatus) (string, error) {
	if probe.contentFile == "" {
		return ExecReasonInconsistent, nil
	}
	if err := probe.verifyRemoteStat(); err != nil {
		return ExecReasonInconsistent, err
	}
	for _, command := range []CommandID{CommandSHA1, CommandMD5} {
		if !slices.Contains(status.Hashes, hashAlgorithms[command].name) {
			continue
		}
		if err := probe.verifyRemoteHash(command); err != nil {
			return ExecReasonInconsistent, err
		}
	}
	return "", nil
}

// verifyRemoteStat : le fichier de sonde est lu deux fois (identité stable), avec son lien dur s'il existe (même
// identité, au moins deux liens) et la racine (autre inode) ; inodes non nuls, un seul périphérique, tailles et dates
// égales à SFTP.
func (probe *capabilityProbe) verifyRemoteStat() error {
	paths := []string{probe.contentFile, probe.contentFile}
	if probe.linkedPartner != "" {
		paths = append(paths, probe.linkedPartner)
	}
	paths = append(paths, probe.root.dir)
	results, err := probe.root.volume.Exec(probe.ctx, CommandStat, paths)
	if err != nil {
		return err
	}
	if err := probe.checkAgainstSFTP(results); err != nil {
		return err
	}
	return checkIdentities(results, probe.linkedPartner != "")
}

// checkAgainstSFTP exige pour chaque chemin un résultat sans erreur (taille déjà comparée par Exec) et une date à une
// seconde près de celle de SFTP.
func (probe *capabilityProbe) checkAgainstSFTP(results []ExecResult) error {
	for _, result := range results {
		if result.Err != nil {
			return fmt.Errorf("%w: %w", errExecInconsistent, result.Err)
		}
		info, err := probe.root.volume.Lstat(result.Path)
		if err != nil {
			return err
		}
		skew := info.ModTime().Unix() - result.ModTime.Unix()
		if skew > maxModTimeSkewSeconds || skew < -maxModTimeSkewSeconds {
			return fmt.Errorf("%w: modification time", errExecInconsistent)
		}
	}
	return nil
}

// checkIdentities : results tient le fichier, le même fichier, son lien dur éventuel, puis la racine.
func checkIdentities(results []ExecResult, withPartner bool) error {
	file, again, root := results[0], results[1], results[len(results)-1]
	isPlausible := file.Inode != 0 && root.Inode != 0 && file.Device == root.Device && root.Inode != file.Inode
	isStable := sameIdentity(file, again) && file.Links == again.Links
	if !isPlausible || !isStable {
		return fmt.Errorf("%w: unusable inodes", errExecInconsistent)
	}
	if !withPartner {
		return nil
	}
	partner := results[2]
	if !sameIdentity(file, partner) || file.Links < 2 || partner.Links < 2 {
		return fmt.Errorf("%w: hard link not seen as one file", errExecInconsistent)
	}
	return nil
}

func sameIdentity(first, second ExecResult) bool {
	return first.Device == second.Device && first.Inode == second.Inode
}

// verifyRemoteHash exige l'empreinte de probeContent sur le fichier de sonde.
func (probe *capabilityProbe) verifyRemoteHash(command CommandID) error {
	results, err := probe.root.volume.Exec(probe.ctx, command, []string{probe.contentFile})
	if err != nil {
		return err
	}
	if results[0].Err != nil || results[0].Hash != expectedProbeHashes[command] {
		return fmt.Errorf("%w: %s", errExecInconsistent, hashAlgorithms[command].name)
	}
	return nil
}

// warnOnce pose l'avertissement (code, path) s'il ne l'est pas déjà sur cette connexion.
func (volume *Volume) warnOnce(code, path string) {
	volume.mutex.Lock()
	defer volume.mutex.Unlock()
	for _, known := range volume.warnings {
		if known.Code == code && known.Path == path {
			return
		}
	}
	volume.warnings = append(volume.warnings, Warning{Code: code, Path: path, Count: 1})
}

// isExecItemFailure : un échec de commande après une sonde réussie (lot refusé, délai, sortie non conforme, fichier
// disparu), à compter remote_exec_failed par élément ; ni une panne du volume, ni une exécution coupée, ni un chemin
// hors racines (jamais envoyé).
func isExecItemFailure(err error) bool {
	if err == nil {
		return false
	}
	isExpected := errors.Is(err, fsys.ErrRemoteUnavailable) || errors.Is(err, ErrExecDisabled) ||
		errors.Is(err, ErrExecPathOutsideRoots) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
	return !isExpected
}

// NoteExecFailures compte remote_exec_failed pour count éléments du volume : la preuve de ces éléments repasse par
// une lecture SFTP. Les appelants hors du paquet (hashsource) le font après un échec d'Exec sur un élément.
func (volume *Volume) NoteExecFailures(count int) {
	for range count {
		volume.countWarning(CodeRemoteExecFailed, volume.provider.Name)
	}
}
