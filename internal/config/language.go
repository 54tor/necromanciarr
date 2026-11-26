package config

import (
	"fmt"
	"strings"

	"necromanciarr/internal/i18n"
)

const (
	languageSourceDefault = "default"
	languageSourceFile    = sourceConfigFile
	languageSourceFlag    = "--language"
	// maxEchoedLanguageRunes borne la valeur renvoyée dans l'erreur : elle vient de l'opérateur mais peut être collée en
	// masse (même borne que crossSeedMode).
	maxEchoedLanguageRunes = 20
)

// resolveLanguage applique la priorité option > environnement > base > fr et retient la source retenue.
func resolveLanguage(target *Config, fileValue string, options commandLine, lookup func(string) (string, bool)) error {
	rawValue, source := "", languageSourceDefault
	if strings.TrimSpace(fileValue) != "" {
		rawValue, source = fileValue, languageSourceFile
	}
	if value, found := lookup(envLanguage); found && strings.TrimSpace(value) != "" {
		rawValue, source = value, envLanguage
	}
	if options.set["language"] {
		rawValue, source = options.language, languageSourceFlag
	}
	if source == languageSourceDefault {
		target.Language, target.LanguageSource = i18n.French, source
		return nil
	}
	language, err := i18n.ParseLanguage(rawValue)
	if err != nil {
		return &UnknownLanguageError{Value: truncateRunes(rawValue, maxEchoedLanguageRunes), Source: source}
	}
	target.Language, target.LanguageSource = language, source
	return nil
}

func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

// UnknownLanguageError signale une langue hors de fr et en ; Value est tronquée, Source dit d'où elle vient. C'est une
// erreur d'usage : la CLI la rend avec le message traduit error.usage.unknown_language.
type UnknownLanguageError struct {
	Value  string
	Source string
}

func (failure *UnknownLanguageError) Error() string {
	return fmt.Sprintf("%s: unknown language %q (%s): allowed values are fr, en", ErrUsage.Error(), failure.Value, failure.Source)
}

// Is rattache l'erreur à ErrUsage.
func (failure *UnknownLanguageError) Is(target error) bool { return target == ErrUsage }
