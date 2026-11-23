package config

import (
	"strings"
	"testing"
)

func lookupFrom(variables map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		variableValue, found := variables[name]
		return variableValue, found
	}
}

func TestInterpolate_replacesKnownVariable(t *testing.T) {
	expectedValue := "http://example.com?apikey=abc"

	gotValue, err := Interpolate("http://example.com?apikey=${KEY}", lookupFrom(map[string]string{"KEY": "abc"}))
	if err != nil {
		t.Fatal(err)
	}

	if gotValue != expectedValue {
		t.Fatalf("gotValue %q, expectedValue %q", gotValue, expectedValue)
	}
}

func TestInterpolate_unknownVariableNamesItWithoutValue(t *testing.T) {
	_, err := Interpolate("a-${MISSING}-b", lookupFrom(map[string]string{"OTHER": "secret-value"}))

	if err == nil || !strings.Contains(err.Error(), "MISSING") || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInterpolate_escapedDollarStaysLiteral(t *testing.T) {
	expectedValue := "pa$$ ${KEY}"

	gotValue, err := Interpolate("pa$$$$ $${KEY}", lookupFrom(nil))
	if err != nil {
		t.Fatal(err)
	}

	if gotValue != expectedValue {
		t.Fatalf("gotValue %q, expectedValue %q", gotValue, expectedValue)
	}
}

func TestInterpolate_emptyDefinedVariableIsAllowed(t *testing.T) {
	gotValue, err := Interpolate("x${EMPTY}y", lookupFrom(map[string]string{"EMPTY": ""}))

	if err != nil || gotValue != "xy" {
		t.Fatalf("gotValue %q, err %v", gotValue, err)
	}
}

func TestInterpolate_rejectsUnterminatedReference(t *testing.T) {
	if _, err := Interpolate("${KEY", lookupFrom(nil)); err == nil {
		t.Fatal("expected an error")
	}
}
