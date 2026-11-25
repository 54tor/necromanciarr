package sftpfs

import (
	"context"
	"io/fs"
	"time"

	"necromanciarr/internal/fsys"
)

// identityWait borne FileID, attente du droit d'exécuter comprise (un long lot d'empreintes peut le tenir) ; au-delà,
// l'identité reste inconnue.
func identityWait() time.Duration { return 2 * statExecTimeout }

// FileID rend (périphérique, inode) lus par un stat distant frais, à chaque appel (jamais de cache : une identité
// périmée ferait passer un lien dur pour une copie, Force et l'adoption s'y fient), quand la sonde de la session a
// prouvé RemoteStat sur ce périphérique ; known=false sinon. Une identité distante ne se compare qu'à une autre du même
// volume : elle ne prouve jamais un lien (I4) ni une décision destructive. Coût : une session exec par appel.
func (volume *Volume) FileID(info fs.FileInfo) (fsys.FileID, bool) {
	ctx, cancel := context.WithTimeout(background, identityWait())
	defer cancel()
	identity := volume.FileIDs(ctx, []fs.FileInfo{info})[0]
	return identity.ID, identity.Known
}

// namedInfo est une information rendue par Stat ou Lstat qui garde le chemin demandé : FileID en a besoin pour
// interroger stat distant. followed dit qu'elle vient de Stat (lien final suivi).
type namedInfo struct {
	fs.FileInfo
	name     string
	followed bool
}

// FileIdentity est l'identité distante d'un chemin rendue par FileIDs ; Known est faux quand elle n'a pas pu être lue.
type FileIdentity struct {
	ID    fsys.FileID
	Known bool
}

// FileIDs lit, par lots de 64 et toujours à neuf, l'identité distante de chaque information rendue par Stat ou Lstat
// de ce volume. Rien n'est connu sans RemoteStat prouvé sur la session, pour un lien symbolique lu sans le suivre,
// hors des racines admises, sur un autre périphérique que celui validé par la sonde, ou si stat distant ne rend pas
// la taille et la date que SFTP a vues ; un échec de commande compte remote_exec_failed par élément.
func (volume *Volume) FileIDs(ctx context.Context, infos []fs.FileInfo) []FileIdentity {
	identities := make([]FileIdentity, len(infos))
	probedDevice, proven := volume.remoteStatDevice(ctx)
	if !proven {
		return identities
	}
	pending := map[string][]int{}
	var canonicalNames []string
	for infoIndex, info := range infos {
		canonical, ready := volume.identityRequest(info)
		if !ready {
			continue
		}
		if _, queued := pending[canonical]; !queued {
			canonicalNames = append(canonicalNames, canonical)
		}
		pending[canonical] = append(pending[canonical], infoIndex)
	}
	reading := identityRead{volume: volume, infos: infos, pending: pending, identities: identities, device: probedDevice}
	reading.readAll(ctx, canonicalNames)
	return identities
}

// identityRequest rend le nom canonique (résolu par le serveur) à interroger pour info, ou ready faux.
func (volume *Volume) identityRequest(info fs.FileInfo) (string, bool) {
	named, isNamed := info.(namedInfo)
	isUnfollowedLink := isNamed && !named.followed && named.Mode()&fs.ModeSymlink != 0
	if !isNamed || isUnfollowedLink {
		return "", false
	}
	canonical, err := volume.Canonical(named.name)
	if err != nil || volume.admitPaths([]string{canonical}) != nil {
		return "", false
	}
	return canonical, true
}

// identityRead est une lecture d'identités en cours : pending relie chaque nom canonique aux informations demandées.
type identityRead struct {
	volume     *Volume
	infos      []fs.FileInfo
	pending    map[string][]int
	identities []FileIdentity
	device     uint64
}

// readAll envoie stat sur canonicalNames ; un lot en échec est retenté fichier par fichier, pour qu'un fichier
// disparu ne coupe que lui.
func (reading identityRead) readAll(ctx context.Context, canonicalNames []string) {
	if len(canonicalNames) == 0 {
		return
	}
	results, err := reading.volume.Exec(ctx, CommandStat, canonicalNames)
	if err == nil {
		for _, result := range results {
			reading.keep(result, reading.pending[result.Path])
		}
		return
	}
	if !isExecItemFailure(err) {
		return
	}
	if len(canonicalNames) == 1 {
		reading.volume.NoteExecFailures(len(reading.pending[canonicalNames[0]]))
		return
	}
	for _, canonical := range canonicalNames {
		reading.readAll(ctx, []string{canonical})
	}
}

// keep n'accepte qu'un résultat sans erreur, d'inode non nul, sur le périphérique validé par la sonde, de taille et
// de date égales à celles de SFTP.
func (reading identityRead) keep(result ExecResult, infoIndexes []int) {
	if result.Err != nil {
		reading.volume.NoteExecFailures(len(infoIndexes))
		return
	}
	for _, infoIndex := range infoIndexes {
		info := reading.infos[infoIndex]
		matchesSFTP := result.Inode != 0 && result.Device == reading.device && result.Size == info.Size() &&
			result.ModTime.Unix() == info.ModTime().Unix()
		if matchesSFTP {
			reading.identities[infoIndex] = FileIdentity{ID: fsys.FileID{Device: result.Device, Inode: result.Inode}, Known: true}
		}
	}
}

// remoteStatDevice rend le périphérique que la sonde de la session courante a validé pour stat distant ; proven est
// faux sans RemoteStat prouvé.
func (volume *Volume) remoteStatDevice(ctx context.Context) (uint64, bool) {
	capabilities, err := volume.Capabilities(ctx)
	current := volume.live()
	if err != nil || !capabilities.RemoteStat || current == nil {
		return 0, false
	}
	device := current.remoteStatDevice.Load()
	if device == nil {
		return 0, false
	}
	return *device, true
}
