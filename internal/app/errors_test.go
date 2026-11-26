package app

import (
	"errors"
	"fmt"
	"testing"

	"necromanciarr/internal/config"
	"necromanciarr/internal/fsys"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/linker"
	"necromanciarr/internal/pipeline"
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

func TestMessageOf_remoteForceRefusalNamesTheVolumeInBothLanguages(t *testing.T) {
	for _, scenario := range []struct {
		reason       string
		cause        error
		french, text string
	}{
		{
			pipeline.ReasonServerCopyUnavailable, fsys.ErrServerCopyUnavailable,
			"Forcer est impossible sur seedbox : le serveur n'offre pas la copie côté serveur (copy-data)",
			"Force is not available on seedbox: the server offers no server-side copy (copy-data)",
		},
		{
			pipeline.ReasonFreeSpaceUnknown, linker.ErrFreeSpaceUnknown,
			"Forcer est impossible sur seedbox : la place libre du dossier de liens est inconnue (statvfs absent)",
			"Force is not available on seedbox: the free space of the link folder is unknown (no statvfs)",
		},
	} {
		refused := forceRefusal(fmt.Errorf("isolate: %w", &pipeline.ForceRefusal{Reason: scenario.reason, Volume: "seedbox", Cause: scenario.cause}))

		code, _ := Classify(refused)
		message := MessageOf(refused)

		if code != ErrorCodeRefused {
			t.Errorf("%s: valeurObtenue code %s, valeurAttendue %s", scenario.reason, code, ErrorCodeRefused)
		}
		if valeurObtenue := i18n.New(i18n.French, nil).Render(message); valeurObtenue != scenario.french {
			t.Errorf("%s fr: valeurObtenue %q, valeurAttendue %q", scenario.reason, valeurObtenue, scenario.french)
		}
		if valeurObtenue := i18n.New(i18n.English, nil).Render(message); valeurObtenue != scenario.text {
			t.Errorf("%s en: valeurObtenue %q, valeurAttendue %q", scenario.reason, valeurObtenue, scenario.text)
		}
	}
	if forceRefusal(errors.New("other")) != nil {
		t.Error("valeurObtenue a refusal, valeurAttendue nil for any other error")
	}
}

func TestClassify_configEditErrorsHaveTheirOwnCodes(t *testing.T) {
	scenarios := []struct {
		name           string
		err            error
		valeurAttendue string
	}{
		{"secret key missing", fmt.Errorf("item radarr: %w", config.ErrSecretKeyMissing), ErrorCodeSecretKeyMissing},
		{"unreadable stored configuration", fmt.Errorf("%w: sealed value", config.ErrInvalidStoredConfig), ErrorCodeInvalidStoredConfig},
		{"unknown setting", requireKnownSetting("notASetting"), ErrorCodeUsage},
		{"unknown list", requireKnownList("notAList"), ErrorCodeUsage},
	}
	for _, scenario := range scenarios {
		valeurObtenue, _ := Classify(scenario.err)
		if valeurObtenue != scenario.valeurAttendue {
			t.Errorf("%s: valeurObtenue %q, valeurAttendue %q", scenario.name, valeurObtenue, scenario.valeurAttendue)
		}
	}
}

func TestMessageOf_unknownConfigKeyHasItsOwnMessage(t *testing.T) {
	valeurAttendue := i18n.KeyErrorUnknownConfigKey

	valeurObtenue := MessageOf(requireKnownSetting("notASetting")).Key

	if valeurObtenue != valeurAttendue {
		t.Fatalf("valeurObtenue %q, valeurAttendue %q", valeurObtenue, valeurAttendue)
	}
}
