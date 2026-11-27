package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"
)

const (
	SessionAbsoluteLifetime = 14 * 24 * time.Hour
	SessionIdleTimeout      = 24 * time.Hour

	sessionTokenBytes = 32
)

func NewSessionToken() (token string, id string, err error) {
	raw := make([]byte, sessionTokenBytes)
	if _, err = rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("generate session token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, SessionIDOf(token), nil
}

func SessionIDOf(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}
