package i18n

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// Key identifie un message ; une constante par message, jamais construite à l'exécution.
type Key string

// Message est un texte à rendre plus tard : clé et paramètres nommés, sérialisable en base.
type Message struct {
	Key    Key               `json:"key"`
	Params map[string]string `json:"params,omitempty"`
}

// KeyLegacyText porte un texte déjà rendu, hérité d'avant le catalogue ; il est rendu tel quel.
const KeyLegacyText Key = "legacy.text"

// DecodeMessages lit un tableau JSON dont chaque élément est un objet {key, params} ou une chaîne héritée,
// rendue sous la clé KeyLegacyText.
func DecodeMessages(raw []byte) ([]Message, error) {
	var elements []json.RawMessage
	if err := json.Unmarshal(raw, &elements); err != nil {
		return nil, fmt.Errorf("decode messages: %w", err)
	}
	messages := make([]Message, 0, len(elements))
	for elementIndex, element := range elements {
		message, err := decodeMessage(element)
		if err != nil {
			return nil, fmt.Errorf("decode messages[%d]: %w", elementIndex, err)
		}
		messages = append(messages, message)
	}
	return messages, nil
}

func decodeMessage(element json.RawMessage) (Message, error) {
	trimmed := bytes.TrimSpace(element)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var legacyText string
		if err := json.Unmarshal(trimmed, &legacyText); err != nil {
			return Message{}, err
		}
		return Message{Key: KeyLegacyText, Params: map[string]string{"text": legacyText}}, nil
	}
	var message Message
	if err := json.Unmarshal(trimmed, &message); err != nil {
		return Message{}, err
	}
	if message.Key == "" {
		return Message{}, errors.New("message without key")
	}
	return message, nil
}
