package config

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
)

var variableNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Interpolate remplace chaque ${NOM} de raw par la valeur renvoyée par lookup ; $$ donne un $ littéral.
// Une variable absente est une erreur qui cite le nom, jamais une valeur.
func Interpolate(raw string, lookup func(string) (string, bool)) (string, error) {
	var result strings.Builder
	for position := 0; position < len(raw); {
		remaining := raw[position:]
		switch {
		case strings.HasPrefix(remaining, "$$"):
			result.WriteByte('$')
			position += 2
		case strings.HasPrefix(remaining, "${"):
			closingIndex := strings.IndexByte(remaining, '}')
			if closingIndex < 0 {
				return "", errors.New("unterminated ${ reference")
			}
			variableValue, err := resolveVariable(remaining[2:closingIndex], lookup)
			if err != nil {
				return "", err
			}
			result.WriteString(variableValue)
			position += closingIndex + 1
		default:
			result.WriteByte(raw[position])
			position++
		}
	}
	return result.String(), nil
}

// variableRecorder entoure un lookup et retient, dans l'ordre, le nom et la valeur de chaque variable résolue : les
// noms pour `config check`, les valeurs pour KnownSecrets.
type variableRecorder struct {
	lookup        func(string) (string, bool)
	names, values []string
}

func (recorder *variableRecorder) resolve(name string) (string, bool) {
	variableValue, found := recorder.lookup(name)
	if found {
		recorder.names = append(recorder.names, name)
		recorder.values = append(recorder.values, variableValue)
	}
	return variableValue, found
}

func resolveVariable(name string, lookup func(string) (string, bool)) (string, error) {
	if !variableNamePattern.MatchString(name) {
		return "", fmt.Errorf("invalid variable name %q", name)
	}
	variableValue, found := lookup(name)
	if !found {
		return "", fmt.Errorf("undefined variable %s", name)
	}
	return variableValue, nil
}

// interpolateValue applique Interpolate, en place, à toute chaîne atteignable depuis value
// (champs de structure et éléments de tranche) ; path nomme la clé fautive dans l'erreur.
func interpolateValue(value reflect.Value, path string, lookup func(string) (string, bool)) error {
	switch value.Kind() {
	case reflect.String:
		interpolated, err := Interpolate(value.String(), lookup)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		value.SetString(interpolated)
	case reflect.Slice:
		for elementIndex := range value.Len() {
			elementPath := fmt.Sprintf("%s[%d]", path, elementIndex)
			if err := interpolateValue(value.Index(elementIndex), elementPath, lookup); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for fieldIndex := range value.NumField() {
			fieldName := strings.Split(value.Type().Field(fieldIndex).Tag.Get("json"), ",")[0]
			if err := interpolateValue(value.Field(fieldIndex), joinPath(path, fieldName), lookup); err != nil {
				return err
			}
		}
	default:
	}
	return nil
}

func joinPath(parent, child string) string {
	if parent == "" {
		return child
	}
	return parent + "." + child
}
