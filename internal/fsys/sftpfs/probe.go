package sftpfs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/pkg/sftp"

	"necromanciarr/internal/fsys"
)

// Verdicts de la sonde d'ordre des arguments de SSH_FXP_SYMLINK, gardés par session.
const (
	symlinkOrderUnknown int32 = iota
	symlinkOrderVerified
	symlinkOrderRefused
)

const (
	probeNamePrefix = ".necromanciarr-probe-"
	probeTargetTail = "-t"
	probeRandomSize = 16
)

// probeSymlinkOrder sonde, jusqu'à un verdict gardé par la session, dans dir (racine dont le RealPath vient d'être vérifié), l'ordre
// des arguments : lien et cible sont deux noms absolus imprévisibles de dir, si bien que les deux ordres restent
// dans la racine. Le lien doit se lire au nom demandé avec la cible demandée. Les deux noms sont retirés ensuite :
// ce sont des entrées que cette sonde vient de créer dans la racine.
func probeSymlinkOrder(current *session, dir string) (bool, error) {
	if verdict := current.symlinkOrder.Load(); verdict != symlinkOrderUnknown {
		return verdict == symlinkOrderVerified, nil
	}
	random := make([]byte, probeRandomSize)
	if _, err := rand.Read(random); err != nil {
		return false, err
	}
	linkPath := path.Join(dir, probeNamePrefix+hex.EncodeToString(random))
	targetPath := linkPath + probeTargetTail
	verdict := probeSymlink(current.client, linkPath, targetPath)
	if err := removeProbeEntries(current.client, linkPath, targetPath); err != nil {
		return false, err
	}
	if verdict != symlinkOrderUnknown {
		current.symlinkOrder.Store(verdict)
	}
	return verdict == symlinkOrderVerified, nil
}

// probeSymlink rend Verified quand le lien se lit au nom demandé avec la cible demandée, Refused quand il est absent
// ou mène ailleurs (ordre inverse), Unknown sur un échec passager (Symlink refusé, quota, disque plein ; ReadLink en
// échec autre qu'une absence) : la sonde sera refaite à la prochaine ouverture, les liens restant refusés d'ici là.
func probeSymlink(client *sftp.Client, linkPath, targetPath string) int32 {
	if err := client.Symlink(targetPath, linkPath); err != nil {
		return symlinkOrderUnknown
	}
	applied, err := client.ReadLink(linkPath)
	switch {
	case err == nil && applied == targetPath:
		return symlinkOrderVerified
	case err == nil || errors.Is(err, fs.ErrNotExist):
		return symlinkOrderRefused
	default:
		return symlinkOrderUnknown
	}
}

// removeProbeEntries retire celui des deux noms que le serveur a créé ; un nom absent n'est pas une erreur.
func removeProbeEntries(client *sftp.Client, names ...string) error {
	var failures []error
	for _, name := range names {
		if err := client.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// Suffixes des fichiers de la sonde des capacités : a est créé puis lié (h), b est remplacé par a.
const (
	probeSourceTail   = "-a"
	probeLinkTail     = "-h"
	probeReplacedTail = "-b"
	// probeContent : un octet écrit dans a après le lien dur ; h doit le voir (même fichier), b doit l'avoir après
	// le remplacement (b est créé vide).
	probeContent = "x"
)

// probeNamePattern reconnaît exactement les noms que posent les sondes : préfixe, 32 chiffres hexadécimaux
// minuscules, suffixe d'une lettre facultatif. SweepProbes ne retire que ceux-là.
var probeNamePattern = regexp.MustCompile(`^\.necromanciarr-probe-[0-9a-f]{32}(-[a-z])?$`)

// unprobedCapabilities : rien n'est prouvé ; une extension non annoncée est dite manquante, les autres non sondées.
func unprobedCapabilities(client *sftp.Client) fsys.Capabilities {
	reasons := map[string]string{
		reasonKeyFileIdentity: reasonRemoteStatMissing,
		reasonKeyServerCopy:   reasonServerCopyByClient,
		reasonKeySymlink:      reasonNotProbed,
	}
	for key, extension := range probedExtensions {
		reasons[key] = reasonNotProbed
		if _, offered := client.HasExtension(extension); !offered {
			reasons[key] = reasonExtensionMissing
		}
	}
	return fsys.Capabilities{Reasons: reasons}
}

// probedExtensions : clé de Reasons -> extension dont dépend la fonction.
var probedExtensions = map[string]string{
	reasonKeyHardlink:       extensionHardlink,
	reasonKeyAtomicReplace:  extensionPosixRename,
	reasonKeyVolumeIdentity: extensionStatVFS,
}

func cloneCapabilities(capabilities fsys.Capabilities) fsys.Capabilities {
	capabilities.Reasons = maps.Clone(capabilities.Reasons)
	capabilities.RemoteHash = slices.Clone(capabilities.RemoteHash)
	return capabilities
}

// ProbeCapabilities sonde réellement, dans root (un dossier de liens ouvert par Registry.WriteRoot, jamais une
// bibliothèque), chaque fonction dont l'extension est annoncée : lien symbolique (ordre des arguments, sonde de la
// session), lien dur (le lien voit l'octet écrit ensuite dans sa source), posix-rename (b vide remplacé par a),
// statvfs (valeurs plausibles). copy-data n'est jamais disponible (pkg/sftp ne l'envoie pas). Les fichiers de sonde
// portent un nom imprévisible « .necromanciarr-probe-… », sont créés directement dans root sur sa session et retirés
// par elle (succès, échec, annulation de ctx, panique), sauf si root ne se résout plus en lui-même au début ou à la
// fin de la sonde : fsys.ErrUnconfined, rien n'est gardé ni retiré par chemin. Les restes d'une sonde interrompue
// sont d'abord balayés (SweepProbes). Le résultat est gardé pour la session ; Capabilities le rend ensuite. Balayage,
// essais et nettoyage partent sous un contexte détaché, chaque requête bornée par timeout ; ctx n'est lu qu'entre deux
// requêtes : une annulation ne coupe jamais la session et laisse le nettoyage se faire. Erreurs : ErrNoProbeDir (root nil ou d'un autre volume), fsys.ErrWriteInLibrary (root dans
// Deps.LibraryRoots), fs.ErrClosed, fsys.ErrRemoteUnavailable, fsys.ErrUnconfined, l'erreur de ctx.
func (volume *Volume) ProbeCapabilities(ctx context.Context, root fsys.WriteRoot) (fsys.Capabilities, error) {
	confined, err := volume.probeRoot(root)
	if err != nil {
		return fsys.Capabilities{}, err
	}
	volume.probeMutex.Lock()
	defer volume.probeMutex.Unlock()
	if probed := confined.current.capabilities.Load(); probed != nil {
		return cloneCapabilities(*probed), nil
	}
	if _, err := confined.sweepProbes(ctx); err != nil {
		return fsys.Capabilities{}, err
	}
	capabilities, err := confined.probeCapabilities(ctx)
	if err != nil {
		return fsys.Capabilities{}, err
	}
	confined.current.capabilities.Store(&capabilities)
	return cloneCapabilities(capabilities), nil
}

// SweepProbes retire de root, sans descendre dans ses dossiers, les fichiers et liens symboliques dont le nom est
// exactement celui d'une sonde (probeNamePattern), restes d'une sonde interrompue ; rien d'autre n'est touché. Une
// racine qui ne se résout plus en elle-même rend fsys.ErrUnconfined sans rien retirer. Rend le nombre d'entrées
// retirées.
func SweepProbes(root fsys.WriteRoot) (int, error) {
	confined, err := sweepRoot(root)
	if err != nil {
		return 0, err
	}
	confined.volume.probeMutex.Lock()
	defer confined.volume.probeMutex.Unlock()
	return confined.sweepProbes(background)
}

func sweepRoot(root fsys.WriteRoot) (*writeRoot, error) {
	confined, isOurs := root.(*writeRoot)
	if !isOurs || confined == nil {
		return nil, ErrNoProbeDir
	}
	if confined.closed.Load() {
		return nil, fmt.Errorf("sftpfs: probe %q: %w", confined.dir, fs.ErrClosed)
	}
	return confined, nil
}

// probeRoot n'accepte qu'une racine ouverte par ce volume, encore ouverte, hors de toute racine de bibliothèque connue.
func (volume *Volume) probeRoot(root fsys.WriteRoot) (*writeRoot, error) {
	confined, err := sweepRoot(root)
	if err != nil {
		return nil, err
	}
	if confined.volume != volume {
		return nil, ErrNoProbeDir
	}
	for _, libraryRoot := range volume.deps.LibraryRoots {
		if overlaps(confined.dir, path.Clean(libraryRoot)) {
			return nil, fmt.Errorf("%w: sftpfs: probe directory %q", fsys.ErrWriteInLibrary, confined.dir)
		}
	}
	return confined, nil
}

func overlaps(left, right string) bool {
	return isWithin(left, right) || isWithin(right, left)
}

func isWithin(child, parent string) bool {
	return child == parent || parent == "/" || strings.HasPrefix(child, parent+"/")
}

// sweepProbes revérifie d'abord que root.dir se résout en lui-même (un dossier détourné : fsys.ErrUnconfined, rien
// n'est listé ni retiré). Chaque requête part sous un contexte détaché ; ctx n'est lu qu'entre deux requêtes, pour
// qu'une annulation ne coupe jamais la session.
func (root *writeRoot) sweepProbes(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	requests := context.WithoutCancel(ctx)
	var infos []fs.FileInfo
	err := root.volume.callOn(requests, root.current, "sweep", func(current *session) error {
		if err := checkResolvesTo(current.client, root.dir); err != nil {
			return err
		}
		var listErr error
		infos, listErr = current.client.ReadDir(root.dir)
		return listErr
	})
	if err != nil {
		return 0, mapError("sweep", root.dir, err)
	}
	entries, err := listingEntries(root.dir, infos)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, entry := range entries {
		isProbeEntry := probeNamePattern.MatchString(entry.Name()) && (entry.Type().IsRegular() || entry.Type() == fs.ModeSymlink)
		if !isProbeEntry {
			continue
		}
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		if err := root.removeProbeEntry(requests, path.Join(root.dir, entry.Name())); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// removeProbeEntry retire, sur la session de la racine, une entrée de sonde posée directement dans root.dir.
func (root *writeRoot) removeProbeEntry(ctx context.Context, fullPath string) error {
	isProbePath := path.Dir(fullPath) == root.dir && probeNamePattern.MatchString(path.Base(fullPath))
	if !isProbePath {
		return unconfined(fmt.Sprintf("%q is not a probe entry of the root", fullPath))
	}
	err := root.volume.callOn(ctx, root.current, "probe", func(current *session) error {
		return current.client.Remove(fullPath)
	})
	if err != nil && !errors.Is(mapError("probe", fullPath, err), fs.ErrNotExist) {
		return mapError("probe", fullPath, err)
	}
	return nil
}

// capabilityProbe est une sonde en cours : ses requêtes passent par la session de la racine, created liste les
// entrées posées, à retirer quoi qu'il arrive.
type capabilityProbe struct {
	root *writeRoot
	// ctx est lu entre deux requêtes ; requests (sans annulation) porte chaque requête, pour qu'une annulation ne coupe
	// jamais la session au milieu de la sonde ni n'empêche le nettoyage.
	ctx, requests context.Context
	base          string
	created       []string
	handle        *sftp.File
	capabilities  fsys.Capabilities
}

func (root *writeRoot) probeCapabilities(ctx context.Context) (capabilities fsys.Capabilities, err error) {
	random := make([]byte, probeRandomSize)
	if _, err := rand.Read(random); err != nil {
		return fsys.Capabilities{}, err
	}
	probe := &capabilityProbe{
		root: root, ctx: ctx, requests: context.WithoutCancel(ctx),
		base: path.Join(root.dir, probeNamePrefix+hex.EncodeToString(random)),
		capabilities: fsys.Capabilities{Reasons: map[string]string{
			reasonKeyFileIdentity: reasonRemoteStatMissing,
			reasonKeyServerCopy:   reasonServerCopyByClient,
		}},
	}
	defer func() {
		if errors.Is(err, fsys.ErrUnconfined) {
			// Racine détournée : aucun retrait par chemin, qui suivrait ce lien posé ; seul le handle est fermé.
			err = errors.Join(err, probe.closeHandle())
			return
		}
		err = errors.Join(err, probe.cleanup())
	}()
	if err := probe.run(); err != nil {
		return fsys.Capabilities{}, err
	}
	return probe.capabilities, nil
}

// run enchaîne les essais ; seule une erreur qui empêche toute conclusion (session perdue, racine détournée, ctx)
// est rendue, un refus du serveur ne fait que désactiver la fonction essayée. Le balayage qui précède a vérifié que
// root se résout en lui-même ; la dernière requête le revérifie.
func (probe *capabilityProbe) run() error {
	for _, step := range []func() error{probe.symlink, probe.hardlinkAndReplace, probe.statVFS} {
		if err := step(); err != nil {
			return err
		}
	}
	// Une racine détournée pendant la sonde rend ses essais sans valeur : rien n'est gardé ni retiré.
	return probe.do(func(client *sftp.Client) error { return checkResolvesTo(client, probe.root.dir) })
}

// do envoie une requête de la sonde, après avoir relu ctx.
func (probe *capabilityProbe) do(fn func(client *sftp.Client) error) error {
	if err := probe.ctx.Err(); err != nil {
		return err
	}
	return probe.always(fn)
}

func (probe *capabilityProbe) always(fn func(client *sftp.Client) error) error {
	return probe.root.volume.callOn(probe.requests, probe.root.current, "probe", func(current *session) error {
		return fn(current.client)
	})
}

// isConclusive : une erreur qui arrête la sonde (la session, la racine ou ctx) plutôt qu'un refus de la fonction.
func (probe *capabilityProbe) isConclusive(err error) bool {
	return err != nil && (errors.Is(err, fsys.ErrRemoteUnavailable) || errors.Is(err, fsys.ErrUnconfined) || probe.ctx.Err() != nil)
}

// outcome range le résultat d'un essai : nil prouve la fonction, un refus la désactive (probe_failed), une erreur
// concluante est rendue.
func (probe *capabilityProbe) outcome(key string, proven *bool, err error) error {
	if probe.isConclusive(err) {
		return err
	}
	*proven = err == nil
	if err != nil {
		probe.capabilities.Reasons[key] = reasonProbeFailed
	}
	return nil
}

func (probe *capabilityProbe) offered(key string) bool {
	_, isOffered := probe.root.current.client.HasExtension(probedExtensions[key])
	if !isOffered {
		probe.capabilities.Reasons[key] = reasonExtensionMissing
	}
	return isOffered
}

// symlink reprend le verdict de la sonde d'ordre de la session (refaite si elle était restée sans verdict).
func (probe *capabilityProbe) symlink() error {
	var verified bool
	err := probe.do(func(*sftp.Client) error {
		var probeErr error
		verified, probeErr = probeSymlinkOrder(probe.root.current, probe.root.dir)
		return probeErr
	})
	if err := probe.outcome(reasonKeySymlink, &probe.capabilities.Symlink, err); err != nil {
		return err
	}
	if !verified {
		probe.capabilities.Symlink = false
		probe.capabilities.Reasons[reasonKeySymlink] = reasonProbeFailed
	}
	return nil
}

// hardlinkAndReplace crée a (vide), le lie en h, y écrit un octet : h doit le voir. Puis b (vide) est remplacé par a
// et doit avoir son octet. Une fonction non annoncée n'envoie aucune requête.
func (probe *capabilityProbe) hardlinkAndReplace() error {
	hardlinkOffered, replaceOffered := probe.offered(reasonKeyHardlink), probe.offered(reasonKeyAtomicReplace)
	if !hardlinkOffered && !replaceOffered {
		return nil
	}
	source := probe.base + probeSourceTail
	if err := probe.create(source); err != nil {
		return probe.failBoth(hardlinkOffered, replaceOffered, err)
	}
	linked := false
	if hardlinkOffered {
		linkErr := probe.createdBy(probe.base+probeLinkTail, func(client *sftp.Client) error { return client.Link(source, probe.base+probeLinkTail) })
		if err := probe.outcome(reasonKeyHardlink, &linked, linkErr); err != nil {
			return err
		}
	}
	if err := probe.writeAndClose(); err != nil {
		return probe.failBoth(hardlinkOffered && linked, replaceOffered, err)
	}
	if linked {
		if err := probe.outcome(reasonKeyHardlink, &probe.capabilities.Hardlink, probe.expectContent(probe.base+probeLinkTail)); err != nil {
			return err
		}
	}
	if replaceOffered {
		return probe.outcome(reasonKeyAtomicReplace, &probe.capabilities.AtomicReplace, probe.replace(source))
	}
	return nil
}

// failBoth désactive les fonctions en jeu après un essai commun refusé (création, écriture).
func (probe *capabilityProbe) failBoth(hardlinkAtStake, replaceAtStake bool, err error) error {
	var ignored bool
	for _, atStake := range []struct {
		isAtStake bool
		key       string
	}{{hardlinkAtStake, reasonKeyHardlink}, {replaceAtStake, reasonKeyAtomicReplace}} {
		if !atStake.isAtStake {
			continue
		}
		if conclusiveErr := probe.outcome(atStake.key, &ignored, err); conclusiveErr != nil {
			return conclusiveErr
		}
	}
	return nil
}

// createdBy envoie fn, qui crée fullPath ; l'entrée est notée pour le nettoyage dès que la requête a réussi.
func (probe *capabilityProbe) createdBy(fullPath string, fn func(client *sftp.Client) error) error {
	err := probe.do(fn)
	if err == nil {
		probe.created = append(probe.created, fullPath)
	}
	return err
}

// create ouvre fullPath en SSH_FXF_WRITE|CREAT|EXCL et garde le handle.
func (probe *capabilityProbe) create(fullPath string) error {
	return probe.createdBy(fullPath, func(client *sftp.Client) error {
		file, err := client.OpenFile(fullPath, exclusiveCreateFlags)
		probe.handle = file
		return err
	})
}

func (probe *capabilityProbe) writeAndClose() error {
	writeErr := probe.do(func(*sftp.Client) error {
		_, err := probe.handle.Write([]byte(probeContent))
		return err
	})
	return errors.Join(writeErr, probe.closeHandle())
}

func (probe *capabilityProbe) closeHandle() error {
	if probe.handle == nil {
		return nil
	}
	handle := probe.handle
	probe.handle = nil
	return probe.always(func(*sftp.Client) error { return handle.Close() })
}

// expectContent exige un fichier ordinaire qui porte exactement l'octet de la sonde.
func (probe *capabilityProbe) expectContent(fullPath string) error {
	return probe.do(func(client *sftp.Client) error {
		info, err := client.Lstat(fullPath)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() != int64(len(probeContent)) {
			return fmt.Errorf("sftpfs: probe %q: unexpected entry after the try", fullPath)
		}
		return nil
	})
}

// replace crée b vide puis le remplace par source avec posix-rename@openssh.com.
func (probe *capabilityProbe) replace(source string) error {
	replaced := probe.base + probeReplacedTail
	if err := probe.create(replaced); err != nil {
		return err
	}
	if err := probe.closeHandle(); err != nil {
		return err
	}
	if err := probe.do(func(client *sftp.Client) error { return client.PosixRename(source, replaced) }); err != nil {
		return err
	}
	probe.created = slices.DeleteFunc(probe.created, func(createdPath string) bool { return createdPath == source })
	return probe.expectContent(replaced)
}

// statVFS exige une réponse plausible pour root (contrôles de FreeSpace) ; un fsid nul laisse l'identité inconnue.
func (probe *capabilityProbe) statVFS() error {
	if !probe.offered(reasonKeyVolumeIdentity) {
		return nil
	}
	var reply *sftp.StatVFS
	err := probe.do(func(client *sftp.Client) error {
		var statErr error
		reply, statErr = client.StatVFS(probe.root.dir)
		return statErr
	})
	if err == nil {
		_, err = availableBytes(probe.root.dir, reply)
	}
	if err == nil && reply.Fsid == 0 {
		err = fmt.Errorf("sftpfs: probe statvfs %q: no filesystem identity", probe.root.dir)
	}
	return probe.outcome(reasonKeyVolumeIdentity, &probe.capabilities.VolumeIdentity, err)
}

// cleanup ferme le handle ouvert et retire chaque entrée posée, en ordre inverse, même après une annulation.
func (probe *capabilityProbe) cleanup() error {
	failures := []error{probe.closeHandle()}
	for createdIndex := len(probe.created) - 1; createdIndex >= 0; createdIndex-- {
		failures = append(failures, probe.root.removeProbeEntry(probe.requests, probe.created[createdIndex]))
	}
	probe.created = nil
	return errors.Join(failures...)
}
