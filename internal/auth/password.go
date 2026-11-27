package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	MinPasswordLength = 12
	MaxPasswordBytes  = 128

	argonMemoryKiB = 64 * 1024
	argonPasses    = 2
	argonLanes     = 2
	argonKeyBytes  = 32
	argonSaltBytes = 16
	argonFields    = 6
)

var (
	ErrPasswordTooShort = errors.New("password is too short")
	ErrPasswordTooLong  = errors.New("password is too long")
	errMalformedHash    = errors.New("malformed password hash")
)

type argonParameters struct {
	memoryKiB uint32
	passes    uint32
	lanes     uint8
}

var currentParameters = argonParameters{memoryKiB: argonMemoryKiB, passes: argonPasses, lanes: argonLanes}

func ValidatePassword(password string) error {
	if len(password) > MaxPasswordBytes {
		return ErrPasswordTooLong
	}
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return ErrPasswordTooShort
	}
	return nil
}

func HashPassword(password string) (string, error) {
	if len(password) > MaxPasswordBytes {
		return "", ErrPasswordTooLong
	}
	salt := make([]byte, argonSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	key := deriveKey(password, salt, currentParameters)
	return encodeHash(currentParameters, salt, key), nil
}

func VerifyPassword(password, encoded string) (matches bool, needsRehash bool, err error) {
	if len(password) > MaxPasswordBytes {
		return false, false, ErrPasswordTooLong
	}
	parameters, salt, expectedKey, err := decodeHash(encoded)
	if err != nil {
		return false, false, err
	}
	obtainedKey := deriveKey(password, salt, parameters)
	matches = subtle.ConstantTimeCompare(obtainedKey, expectedKey) == 1
	return matches, matches && parameters != currentParameters, nil
}

var (
	dummySalt     []byte
	dummySaltOnce sync.Once
)

func VerifyDummy(password string) {
	dummySaltOnce.Do(func() {
		dummySalt = make([]byte, argonSaltBytes)
		_, _ = rand.Read(dummySalt)
	})
	if len(password) > MaxPasswordBytes {
		password = password[:MaxPasswordBytes]
	}
	deriveKey(password, dummySalt, currentParameters)
}

func deriveKey(password string, salt []byte, parameters argonParameters) []byte {
	return argon2.IDKey([]byte(password), salt, parameters.passes, parameters.memoryKiB, parameters.lanes, argonKeyBytes)
}

func encodeHash(parameters argonParameters, salt, key []byte) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version,
		parameters.memoryKiB, parameters.passes, parameters.lanes,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

func decodeHash(encoded string) (parameters argonParameters, salt, key []byte, err error) {
	fields := strings.Split(encoded, "$")
	if len(fields) != argonFields || fields[0] != "" || fields[1] != "argon2id" {
		return parameters, nil, nil, errMalformedHash
	}
	var version int
	if _, err = fmt.Sscanf(fields[2], "v=%d", &version); err != nil || version != argon2.Version {
		return parameters, nil, nil, errMalformedHash
	}
	var lanes uint
	if _, err = fmt.Sscanf(fields[3], "m=%d,t=%d,p=%d", &parameters.memoryKiB, &parameters.passes, &lanes); err != nil {
		return parameters, nil, nil, errMalformedHash
	}
	if lanes == 0 || lanes > 255 || parameters.passes == 0 || parameters.memoryKiB == 0 {
		return parameters, nil, nil, errMalformedHash
	}
	parameters.lanes = uint8(lanes)
	if salt, err = base64.RawStdEncoding.DecodeString(fields[4]); err != nil || len(salt) == 0 {
		return parameters, nil, nil, errMalformedHash
	}
	if key, err = base64.RawStdEncoding.DecodeString(fields[5]); err != nil || len(key) == 0 {
		return parameters, nil, nil, errMalformedHash
	}
	return parameters, salt, key, nil
}
