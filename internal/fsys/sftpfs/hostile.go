package sftpfs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/bits"
	"slices"
	"strings"

	"github.com/pkg/sftp"
)

// Plafonds des réponses du serveur, qui est hostile : au-delà, ErrHostileResponse, jamais une troncature.
const (
	// maxNameBytes borne un nom d'entrée et une cible de lien (PATH_MAX Linux) ; un nom plus long est écarté.
	maxNameBytes = 4096
	// maxPlausibleBytes : aucune taille de fichier ni place libre réelle n'atteint 2^62 octets (4 Eio).
	maxPlausibleBytes = 1 << 62
)

// maxListingEntries borne une liste de dossier. ponytail: pkg/sftp v1.13.13 n'offre que ReadDirContext, qui garde
// la liste entière en mémoire avant de la rendre : le plafond se vérifie après coup, et une liste sans fin n'est
// arrêtée que par timeout. Mémoire ≈ débit × timeout × 10 (une entrée ≈ 13 octets sur le fil, ≈ 150 octets
// décodée). Passer à une lecture de dossier en flux si pkg/sftp en expose une.
var maxListingEntries = 1_000_000

// keepListingName garde un nom d'entrée sûr à joindre à son dossier : ni vide, ni « . », ni « .. », sans « / » ni
// NUL, au plus maxNameBytes. Octets non UTF-8 et caractères de contrôle restent (noms Linux possibles) : ils ne
// sont jamais affichés bruts (erreurs en %q).
func keepListingName(name string) bool {
	isTraversal := name == "" || name == "." || name == ".."
	isSplitting := strings.ContainsAny(name, "/\x00")
	return !isTraversal && !isSplitting && len(name) <= maxNameBytes
}

// isPlausibleSize refuse une taille négative (uint64 du protocole au-delà de 2^63) ou démesurée.
func isPlausibleSize(size int64) bool {
	return size >= 0 && size <= maxPlausibleBytes
}

// checkInfo refuse une information de fichier à la taille impossible.
func checkInfo(operation, name string, info fs.FileInfo) (fs.FileInfo, error) {
	if !isPlausibleSize(info.Size()) {
		return nil, fmt.Errorf("%w: sftpfs: %s %q: size %d", ErrHostileResponse, operation, name, info.Size())
	}
	return info, nil
}

// listingEntries filtre une liste reçue : noms dangereux et tailles impossibles écartés, doublons ramenés à la
// première occurrence, tri par nom.
func listingEntries(name string, infos []fs.FileInfo) ([]fs.DirEntry, error) {
	if len(infos) > maxListingEntries {
		return nil, fmt.Errorf("%w: sftpfs: readdir %q: more than %d entries", ErrHostileResponse, name, maxListingEntries)
	}
	seen := make(map[string]bool, len(infos))
	entries := make([]fs.DirEntry, 0, len(infos))
	for _, info := range infos {
		isKept := keepListingName(info.Name()) && isPlausibleSize(info.Size()) && !seen[info.Name()]
		if !isKept {
			continue
		}
		seen[info.Name()] = true
		entries = append(entries, fs.FileInfoToDirEntry(info))
	}
	slices.SortFunc(entries, func(left, right fs.DirEntry) int { return strings.Compare(left.Name(), right.Name()) })
	return entries, nil
}

// checkLinkTarget refuse une cible de lien démesurée ou coupée par un NUL.
func checkLinkTarget(name, target string) (string, error) {
	if len(target) > maxNameBytes || strings.ContainsRune(target, 0) {
		return "", fmt.Errorf("%w: sftpfs: readlink %q: target of %d bytes", ErrHostileResponse, name, len(target))
	}
	return target, nil
}

// availableBytes calcule f_bavail × f_frsize ; fragment nul, dépassement ou valeur au-delà de 2^62 sont hostiles.
func availableBytes(name string, reply *sftp.StatVFS) (uint64, error) {
	high, product := bits.Mul64(reply.Bavail, reply.Frsize)
	isHostile := reply.Frsize == 0 || high != 0 || product > maxPlausibleBytes
	if isHostile {
		return 0, fmt.Errorf("%w: sftpfs: statvfs %q: %d blocks of %d bytes", ErrHostileResponse, name, reply.Bavail, reply.Frsize)
	}
	return product, nil
}

// mapError range une erreur d'opération : absence et refus deviennent fs.ErrNotExist et fs.ErrPermission, une
// opération non prise en charge errors.ErrUnsupported ; io.EOF et les erreurs du volume (ErrRemoteUnavailable,
// ErrHostileResponse) passent telles quelles. Le chemin est cité en %q : jamais de caractère de contrôle brut.
func mapError(operation, name string, err error) error {
	if err == nil || errors.Is(err, io.EOF) {
		return err
	}
	if sentinel := statusSentinel(err); sentinel != nil {
		return fmt.Errorf("sftpfs: %s %q: %w: %w", operation, name, sentinel, err)
	}
	return fmt.Errorf("sftpfs: %s %q: %w", operation, name, err)
}

// statusSentinel traduit un statut SFTP que pkg/sftp laisse brut ; nil quand err porte déjà son erreur typée.
func statusSentinel(err error) error {
	var status *sftp.StatusError
	if !errors.As(err, &status) {
		return nil
	}
	switch status.FxCode() {
	case sftp.ErrSSHFxNoSuchFile:
		return fs.ErrNotExist
	case sftp.ErrSSHFxPermissionDenied:
		return fs.ErrPermission
	case sftp.ErrSSHFxOpUnsupported:
		return errors.ErrUnsupported
	default:
		return nil
	}
}
