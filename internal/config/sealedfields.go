package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"slices"
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
	"radarr":         {"apiKey", "password", "url"},
	"sonarr":         {"apiKey", "password", "url"},
	"torrentClients": {"password", "url"},
	"trackers":       {"announce"},
	"torznab":        {"url"},
	"fsProviders":    {"rc", "password", "identityPassphrase"},
}

// secretSettings liste les réglages scalaires scellés.
var secretSettings = []string{"prowlarr", "crossSeed", "libraryApi"}

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

// stringEntries lit un champ secret : une chaîne ou un tableau de chaînes (non lié à l'index de chaque entrée).
func stringEntries(value any) (entries []string, isArray, isValid bool) {
	switch typed := value.(type) {
	case nil:
		return nil, false, true
	case string:
		return []string{typed}, false, true
	case []string:
		return typed, true, true
	case []any:
		for _, entry := range typed {
			text, isString := entry.(string)
			if !isString {
				return nil, true, false
			}
			entries = append(entries, text)
		}
		return entries, true, true
	}
	return nil, false, false
}

func rejectNonStringSecrets(list string, element map[string]any) error {
	for _, champCourant := range secretFieldsByList[list] {
		if _, _, isValid := stringEntries(element[champCourant]); !isValid {
			return usageError{message: fmt.Sprintf("config: secret field %s.%s must be a string", list, champCourant)}
		}
	}
	return nil
}

// transformSecretField applique transform à chaque entrée d'un champ secret en gardant sa forme (chaîne ou tableau).
func transformSecretField(element map[string]any, field string, transform func(string) (string, error)) error {
	entries, isArray, _ := stringEntries(element[field])
	transformed := make([]any, len(entries))
	for position, entry := range entries {
		result, err := transform(entry)
		if err != nil {
			return err
		}
		transformed[position] = result
	}
	switch {
	case isArray:
		element[field] = transformed
	case len(transformed) == 1:
		element[field] = transformed[0]
	}
	return nil
}

// SealElement scelle, en place, les champs secrets non vides d'un élément de la liste.
func (sealer *fieldSealer) SealElement(list, itemID string, element map[string]any) error {
	if err := rejectNonStringSecrets(list, element); err != nil {
		return err
	}
	for _, champCourant := range secretFieldsByList[list] {
		associated := associatedFor(list, itemID, champCourant)
		err := transformSecretField(element, champCourant, func(clair string) (string, error) {
			if clair == "" {
				return clair, nil
			}
			return sealer.seal(clair, associated)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// OpenElement descelle, en place, les champs secrets scellés d'un élément ; les autres valeurs restent telles quelles.
func (sealer *fieldSealer) OpenElement(list, itemID string, element map[string]any) error {
	for _, champCourant := range secretFieldsByList[list] {
		associated := associatedFor(list, itemID, champCourant)
		err := transformSecretField(element, champCourant, func(valeurScellee string) (string, error) {
			if !strings.HasPrefix(valeurScellee, sealedPrefix) {
				return valeurScellee, nil
			}
			return sealer.open(valeurScellee, associated)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// SealSetting scelle la valeur d'un réglage secret ; sans clé, ErrSecretKeyMissing.
func (sealer *fieldSealer) SealSetting(key, plain string) (string, error) {
	return sealer.seal(plain, associatedFor(settingsScope, key, ""))
}

// OpenSetting descelle la valeur d'un réglage secret.
func (sealer *fieldSealer) OpenSetting(key, sealed string) (string, error) {
	return sealer.open(sealed, associatedFor(settingsScope, key, ""))
}

// SecretFields nomme les champs scellés d'un élément de la liste.
func SecretFields(list string) []string { return slices.Clone(secretFieldsByList[list]) }

// IsSecretSetting dit si le réglage est scellé en base.
func IsSecretSetting(key string) bool { return slices.Contains(secretSettings, key) }

func containsSealed(element map[string]any) bool {
	for _, valeurCourante := range element {
		entries, _, _ := stringEntries(valeurCourante)
		for _, entry := range entries {
			if strings.HasPrefix(entry, sealedPrefix) {
				return true
			}
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
