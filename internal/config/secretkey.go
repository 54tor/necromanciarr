package config

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

const (
	// SecretKeySize est la taille exacte de NECROMANCIARR_SECRET_KEY une fois décodée (AES-256).
	SecretKeySize = 32
	envSecretKey  = "NECROMANCIARR_SECRET_KEY"
)

// secretKeyEncoding est le seul encodage admis : base64url sans remplissage, bits de fin nuls. Ni base64 standard ni
// hexadécimal : une seule écriture par clé, celle de `api-key --secret`, que KnownSecrets masque à coup sûr.
var secretKeyEncoding = base64.RawURLEncoding.Strict()

// ErrSecretKeyRequired refuse un indexeur Torznab sans clé de scellement : sa passkey ne serait gardée qu'en clair.
// La CLI la rend avec le message traduit error.config.secret_key_required.
var ErrSecretKeyRequired = fmt.Errorf("torznab requires %s", envSecretKey)

// InvalidSecretKeyError signale une NECROMANCIARR_SECRET_KEY mal encodée ou de mauvaise longueur. C'est une erreur
// d'usage ; elle ne cite que le nom de la variable, jamais un fragment de la valeur.
type InvalidSecretKeyError struct{}

func (InvalidSecretKeyError) Error() string {
	return fmt.Sprintf("%s: %s must be %d bytes in base64url without padding (generate one with necromanciarr api-key --secret)",
		ErrUsage.Error(), envSecretKey, SecretKeySize)
}

// Is rattache l'erreur à ErrUsage.
func (InvalidSecretKeyError) Is(target error) bool { return target == ErrUsage }

// readSecretKey décode la clé de l'environnement ; absente ou vide : nil.
func readSecretKey(lookup func(string) (string, bool)) ([]byte, error) {
	rawValue, found := lookup(envSecretKey)
	if !found || rawValue == "" {
		return nil, nil
	}
	// Le décodeur ignore « \r » et « \n » : la longueur exacte écarte toute écriture autre que celle d'api-key --secret.
	if len(rawValue) != secretKeyEncoding.EncodedLen(SecretKeySize) {
		return nil, InvalidSecretKeyError{}
	}
	decoded, err := secretKeyEncoding.DecodeString(rawValue)
	if err != nil || len(decoded) != SecretKeySize {
		return nil, InvalidSecretKeyError{}
	}
	return decoded, nil
}

// secretKeyForms donne la clé sous ses écritures probables (brute, base64 avec et sans remplissage, hexadécimal) pour
// que la redaction la masque même recopiée sous une autre forme qu'à la lecture.
func secretKeyForms(key []byte) []string {
	if len(key) == 0 {
		return nil
	}
	return []string{
		string(key), secretKeyEncoding.EncodeToString(key), base64.URLEncoding.EncodeToString(key),
		base64.StdEncoding.EncodeToString(key), base64.RawStdEncoding.EncodeToString(key), hex.EncodeToString(key),
	}
}
