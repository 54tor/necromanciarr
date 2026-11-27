package app

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

const stateSealKeyBytes = 32

var stateSealSeparator = []byte(".")

// stateSealKey tire la clé du processus au premier usage ; un défaut d'aléa est une erreur, jamais une clé faible.
func (svc *service) stateSealKey() ([]byte, error) {
	svc.sealKeyOnce.Do(func() {
		key := make([]byte, stateSealKeyBytes)
		if _, err := rand.Read(key); err != nil {
			svc.sealKeyErr = fmt.Errorf("generate webauthn state key: %w", err)
			return
		}
		svc.sealKey = key
	})
	return svc.sealKey, svc.sealKeyErr
}

func stateMAC(key, encodedPayload []byte) []byte {
	signer := hmac.New(sha256.New, key)
	signer.Write(encodedPayload)
	return signer.Sum(nil)
}

// sealState rend base64url(charge) "." base64url(HMAC-SHA256) avec une clé du processus : un état ne survit pas à une relance.
func (svc *service) sealState(payload []byte) ([]byte, error) {
	key, err := svc.stateSealKey()
	if err != nil {
		return nil, err
	}
	encodedPayload := []byte(base64.RawURLEncoding.EncodeToString(payload))
	encodedMAC := base64.RawURLEncoding.EncodeToString(stateMAC(key, encodedPayload))
	return append(append(encodedPayload, stateSealSeparator...), encodedMAC...), nil
}

// unsealState vérifie le MAC sur la charge encodée avant de la décoder ; un sceau absent ou faux rend ErrInvalidAssertion.
func (svc *service) unsealState(state []byte) ([]byte, error) {
	key, err := svc.stateSealKey()
	if err != nil {
		return nil, err
	}
	encodedPayload, encodedMAC, found := bytes.Cut(state, stateSealSeparator)
	if !found {
		return nil, ErrInvalidAssertion
	}
	mac, err := base64.RawURLEncoding.DecodeString(string(encodedMAC))
	if err != nil || !hmac.Equal(mac, stateMAC(key, encodedPayload)) {
		return nil, ErrInvalidAssertion
	}
	payload, err := base64.RawURLEncoding.DecodeString(string(encodedPayload))
	if err != nil {
		return nil, ErrInvalidAssertion
	}
	return payload, nil
}
