package web

import (
	"encoding/json"
	"errors"

	"necromanciarr/internal/app"
	"necromanciarr/internal/config"
)

// configWriteError porte l'erreur d'une écriture de configuration et les secrets saisis dans la requête.
type configWriteError struct {
	err          error
	typedSecrets []string
}

func (failure *configWriteError) Error() string { return failure.err.Error() }

func (failure *configWriteError) Unwrap() error { return failure.err }

func withConfigDetail(err error, typedSecrets []string) error {
	if err == nil {
		return nil
	}
	return &configWriteError{err: err, typedSecrets: typedSecrets}
}

// configDetail rend, masqué, le texte d'un refus typé d'écriture de configuration ; vide pour toute autre erreur.
func configDetail(err error, knownSecrets []string) string {
	failure, isConfigWrite := errors.AsType[*configWriteError](err)
	if !isConfigWrite {
		return ""
	}
	code, _ := app.Classify(err)
	isUsage := errors.Is(err, config.ErrUsage) || errors.Is(err, config.ErrInvalidStoredConfig)
	if !isUsage && code != app.ErrorCodeRefused {
		return ""
	}
	return config.RedactText(failure.err.Error(), append(knownSecrets, failure.typedSecrets...)...)
}

func elementSecrets(list string, element map[string]any) []string {
	var secrets []string
	for _, field := range config.SecretFields(list) {
		secrets = append(secrets, stringLeaves(element[field])...)
	}
	return secrets
}

func settingSecrets(key string, value json.RawMessage) []string {
	if !config.IsSecretSetting(key) {
		return nil
	}
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		return nil
	}
	return stringLeaves(decoded)
}

func stringLeaves(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []any:
		var leaves []string
		for _, entry := range typed {
			leaves = append(leaves, stringLeaves(entry)...)
		}
		return leaves
	case map[string]any:
		var leaves []string
		for _, entry := range typed {
			leaves = append(leaves, stringLeaves(entry)...)
		}
		return leaves
	}
	return nil
}
