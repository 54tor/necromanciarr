package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

const dummySecret = "dummy-secret-value"

func newTestFieldSealer(t *testing.T, fillByte byte) *fieldSealer {
	t.Helper()
	sealer, err := newFieldSealer(bytes.Repeat([]byte{fillByte}, SecretKeySize))
	if err != nil {
		t.Fatalf("newFieldSealer: %v", err)
	}
	return sealer
}

func TestFieldSealer_roundTripsEveryListSecretField(t *testing.T) {
	sealer := newTestFieldSealer(t, 1)
	for listName, secretFields := range secretFieldsByList {
		element := map[string]any{"name": "visible"}
		for _, champCourant := range secretFields {
			element[champCourant] = dummySecret + "-" + champCourant
		}
		if err := sealer.sealElement(listName, "item-1", element); err != nil {
			t.Fatalf("%s: sealElement: %v", listName, err)
		}
		if !containsSealed(element) {
			t.Fatalf("%s: containsSealed = false after sealing", listName)
		}
		if err := sealer.openElement(listName, "item-1", element); err != nil {
			t.Fatalf("%s: openElement: %v", listName, err)
		}
		for _, champCourant := range secretFields {
			valeurAttendue := dummySecret + "-" + champCourant
			valeurObtenue := element[champCourant]
			if valeurObtenue != valeurAttendue {
				t.Errorf("%s.%s: valeurAttendue %q, valeurObtenue %v", listName, champCourant, valeurAttendue, valeurObtenue)
			}
		}
	}
}

func TestFieldSealer_wrongKeyReturnsSealedSecretUnreadable(t *testing.T) {
	element := map[string]any{"apiKey": dummySecret}
	if err := newTestFieldSealer(t, 1).sealElement("radarr", "item-1", element); err != nil {
		t.Fatalf("sealElement: %v", err)
	}
	erreurObtenue := newTestFieldSealer(t, 2).openElement("radarr", "item-1", element)
	if !errors.Is(erreurObtenue, ErrSealedSecretUnreadable) {
		t.Fatalf("valeurAttendue ErrSealedSecretUnreadable, valeurObtenue %v", erreurObtenue)
	}
	if !errors.Is(erreurObtenue, ErrUsage) {
		t.Errorf("l'erreur doit se rattacher à ErrUsage, valeurObtenue %v", erreurObtenue)
	}
}

func TestFieldSealer_bindsTheCiphertextToItsItem(t *testing.T) {
	sealer := newTestFieldSealer(t, 1)
	element := map[string]any{"apiKey": dummySecret}
	if err := sealer.sealElement("radarr", "item-1", element); err != nil {
		t.Fatalf("sealElement: %v", err)
	}
	erreurObtenue := sealer.openElement("radarr", "item-2", element)
	if !errors.Is(erreurObtenue, ErrSealedSecretUnreadable) {
		t.Fatalf("valeurAttendue ErrSealedSecretUnreadable, valeurObtenue %v", erreurObtenue)
	}
}

func TestFieldSealer_leavesNonSecretFieldsUntouched(t *testing.T) {
	sealer := newTestFieldSealer(t, 1)
	element := map[string]any{"name": "visible", "url": "http://example.com", "apiKey": "", "port": 7878}
	if err := sealer.sealElement("radarr", "item-1", element); err != nil {
		t.Fatalf("sealElement: %v", err)
	}
	if element["name"] != "visible" || element["url"] != "http://example.com" || element["port"] != 7878 {
		t.Errorf("champs non secrets modifiés : %v", element)
	}
	if element["apiKey"] != "" {
		t.Errorf("valeurAttendue champ vide laissé vide, valeurObtenue %v", element["apiKey"])
	}
	if containsSealed(element) {
		t.Errorf("un champ vide ne doit pas être scellé")
	}
}

func TestFieldSealer_neverEmitsThePlaintext(t *testing.T) {
	sealer := newTestFieldSealer(t, 1)
	element := map[string]any{"password": dummySecret}
	if err := sealer.sealElement("torrentClients", "item-1", element); err != nil {
		t.Fatalf("sealElement: %v", err)
	}
	serialisation, err := json.Marshal(element)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if strings.Contains(string(serialisation), dummySecret) {
		t.Errorf("le clair apparaît dans la sérialisation : %s", serialisation)
	}
	if strings.Contains(fmt.Sprintf("%v|%+v|%#v", sealer, sealer, sealer), "aead") {
		t.Errorf("le sealer doit être masqué par fmt")
	}
}

func TestFieldSealer_roundTripsASetting(t *testing.T) {
	sealer := newTestFieldSealer(t, 1)
	sealedValue, err := sealer.sealSetting("prowlarr", dummySecret)
	if err != nil {
		t.Fatalf("sealSetting: %v", err)
	}
	if strings.Contains(sealedValue, dummySecret) {
		t.Fatalf("le clair apparaît dans la valeur scellée")
	}
	valeurObtenue, err := sealer.openSetting("prowlarr", sealedValue)
	if err != nil || valeurObtenue != dummySecret {
		t.Fatalf("valeurAttendue %q, valeurObtenue %q (%v)", dummySecret, valeurObtenue, err)
	}
	if _, err := sealer.openSetting("autre", sealedValue); !errors.Is(err, ErrSealedSecretUnreadable) {
		t.Errorf("valeurAttendue ErrSealedSecretUnreadable pour une autre clé de réglage, valeurObtenue %v", err)
	}
}

func TestFieldSealer_nilSealerRefusesToSeal(t *testing.T) {
	sealer, err := NewSecretSealer(nil)
	if err != nil {
		t.Fatalf("NewSecretSealer(nil): %v", err)
	}
	erreurObtenue := sealer.sealElement("radarr", "item-1", map[string]any{"apiKey": dummySecret})
	if !errors.Is(erreurObtenue, ErrSecretKeyMissing) || !errors.Is(erreurObtenue, ErrUsage) {
		t.Fatalf("valeurAttendue ErrSecretKeyMissing rattachée à ErrUsage, valeurObtenue %v", erreurObtenue)
	}
}

func TestNewFieldSealer_rejectsAShortKey(t *testing.T) {
	_, erreurObtenue := newFieldSealer(make([]byte, SecretKeySize-1))
	if !errors.Is(erreurObtenue, ErrUsage) {
		t.Fatalf("valeurAttendue InvalidSecretKeyError, valeurObtenue %v", erreurObtenue)
	}
	if _, ok := erreurObtenue.(InvalidSecretKeyError); !ok {
		t.Errorf("valeurAttendue InvalidSecretKeyError, valeurObtenue %T", erreurObtenue)
	}
}
