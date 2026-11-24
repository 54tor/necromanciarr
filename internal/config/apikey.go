package config

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

const (
	// authTokenBytes donne 256 bits d'aléa, soit 43 caractères en base64url sans remplissage.
	authTokenBytes = 32
	// MinAuthTokenLength est la longueur minimale de NECROMANCIARR_AUTH_TOKEN : un jeton court se devine.
	MinAuthTokenLength = 32
)

// GenerateAuthToken tire un jeton aléatoire pour NECROMANCIARR_AUTH_TOKEN ; il n'est écrit nulle part, l'opérateur
// le recopie lui-même dans son .env.
func GenerateAuthToken() (string, error) {
	randomBytes := make([]byte, authTokenBytes)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", fmt.Errorf("generate auth token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(randomBytes), nil
}

// GenerateSecretKey tire une clé de scellement neuve (32 octets de crypto/rand, base64url sans remplissage) ; elle
// n'est écrite nulle part, l'opérateur la recopie lui-même dans son .env.
func GenerateSecretKey() (string, error) {
	randomBytes := make([]byte, SecretKeySize)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", errors.New("generate secret key: random source failed")
	}
	return secretKeyEncoding.EncodeToString(randomBytes), nil
}
