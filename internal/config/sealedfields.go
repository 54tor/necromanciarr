package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"strings"
)

const (
	sealedPrefix    = "sealed:v1:"
	associatedJoin  = "|"
	settingsScope   = "settings"
	redactedSealer  = "config.FieldSealer{***}"
	gcmNonceByteLen = 12
)

// secretFieldsByList nomme, par liste de configuration, les champs gardés scellés en base.
var secretFieldsByList = map[string][]string{
	"radarr":         {"apiKey"},
	"sonarr":         {"apiKey"},
	"torrentClients": {"password"},
	"trackers":       {"announce"},
	"torznab":        {"url"},
	"fsProviders":    {"rc", "password", "identityPassphrase"},
}

// secretSettings liste les réglages scalaires scellés.
var secretSettings = []string{"prowlarr"}

// ErrSecretKeyMissing refuse d'enregistrer un secret sans clé de scellement.
var ErrSecretKeyMissing = usageError{message: "NECROMANCIARR_SECRET_KEY is required to store a secret"}

// ErrSealedSecretUnreadable signale un secret scellé qu'on ne peut pas desceller : clé absente, changée ou valeur déplacée.
var ErrSealedSecretUnreadable = usageError{
	message: "sealed config secrets cannot be read: NECROMANCIARR_SECRET_KEY is missing or changed",
}

// usageError est une erreur d'usage comparable par valeur, rattachée à ErrUsage.
type usageError struct{ message string }

func (erreur usageError) Error() string { return erreur.message }

// Is rattache l'erreur à ErrUsage.
func (usageError) Is(target error) bool { return target == ErrUsage }

// fieldSealer scelle les champs secrets de la configuration (AES-256-GCM) ; il ne se rend sous aucune forme.
type fieldSealer struct {
	aead cipher.AEAD
}

// FieldSealer est la forme exportée du scelleur de champs, pour internal/app.
type FieldSealer = fieldSealer

func newFieldSealer(key []byte) (*fieldSealer, error) {
	if len(key) != SecretKeySize {
		return nil, InvalidSecretKeyError{}
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, InvalidSecretKeyError{}
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, InvalidSecretKeyError{}
	}
	return &fieldSealer{aead: aead}, nil
}

// NewSecretSealer prépare le scelleur ; sans clé (nil), il rend un scelleur nil qui refuse de sceller.
func NewSecretSealer(key []byte) (*FieldSealer, error) {
	if key == nil {
		return nil, nil
	}
	return newFieldSealer(key)
}

// Format rend le scelleur masqué pour tous les verbes de fmt.
func (sealer *fieldSealer) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, redactedSealer)
}

// LogValue rend le scelleur masqué dans les journaux slog.
func (sealer *fieldSealer) LogValue() slog.Value { return slog.StringValue(redactedSealer) }

func (sealer *fieldSealer) sealElement(list, itemID string, element map[string]any) error {
	for _, champCourant := range secretFieldsByList[list] {
		clair, isSecretPresent := element[champCourant].(string)
		if !isSecretPresent || clair == "" {
			continue
		}
		valeurScellee, err := sealer.seal(clair, associatedFor(list, itemID, champCourant))
		if err != nil {
			return err
		}
		element[champCourant] = valeurScellee
	}
	return nil
}

func (sealer *fieldSealer) openElement(list, itemID string, element map[string]any) error {
	for _, champCourant := range secretFieldsByList[list] {
		valeurScellee, isString := element[champCourant].(string)
		if !isString || !strings.HasPrefix(valeurScellee, sealedPrefix) {
			continue
		}
		clair, err := sealer.open(valeurScellee, associatedFor(list, itemID, champCourant))
		if err != nil {
			return err
		}
		element[champCourant] = clair
	}
	return nil
}

func (sealer *fieldSealer) sealSetting(key, plain string) (string, error) {
	return sealer.seal(plain, associatedFor(settingsScope, key, ""))
}

func (sealer *fieldSealer) openSetting(key, sealed string) (string, error) {
	return sealer.open(sealed, associatedFor(settingsScope, key, ""))
}

func containsSealed(element map[string]any) bool {
	for _, valeurCourante := range element {
		texte, isString := valeurCourante.(string)
		if isString && strings.HasPrefix(texte, sealedPrefix) {
			return true
		}
	}
	return false
}

func associatedFor(list, itemID, field string) []byte {
	return []byte(list + associatedJoin + itemID + associatedJoin + field)
}

func (sealer *fieldSealer) seal(plain string, associated []byte) (string, error) {
	if sealer == nil {
		return "", ErrSecretKeyMissing
	}
	nonce := make([]byte, gcmNonceByteLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("config: draw seal nonce: %w", err)
	}
	blob := sealer.aead.Seal(nonce, nonce, []byte(plain), associated)
	return sealedPrefix + base64.RawURLEncoding.EncodeToString(blob), nil
}

func (sealer *fieldSealer) open(sealed string, associated []byte) (string, error) {
	encoded, hasPrefix := strings.CutPrefix(sealed, sealedPrefix)
	if sealer == nil || !hasPrefix {
		return "", ErrSealedSecretUnreadable
	}
	blob, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(blob) < gcmNonceByteLen+sealer.aead.Overhead() {
		return "", ErrSealedSecretUnreadable
	}
	nonce, ciphertext := blob[:gcmNonceByteLen], blob[gcmNonceByteLen:]
	clair, err := sealer.aead.Open(nil, nonce, ciphertext, associated)
	if err != nil {
		return "", ErrSealedSecretUnreadable
	}
	return string(clair), nil
}
