// Package hashsource donne l'empreinte de contenu d'un fichier sans le relire quand le stockage la connaît : sur un
// montage rclone S3, `rclone hashsum` renvoie le MD5 du backend sans télécharger les données.
package hashsource

import (
	"context"
	"errors"
	"fmt"
)

// Algorithm nomme une fonction de hachage, en majuscules comme dans `rclone hashsum`.
type Algorithm string

// Hachages pris en charge ; la liste est fermée, rien d'autre n'est passé à rclone.
const (
	MD5    Algorithm = "MD5"
	SHA1   Algorithm = "SHA1"
	SHA256 Algorithm = "SHA256"
)

// AllAlgorithms est l'ordre d'essai des sondes : du plus courant sur les stockages objet au plus rare.
var AllAlgorithms = []Algorithm{MD5, SHA1, SHA256}

// Origin dit d'où vient une empreinte : fournie par le stockage sans lecture, ou calculée en lisant le fichier.
type Origin string

// Valeurs possibles de Result.Origin.
const (
	Backend     Origin = "backend"
	ReadLocally Origin = "local"
)

// Result est l'empreinte d'un fichier. Available est faux, sans erreur, quand le stockage ne la fournit pas : c'est
// une absence normale, jamais un échec.
type Result struct {
	Value     string
	Origin    Origin
	Available bool
}

// HashSource donne l'empreinte d'un fichier local selon un algorithme.
type HashSource interface {
	Hash(ctx context.Context, path string, algorithm Algorithm) (Result, error)
}

// Limiter plafonne le débit de lecture ; compatible avec golang.org/x/time/rate.Limiter et avec dedupe.Limiter.
type Limiter interface {
	WaitN(ctx context.Context, n int) error
}

// Erreurs typées du paquet.
var (
	// ErrHashUnavailable : aucune empreinte exploitable, et l'appelant a exigé qu'on ne relise pas le fichier.
	ErrHashUnavailable = errors.New("hashsource: hash not available")
	// ErrNoMountForPath : le chemin n'est sous aucun montage configuré.
	ErrNoMountForPath = errors.New("hashsource: path is not under a configured mount")
	// ErrRcloneFailed : rclone n'a pas pu répondre (échec, délai, sortie inexploitable) ; le message ne cite jamais sa sortie.
	ErrRcloneFailed = errors.New("hashsource: rclone failed")
	// ErrUnsafePath : chemin relatif, non nettoyé, avec « .. » ou avec un octet nul.
	ErrUnsafePath = errors.New("hashsource: unsafe path")
	// ErrUnknownAlgorithm : algorithme hors de la liste fermée.
	ErrUnknownAlgorithm = errors.New("hashsource: unknown algorithm")
)

// Valid dit si l'algorithme est dans la liste fermée.
func (algorithm Algorithm) Valid() bool {
	return algorithm.hexLength() > 0
}

// hexLength est la longueur de l'empreinte en hexadécimal ; 0 pour un algorithme inconnu.
func (algorithm Algorithm) hexLength() int {
	switch algorithm {
	case MD5:
		return 32
	case SHA1:
		return 40
	case SHA256:
		return 64
	default:
		return 0
	}
}

func checkAlgorithm(algorithm Algorithm) error {
	if !algorithm.Valid() {
		return fmt.Errorf("%w: %q", ErrUnknownAlgorithm, string(algorithm))
	}
	return nil
}
