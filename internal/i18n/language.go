package i18n

import (
	"errors"
	"strings"
)

// Language est une langue d'affichage prise en charge.
type Language string

const (
	// French est la langue par défaut.
	French Language = "fr"
	// English est la seconde langue prise en charge.
	English Language = "en"
)

// ErrUnknownLanguage signale une langue hors de fr et en.
var ErrUnknownLanguage = errors.New("unknown language")

// ParseLanguage lit une langue en ignorant casse et espaces ; seuls « fr » et « en » exacts sont admis (« fr_FR » est refusé).
func ParseLanguage(raw string) (Language, error) {
	switch language := Language(strings.ToLower(strings.TrimSpace(raw))); language {
	case French, English:
		return language, nil
	default:
		return "", ErrUnknownLanguage
	}
}
