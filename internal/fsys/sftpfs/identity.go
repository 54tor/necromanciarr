package sftpfs

import (
	"context"
	"io/fs"
	"sync"
	"time"

	"necromanciarr/internal/fsys"
)

// maxIdentityCacheEntries borne le cache des identités distantes ; plein, il repart vide.
const maxIdentityCacheEntries = 4096

// identityWait borne FileID, attente du droit d'exécuter comprise (un long lot d'empreintes peut le tenir) ; au-delà,
// l'identité reste inconnue.
func identityWait() time.Duration { return 2 * statExecTimeout }

// FileID rend (périphérique, inode) lus par stat distant quand la sonde de la session a prouvé RemoteStat (voir
// FileIDs) ; known=false sinon. Une identité distante ne se compare qu'à une autre du même volume : elle ne prouve
// jamais un lien (I4) ni une décision destructive.
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

// identityKey : une identité n'est réutilisée que pour le même chemin, à taille et date inchangées.
type identityKey struct {
	name    string
	size    int64
	modTime int64
}

// identityCache garde, pour une étape, les identités distantes déjà lues.
type identityCache struct {
	mutex   sync.Mutex
	entries map[identityKey]fsys.FileID
}

func (cache *identityCache) lookup(key identityKey) (fsys.FileID, bool) {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	id, found := cache.entries[key]
	return id, found
}

func (cache *identityCache) store(key identityKey, id fsys.FileID) {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	if cache.entries == nil || len(cache.entries) >= maxIdentityCacheEntries {
		cache.entries = map[identityKey]fsys.FileID{}
	}
	cache.entries[key] = id
}

func (cache *identityCache) reset() {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	cache.entries = nil
}

// FileIdentity est l'identité distante d'un chemin rendue par FileIDs ; Known est faux quand elle n'a pas pu être lue.
type FileIdentity struct {
	ID    fsys.FileID
	Known bool
}

// FileIDs lit d'un coup (lots de 64) l'identité distante de chaque information rendue par Stat ou Lstat de ce volume,
// et la garde pour l'étape : FileID la rend ensuite sans commande. Rien n'est connu sans RemoteStat prouvé sur la
// session, pour un lien symbolique lu sans le suivre, hors des racines admises, ou si stat distant ne rend pas la
// taille et la date que SFTP a vues ; un échec de commande compte remote_exec_failed par élément.
func (volume *Volume) FileIDs(ctx context.Context, infos []fs.FileInfo) []FileIdentity {
	identities := make([]FileIdentity, len(infos))
	if !volume.remoteStatProven(ctx) {
		return identities
	}
	pending := map[string][]int{}
	var canonicalNames []string
	for infoIndex, info := range infos {
		key, canonical, ready := volume.identityRequest(info)
		if !ready {
			continue
		}
		if id, found := volume.identities.lookup(key); found {
			identities[infoIndex] = FileIdentity{ID: id, Known: true}
			continue
		}
		if _, queued := pending[canonical]; !queued {
			canonicalNames = append(canonicalNames, canonical)
		}
		pending[canonical] = append(pending[canonical], infoIndex)
	}
	volume.readIdentities(ctx, infos, canonicalNames, pending, identities)
	return identities
}

// identityRequest rend la clé de cache et le nom canonique à interroger pour info, ou ready faux.
func (volume *Volume) identityRequest(info fs.FileInfo) (identityKey, string, bool) {
	named, isNamed := info.(namedInfo)
	isUnfollowedLink := isNamed && !named.followed && named.Mode()&fs.ModeSymlink != 0
	if !isNamed || isUnfollowedLink {
		return identityKey{}, "", false
	}
	key := identityKey{name: named.name, size: named.Size(), modTime: named.ModTime().Unix()}
	canonical, err := volume.Canonical(named.name)
	if err != nil || volume.admitPaths([]string{canonical}) != nil {
		return identityKey{}, "", false
	}
	return key, canonical, true
}

// readIdentities envoie stat sur canonicalNames ; un lot en échec est retenté fichier par fichier, pour qu'un fichier
// disparu ne coupe que lui.
func (volume *Volume) readIdentities(ctx context.Context, infos []fs.FileInfo, canonicalNames []string, pending map[string][]int, identities []FileIdentity) {
	if len(canonicalNames) == 0 {
		return
	}
	results, err := volume.Exec(ctx, CommandStat, canonicalNames)
	if err != nil {
		volume.readIdentitiesOneByOne(ctx, infos, canonicalNames, pending, identities, err)
		return
	}
	for _, result := range results {
		volume.keepIdentity(infos, result, pending[result.Path], identities)
	}
}

func (volume *Volume) readIdentitiesOneByOne(ctx context.Context, infos []fs.FileInfo, canonicalNames []string, pending map[string][]int, identities []FileIdentity, batchErr error) {
	if !isExecItemFailure(batchErr) || len(canonicalNames) == 1 {
		if isExecItemFailure(batchErr) {
			volume.NoteExecFailures(len(pending[canonicalNames[0]]))
		}
		return
	}
	for _, canonical := range canonicalNames {
		results, err := volume.Exec(ctx, CommandStat, []string{canonical})
		if err != nil {
			if isExecItemFailure(err) {
				volume.NoteExecFailures(len(pending[canonical]))
			}
			continue
		}
		volume.keepIdentity(infos, results[0], pending[canonical], identities)
	}
}

// keepIdentity n'accepte qu'un résultat sans erreur, d'inode non nul, de taille et de date égales à celles de SFTP.
func (volume *Volume) keepIdentity(infos []fs.FileInfo, result ExecResult, infoIndexes []int, identities []FileIdentity) {
	if result.Err != nil {
		volume.NoteExecFailures(len(infoIndexes))
		return
	}
	for _, infoIndex := range infoIndexes {
		info := infos[infoIndex].(namedInfo) //nolint:forcetypeassert // filtré par identityRequest
		matchesSFTP := result.Inode != 0 && result.Size == info.Size() && result.ModTime.Unix() == info.ModTime().Unix()
		if !matchesSFTP {
			continue
		}
		id := fsys.FileID{Device: result.Device, Inode: result.Inode}
		volume.identities.store(identityKey{name: info.name, size: info.Size(), modTime: info.ModTime().Unix()}, id)
		identities[infoIndex] = FileIdentity{ID: id, Known: true}
	}
}

// remoteStatProven : la sonde de la session courante a prouvé stat distant.
func (volume *Volume) remoteStatProven(ctx context.Context) bool {
	capabilities, err := volume.Capabilities(ctx)
	return err == nil && capabilities.RemoteStat
}
