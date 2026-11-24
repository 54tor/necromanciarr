// Package i18n rend les textes destinés à l'opérateur en français ou en anglais.
//
// Le catalogue est une table typée écrite à la main (une constante Key par message, un gabarit par langue) plutôt
// qu'une bibliothèque : deux langues seulement, des pluriels simples (fr : n > 1 ; en : n ≠ 1), ni CLDR, ni format de
// fichier, ni extraction. Les clés vérifiées à la compilation et le test de complétude suffisent. Les gabarits
// utilisent des paramètres nommés {nom}, jamais des verbes fmt : l'ordre des mots change d'une langue à l'autre.
package i18n

import (
	"regexp"
	"strconv"
)

var placeholder = regexp.MustCompile(`\{([a-z_]+)\}`)

// Translator rend les messages dans une langue fixée à la création ; la valeur zéro est utilisable mais sans catalogue :
// Text, Count et Render rendent alors la clé telle quelle.
type Translator struct {
	language Language
	catalog  map[Key]string
	redact   func(string) string
}

func identity(text string) string { return text }

// New crée un Translator ; redact masque chaque valeur de paramètre avant rendu (nil : aucune transformation).
func New(language Language, redact func(string) string) Translator {
	if redact == nil {
		redact = identity
	}
	catalog := french
	if language == English {
		catalog = english
	}
	return Translator{language: language, catalog: catalog, redact: redact}
}

// Language donne la langue de rendu.
func (translator Translator) Language() Language { return translator.language }

// Text rend la clé ; un paramètre absent laisse son {nom} visible, une clé inconnue est rendue telle quelle.
func (translator Translator) Text(key Key, params map[string]string) string {
	template, found := translator.catalog[key]
	if !found {
		return string(key)
	}
	// Un seul passage sur le gabarit : une valeur de paramètre contenant {x} n'est pas réinterprétée.
	return placeholder.ReplaceAllStringFunc(template, func(token string) string {
		value, provided := params[token[1:len(token)-1]]
		if !provided {
			return token
		}
		return translator.redact(value)
	})
}

// Render rend un message stocké.
func (translator Translator) Render(message Message) string {
	return translator.Text(message.Key, message.Params)
}

// Count choisit le singulier ou le pluriel selon la langue et ajoute le paramètre count.
func (translator Translator) Count(singular, plural Key, count int, params map[string]string) string {
	withCount := make(map[string]string, len(params)+1)
	for name, value := range params {
		withCount[name] = value
	}
	withCount["count"] = strconv.Itoa(count)
	if translator.isPlural(count) {
		return translator.Text(plural, withCount)
	}
	return translator.Text(singular, withCount)
}

func (translator Translator) isPlural(count int) bool {
	if translator.language == English {
		return count != 1
	}
	return count > 1
}

// Keys liste toutes les clés déclarées, pour les tests de complétude.
func Keys() []Key {
	keys := []Key{KeyLegacyText}
	keys = append(keys, errorKeys...)
	keys = append(keys, cliKeys...)
	keys = append(keys, actionKeys...)
	return append(keys, doctorKeys...)
}
