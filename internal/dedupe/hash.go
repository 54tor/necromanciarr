package dedupe

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"necromanciarr/internal/hashsource"
)

// Method est la preuve d'identité réellement utilisée pour un Replacement.
type Method string

// Méthodes de preuve : empreinte du stockage (éventuellement avec une lecture locale d'un seul côté), comparaison
// complète octet par octet, ou par échantillons.
const (
	MethodHash    Method = "hash"
	MethodFull    Method = "full"
	MethodSampled Method = "sampled"
)

// ErrHashUnavailable est renvoyée en mode hash quand aucune empreinte exploitable n'existe : jamais de repli sur une
// lecture complète que l'opérateur n'a pas demandée.
var ErrHashUnavailable = hashsource.ErrHashUnavailable

var defaultHashAlgorithms = []hashsource.Algorithm{hashsource.MD5}

// hashDecision dit quel algorithme sert et quel côté doit être lu localement faute d'empreinte du stockage.
type hashDecision struct {
	algorithm hashsource.Algorithm
	torrent   hashsource.Result
	library   hashsource.Result
}

func (decision hashDecision) torrentNeedsLocalRead() bool { return !decision.torrent.Available }
func (decision hashDecision) libraryNeedsLocalRead() bool { return !decision.library.Available }

// localReadBytes : 0 si le stockage fournit les deux empreintes, la taille du fichier si un côté doit être lu.
func (decision hashDecision) localReadBytes(size int64) int64 {
	if decision.torrentNeedsLocalRead() || decision.libraryNeedsLocalRead() {
		return size
	}
	return 0
}

func (options Options) hashAlgorithms() []hashsource.Algorithm {
	if len(options.HashAlgorithms) == 0 {
		return defaultHashAlgorithms
	}
	return options.HashAlgorithms
}

// decideHash interroge le stockage pour les deux fichiers. Préfère un algorithme fourni des deux côtés ; sinon, un
// algorithme fourni d'un seul côté (l'autre sera lu une fois en local) ; found est faux si aucun côté n'en fournit.
// Un chemin distant n'a encore aucune empreinte du stockage (pas de source sftp-exec) : la preuve est une lecture.
func decideHash(ctx context.Context, backend hashsource.HashSource, algorithms []hashsource.Algorithm, torrentPath, libraryPath string) (decision hashDecision, found bool, err error) {
	if backend == nil || IsRemote(torrentPath) || IsRemote(libraryPath) {
		return hashDecision{}, false, nil
	}
	var oneSided hashDecision
	foundOneSided := false
	for _, algorithm := range algorithms {
		torrent, err := backendHash(ctx, backend, torrentPath, algorithm)
		if err != nil {
			return hashDecision{}, false, err
		}
		library, err := backendHash(ctx, backend, libraryPath, algorithm)
		if err != nil {
			return hashDecision{}, false, err
		}
		candidate := hashDecision{algorithm: algorithm, torrent: torrent, library: library}
		switch {
		case torrent.Available && library.Available:
			return candidate, true, nil
		case (torrent.Available || library.Available) && !foundOneSided:
			oneSided, foundOneSided = candidate, true
		}
	}
	return oneSided, foundOneSided, nil
}

// backendHash : un chemin hors de tout montage n'a simplement pas d'empreinte du stockage ; toute autre erreur (rclone
// en panne, annulation) remonte, pour ne pas basculer sans bruit sur la lecture de dizaines de Go.
func backendHash(ctx context.Context, backend hashsource.HashSource, path string, algorithm hashsource.Algorithm) (hashsource.Result, error) {
	result, err := backend.Hash(ctx, path, algorithm)
	if errors.Is(err, hashsource.ErrNoMountForPath) || errors.Is(err, hashsource.ErrUnsafePath) {
		return hashsource.Result{}, nil
	}
	if err != nil || result.Origin != hashsource.Backend || !result.Available || len(result.Value) != hexLength(algorithm) {
		return hashsource.Result{}, err
	}
	return result, nil
}

// chooseMethod fixe Method, HashAlgorithm et BytesToRead d'un Replacement dont le fichier de la bibliothèque est connu.
// En mode hash sans empreinte exploitable, l'erreur est ErrHashUnavailable : l'appelant retire alors le lien proposé.
func (run *planRun) chooseMethod(replacement *Replacement, libraryPath string) error {
	if run.options.Verify == VerifyAuto || run.options.Verify == VerifyHash {
		decision, found, err := decideHash(run.ctx, run.options.Backend, run.options.hashAlgorithms(), replacement.TorrentPath, libraryPath)
		if err != nil {
			return err
		}
		if found {
			replacement.Method, replacement.HashAlgorithm = MethodHash, decision.algorithm
			replacement.BytesToRead = decision.localReadBytes(replacement.Size)
			return nil
		}
	}
	run.useReadMethod(replacement)
	if run.options.Verify == VerifyHash {
		return ErrHashUnavailable
	}
	return nil
}

// useReadMethod : comparaison complète, ou par échantillons si l'opérateur l'a demandée.
func (run *planRun) useReadMethod(replacement *Replacement) {
	verification := VerifyFull
	replacement.Method = MethodFull
	if run.options.Verify == VerifySampled {
		verification, replacement.Method = VerifySampled, MethodSampled
	}
	replacement.BytesToRead = proofBytes(replacement.Size, verification)
}

// proof est ce qu'Apply a établi : méthode utilisée, octets lus et, si le stockage ou une lecture a donné l'empreinte
// du fichier de la bibliothèque, cette empreinte pour le lien enregistré.
type proof struct {
	method    Method
	bytesRead int64
	algorithm hashsource.Algorithm
	hash      string
}

// methodFor : une proposition planifiée par empreinte reste par empreinte ; sinon Verify tranche entre complet et échantillons.
func methodFor(replacement Replacement, options Options) Method {
	switch {
	case replacement.Method == MethodHash:
		return MethodHash
	case options.Verify == VerifySampled:
		return MethodSampled
	default:
		return MethodFull
	}
}

// proveByHash refait la décision d'empreinte (le stockage a pu changer depuis Plan), lit localement le côté sans
// empreinte, et exige l'égalité. Les tailles égales ont déjà été contrôlées par checkUnchanged.
func proveByHash(ctx context.Context, replacement Replacement, options Options, readMeter *meter) (proof, error) {
	algorithms := options.hashAlgorithms()
	if replacement.HashAlgorithm != "" {
		algorithms = []hashsource.Algorithm{replacement.HashAlgorithm}
	}
	decision, found, err := decideHash(ctx, options.Backend, algorithms, replacement.TorrentPath, replacement.LibraryPath)
	if err != nil {
		return proof{}, err
	}
	if !found {
		return proof{}, ErrHashUnavailable
	}
	local := hashsource.NewLocal(readMeter)
	torrentHash, err := valueOf(ctx, local, decision.torrent, replacement.TorrentPath, decision.algorithm)
	if err != nil {
		return proof{}, err
	}
	libraryHash, err := valueOf(ctx, local, decision.library, replacement.LibraryPath, decision.algorithm)
	if err != nil {
		return proof{}, err
	}
	if !strings.EqualFold(torrentHash, libraryHash) {
		return proof{}, fmt.Errorf("%w: %s differs from %s", ErrNotIdentical, replacement.TorrentPath, replacement.LibraryPath)
	}
	return proof{algorithm: decision.algorithm, hash: strings.ToLower(libraryHash)}, nil
}

// valueOf renvoie l'empreinte du stockage, ou la calcule en lisant le fichier si le stockage n'en a pas.
func valueOf(ctx context.Context, local hashsource.HashSource, backendResult hashsource.Result, path string, algorithm hashsource.Algorithm) (string, error) {
	if backendResult.Available {
		return backendResult.Value, nil
	}
	read, err := local.Hash(ctx, path, algorithm)
	return read.Value, err
}

// hexLength : longueur hexadécimale attendue ; une valeur de longueur différente (ou vide) n'est pas une empreinte.
func hexLength(algorithm hashsource.Algorithm) int {
	return map[hashsource.Algorithm]int{hashsource.MD5: 32, hashsource.SHA1: 40, hashsource.SHA256: 64}[algorithm]
}
