package app

import (
	"errors"
	"fmt"
	"testing"

	"necromanciarr/internal/i18n"
)

func TestClassify_everyTypedErrorHasMessageKey(t *testing.T) {
	englishTranslator, frenchTranslator := i18n.New(i18n.English, nil), i18n.New(i18n.French, nil)
	for _, class := range classes {
		for _, typed := range class.errors {
			key, hasKey := messageKeys[typed]
			if !hasKey {
				t.Errorf("%v (%s): no message key", typed, class.code)
				continue
			}
			englishText := englishTranslator.Text(key, nil)
			frenchText := frenchTranslator.Text(key, nil)
			if englishText == string(key) || frenchText == string(key) || englishText == frenchText {
				t.Errorf("%v: key %q rendered en %q fr %q, expectedValue two distinct translations", typed, key, englishText, frenchText)
			}
		}
	}
}

func TestMessageOf_typedErrorKeepsItsKeyThroughWrapping(t *testing.T) {
	wrapped := fmt.Errorf("%w: library provider", ErrNotConfigured)

	gotValue := MessageOf(wrapped)

	if expectedValue := i18n.KeyErrorNotConfigured; gotValue.Key != expectedValue {
		t.Fatalf("gotValue %q, expectedValue %q", gotValue.Key, expectedValue)
	}
}

func TestMessageOf_untypedErrorIsInternal(t *testing.T) {
	gotValue := MessageOf(errors.New("open /home/u/secret.db: disk on fire"))

	if gotValue.Key != i18n.KeyErrorInternal || len(gotValue.Params) != 0 {
		t.Fatalf("gotValue %+v, expectedValue the bare internal message", gotValue)
	}
}

func TestActions_descriptionsAreRenderedInTheLanguage(t *testing.T) {
	englishActions := Actions(i18n.New(i18n.English, nil))
	frenchActions := Actions(i18n.New(i18n.French, nil))

	if len(englishActions) != len(frenchActions) || len(englishActions) == 0 {
		t.Fatalf("gotValue %d en and %d fr actions, expectedValue the same non-empty registry", len(englishActions), len(frenchActions))
	}
	for position, englishAction := range englishActions {
		frenchAction := frenchActions[position]
		if englishAction.Name != frenchAction.Name || englishAction.Method != frenchAction.Method {
			t.Errorf("action %q: name or method changed with the language (%q, %q)", englishAction.Name, frenchAction.Name, frenchAction.Method)
		}
		if englishAction.Description == "" || englishAction.Description == frenchAction.Description {
			t.Errorf("action %q: en %q fr %q, expectedValue two distinct descriptions", englishAction.Name, englishAction.Description, frenchAction.Description)
		}
	}
}

func TestStatus_reportsLanguage(t *testing.T) {
	fixture := newDecisionFixture(t)
	service := New(Deps{Store: fixture.store, Translator: i18n.New(i18n.English, nil)})

	status, err := service.Status(t.Context())

	if err != nil || status.Language != "en" {
		t.Fatalf("gotValue %q (%v), expectedValue en", status.Language, err)
	}
}
