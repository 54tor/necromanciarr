// Package sources rassemble ce que la chaîne de sources fait d'un .torrent obtenu : copie assainie sans annonce et
// copie complète scellée (AES-256-GCM), qui seule garde la passkey au repos.
package sources

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// KeySize est la taille exacte de la clé de scellement (AES-256).
	KeySize = 32
	// formatVersion préfixe chaque bloc : version‖nonce‖chiffré‖étiquette. Une autre valeur est illisible.
	formatVersion  = 1
	nonceSize      = 12
	tagSize        = 16
	infohashLength = 40
	// bindingSeparator joint les composants de la donnée associée ; il est refusé dans chacun d'eux.
	bindingSeparator = "|"
	redactedSealer   = "sources.Sealer{***}"
)

var (
	// ErrSealedUnreadable est la seule erreur d'Unseal : clé changée, bloc tronqué ou altéré, bloc déplacé vers un
	// autre candidat, tracker ou source. Une erreur unique ne dit pas laquelle (pas d'oracle).
	ErrSealedUnreadable = errors.New("sources: sealed torrent unreadable")
	// ErrInvalidBinding refuse au scellement une liaison dont un composant rendrait la donnée associée ambiguë.
	ErrInvalidBinding = errors.New("sources: invalid sealed torrent binding")
	// ErrInvalidKey refuse une clé qui ne fait pas exactement KeySize octets ; la clé n'est jamais citée.
	ErrInvalidKey = errors.New("sources: secret key must be 32 bytes")
)

// nonceSource fournit les nonces ; remplacée seulement par les tests pour simuler un aléa défaillant.
var nonceSource io.Reader = rand.Reader

// ID identifie une source : "provider" ou "torznab:<nom>".
type ID string

// Binding lie un bloc scellé à son candidat : la donnée associée est exactement « infohash|tracker|source ».
// Infohash est en hexadécimal minuscule (40 caractères) ; Tracker peut être vide.
type Binding struct {
	Infohash, Tracker string
	Source            ID
}

// Sealer scelle et descelle les .torrent d'indexeur. Il ne garde que l'AEAD dérivé de la clé, jamais la clé, et ne
// se rend sous aucune forme (fmt, slog).
type Sealer struct {
	aead cipher.AEAD
}

// NewSealer prépare un Sealer pour une clé d'exactement 32 octets. L'appelant reste maître de la clé : la remettre à
// zéro après coup est à sa charge et ne reste qu'un effort (Go peut en avoir copié ailleurs en mémoire).
func NewSealer(key []byte) (*Sealer, error) {
	if len(key) != KeySize {
		return nil, ErrInvalidKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrInvalidKey
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrInvalidKey
	}
	return &Sealer{aead: aead}, nil
}

// Seal chiffre plain avec un nonce neuf tiré de crypto/rand : version‖nonce‖chiffré‖étiquette. Un aléa défaillant
// est une erreur, jamais un nonce nul ou fixe.
func (sealer *Sealer) Seal(plain []byte, binding Binding) ([]byte, error) {
	additionalData, err := associatedData(binding)
	if err != nil {
		return nil, err
	}
	header := make([]byte, 1+nonceSize, 1+nonceSize+len(plain)+tagSize)
	header[0] = formatVersion
	if _, err := io.ReadFull(nonceSource, header[1:]); err != nil {
		return nil, fmt.Errorf("sources: draw seal nonce: %w", err)
	}
	return sealer.aead.Seal(header, header[1:], plain, additionalData), nil
}

// Unseal rend le .torrent complet si le bloc a été scellé avec la même clé pour la même liaison ; toute autre issue
// donne ErrSealedUnreadable.
func (sealer *Sealer) Unseal(blob []byte, binding Binding) ([]byte, error) {
	additionalData, err := associatedData(binding)
	if err != nil || len(blob) < 1+nonceSize+tagSize || blob[0] != formatVersion {
		return nil, ErrSealedUnreadable
	}
	nonce, sealed := blob[1:1+nonceSize], blob[1+nonceSize:]
	plain, err := sealer.aead.Open(nil, nonce, sealed, additionalData)
	if err != nil {
		return nil, ErrSealedUnreadable
	}
	return plain, nil
}

// Format rend le Sealer masqué pour tous les verbes de fmt.
func (sealer *Sealer) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, redactedSealer) }

// LogValue rend le Sealer masqué dans les journaux slog.
func (sealer *Sealer) LogValue() slog.Value { return slog.StringValue(redactedSealer) }

// associatedData joint les composants par « | ». Le séparateur, les caractères de contrôle et l'UTF-8 invalide sont
// refusés dans chaque composant, l'infohash est strict : deux liaisons distinctes ne donnent jamais la même donnée.
func associatedData(binding Binding) ([]byte, error) {
	source := string(binding.Source)
	if !isInfohash(binding.Infohash) || source == "" || !isPlainComponent(binding.Tracker) || !isPlainComponent(source) {
		return nil, ErrInvalidBinding
	}
	return []byte(binding.Infohash + bindingSeparator + binding.Tracker + bindingSeparator + source), nil
}

func isInfohash(text string) bool {
	if len(text) != infohashLength {
		return false
	}
	for _, character := range text {
		isLowerHex := (character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')
		if !isLowerHex {
			return false
		}
	}
	return true
}

func isPlainComponent(text string) bool {
	if !utf8.ValidString(text) || strings.Contains(text, bindingSeparator) {
		return false
	}
	return !strings.ContainsFunc(text, unicode.IsControl)
}
